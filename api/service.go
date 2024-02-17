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
	"strings"
	"sync"
	"time"

	"github.com/patrickmn/go-cache"

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
	requestTimeout = 30 * time.Second
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
	RegisterValidator(ctx context.Context, receivedAt time.Time, payload []byte, clientIP, authHeader string) (any, *LogMetric, error)
	GetHeader(ctx context.Context, receivedAt time.Time, clientIP, slot, parentHash, pubKey, authHeader string) (any, *LogMetric, error)
	GetPayload(ctx context.Context, receivedAt time.Time, payload []byte, clientIP, authHeader string) (any, *LogMetric, error)
	NodeID() string
}
type Service struct {
	logger      *zap.Logger
	version     string // build version
	nodeID      string // UUID
	authKey     string
	secretToken string

	tracer  trace.Tracer
	fluentD fluentstats.Stats

	bids               *syncmap.SyncMap[string, []*Bid]
	builderBidsForSlot *cache.Cache
	beaconGenesisTime  int64

	clients                       []*Client
	registrationClients           []*Client
	currentRegistrationRelayIndex int
	registrationRelayMutex        sync.Mutex
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

func NewService(logger *zap.Logger, tracer trace.Tracer, version string, secretToken string, nodeID string, authKey string, beaconGenesisTime int64, fluentD fluentstats.Stats, clients []*Client, registrationClients ...*Client) *Service {
	return &Service{
		logger:                        logger,
		version:                       version,
		nodeID:                        nodeID,
		authKey:                       authKey,
		secretToken:                   secretToken,
		bids:                          syncmap.NewStringMapOf[[]*Bid](),
		builderBidsForSlot:            cache.New(builderBidsCleanupInterval, builderBidsCleanupInterval),
		beaconGenesisTime:             beaconGenesisTime,
		clients:                       clients,
		registrationClients:           registrationClients,
		registrationRelayMutex:        sync.Mutex{},
		currentRegistrationRelayIndex: 0,
		tracer:                        tracer,
		fluentD:                       fluentD,
	}
}

func (s *Service) RegisterValidator(ctx context.Context, receivedAt time.Time, payload []byte, clientIP, authHeader string) (any, *LogMetric, error) {
	var (
		errChan  = make(chan *ErrorResp, len(s.clients))
		respChan = make(chan *relaygrpc.RegisterValidatorResponse, len(s.clients))
		_err     *ErrorResp
	)

	parentSpan := trace.SpanFromContext(ctx)
	ctx = trace.ContextWithSpan(context.Background(), parentSpan)
	aKey := s.authKey
	if authHeader != "" {
		aKey = authHeader
	}
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", aKey)
	registerValidaorCtx, span := s.tracer.Start(ctx, "registerValidator")
	defer span.End()

	id := uuid.NewString()
	logMetric := NewLogMetric(
		[]zap.Field{
			zap.String("method", "registerValidator"),
			zap.String("clientIP", clientIP),
			zap.String("reqID", id),
			zap.String("traceID", parentSpan.SpanContext().TraceID().String()),
			zap.Time("receivedAt", receivedAt),
			zap.String("secretToken", s.secretToken),
		},
		[]attribute.KeyValue{
			attribute.String("method", "registerValidator"),
			attribute.String("clientIP", clientIP),
			attribute.String("reqID", id),
			attribute.String("traceID", parentSpan.SpanContext().TraceID().String()),
			attribute.Int64("receivedAt", receivedAt.Unix()),
			attribute.String("authHeader", authHeader),
		},
	)
	s.logger.Info("received", logMetric.fields...)

	// decode auth header
	_, decodeAuthSpan := s.tracer.Start(registerValidaorCtx, "decodeAuth")
	accountID, _, err := DecodeAuth(authHeader)
	if err != nil {
		logMetric.String("proxyError", fmt.Sprintf("failed to decode auth header %s", authHeader))
		logMetric.Error(err)
		s.logger.Warn("failed to decode auth header", logMetric.fields...)
		//zap.Error(err), 		return nil, nil, toErrorResp(http.StatusUnauthorized, "", fmt.Sprintf("failed to decode auth header: %v", authHeader), id, "invalid auth header", clientIP)
	}
	go decodeAuthSpan.End(trace.WithTimestamp(time.Now()))

	logMetric.String("accountID", accountID)
	span.SetAttributes(logMetric.attributes...)
	//TODO: For now using relay proxy auth-header to allow every validator to connect  But this needs to be updated in the future to  use validator auth header.
	_, spanWaitForResponse := s.tracer.Start(registerValidaorCtx, "waitForResponse")
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
				errChan <- toErrorResp(http.StatusInternalServerError, "relay returned error", zap.String("relayError", err.Error()), zap.String("url", c.URL))
				return
			}
			if out == nil {
				regSpan.SetStatus(otelcodes.Error, errors.New("empty response from relay").Error())
				errChan <- toErrorResp(http.StatusInternalServerError, "empty response from relay", zap.String("url", c.URL))
				return
			}
			if out.Code != uint32(codes.OK) {
				regSpan.SetStatus(otelcodes.Error, errors.New("relay returned failure response code").Error())
				errChan <- toErrorResp(http.StatusBadRequest, "relay returned failure response code", zap.String("relayError", out.Message), zap.String("url", c.URL))
				return
			}
			regSpan.SetStatus(otelcodes.Ok, "relay returned success response code")
			respChan <- out
		}(registrationClient)
	}
	go spanWaitForResponse.End(trace.WithTimestamp(time.Now()))

	_, spanWaitForSuccessfulResponse := s.tracer.Start(registerValidaorCtx, "waitForSuccessfulResponse")
	// Wait for the first successful response or until all responses are processed
	for i := 0; i < len(s.registrationClients); i++ {
		select {
		case <-ctx.Done():
			logMetric.Error(ctx.Err())
			logMetric.String("relayError", "failed to register")
			return nil, nil, toErrorResp(http.StatusInternalServerError, ctx.Err().Error(), logMetric.fields...)
		case _err = <-errChan:
			// if multiple client return errors, first error gets replaced by the subsequent errors
		case <-respChan:
			return struct{}{}, logMetric, nil
		}
	}
	go spanWaitForSuccessfulResponse.End(trace.WithTimestamp(time.Now()))
	logMetric.Error(errors.New(_err.Message))
	logMetric.Fields(_err.fields...)
	return nil, logMetric, _err
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
		attribute.String("traceID", parentSpan.SpanContext().TraceID().String()),
	)

	for {
		select {
		case <-ctx.Done():
			s.logger.Warn("stream header context cancelled",
				zap.String("traceID", parentSpan.SpanContext().TraceID().String()),
			)
			return
		default:
			if _, err := s.StreamHeader(ctx, client); err != nil {
				s.logger.Warn("failed to stream header. Sleeping 1 second and then reconnecting",
					zap.String("url", client.URL),
					zap.String("traceID", parentSpan.SpanContext().TraceID().String()),
					zap.Error(err))
				span.SetAttributes(attribute.KeyValue{Key: "sleepingFor", Value: attribute.Int64Value(1)}, attribute.KeyValue{Key: "error", Value: attribute.StringValue(err.Error())})
			} else {
				s.logger.Warn("stream header stopped.  Sleeping 1 second and then reconnecting",
					zap.String("url", client.URL),
					zap.String("traceID", parentSpan.SpanContext().TraceID().String()),
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
	streamHeaderCtx, span := s.tracer.Start(ctx, "streamHeaderFunction")
	defer span.End()
	id := uuid.NewString()
	client.nodeID = fmt.Sprintf("%v-%v-%v-%v", s.nodeID, client.URL, id, time.Now().UTC().Format("15:04:05.999999999"))
	stream, err := client.StreamHeader(ctx, &relaygrpc.StreamHeaderRequest{
		ReqId:       id,
		NodeId:      client.nodeID,
		Version:     s.version,
		SecretToken: s.secretToken,
	})
	logMetric := NewLogMetric(
		[]zap.Field{
			zap.String("method", "streamHeader"),
			zap.String("nodeID", client.nodeID),
			zap.String("reqID", id),
			zap.String("url", client.URL),
			zap.String("secretToken", s.secretToken),
			zap.String("traceID", parentSpan.SpanContext().TraceID().String()),
		},
		[]attribute.KeyValue{
			attribute.String("method", "streamHeader"),
			attribute.String("nodeID", client.nodeID),
			attribute.String("url", client.URL),
			attribute.String("traceID", parentSpan.SpanContext().TraceID().String()),
			attribute.String("reqID", id),
		},
	)
	span.SetAttributes(logMetric.attributes...)

	s.logger.Info("streaming headers", logMetric.fields...)
	if err != nil {
		logMetric.Error(err)
		s.logger.Warn("failed to stream header", logMetric.fields...)
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

	go func(lm LogMetric) {
		select {
		case <-stream.Context().Done():
			lm.Error(stream.Context().Err())
			s.logger.Warn("stream context cancelled, closing connection", lm.fields...)
			closeDone()
		case <-ctx.Done():
			logMetric.Error(ctx.Err())
			s.logger.Warn("context cancelled, closing connection", lm.fields...)
			closeDone()
		}
	}(*logMetric)

	_, streamReceiveSpan := s.tracer.Start(streamHeaderCtx, "streamReceive")
	defer streamReceiveSpan.End()

	for {
		select {
		case <-done:
			return nil, nil
		default:
		}
		header, err := stream.Recv()
		if err == io.EOF {
			s.logger.With(zap.Error(err)).Warn("stream received EOF", logMetric.fields...)
			span.SetStatus(otelcodes.Error, err.Error())
			closeDone()
			break
		}
		_s, ok := status.FromError(err)
		if !ok {
			s.logger.With(zap.Error(err)).Warn("invalid grpc error status", logMetric.fields...)
			span.SetStatus(otelcodes.Error, "invalid grpc error status")
			continue
		}

		if _s.Code() == codes.Canceled {
			logMetric.Error(err)
			s.logger.With(zap.Error(err)).Warn("received cancellation signal, shutting down", logMetric.fields...)
			// mark as canceled to stop the upstream retry loop
			span.SetStatus(otelcodes.Error, "received cancellation signal")
			closeDone()
			break
		}

		if _s.Code() != codes.OK {
			s.logger.With(zap.Error(_s.Err())).With(zap.String("code", _s.Code().String())).Warn("server unavailable,try reconnecting", logMetric.fields...)
			span.SetStatus(otelcodes.Error, "server unavailable,try reconnecting")
			closeDone()
			break
		}
		if err != nil {
			s.logger.With(zap.Error(err)).Warn("failed to receive stream, disconnecting the stream", logMetric.fields...)
			span.SetStatus(otelcodes.Error, err.Error())
			closeDone()
			break
		}
		// Added empty streaming as a temporary workaround to maintain streaming alive
		// TODO: this need to be handled by adding settings for keep alive params on both server and client
		if header.GetBlockHash() == "" {
			s.logger.Warn("received empty stream", logMetric.fields...)
			continue
		}
		k := s.keyForCachingBids(header.GetSlot(), header.GetParentHash(), header.GetPubkey())
		lm := new(LogMetric)
		lm.Fields(
			zap.String("keyForCachingBids", k),
			zap.Uint64("slot", header.GetSlot()),
			zap.String("parentHash", header.GetParentHash()),
			zap.String("blockHash", header.GetBlockHash()),
			zap.String("blockValue", new(big.Int).SetBytes(header.GetValue()).String()),
			zap.String("pubKey", header.GetPubkey()),
			zap.String("builderPubKey", header.GetBuilderPubkey()),
			zap.String("extraData", header.GetBuilderExtraData()),
			zap.String("traceID", parentSpan.SpanContext().TraceID().String()),
		)
		lm.Merge(logMetric)
		s.logger.Info("received header", lm.fields...)

		_, storeBidsSpan := s.tracer.Start(ctx, "storeBids")
		// store the bid for builder pubkey
		bid := &Bid{
			Value:            header.GetValue(),
			Payload:          header.GetPayload(),
			BlockHash:        header.GetBlockHash(),
			BuilderPubkey:    header.GetBuilderPubkey(),
			BuilderExtraData: header.GetBuilderExtraData(),
		}
		s.setBuilderBidForSlot(logMetric, k, header.GetBuilderPubkey(), bid) // run it in goroutine ?
		go storeBidsSpan.End(trace.WithTimestamp(time.Now()))
	}
	<-done

	s.logger.Warn("closing connection", logMetric.fields...)
	return nil, nil
}
func (s *Service) GetHeader(ctx context.Context, receivedAt time.Time, clientIP, slot, parentHash, pubKey, authHeader string) (any, *LogMetric, error) {
	parentSpan := trace.SpanFromContext(ctx)
	ctx = trace.ContextWithSpan(context.Background(), parentSpan)
	getHeaderctx, span := s.tracer.Start(ctx, "getHeader")
	defer span.End()
	startTime := time.Now().UTC()

	k := fmt.Sprintf("slot-%v-parentHash-%v-pubKey-%v", slot, parentHash, pubKey)
	id := uuid.NewString()
	logMetric := NewLogMetric(
		[]zap.Field{
			zap.String("method", getHeader),
			zap.Time("receivedAt", receivedAt),
			zap.String("clientIP", clientIP),
			zap.String("reqID", id),
			zap.String("key", k),
			zap.String("traceID", parentSpan.SpanContext().TraceID().String()),
			zap.String("slot", slot),
			zap.String("secretToken", s.secretToken),
			zap.String("authHeader", authHeader),
		},
		[]attribute.KeyValue{
			attribute.String("method", getHeader),
			attribute.String("clientIP", clientIP),
			attribute.String("req", id),
			attribute.Int64("receivedAt", receivedAt.Unix()),
			attribute.String("key", k),
			attribute.String("traceID", parentSpan.SpanContext().TraceID().String()),
			attribute.String("slot", slot),
			attribute.String("authHeader", authHeader),
		},
	)
	s.logger.Info("received", logMetric.fields...)
	// decode auth header
	accountID, _, err := DecodeAuth(authHeader)
	if err != nil {
		logMetric.String("proxyError", fmt.Sprintf("failed to decode auth header %s", authHeader))
		s.logger.Warn("failed to decode auth header", zap.Error(err), zap.String("authHeader", authHeader), zap.String("reqID", id), zap.String("clientIP", clientIP))
		//return nil, nil, toErrorResp(http.StatusUnauthorized, "", fmt.Sprintf("failed to decode auth header: %v", authHeader), id, "invalid auth header", clientIP)
	}
	logMetric.String("accountID", accountID)
	_slot, err := strconv.ParseUint(slot, 10, 64)
	if err != nil {
		logMetric.String("proxyError", fmt.Sprintf("invalid slot %v", slot))
		return nil, logMetric, toErrorResp(http.StatusNoContent, errInvalidSlot.Error())

	}

	slotStartTime := GetSlotStartTime(s.beaconGenesisTime, int64(_slot))
	msIntoSlot := time.Since(slotStartTime).Milliseconds()
	logMetric.Time("slotStartTime", slotStartTime)
	logMetric.Int64("msTntoSlot", msIntoSlot)

	span.SetAttributes(logMetric.attributes...)

	_, storingHeaderSpan := s.tracer.Start(getHeaderctx, "storingHeader")

	//TODO: send fluentd stats for StatusNoContent error cases

	if len(pubKey) != 98 {
		logMetric.String("proxyError", fmt.Sprintf("pub key should be %d long", 98))
		return nil, logMetric, toErrorResp(http.StatusNoContent, errInvalidPubkey.Error())
	}

	if len(parentHash) != 66 {
		logMetric.String("proxyError", fmt.Sprintf("pub key should be %d long", 98))
		return nil, logMetric, toErrorResp(http.StatusNoContent, errInvalidHash.Error())
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
				Slot:                     int64(_slot),
			}
			s.fluentD.LogToFluentD(fluentstats.Record{
				Type: typeRelayProxyGetHeader,
				Data: headerStats,
			}, time.Now().UTC(), s.NodeID(), statsRelayProxyGetHeader)
		}()
		logMetric.String("proxyError", msg)
		return nil, logMetric, toErrorResp(http.StatusNoContent, "Header value is not present")
	}
	go storingHeaderSpan.End(trace.WithTimestamp(time.Now()))

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
			Slot:                     int64(_slot),
		}
		s.fluentD.LogToFluentD(fluentstats.Record{
			Type: typeRelayProxyGetHeader,
			Data: headerStats,
		}, time.Now().UTC(), s.NodeID(), statsRelayProxyGetHeader)
	}()

	return json.RawMessage(slotBestHeader.Payload), logMetric, nil
}

func (s *Service) GetPayload(ctx context.Context, receivedAt time.Time, payload []byte, clientIP, authHeader string) (any, *LogMetric, error) {
	startTime := time.Now().UTC()
	id := uuid.NewString()
	parentSpan := trace.SpanFromContext(ctx)
	ctx = trace.ContextWithSpan(context.Background(), parentSpan)
	aKey := s.authKey
	if authHeader != "" {
		aKey = authHeader
	}
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", aKey)
	payloadCtx, span := s.tracer.Start(ctx, getPayload)
	defer span.End()

	logMetric := NewLogMetric(
		[]zap.Field{
			zap.String("method", getPayload),
			zap.Time("receivedAt", receivedAt),
			zap.String("clientIP", clientIP),
			zap.String("reqID", id),
			zap.String("traceID", parentSpan.SpanContext().TraceID().String()),
			zap.String("secretToken", s.secretToken),
			zap.String("authHeader", authHeader),
		},
		[]attribute.KeyValue{
			attribute.String("method", getPayload),
			attribute.String("clientIP", clientIP),
			attribute.String("reqID", id),
			attribute.Int64("receivedAt", receivedAt.Unix()),
			attribute.String("traceID", parentSpan.SpanContext().TraceID().String()),
			attribute.String("authHeader", authHeader),
			attribute.String("secretToken", s.secretToken),
		},
	)
	s.logger.Info("received", logMetric.fields...)
	span.SetAttributes(logMetric.attributes...)
	// decode auth header
	accountID, _, err := DecodeAuth(authHeader)
	if err != nil {
		logMetric.String("proxyError", fmt.Sprintf("failed to decode auth header %s", authHeader))
		s.logger.Warn("failed to decode auth header", zap.Error(err), zap.String("authHeader", authHeader), zap.String("reqID", id), zap.String("clientIP", clientIP))
		//return nil, nil, toErrorResp(http.StatusUnauthorized, "", fmt.Sprintf("failed to decode auth header: %v", authHeader), id, "invalid auth header", clientIP)
	}

	logMetric.String("accountID", accountID)

	//TODO: For now using relay proxy auth-header to allow every validator to connect  But this needs to be updated in the future to  use validator auth header.

	req := &relaygrpc.GetPayloadRequest{
		ReqId:       id,
		Payload:     payload,
		ClientIp:    clientIP,
		Version:     s.version,
		ReceivedAt:  timestamppb.New(receivedAt),
		SecretToken: s.secretToken,
	}
	var (
		errChan  = make(chan *ErrorResp, len(s.clients))
		respChan = make(chan *relaygrpc.GetPayloadResponse, len(s.clients))
		_err     *ErrorResp
	)
	payloadResponseCtx, payloadResponseSpan := s.tracer.Start(payloadCtx, "payloadResponseFromRelay")
	for _, client := range s.clients {
		go func(c *Client) {
			_, clientGetPayloadSpan := s.tracer.Start(ctx, "getPayloadForClient")
			defer clientGetPayloadSpan.End()
			clientCtx, cancel := context.WithTimeout(payloadResponseCtx, 3*time.Second)
			defer cancel()
			out, err := c.GetPayload(clientCtx, req)
			clientGetPayloadSpan.End(trace.WithTimestamp(time.Now()))
			if err != nil {
				span.SetStatus(otelcodes.Error, err.Error())
				errChan <- toErrorResp(http.StatusInternalServerError, "relay returned error", zap.String("relayError", err.Error()), zap.String("url", c.URL))
				return
			}
			if out == nil {
				span.SetStatus(otelcodes.Error, "empty response from relay")
				errChan <- toErrorResp(http.StatusInternalServerError, "empty response from relay", zap.String("url", c.URL))
				return
			}
			if out.Code != uint32(codes.OK) {
				span.SetStatus(otelcodes.Error, out.Message)
				errChan <- toErrorResp(http.StatusBadRequest, "relay returned failure response code", zap.String("relayError", out.Message), zap.String("url", c.URL))
				return
			}
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
					s.logger.With(logMetric.fields...).Warn("failed to decode getPayload request")
				} else {
					_slot, err := decodedPayload.Slot()
					if err != nil {
						s.logger.With(logMetric.fields...).Warn("failed to decode getPayload slot")
					} else {
						slot = int64(_slot)
						slotStartTime = GetSlotStartTime(s.beaconGenesisTime, slot)
						msIntoSlot = time.Since(slotStartTime).Milliseconds()

						_blockHash, err := decodedPayload.ExecutionBlockHash()
						if err != nil {
							s.logger.With(logMetric.fields...).Warn("failed to decode getPayload blockHash")
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
			logMetric.Error(ctx.Err())
			logMetric.String("relayError", "failed to getPayload")
			return nil, nil, toErrorResp(http.StatusInternalServerError, ctx.Err().Error(), zap.String("relayError", "failed to getPayload"))
		case _err = <-errChan:
			// if multiple client return errors, first error gets replaced by the subsequent errors
		case out := <-respChan:
			slotStartTime := GetSlotStartTime(s.beaconGenesisTime, int64(out.GetSlot()))
			msIntoSlot := time.Since(slotStartTime).Milliseconds()
			go func(_slotStartTime time.Time, _msIntoSlot int64) {
				payloadStats := getPayloadStatsRecord{
					RequestReceivedAt: receivedAt,
					Duration:          time.Since(startTime),
					SlotStartTime:     _slotStartTime,
					MsIntoSlot:        _msIntoSlot,
					Slot:              out.GetSlot(),
					ParentHash:        out.GetParentHash(),
					PubKey:            out.GetPubkey(),
					BlockHash:         out.GetBlockHash(),
					ReqID:             id,
					ClientIP:          clientIP,
					Succeeded:         true,
					NodeID:            s.nodeID,
				}
				s.fluentD.LogToFluentD(fluentstats.Record{
					Type: typeRelayProxyGetPayload,
					Data: payloadStats,
				}, time.Now().UTC(), s.NodeID(), statsRelayProxyGetPayload)
			}(slotStartTime, msIntoSlot)
			logMetric.Fields([]zap.Field{
				zap.Duration("duration", time.Since(startTime)),
				zap.Uint64("slot", out.GetSlot()),
				zap.Time("slotStartTime", slotStartTime),
				zap.Int64("msIntoSlot", msIntoSlot),
				zap.String("parentHash", out.GetParentHash()),
				zap.String("blockHash", out.GetBlockHash()),
			}...)
			return json.RawMessage(out.GetVersionedExecutionPayload()), logMetric, nil
		}
	}
	go payloadResponseSpan.End(trace.WithTimestamp(time.Now()))
	logMetric.Error(errors.New(_err.Message))
	logMetric.Fields(_err.fields...)
	return nil, nil, _err
}

func (s *Service) keyForCachingBids(slot uint64, parentHash string, proposerPubkey string) string {
	return fmt.Sprintf("%d_%s_%s", slot, strings.ToLower(parentHash), strings.ToLower(proposerPubkey))
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

func (s *Service) setBuilderBidForSlot(logMetric *LogMetric, cacheKey string, builderPubkey string, bid *Bid) {
	var builderBidsMap *syncmap.SyncMap[string, *Bid]

	// if the cache key does not exist, create a new syncmap and store it in the cache
	if entry, bidsMapFound := s.builderBidsForSlot.Get(cacheKey); !bidsMapFound {
		builderBidsMap = syncmap.NewStringMapOf[*Bid]()
		s.builderBidsForSlot.Set(cacheKey, builderBidsMap, cache.DefaultExpiration)
	} else {
		// otherwise use the existing syncmap
		builderBidsMap = entry.(*syncmap.SyncMap[string, *Bid])
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

func (s *Service) NodeID() string {
	return s.nodeID
}
