package api

import (
	"context"
	"encoding/json"
	"fmt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/metadata"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/bloXroute-Labs/gateway/v2/utils/syncmap"
	relaygrpc "github.com/bloXroute-Labs/relay-grpc"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
)

const (
	connReconnectTimeout = 5 * time.Second
	requestTimeout       = 3 * time.Second
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
	//TODO: add flag to receive node id
	nodeID string // UUID
	//slotCleanUpCh chan uint64
	authKey string
}

type Client struct {
	URL       string
	Conn      *grpc.ClientConn
	Done      chan struct{}
	Reconnect chan struct{}
	relaygrpc.RelayClient
}

type Header struct {
	Value     []byte // block value
	Payload   []byte // blinded block
	BlockHash string
}

func NewService(logger *zap.Logger, version string, nodeID string, authKey string, clients ...*Client) *Service {
	return &Service{
		logger:  logger,
		version: version,
		clients: clients,
		headers: syncmap.NewStringMapOf[[]*Header](),
		nodeID:  nodeID,
		authKey: authKey,
	}
}

func (s *Service) RegisterValidator(ctx context.Context, receivedAt time.Time, payload []byte, clientIP string, authHeader string) (any, any, error) {
	id := uuid.NewString()
	s.logger.Info("received",
		zap.String("method", "registerValidator"),
		zap.String("clientIP", clientIP),
		zap.String("reqID", id),
		zap.Time("receivedAt", receivedAt),
	)
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", s.authKey)
	req := &relaygrpc.RegisterValidatorRequest{
		ReqId:      id,
		Payload:    payload,
		ClientIp:   clientIP,
		NodeId:     s.nodeID,
		Version:    s.version,
		ReceivedAt: timestamppb.New(receivedAt),
		AuthHeader: authHeader,
	}

	var (
		errChan  = make(chan error, len(s.clients))
		respChan = make(chan *relaygrpc.RegisterValidatorResponse, len(s.clients))
		_err     error
	)
	for _, client := range s.clients {
		go func(c relaygrpc.RelayClient) {
			clientCtx, cancel := context.WithTimeout(ctx, requestTimeout)
			defer cancel()

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
			if err := s.ProcessStream(ctx, client); err != nil {
				s.logger.Warn("failed to stream header. Sleeping 1 second and then reconnecting", zap.String("url", client.URL), zap.Error(err))
			}
			time.Sleep(time.Second)
		}
	}
}

func (s *Service) waitUntilReady(ctx context.Context, client *Client) bool {
	rCtx, cancel := context.WithTimeout(ctx, connReconnectTimeout) //define how long you want to wait for connection to be restored before giving up
	defer cancel()

	currentState := client.Conn.GetState()
	stillConnecting := true

	for currentState != connectivity.Ready && stillConnecting {
		//will return true when state has changed from thisState, false if timeout
		stillConnecting = client.Conn.WaitForStateChange(rCtx, currentState)
		currentState = client.Conn.GetState()
		s.logger.With(zap.Any("state: ", currentState), zap.String("url", client.URL), zap.Duration("timeout", connReconnectTimeout)).Info("Attempting reconnection. State has changed to:")
	}

	if !stillConnecting {
		s.logger.Error("Connection attempt has timed out.")
		return false
	}
	return true
}
func (s *Service) ProcessStream(ctx context.Context, client *Client) error {

	go func() {
		sCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		if _, err := s.StreamHeader(sCtx, client); err != nil {
			s.logger.Warn("failed to start streaming headers", zap.Error(err), zap.String("url", client.URL))
		}
	}()
	for {
		select {
		case <-client.Reconnect:
			if !s.waitUntilReady(ctx, client) {
				return fmt.Errorf("failed to establish a connection within the defined timeout, url: %s", client.URL)
			}
			go func() {
				sCtx, cancel := context.WithCancel(ctx)
				defer cancel()
				if _, err := s.StreamHeader(sCtx, client); err != nil {
					s.logger.Warn("failed to start streaming headers", zap.Error(err), zap.String("url", client.URL))
				}
			}()
		case <-client.Done:
			s.logger.Info("context done for processing requests")
			return nil
		}
	}
}

//func (s *Service) streamAndReconnect(ctx context.Context, client *Client) error {
//	for {
//		select {
//		case <-ctx.Done():
//			s.logger.Warn("context cancelled, closing connection", zap.Error(ctx.Err()), zap.String("method", "streamAndReconnect"))
//			return ctx.Err()
//		default:
//			if _, err := s.StreamHeader(ctx, client); err != nil {
//				return err
//			}
//
//		}
//	}
//}

func (s *Service) StreamHeader(ctx context.Context, client *Client) (*relaygrpc.StreamHeaderResponse, error) {
	id := uuid.NewString()
	nodeID := fmt.Sprintf("%v-%v-%v", s.nodeID, id, time.Now().UTC().String())

	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", s.authKey)
	stream, err := client.StreamHeader(ctx, &relaygrpc.StreamHeaderRequest{
		ReqId:   id,
		NodeId:  nodeID,
		Version: s.version,
	})
	s.logger.Info("streaming headers", zap.String("nodeID", nodeID), zap.String("url", client.URL))
	if err != nil {
		s.logger.Warn("failed to stream header", zap.Error(err), zap.String("nodeID", nodeID), zap.String("reqID", id), zap.String("url", client.URL))
		return nil, err
	}
	stateChecker := time.NewTicker(time.Second * 10)
StreamLoop:
	for {
		select {
		case <-stateChecker.C:
			s.logger.Info("stream state info", zap.String("state", client.Conn.GetState().String()))
		case <-ctx.Done():
			s.logger.Warn("context cancelled, closing connection", zap.Error(ctx.Err()), zap.String("nodeID", nodeID), zap.String("reqID", id), zap.String("method", "StreamHeader"), zap.String("url", client.URL))
			return nil, ctx.Err()
		default:
			s.logger.Info("running default")
			header, err := stream.Recv()
			if err == io.EOF {
				s.logger.Warn("stream received EOF", zap.Error(err), zap.String("nodeID", nodeID), zap.String("reqID", id), zap.String("url", client.URL))
				client.Done <- struct{}{}
				break StreamLoop
			}
			if err != nil {
				s.logger.Warn("failed to receive stream, disconnecting the stream", zap.Error(err), zap.String("nodeID", nodeID), zap.String("reqID", id), zap.String("url", client.URL))
				client.Reconnect <- struct{}{}
				break StreamLoop
			}
			// Added empty streaming as a temporary workaround to maintain streaming alive
			// TODO: this need to be handled by adding settings for keep alive params on both server and client
			if header.GetBlockHash() == "" {
				s.logger.Debug("received empty stream", zap.String("nodeID", nodeID), zap.String("reqID", id), zap.String("url", client.URL))
				continue StreamLoop
			}
			k := fmt.Sprintf("slot-%v-parentHash-%v-pubKey-%v", header.GetSlot(), header.GetParentHash(), header.GetPubkey())
			s.logger.Info("received header",
				zap.String("reqID", id),
				zap.Uint64("slot", header.GetSlot()),
				zap.String("parentHash", header.GetParentHash()),
				zap.String("blockHash", header.GetBlockHash()),
				zap.String("blockValue", new(big.Int).SetBytes(header.GetValue()).String()),
				zap.String("pubKey", header.GetPubkey()),
				zap.String("nodeID", nodeID),
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
	}
	s.logger.Warn("closing connection", zap.String("nodeID", nodeID), zap.String("reqID", id), zap.String("url", client.URL))
	return nil, nil
}

func (s *Service) GetHeader(ctx context.Context, receivedAt time.Time, clientIP, slot, parentHash, pubKey string) (any, any, error) {
	k := fmt.Sprintf("slot-%v-parentHash-%v-pubKey-%v", slot, parentHash, pubKey)
	id := uuid.NewString()
	s.logger.Info("received",
		zap.String("method", "getHeader"),
		zap.Time("receivedAt", receivedAt),
		zap.String("clientIP", clientIP),
		zap.String("key", k),
		zap.String("reqID", id),
	)
	defer func() {
		go func() {
			// cleanup old slots/headers
			time.AfterFunc(time.Second*30, func() {
				s.logger.Info("cleanup old slot", zap.String("key", k))
				s.headers.Delete(k)
			})
		}()
	}()

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
	return nil, k, toErrorResp(http.StatusNoContent, "", id, fmt.Sprintf("header value is not present for the requested key %v", k), clientIP)
}

func (s *Service) GetPayload(ctx context.Context, receivedAt time.Time, payload []byte, clientIP string) (any, any, error) {
	id := uuid.NewString()
	s.logger.Info("received",
		zap.String("method", "getPayload"),
		zap.String("clientIP", clientIP),
		zap.String("reqID", id),
		zap.Time("receivedAt", receivedAt),
	)
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", s.authKey)
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
