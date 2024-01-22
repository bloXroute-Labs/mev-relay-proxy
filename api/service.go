package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/bloXroute-Labs/gateway/v2/utils/syncmap"
	relaygrpc "github.com/bloXroute-Labs/relay-grpc"
	"github.com/fluent/fluent-logger-golang/fluent"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
)

const (
	connReconnectTimeout = 5 * time.Second
	requestTimeout       = 3 * time.Second
	stateCheckerInterval = 5 * time.Second
)

type IService interface {
	RegisterValidator(ctx context.Context, receivedAt time.Time, payload []byte, clientIP, authHeader string) (any, any, error)
	GetHeader(ctx context.Context, receivedAt time.Time, clientIP, slot, parentHash, pubKey string) (any, any, error)
	GetPayload(ctx context.Context, receivedAt time.Time, payload []byte, clientIP string) (any, any, error)
}
type Service struct {
	logger  *zap.Logger
	version string // build version
	headers *syncmap.SyncMap[string, []*Header]
	clients []*Client
	nodeID  string // UUID
	//slotCleanUpCh chan uint64
	authKey      string
	secretToken  string
	isStreamOpen bool
	tracer       trace.Tracer
	fluentD      *fluent.Fluent
}

type Client struct {
	URL    string
	nodeID string
	Conn   *grpc.ClientConn
	relaygrpc.RelayClient
}

type Header struct {
	Value     []byte // block value
	Payload   []byte // blinded block
	BlockHash string
}

func NewService(logger *zap.Logger, tracer trace.Tracer, version string, nodeID string, authKey string, secretToken string, fluentD *fluent.Fluent, clients ...*Client) *Service {
	return &Service{
		logger:      logger,
		version:     version,
		clients:     clients,
		headers:     syncmap.NewStringMapOf[[]*Header](),
		nodeID:      nodeID,
		authKey:     authKey,
		secretToken: secretToken,
		tracer:      tracer,
		fluentD:     fluentD,
	}
}

func (s *Service) RegisterValidator(ctx context.Context, receivedAt time.Time, payload []byte, clientIP string, authHeader string) (any, any, error) {
	var (
		errChan  = make(chan error, len(s.clients))
		respChan = make(chan *relaygrpc.RegisterValidatorResponse, len(s.clients))
		_err     error
	)

	id := uuid.NewString()
	s.logger.Info("received",
		zap.String("method", "registerValidator"),
		zap.String("clientIP", clientIP),
		zap.String("reqID", id),
		zap.Time("receivedAt", receivedAt),
	)

	s.fluentD.PostWithTime(
		"mev.relay.proxy",
		time.Now(),
		map[string]string{
			"reqID":    id,
			"method":   "registerValidator",
			"clientIP": clientIP,
			"received": receivedAt.String(),
		},
	)

	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", s.authKey)

	parentSpan := trace.SpanFromContext(ctx)
	parentSpan.SetAttributes(
		attribute.String("method", "registerValidator"),
		attribute.String("clientIP", clientIP),
		attribute.String("reqID", id),
		attribute.Int64("receivedAt", receivedAt.Unix()),
	)

	spanCtx := trace.ContextWithSpan(ctx, parentSpan)
	_, span := s.tracer.Start(spanCtx, "registerValidator")
	defer span.End()

	for _, client := range s.clients {
		go func(c *Client) {
			clientCtx, cancel := context.WithTimeout(ctx, requestTimeout)
			defer cancel()
			req := &relaygrpc.RegisterValidatorRequest{
				ReqId:      id,
				Payload:    payload,
				ClientIp:   clientIP,
				Version:    s.version,
				NodeId:     c.nodeID,
				ReceivedAt: timestamppb.New(receivedAt),
				AuthHeader: authHeader,
			}
			out, err := c.RegisterValidator(clientCtx, req)
			if err != nil {
				errChan <- toErrorResp(http.StatusInternalServerError, err.Error(), id, "relay returned error", clientIP)
				return
			}
			if out == nil {
				errChan <- toErrorResp(http.StatusInternalServerError, "failed to register", id, "empty response from relay", clientIP)
				return
			}
			if out.Code != uint32(codes.OK) {
				errChan <- toErrorResp(http.StatusBadRequest, out.Message, id, "relay returned failure response code", clientIP)
				return
			}
			respChan <- out
		}(client)
	}

	// Wait for the first successful response or until all responses are processed
	for i := 0; i < len(s.clients); i++ {
		select {
		case <-ctx.Done():
			return nil, nil, toErrorResp(http.StatusInternalServerError, "failed to register", id, ctx.Err().Error(), clientIP)
		case _err = <-errChan:
			// if multiple client return errors, first error gets replaced by the subsequent errors
		case <-respChan:
			return struct{}{}, nil, nil
		}
	}
	return nil, nil, _err
}

func (s *Service) WrapStreamHeaders(ctx context.Context, wg *sync.WaitGroup) {
	for _, client := range s.clients {
		wg.Add(1)
		go func(_ctx context.Context, c *Client) {
			defer wg.Done()
			s.handleStream(_ctx, c)
		}(ctx, client)
	}
	wg.Wait()
}

func (s *Service) handleStream(ctx context.Context, client *Client) {
	for {
		select {
		case <-ctx.Done():
			s.logger.Warn("stream header context cancelled")
			return
		default:
			if _, err := s.StreamHeader(ctx, client); err != nil {
				s.logger.Warn("failed to stream header. Sleeping 1 second and then reconnecting", zap.String("url", client.URL), zap.Error(err))
			}
			time.Sleep(time.Second)
		}
	}
}

func (s *Service) WrapStreamHeader(ctx context.Context, client *Client) {
	parentSpan := trace.SpanFromContext(ctx)
	parentSpan.SetAttributes(
		attribute.String("method", "streamHeader"),
		attribute.String("url", client.URL),
	)
	spanCtx := trace.ContextWithSpan(ctx, parentSpan)
	_, span := s.tracer.Start(spanCtx, "streamHeader")
	defer span.End()

	for {
		select {
		case <-ctx.Done():
			s.logger.Warn("Stream header context cancelled")
			return
		default:
			if _, err := s.StreamHeader(ctx, client); err != nil {
				s.logger.Warn("Failed to stream header. Sleeping 1 second and then reconnecting", zap.String("url", client.URL), zap.Error(err))
			} else {
				s.logger.Warn("Stream header stopped.  Sleeping 1 second and then reconnecting", zap.String("url", client.URL))
			}
			time.Sleep(1 * time.Second)
		}
	}
}

func (s *Service) StreamHeader(ctx context.Context, client *Client) (*relaygrpc.StreamHeaderResponse, error) {
	id := uuid.NewString()
	client.nodeID = fmt.Sprintf("%v-%v-%v-%v", s.nodeID, client.URL, id, time.Now().UTC().Format("15:04:05.999999999"))
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", s.authKey)

	parentSpan := trace.SpanFromContext(ctx)

	stream, err := client.StreamHeader(ctx, &relaygrpc.StreamHeaderRequest{
		ReqId:   id,
		NodeId:  client.nodeID,
		Version: s.version,
	})

	s.logger.Info("streaming headers", zap.String("nodeID", client.nodeID))
	parentSpan.SetAttributes(
		attribute.String("method", "streamHeader"),
		attribute.String("nodeID", client.nodeID),
		attribute.String("reqID", id),
	)

	parentSpanCtx := trace.ContextWithSpan(context.Background(), parentSpan)
	_, span := s.tracer.Start(parentSpanCtx, "streamHeader")
	defer span.End()

	s.logger.Info("streaming headers", zap.String("nodeID", client.nodeID), zap.String("url", client.URL))
	if err != nil {
		s.logger.Warn("failed to stream header", zap.Error(err), zap.String("nodeID", client.nodeID), zap.String("reqID", id), zap.String("url", client.URL))
		return nil, err
	}
	s.isStreamOpen = true
	done := make(chan struct{})
	var once sync.Once
	closeDone := func() {
		once.Do(func() {
			s.logger.Info("calling close done once")
			close(done)
		})
	}
	go func() {
		select {
		case <-stream.Context().Done():
			s.logger.Warn("stream context cancelled, closing connection", zap.Error(stream.Context().Err()), zap.String("nodeID", client.nodeID), zap.String("reqID", id), zap.String("method", "StreamHeader"), zap.String("url", client.URL))
			closeDone()
		case <-ctx.Done():
			s.logger.Warn("context cancelled, closing connection", zap.Error(ctx.Err()), zap.String("nodeID", client.nodeID), zap.String("reqID", id), zap.String("method", "StreamHeader"), zap.String("url", client.URL))
			closeDone()
		}
	}()
	for {
		select {
		case <-done:
			return nil, nil
		default:
		}
		header, err := stream.Recv()
		if err == io.EOF {
			s.logger.Warn("stream received EOF", zap.Error(err), zap.String("nodeID", client.nodeID), zap.String("reqID", id), zap.String("url", client.URL))
			closeDone()
			break
		}
		_s, ok := status.FromError(err)
		if !ok {
			s.logger.Warn("invalid grpc error status", zap.Error(err), zap.String("nodeID", client.nodeID), zap.String("reqID", id), zap.String("url", client.URL))
			continue
		}

		if _s.Code() == codes.Canceled {
			s.logger.Warn("received cancellation signal, shutting down", zap.Error(err), zap.String("nodeID", client.nodeID), zap.String("reqID", id), zap.String("url", client.URL))
			// mark as canceled to stop the upstream retry loop
			s.isStreamOpen = false
			closeDone()
			break
		}

		if _s.Code() != codes.OK {
			s.logger.Warn("server unavailable,try reconnecting", zap.Error(_s.Err()), zap.String("nodeID", client.nodeID), zap.String("code", _s.Code().String()), zap.String("reqID", id), zap.String("url", client.URL))
			s.isStreamOpen = false
			closeDone()
			break
		}
		if err != nil {
			s.logger.Warn("failed to receive stream, disconnecting the stream", zap.Error(err), zap.String("nodeID", client.nodeID), zap.String("reqID", id), zap.String("url", client.URL))
			closeDone()
			break
		}
		// Added empty streaming as a temporary workaround to maintain streaming alive
		// TODO: this need to be handled by adding settings for keep alive params on both server and client
		if header.GetBlockHash() == "" {
			s.logger.Debug("received empty stream", zap.String("nodeID", client.nodeID), zap.String("reqID", id), zap.String("url", client.URL))
			continue
		}
		k := fmt.Sprintf("slot-%v-parentHash-%v-pubKey-%v", header.GetSlot(), header.GetParentHash(), header.GetPubkey())
		s.logger.Info("received header",
			zap.String("reqID", id),
			zap.Uint64("slot", header.GetSlot()),
			zap.String("parentHash", header.GetParentHash()),
			zap.String("blockHash", header.GetBlockHash()),
			zap.String("blockValue", new(big.Int).SetBytes(header.GetValue()).String()),
			zap.String("pubKey", header.GetPubkey()),
			zap.String("nodeID", client.nodeID),
			zap.String("url", client.URL),
		)
		v := &Header{
			Value:     header.GetValue(),
			Payload:   header.GetPayload(),
			BlockHash: header.GetBlockHash(),
		}
		if h, ok := s.headers.Load(k); ok {
			h = append(h, v)
			s.headers.Store(k, h)
			continue
		}
		h := make([]*Header, 0)
		h = append(h, v)
		s.headers.Store(k, h)
	}
	<-done
	s.logger.Warn("closing connection", zap.String("nodeID", client.nodeID), zap.String("reqID", id), zap.String("url", client.URL))
	return nil, nil
}

func (s *Service) GetHeader(ctx context.Context, receivedAt time.Time, clientIP, slot, parentHash, pubKey string) (any, any, error) {
	k := fmt.Sprintf("slot-%v-parentHash-%v-pubKey-%v", slot, parentHash, pubKey)
	id := uuid.NewString()
	parentSpan := trace.SpanFromContext(ctx)
	s.logger.Info("received",
		zap.String("method", "getHeader"),
		zap.Time("receivedAt", receivedAt),
		zap.String("clientIP", clientIP),
		zap.String("key", k),
		zap.String("reqID", id),
	)
	parentSpan.SetAttributes(
		attribute.String("method", "getHeader"),
		attribute.String("clientIP", clientIP),
		attribute.String("reqID", id),
		attribute.Int64("receivedAt", receivedAt.Unix()),
		attribute.String("key", k),
		attribute.String("reqID", id),
	)

	parentSpanCtx := trace.ContextWithSpan(context.Background(), parentSpan)
	_, span := s.tracer.Start(parentSpanCtx, "getHeader")
	defer span.End()

	defer func() {
		go func() {
			// cleanup old slots/headers
			time.AfterFunc(time.Second*30, func() {
				s.logger.Info("cleanup old slot", zap.String("key", k))
				s.headers.Delete(k)
			})
		}()
	}()

	_, spanStoringHeader := s.tracer.Start(parentSpanCtx, "storingHeader")
	val := new(big.Int)
	index := 0
	//TODO: currently storing all the header values for the particular slot
	// can be stored only the higher values to avoid looping through all the values
	if headers, ok := s.headers.Load(k); ok {
		for i, header := range headers {
			hVal := new(big.Int).SetBytes(header.Value)
			if hVal.Cmp(val) == 1 {
				val.Set(hVal)
				index = i
			}
		}
		out := headers[index]

		return json.RawMessage(out.Payload), fmt.Sprintf("%v-blockHash-%v-value-%v", k, out.BlockHash, val.String()), nil
	}
	spanStoringHeader.End()
	msg := fmt.Sprintf("header value is not present for the requested key %v", k)
	return nil, k, toErrorResp(http.StatusNoContent, msg, id, msg, clientIP)
}

func (s *Service) GetPayload(ctx context.Context, receivedAt time.Time, payload []byte, clientIP string) (any, any, error) {
	id := uuid.NewString()
	parentSpan := trace.SpanFromContext(ctx)
	s.logger.Info("received",
		zap.String("method", "getPayload"),
		zap.String("clientIP", clientIP),
		zap.String("reqID", id),
		zap.Time("receivedAt", receivedAt),
	)
	parentSpan.SetAttributes(
		attribute.String("method", "getPayload"),
		attribute.String("clientIP", clientIP),
		attribute.String("reqID", id),
		attribute.Int64("receivedAt", receivedAt.Unix()),
		attribute.String("reqID", id),
	)
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", s.authKey)

	parentSpanCtx := trace.ContextWithSpan(ctx, parentSpan)
	_, span := s.tracer.Start(parentSpanCtx, "getPayload")
	defer span.End()

	req := &relaygrpc.GetPayloadRequest{
		ReqId:      id,
		Payload:    payload,
		ClientIp:   clientIP,
		Version:    s.version,
		ReceivedAt: timestamppb.New(receivedAt),
	}
	var (
		errChan  = make(chan error, len(s.clients))
		respChan = make(chan *relaygrpc.GetPayloadResponse, len(s.clients))
		_err     error
		meta     string
	)
	for _, client := range s.clients {
		go func(c relaygrpc.RelayClient) {
			clientCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()

			out, err := c.GetPayload(clientCtx, req)
			if err != nil {
				errChan <- toErrorResp(http.StatusInternalServerError, err.Error(), id, "relay returned error", clientIP)
				return
			}
			if out == nil {
				errChan <- toErrorResp(http.StatusInternalServerError, "failed to getPayload", id, "empty response from relay", clientIP)
				return
			}
			if out.Code != uint32(codes.OK) {
				errChan <- toErrorResp(http.StatusBadRequest, out.Message, id, "relay returned failure response code", clientIP)
				return
			}
			// Set meta and send the response
			meta = fmt.Sprintf("slot-%v-parentHash-%v-pubKey-%v-blockHash-%v-proposerIndex-%v", out.GetSlot(), out.GetParentHash(), out.GetPubkey(), out.GetBlockHash(), out.GetProposerIndex())
			respChan <- out
		}(client)
	}
	// Wait for the first successful response or until all responses are processed
	for i := 0; i < len(s.clients); i++ {
		select {
		case <-ctx.Done():
			return nil, meta, toErrorResp(http.StatusInternalServerError, "failed to getPayload", id, ctx.Err().Error(), clientIP)
		case _err = <-errChan:
			// if multiple client return errors, first error gets replaced by the subsequent errors
		case out := <-respChan:
			return json.RawMessage(out.GetVersionedExecutionPayload()), meta, nil
		}
	}
	return nil, meta, _err
}

type ErrorResp struct {
	Code        int    `json:"code"`
	Message     string `json:"message"`
	BlxrMessage BlxrMessage
}

type BlxrMessage struct {
	reqID    string
	msg      string
	clientIP string
}

func (e *ErrorResp) Error() string {
	return e.Message
}

func toErrorResp(code int, message, reqID, blxrMessage, clientIP string) *ErrorResp {
	return &ErrorResp{
		Code:    code,
		Message: message,
		BlxrMessage: BlxrMessage{
			reqID:    reqID,
			msg:      blxrMessage,
			clientIP: clientIP,
		},
	}
}
