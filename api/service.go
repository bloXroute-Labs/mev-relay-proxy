package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/patrickmn/go-cache"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
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
	requestTimeout = 3 * time.Second
	// cache
	builderBidsCleanupInterval = 60 * time.Second // 5 slots
	cacheKeySeparator          = "_"
)

var (

	// errors
	errInvalidSlot   = errors.New("invalid slot")
	errInvalidPubkey = errors.New("invalid pubkey")
	errInvalidHash   = errors.New("invalid hash")
)

type IService interface {
	RegisterValidator(ctx context.Context, receivedAt time.Time, payload []byte, clientIP, authHeader string) (any, any, error)
	GetHeader(ctx context.Context, receivedAt time.Time, clientIP, slot, parentHash, pubKey, authHeader string) (any, any, error)
	GetPayload(ctx context.Context, receivedAt time.Time, payload []byte, clientIP string, authHeader string) (any, any, error)
	NodeID() string
}
type Service struct {
	logger             *zap.Logger
	version            string // build version
	bids               *syncmap.SyncMap[string, []*Bid]
	clients            []*Client
	nodeID             string // UUID
	authKey            string
	secretToken        string
	tracer             trace.Tracer
	fluentD            fluentstats.Stats
	builderBidsForSlot *cache.Cache
	beaconGenesisTime  int64
}

type Client struct {
	URL    string
	nodeID string
	Conn   *grpc.ClientConn
	relaygrpc.RelayClient
}

type Bid struct {
	Value            []byte // block value
	Payload          []byte // blinded block
	BlockHash        string
	BuilderPubkey    string
	BuilderExtraData string
}

func NewService(logger *zap.Logger, tracer trace.Tracer, version string, secretToken string, nodeID string, authKey string, beaconGenesisTime int64, fluentD fluentstats.Stats, clients ...*Client) *Service {
	return &Service{
		logger:             logger,
		version:            version,
		clients:            clients,
		bids:               syncmap.NewStringMapOf[[]*Bid](),
		nodeID:             nodeID,
		authKey:            authKey,
		secretToken:        secretToken,
		tracer:             tracer,
		fluentD:            fluentD,
		builderBidsForSlot: cache.New(builderBidsCleanupInterval, builderBidsCleanupInterval),
		beaconGenesisTime:  beaconGenesisTime,
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

	for _, client := range s.clients {
		go func(c *Client) {
			_, regSpan := s.tracer.Start(ctx, "registerValidatorForClient")
			clientCtx, cancel := context.WithTimeout(ctx, requestTimeout)
			defer cancel()
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
			out, err := c.RegisterValidator(clientCtx, req)
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
		}(client)
	}

	// Wait for the first successful response or until all responses are processed
	for i := 0; i < len(s.clients); i++ {
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
			span.SetStatus(otelcodes.Error, "server unavailable,try reconnecting")
			s.logger.Warn("server unavailable,try reconnecting", zap.Error(_s.Err()), zap.String("nodeID", client.nodeID), zap.String("code", _s.Code().String()), zap.String("reqID", id), zap.String("url", client.URL))
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

		k := s.keyForCachingBids(header.GetSlot(), header.GetParentHash(), header.GetPubkey())
		s.logger.Info("received header",
			zap.String("reqID", id),
			zap.String("keyForCachingBids", k),
			zap.Uint64("slot", header.GetSlot()),
			zap.String("parentHash", header.GetParentHash()),
			zap.String("blockHash", header.GetBlockHash()),
			zap.String("blockValue", new(big.Int).SetBytes(header.GetValue()).String()),
			zap.String("pubKey", header.GetPubkey()),
			zap.String("builderPubkey", header.GetBuilderPubkey()),
			zap.String("extraData", header.GetBuilderExtraData()),
			zap.String("nodeID", client.nodeID),
			zap.String("url", client.URL),
			zap.String("tracer_id", parentSpan.SpanContext().TraceID().String()),
		)

		// store the bid for builder pubkey
		bid := &Bid{
			Value:            header.GetValue(),
			Payload:          header.GetPayload(),
			BlockHash:        header.GetBlockHash(),
			BuilderPubkey:    header.GetBuilderPubkey(),
			BuilderExtraData: header.GetBuilderExtraData(),
		}
		s.setBuilderBidForSlot(k, header.GetBuilderPubkey(), bid) // run it in goroutine ?
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
		return nil, nil, toErrorResp(http.StatusBadRequest, "", fmt.Sprintf("invalid slot %v", slot), id, errInvalidSlot.Error(), clientIP)

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

	parentSpanCtx := trace.ContextWithSpan(context.Background(), parentSpan)
	_, span = s.tracer.Start(parentSpanCtx, "getHeader")
	defer span.End()

	_, spanStoringHeader := s.tracer.Start(parentSpanCtx, "storingHeader")

	//TODO: send fluentd stats for StatusNoContent error cases

	if len(pubKey) != 98 {
		return nil, k, toErrorResp(http.StatusNoContent, "", fmt.Sprintf("pub key should be %d long", 98), id, errInvalidPubkey.Error(), clientIP)
	}

	if len(parentHash) != 66 {
		return nil, k, toErrorResp(http.StatusNoContent, "", fmt.Sprintf("parent hash hex should be %d long", 66), id, errInvalidHash.Error(), clientIP)
	}

	fetchGetHeaderStartTime := time.Now().UTC()
	keyForCachingBids := s.keyForCachingBids(_slot, parentHash, pubKey)
	slotBestHeader, err := s.GetTopBuilderBid(keyForCachingBids)
	fetchGetHeaderDurationMS := time.Since(fetchGetHeaderStartTime).Milliseconds()
	if slotBestHeader == nil || err != nil {
		msg := fmt.Sprintf("header value is not present for the requested key %v", keyForCachingBids)
		span.AddEvent("Header value is not present", trace.WithAttributes(attribute.String("msg", msg)))
		go func() {
			headerStats := getHeaderStatsRecord{
				RequestReceivedAt:        receivedAt,
				FetchGetHeaderStartTime:  fetchGetHeaderStartTime.String(),
				FetchGetHeaderDurationMS: fetchGetHeaderDurationMS,
				Duration:                 time.Since(startTime),
				MsIntoSlot:               msIntoSlot,
				ParentHash:               parentHash,
				PubKey:                   pubKey,
				BlockHash:                "",
				ReqID:                    id,
				ClientIP:                 clientIP,
				BlockValue:               "",
				Succeeded:                false,
				NodeID:                   s.nodeID,
			}
			s.fluentD.LogToFluentD(fluentstats.Record{
				Type: typeRelayProxyGetHeader,
				Data: headerStats,
			}, time.Now().UTC(), s.NodeID(), statsRelayProxyGetHeader)
		}()
		return nil, k, toErrorResp(http.StatusNoContent, "", msg, id, msg, clientIP)
	}
	spanStoringHeader.End()
	go func() {
		headerStats := getHeaderStatsRecord{
			RequestReceivedAt:        receivedAt,
			FetchGetHeaderStartTime:  fetchGetHeaderStartTime.String(),
			FetchGetHeaderDurationMS: fetchGetHeaderDurationMS,
			Duration:                 time.Since(startTime),
			MsIntoSlot:               msIntoSlot,
			ParentHash:               parentHash,
			PubKey:                   pubKey,
			BlockHash:                slotBestHeader.BlockHash,
			ReqID:                    id,
			ClientIP:                 clientIP,
			BlockValue:               new(big.Int).SetBytes(slotBestHeader.Value).String(),
			Succeeded:                false,
			NodeID:                   s.nodeID,
		}
		s.fluentD.LogToFluentD(fluentstats.Record{
			Type: typeRelayProxyGetHeader,
			Data: headerStats,
		}, time.Now().UTC(), s.NodeID(), statsRelayProxyGetHeader)
	}()

	return json.RawMessage(slotBestHeader.Payload), fmt.Sprintf("%v-blockHash-%v-value-%v", k, slotBestHeader.BlockHash, string(slotBestHeader.Value)), nil
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

				payloadStat := getPayloadStatsRecord{
					RequestReceivedAt: receivedAt,
					Duration:          time.Since(startTime),
					SlotStartTime:     slotStartTime,
					MsIntoSlot:        msIntoSlot,
					Slot:              uint64(slot),
					ParentHash:        "",
					PubKey:            "",
					BlockHash:         blockHash,
					ReqID:             id,
					ClientIP:          clientIP,
					Succeeded:         false,
					NodeID:            s.nodeID,
				}
				s.fluentD.LogToFluentD(fluentstats.Record{
					Type: typeRelayProxyGetPayload,
					Data: payloadStat,
				}, time.Now().UTC(), s.NodeID(), statsRelayProxyGetPayload)
			}()
			return nil, meta, toErrorResp(http.StatusInternalServerError, "", "failed to getPayload", id, ctx.Err().Error(), clientIP)
		case _err = <-errChan:
			// if multiple client return errors, first error gets replaced by the subsequent errors
		case out := <-respChan:
			go func() {

				slotStartTime := GetSlotStartTime(s.beaconGenesisTime, int64(out.GetSlot()))
				msIntoSlot := time.Since(slotStartTime).Milliseconds()

				payloadStats := getPayloadStatsRecord{
					RequestReceivedAt: receivedAt,
					Duration:          time.Since(startTime),
					SlotStartTime:     slotStartTime,
					MsIntoSlot:        msIntoSlot,
					Slot:              out.GetSlot(),
					ParentHash:        out.GetParentHash(),
					PubKey:            out.GetPubkey(),
					BlockHash:         out.GetBlockHash(),
					ReqID:             id,
					ClientIP:          clientIP,
					Succeeded:         false,
					NodeID:            s.nodeID,
				}
				s.fluentD.LogToFluentD(fluentstats.Record{
					Type: typeRelayProxyGetPayload,
					Data: payloadStats,
				}, time.Now().UTC(), s.NodeID(), statsRelayProxyGetPayload)
			}()
			return json.RawMessage(out.GetVersionedExecutionPayload()), meta, nil
		}
	}
	return nil, meta, _err
}

func (s *Service) keyForCachingBids(slot uint64, parentHash string, proposerPubkey string) string {
	return fmt.Sprintf("%d_%s_%s", slot, strings.ToLower(parentHash), strings.ToLower(proposerPubkey))
}

func (s *Service) parseKeyForCachingBids(cacheKey string) (slot uint64, parentHash string, proposerPubkey string, err error) {
	cacheKeyComponents := strings.Split(cacheKey, cacheKeySeparator)
	if len(cacheKeyComponents) != 3 {
		err = errors.New("invalid cache key format")
		return
	}

	slot, parseError := strconv.ParseUint(cacheKeyComponents[0], 10, 64)
	if parseError != nil {
		err = parseError
		return
	}

	parentHash = cacheKeyComponents[1]
	proposerPubkey = cacheKeyComponents[2]

	return
}

func (s *Service) GetTopBuilderBid(cacheKey string) (*Bid, error) {
	var builderBidsMap *syncmap.SyncMap[string, *Bid]
	entry, bidsMapFound := s.builderBidsForSlot.Get(cacheKey)
	if bidsMapFound {
		builderBidsMap = entry.(*syncmap.SyncMap[string, *Bid])
	}

	if !bidsMapFound || builderBidsMap.Size() == 0 {
		return nil, fmt.Errorf("no builder bids found for cache key %s", cacheKey)
	}

	topBid := new(Bid)
	topBidValue := new(big.Int)

	// search for the highest builder bid
	builderBidsMap.Range(func(builderPubkey string, bid *Bid) bool {
		bidValue := new(big.Int).SetBytes(bid.Value)
		if bidValue.Cmp(topBidValue) > 0 {
			topBid = bid
			topBidValue.Set(bidValue)
		}
		return true
	})

	return topBid, nil
}

func (s *Service) setBuilderBidForSlot(cacheKey string, builderPubkey string, bid *Bid) {
	var builderBidsMap *syncmap.SyncMap[string, *Bid]

	// if the cache key does not exist, create a new syncmap and store it in the cache
	if entry, bidsMapFound := s.builderBidsForSlot.Get(cacheKey); !bidsMapFound {
		builderBidsMap = syncmap.NewStringMapOf[*Bid]()
		s.builderBidsForSlot.Set(cacheKey, builderBidsMap, cache.DefaultExpiration)
	} else {
		// otherwise use the existing syncmap
		builderBidsMap = entry.(*syncmap.SyncMap[string, *Bid])
	}

	if existingBuilderBid, found := builderBidsMap.Load(builderPubkey); found {

		oldBidValue := new(big.Int).SetBytes(existingBuilderBid.Value)
		newBidValue := new(big.Int).SetBytes(bid.Value)
		slot, parentHash, proposerPubkey, err := s.parseKeyForCachingBids(cacheKey)
		if err != nil {
			s.logger.With(
				zap.String("cacheKey", cacheKey),
				zap.String("builderPubkey", builderPubkey),
				zap.Error(err),
			).Error("could not parse cache key")
		}

		// if this block is canceling another higher-value block from the same builder,
		// log getHeader cancellation data to fluentd to be loaded into the generic table
		if newBidValue.Cmp(oldBidValue) < 0 {
			go func() {
				stat := blockReplacedStatsRecord{
					Slot:                   slot,
					ParentHash:             parentHash,
					ProposerPubkey:         proposerPubkey,
					BuilderPubkey:          builderPubkey,
					ReplacedBlockHash:      existingBuilderBid.BlockHash,
					ReplacedBlockValue:     oldBidValue.String(),
					ReplacedBlockETHValue:  WeiToEth(oldBidValue.String()),
					ReplacedBlockExtraData: existingBuilderBid.BuilderExtraData,
					NewBlockHash:           bid.BlockHash,
					NewBlockValue:          newBidValue.String(),
					NewBlockEthValue:       WeiToEth(newBidValue.String()),
					NewBlockExtraData:      bid.BuilderExtraData,
					ReplacementTime:        time.Now().UTC(),
				}
				// log the cancellation data
				s.logger.Info("logging block replacement data", zap.Any("stat", stat))
				s.fluentD.LogToFluentD(fluentstats.Record{
					Data: stat,
					Type: "relay-proxy-bid-cancellation",
				}, time.Now(), s.NodeID(), statsRelayProxyBidCancellation)
			}()
		}
	}
	builderBidsMap.Store(builderPubkey, bid)
}

// This is only used for testing
func (s *Service) getBuilderBidForSlot(cacheKey string, builderPubkey string) (*Bid, bool) {
	if entry, bidsMapFound := s.builderBidsForSlot.Get(cacheKey); bidsMapFound {
		builderBidsMap := entry.(*syncmap.SyncMap[string, *Bid])
		builderBid, found := builderBidsMap.Load(builderPubkey)
		return builderBid, found
	}
	return nil, false
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

func (s *Service) NodeID() string {
	return s.nodeID
}
