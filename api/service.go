package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/bloXroute-Labs/gateway/v2/utils/syncmap"
	"github.com/bloXroute-Labs/mev-relay-proxy/fluentstats"
	relaygrpc "github.com/bloXroute-Labs/relay-grpc"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	connReconnectTimeout = 5 * time.Second
	requestTimeout       = 3 * time.Second
	stateCheckerInterval = 5 * time.Second
)

type IService interface {
	RegisterValidator(ctx context.Context, receivedAt time.Time, payload []byte, clientIP, authHeader string) (any, any, error)
	GetHeader(ctx context.Context, receivedAt time.Time, clientIP, slot, parentHash, pubKey, authHeader string) (any, any, error)
	GetPayload(ctx context.Context, receivedAt time.Time, payload []byte, clientIP string, authHeader string) (any, any, error)
}
type Service struct {
	logger                        *zap.Logger
	version                       string // build version
	headers                       *syncmap.SyncMap[string, []*Header]
	clients                       []*Client
	nodeID                        string // UUID
	authKey                       string
	tracer                        trace.Tracer
	registrationClients           []*Client
	currentRegistrationRelayIndex int
	registrationRelayMutex        sync.Mutex
	fluentD                       fluentstats.Stats
	expiredSlotKeyCh              chan string
	secretToken                   string
	beaconGenesisTime             int64
	isStreamOpen                  bool
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

func NewService(logger *zap.Logger, tracer trace.Tracer, version string, secretToken string, nodeID string, authKey string, beaconGenesisTime int64, fluentD fluentstats.Stats, clients []*Client, registrationClients ...*Client) *Service {
	return &Service{
		logger:                        logger,
		version:                       version,
		clients:                       clients,
		headers:                       syncmap.NewStringMapOf[[]*Header](),
		nodeID:                        nodeID,
		authKey:                       authKey,
		secretToken:                   secretToken,
		registrationClients:           registrationClients,
		registrationRelayMutex:        sync.Mutex{},
		currentRegistrationRelayIndex: 0,
		beaconGenesisTime:             beaconGenesisTime,
		tracer:                        tracer,
		fluentD:                       fluentD,
		expiredSlotKeyCh:              make(chan string, 100),
	}
}

func (s *Service) RegisterValidator(ctx context.Context, receivedAt time.Time, payload []byte, clientIP string, authHeader string) (any, any, error) {
	var (
		errChan  = make(chan error, len(s.clients))
		respChan = make(chan *relaygrpc.RegisterValidatorResponse, len(s.clients))
		_err     error
	)

	parentSpan := trace.SpanFromContext(ctx)
	ctx = trace.ContextWithSpan(context.Background(), parentSpan)
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", s.authKey)
	_, span := s.tracer.Start(ctx, "registerValidator")
	defer span.End()
	id := uuid.NewString()

	// decode auth header
	accountID, _, err := DecodeAuth(authHeader)
	if err != nil {
		s.logger.Warn("failed to decode auth header", zap.Error(err), zap.String("authHeader", authHeader), zap.String("reqID", id), zap.String("clientIP", clientIP))
		//zap.Error(err), 		return nil, nil, toErrorResp(http.StatusUnauthorized, "", fmt.Sprintf("failed to decode auth header: %v", authHeader), id, "invalid auth header", clientIP)
	}

	span.SetAttributes(
		attribute.String("method", "registerValidator"),
		attribute.String("client_ip", clientIP),
		attribute.String("req_id", id),
		attribute.String("tracer_id", parentSpan.SpanContext().TraceID().String()),
		attribute.Int64("received_at", receivedAt.Unix()),
		attribute.String("account_id", accountID),
		attribute.String("auth_header", authHeader),
	)

	s.logger.Info("received",
		zap.String("method", "registerValidator"),
		zap.String("client_ip", clientIP),
		zap.String("req_id", id),
		zap.String("tracer_id", parentSpan.SpanContext().TraceID().String()),
		zap.Time("received_at", receivedAt),
	)
	//TODO: For now using relay proxy auth-header to allow every validator to connect  But this needs to be updated in the future to  use validator auth header.

	for _, registrationClient := range s.registrationClients {
		go func(c *Client) {
			_, regSpan := s.tracer.Start(ctx, "registerValidatorForClient")
			clientCtx, cancel := context.WithTimeout(ctx, requestTimeout)
			defer cancel()

			s.registrationRelayMutex.Lock()
			selectedRelay := s.registrationClients[s.currentRegistrationRelayIndex]
			s.currentRegistrationRelayIndex = (s.currentRegistrationRelayIndex + 1) % len(s.registrationClients)
			s.registrationRelayMutex.Unlock()

			req := &relaygrpc.RegisterValidatorRequest{
				ReqId:       id,
				Payload:     payload,
				ClientIp:    clientIP,
				Version:     s.version,
				NodeId:      c.nodeID,
				ReceivedAt:  timestamppb.New(receivedAt),
				AuthHeader:  authHeader,
				SecretToken: s.secretToken,
			}
			out, err := selectedRelay.RegisterValidator(clientCtx, req)
			regSpan.AddEvent("registerValidator", trace.WithAttributes(attribute.String("url", c.URL)))
			if err != nil {
				regSpan.SetStatus(otelcodes.Error, err.Error())
				errChan <- toErrorResp(http.StatusInternalServerError, err.Error(), "", id, "relay returned error", clientIP)
				return
			}
			if out == nil {
				regSpan.SetStatus(otelcodes.Error, errors.New("empty response from relay").Error())
				errChan <- toErrorResp(http.StatusInternalServerError, "", "", id, "empty response from relay", clientIP)
				return
			}
			if out.Code != uint32(codes.OK) {
				regSpan.SetStatus(otelcodes.Error, errors.New("relay returned failure response code").Error())
				errChan <- toErrorResp(http.StatusBadRequest, "", out.Message, id, "relay returned failure response code", clientIP)
				return
			}
			regSpan.SetStatus(otelcodes.Ok, "relay returned success response code")
			respChan <- out
		}(registrationClient)
	}

	// Wait for the first successful response or until all responses are processed
	for i := 0; i < len(s.registrationClients); i++ {
		select {
		case <-ctx.Done():
			return nil, nil, toErrorResp(http.StatusInternalServerError, "", "failed to register", id, ctx.Err().Error(), clientIP)
		case _err = <-errChan:
			// if multiple client return errors, first error gets replaced by the subsequent errors
		case <-respChan:
			return struct{}{}, nil, nil
		}
	}
	return nil, nil, _err
}

func (s *Service) StartStreamHeaders(ctx context.Context, wg *sync.WaitGroup) {

	// Periodically clean up headers
	go s.cleanUpExpiredHeaders(s.expiredSlotKeyCh)
	defer close(s.expiredSlotKeyCh)

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
	parentSpan := trace.SpanFromContext(ctx)
	ctx = trace.ContextWithSpan(context.Background(), parentSpan)
	_, span := s.tracer.Start(ctx, "streamHeader")
	defer span.End()

	span.SetAttributes(
		attribute.String("method", "streamHeader"),
		attribute.String("url", client.URL),
		attribute.String("tracer_id", parentSpan.SpanContext().TraceID().String()),
	)

	for {
		select {
		case <-ctx.Done():
			s.logger.Warn("stream header context cancelled",
				zap.String("tracer_id", parentSpan.SpanContext().TraceID().String()),
			)
			return
		default:
			if _, err := s.StreamHeader(ctx, client); err != nil {
				s.logger.Warn("failed to stream header. Sleeping 1 second and then reconnecting",
					zap.String("url", client.URL),
					zap.String("tracer_id", parentSpan.SpanContext().TraceID().String()),
					zap.Error(err))
				span.SetAttributes(attribute.KeyValue{Key: "sleepingFor", Value: attribute.Int64Value(1)}, attribute.KeyValue{Key: "error", Value: attribute.StringValue(err.Error())})
			} else {
				s.logger.Warn("stream header stopped.  Sleeping 1 second and then reconnecting",
					zap.String("url", client.URL),
					zap.String("tracer_id", parentSpan.SpanContext().TraceID().String()),
				)
				span.SetAttributes(attribute.KeyValue{Key: "sleepingFor", Value: attribute.Int64Value(1)}, attribute.KeyValue{Key: "error", Value: attribute.StringValue("stream header stopped.")})
			}
			time.Sleep(1 * time.Second)
		}
	}
}

func (s *Service) StreamHeader(ctx context.Context, client *Client) (*relaygrpc.StreamHeaderResponse, error) {
	parentSpan := trace.SpanFromContext(ctx)
	ctx = trace.ContextWithSpan(context.Background(), parentSpan)
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", s.authKey)
	_, span := s.tracer.Start(ctx, "streamHeader")
	defer span.End()
	id := uuid.NewString()
	client.nodeID = fmt.Sprintf("%v-%v-%v-%v", s.nodeID, client.URL, id, time.Now().UTC().Format("15:04:05.999999999"))

	stream, err := client.StreamHeader(ctx, &relaygrpc.StreamHeaderRequest{
		ReqId:       id,
		NodeId:      client.nodeID,
		Version:     s.version,
		SecretToken: s.secretToken,
	})

	s.logger.Info("streaming headers", zap.String("nodeID", client.nodeID))
	span.SetAttributes(
		attribute.String("method", "streamHeader"),
		attribute.String("nodeid", client.nodeID),
		attribute.String("tracer_id", parentSpan.SpanContext().TraceID().String()),
		attribute.String("req_id", id),
	)

	s.logger.Info("streaming headers", zap.String("nodeID", client.nodeID), zap.String("url", client.URL))
	if err != nil {
		s.logger.Warn("failed to stream header",
			zap.Error(err), zap.String("node_id", client.nodeID),
			zap.String("req_id", id),
			zap.String("url", client.URL),
			zap.String("tracer_id", parentSpan.SpanContext().TraceID().String()),
		)
		span.SetStatus(otelcodes.Error, err.Error())
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
			s.logger.Warn("stream context cancelled, closing connection",
				zap.Error(stream.Context().Err()),
				zap.String("node_id", client.nodeID),
				zap.String("req_id", id),
				zap.String("method", "StreamHeader"),
				zap.String("tracer_id", parentSpan.SpanContext().TraceID().String()),
				zap.String("url", client.URL),
			)
			closeDone()
		case <-ctx.Done():
			s.logger.Warn("context cancelled, closing connection",
				zap.Error(ctx.Err()),
				zap.String("node_id", client.nodeID),
				zap.String("req_id", id),
				zap.String("method", "StreamHeader"),
				zap.String("tracer_id", parentSpan.SpanContext().TraceID().String()),
				zap.String("url", client.URL),
			)
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
			s.logger.Warn("stream received EOF",
				zap.Error(err),
				zap.String("node_id", client.nodeID),
				zap.String("req_id", id),
				zap.String("url", client.URL),
				zap.String("tracer_id", parentSpan.SpanContext().TraceID().String()),
			)
			span.SetStatus(otelcodes.Error, err.Error())
			closeDone()
			break
		}
		_s, ok := status.FromError(err)
		if !ok {
			s.logger.Warn("invalid grpc error status",
				zap.Error(err),
				zap.String("node_id", client.nodeID),
				zap.String("req_id", id),
				zap.String("tracer_id", parentSpan.SpanContext().TraceID().String()),
				zap.String("url", client.URL),
			)
			span.SetStatus(otelcodes.Error, "invalid grpc error status")
			continue
		}

		if _s.Code() == codes.Canceled {
			s.logger.Warn("received cancellation signal, shutting down",
				zap.Error(err),
				zap.String("node_id", client.nodeID),
				zap.String("req_id", id),
				zap.String("url", client.URL),
				zap.String("tracer_id", parentSpan.SpanContext().TraceID().String()),
			)
			// mark as canceled to stop the upstream retry loop
			s.isStreamOpen = false
			span.SetStatus(otelcodes.Error, "received cancellation signal")
			closeDone()
			break
		}

		if _s.Code() != codes.OK {
			s.logger.Warn("server unavailable,try reconnecting",
				zap.Error(_s.Err()),
				zap.String("node_id", client.nodeID),
				zap.String("code", _s.Code().String()),
				zap.String("req_id", id),
				zap.String("url", client.URL),
				zap.String("tracer_id", parentSpan.SpanContext().TraceID().String()),
			)
			s.isStreamOpen = false
			span.SetStatus(otelcodes.Error, "server unavailable,try reconnecting")
			closeDone()
			break
		}
		if err != nil {
			s.logger.Warn("failed to receive stream, disconnecting the stream",
				zap.Error(err),
				zap.String("node_id", client.nodeID),
				zap.String("req_id", id),
				zap.String("url", client.URL),
				zap.String("tracer_id", parentSpan.SpanContext().TraceID().String()),
			)
			span.SetStatus(otelcodes.Error, err.Error())
			closeDone()
			break
		}
		// Added empty streaming as a temporary workaround to maintain streaming alive
		// TODO: this need to be handled by adding settings for keep alive params on both server and client
		if header.GetBlockHash() == "" {
			s.logger.Debug("received empty stream",
				zap.String("node_id", client.nodeID),
				zap.String("req_id", id),
				zap.String("url", client.URL),
				zap.String("tracer_id", parentSpan.SpanContext().TraceID().String()),
			)
			continue
		}
		k := fmt.Sprintf("slot-%v-parentHash-%v-pubKey-%v", header.GetSlot(), header.GetParentHash(), header.GetPubkey())
		s.logger.Info("received header",
			zap.String("req_id", id),
			zap.Uint64("slot", header.GetSlot()),
			zap.String("parent_hash", header.GetParentHash()),
			zap.String("block_hash", header.GetBlockHash()),
			zap.String("block_value", new(big.Int).SetBytes(header.GetValue()).String()),
			zap.String("pub_key", header.GetPubkey()),
			zap.String("node_id", client.nodeID),
			zap.String("url", client.URL),
			zap.String("tracer_id", parentSpan.SpanContext().TraceID().String()),
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

		// Send the key to chan for expiration after 1 minute to clean-up
		go func(key string) {
			<-time.After(time.Minute)
			s.expiredSlotKeyCh <- key
		}(k)
	}
	<-done
	s.logger.Warn("closing connection",
		zap.String("node_id", client.nodeID),
		zap.String("req_id", id),
		zap.String("url", client.URL),
		zap.String("tracer_id", parentSpan.SpanContext().TraceID().String()),
	)
	return nil, nil
}

func (s *Service) cleanUpExpiredHeaders(expiredKeyCh <-chan string) {
	for k := range expiredKeyCh {
		s.logger.Info("cleanup old slot", zap.String("key", k))
		s.headers.Delete(k)
	}
}

func (s *Service) GetHeader(ctx context.Context, receivedAt time.Time, clientIP, slot, parentHash, pubKey, authHeader string) (any, any, error) {
	parentSpan := trace.SpanFromContext(ctx)
	ctx = trace.ContextWithSpan(context.Background(), parentSpan)
	_, span := s.tracer.Start(ctx, "getHeader")
	defer span.End()
	startTime := time.Now().UTC()

	k := fmt.Sprintf("slot-%v-parentHash-%v-pubKey-%v", slot, parentHash, pubKey)
	id := uuid.NewString()

	// decode auth header
	accountID, _, err := DecodeAuth(authHeader)
	if err != nil {
		s.logger.Warn("failed to decode auth header", zap.Error(err), zap.String("authHeader", authHeader), zap.String("reqID", id), zap.String("clientIP", clientIP))
		//return nil, nil, toErrorResp(http.StatusUnauthorized, "", fmt.Sprintf("failed to decode auth header: %v", authHeader), id, "invalid auth header", clientIP)
	}

	_slot, err := strconv.ParseUint(slot, 10, 64)
	if err != nil {
		return nil, nil, toErrorResp(http.StatusBadRequest, "", fmt.Sprintf("invalid slot %v", slot), id, "invalid slot", clientIP)

	}

	slotStartTime := GetSlotStartTime(s.beaconGenesisTime, int64(_slot))
	msIntoSlot := time.Since(slotStartTime).Milliseconds()

	s.logger.Info("received",
		zap.String("method", "getHeader"),
		zap.Time("received_at", receivedAt),
		zap.String("client_ip", clientIP),
		zap.String("key", k),
		zap.String("req_id", id),
		zap.String("tracer_id", parentSpan.SpanContext().TraceID().String()),
		zap.String("slot", slot),
		zap.Time("slot_start_time", slotStartTime),
		zap.Int64("ms_into_slot", msIntoSlot),
	)

	span.SetAttributes(
		attribute.String("method", "getHeader"),
		attribute.String("client_ip", clientIP),
		attribute.String("req_id", id),
		attribute.Int64("received_at", receivedAt.Unix()),
		attribute.String("key", k),
		attribute.String("tracer_id", parentSpan.SpanContext().TraceID().String()),
		attribute.String("slot", slot),
		attribute.String("slot_start_time", slotStartTime.String()),
		attribute.Int64("ms_into_slot", msIntoSlot),
		attribute.String("account_id", accountID),
		attribute.String("auth_header", authHeader),
	)
	val := new(big.Int)
	index := 0
	//TODO: currently storing all the header values for the particular slot
	// can be stored only the higher values to avoid looping through all the values
	if headers, ok := s.headers.Load(k); ok {
		_, spanStoringHeader := s.tracer.Start(ctx, "storingHeader")
		for i, header := range headers {
			hVal := new(big.Int).SetBytes(header.Value)
			if hVal.Cmp(val) == 1 {
				val.Set(hVal)
				index = i
			}
		}
		out := headers[index]
		go func() {
			s.fluentD.LogToFluentD(fluentstats.Record{
				Type: "relay_proxy_provided_header",
				Data: map[string]interface{}{
					"received":      receivedAt,
					"duration":      time.Since(startTime),
					"slot":          slot,
					"slotStartTime": slotStartTime,
					"msIntoSlot":    msIntoSlot,
					"parentHash":    parentHash,
					"pubKey":        pubKey,
					"blockHash":     out.BlockHash,
					"reqID":         id,
					"clientIP":      clientIP,
					"blockValue":    val.String(),
					"succeeded":     true,
					"nodeID":        s.nodeID,
					"accountID":     accountID,
				},
			}, time.Now().UTC(), s.nodeID, "relay-proxy-getHeader")
		}()
		spanStoringHeader.AddEvent("Header value is present", trace.WithAttributes(attribute.String("blockHash", out.BlockHash), attribute.String("blockValue", val.String())))
		return json.RawMessage(out.Payload), fmt.Sprintf("%v-blockHash-%v-value-%v", k, out.BlockHash, val.String()), nil
	}
	msg := fmt.Sprintf("header value is not present for the requested key %v", k)
	span.AddEvent("Header value is not present", trace.WithAttributes(attribute.String("msg", msg)))
	go func() {
		s.fluentD.LogToFluentD(fluentstats.Record{
			Type: "relay_proxy_provided_header",
			Data: map[string]interface{}{
				"RequestReceivedAt": receivedAt,
				"duration":          time.Since(startTime),
				"slot":              slot,
				"slotStartTime":     slotStartTime,
				"msIntoSlot":        msIntoSlot,
				"parentHash":        parentHash,
				"pubKey":            pubKey,
				"blockHash":         "",
				"reqID":             id,
				"clientIP":          clientIP,
				"blockValue":        "",
				"succeeded":         false,
				"nodeID":            s.nodeID,
				"accountID":         accountID,
			},
		}, time.Now().UTC(), s.nodeID, "relay-proxy-getHeader")
	}()
	return nil, k, toErrorResp(http.StatusNoContent, "", "", id, msg, clientIP)
}

func (s *Service) GetPayload(ctx context.Context, receivedAt time.Time, payload []byte, clientIP, authHeader string) (any, any, error) {
	parentSpan := trace.SpanFromContext(ctx)
	ctx = trace.ContextWithSpan(context.Background(), parentSpan)
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", s.authKey)
	_, span := s.tracer.Start(ctx, "getPayload")
	defer span.End()

	startTime := time.Now().UTC()
	id := uuid.NewString()

	// decode auth header
	accountID, _, err := DecodeAuth(authHeader)
	if err != nil {
		s.logger.Warn("failed to decode auth header", zap.Error(err), zap.String("authHeader", authHeader), zap.String("reqID", id), zap.String("clientIP", clientIP))
		//return nil, nil, toErrorResp(http.StatusUnauthorized, "", fmt.Sprintf("failed to decode auth header: %v", authHeader), id, "invalid auth header", clientIP)
	}

	s.logger.Info("received",
		zap.String("method", "getPayload"),
		zap.String("client_ip", clientIP),
		zap.String("req_id", id),
		zap.Any("trace_id", span.SpanContext().TraceID),
		zap.Time("received_at", receivedAt),
	)
	span.SetAttributes(
		attribute.String("method", "getPayload"),
		attribute.String("client_ip", clientIP),
		attribute.String("req_id", id),
		attribute.String("tracer_id", parentSpan.SpanContext().TraceID().String()),
		attribute.Int64("received_at", receivedAt.Unix()),
		attribute.String("account_id", accountID),
		attribute.String("auth_header", authHeader),
	)

	//TODO: For now using relay proxy auth-header to allow every validator to connect  But this needs to be updated in the future to  use validator auth header.

	defer span.End()

	req := &relaygrpc.GetPayloadRequest{
		ReqId:       id,
		Payload:     payload,
		ClientIp:    clientIP,
		Version:     s.version,
		ReceivedAt:  timestamppb.New(receivedAt),
		SecretToken: s.secretToken,
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
				span.SetStatus(otelcodes.Error, err.Error())
				errChan <- toErrorResp(http.StatusInternalServerError, err.Error(), "", id, "relay returned error", clientIP)
				return
			}
			if out == nil {
				span.SetStatus(otelcodes.Error, "empty response from relay")
				errChan <- toErrorResp(http.StatusInternalServerError, "", "", id, "empty response from relay", clientIP)
				return
			}
			if out.Code != uint32(codes.OK) {
				span.SetStatus(otelcodes.Error, out.Message)
				errChan <- toErrorResp(http.StatusBadRequest, "", out.Message, id, "relay returned failure response code", clientIP)
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

			go func() {
				var (
					slot          int64
					slotStartTime time.Time
					msIntoSlot    int64
					blockHash     string
				)
				// Decode payload
				decodedPayload := new(VersionedSignedBlindedBeaconBlock)
				if err = json.NewDecoder(bytes.NewReader(payload)).Decode(decodedPayload); err != nil {
					s.logger.Warn("failed to decode getPayload request")
				} else {
					_slot, err := decodedPayload.Slot()
					if err != nil {
						s.logger.Warn("failed to decode getPayload slot")
					} else {
						slot = int64(_slot)
						slotStartTime = GetSlotStartTime(s.beaconGenesisTime, slot)
						msIntoSlot = time.Since(slotStartTime).Milliseconds()

						_blockHash, err := decodedPayload.ExecutionBlockHash()
						if err != nil {
							s.logger.Warn("failed to decode getPayload blockHash")
						} else {
							blockHash = _blockHash.String()
						}
					}
				}

				s.fluentD.LogToFluentD(fluentstats.Record{
					Type: "relay_proxy_provided_payload",
					Data: map[string]interface{}{
						"RequestReceivedAt": receivedAt,
						"duration":          time.Since(startTime),
						"slotStartTime":     slotStartTime,
						"msIntoSlot":        msIntoSlot,
						"slot":              slot,
						"parentHash":        "",
						"pubKey":            "",
						"blockHash":         blockHash,
						"reqID":             id,
						"clientIP":          clientIP,
						"succeeded":         false,
						"nodeID":            s.nodeID,
						"accountID":         accountID,
					},
				}, time.Now().UTC(), s.nodeID, "relay-proxy-getPayload")
			}()
			return nil, meta, toErrorResp(http.StatusInternalServerError, "", "failed to getPayload", id, ctx.Err().Error(), clientIP)
		case _err = <-errChan:
			// if multiple client return errors, first error gets replaced by the subsequent errors
		case out := <-respChan:
			go func() {

				slotStartTime := GetSlotStartTime(s.beaconGenesisTime, int64(out.GetSlot()))
				msIntoSlot := time.Since(slotStartTime).Milliseconds()

				s.fluentD.LogToFluentD(fluentstats.Record{
					Type: "relay_proxy_provided_payload",
					Data: map[string]interface{}{
						"RequestReceivedAt": receivedAt,
						"duration":          time.Since(startTime),
						"slot":              out.GetSlot(),
						"slotStartTime":     slotStartTime,
						"msIntoSlot":        msIntoSlot,
						"parentHash":        out.GetParentHash(),
						"pubKey":            out.GetPubkey(),
						"blockHash":         out.GetBlockHash(),
						"reqID":             id,
						"clientIP":          clientIP,
						"succeeded":         true,
						"nodeID":            s.nodeID,
						"accountID":         accountID,
					},
				}, time.Now().UTC(), s.nodeID, "relay-proxy-getPayload")
			}()
			return json.RawMessage(out.GetVersionedExecutionPayload()), meta, nil
		}
	}
	return nil, meta, _err
}

type ErrorResp struct {
	Code    int    `json:"code"`
	Message string `json:"message"`

	BlxrMessage BlxrMessage `json:"-"`
}

type BlxrMessage struct {
	reqID    string
	msg      string
	relayMsg string
	proxyMsg string
	clientIP string
}

func (e *ErrorResp) Error() string {
	return e.Message
}

func toErrorResp(code int, relayMsg, proxyMsg, reqID, msg, clientIP string) *ErrorResp {
	return &ErrorResp{
		Code:    code,
		Message: msg,
		BlxrMessage: BlxrMessage{
			reqID:    reqID,
			msg:      msg,
			relayMsg: relayMsg,
			proxyMsg: proxyMsg,
			clientIP: clientIP,
		},
	}
}
