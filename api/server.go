package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/bloXroute-Labs/mev-relay-proxy/fluentstats"
	"github.com/go-chi/chi"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// Router paths
var (
	pathIndex             = "/"
	pathStatus            = "/eth/v1/builder/status"
	pathRegisterValidator = "/eth/v1/builder/validators"
	pathGetHeader         = "/eth/v1/builder/header/{slot:[0-9]+}/{parent_hash:0x[a-fA-F0-9]+}/{pubkey:0x[a-fA-F0-9]+}"
	pathGetPayload        = "/eth/v1/builder/blinded_blocks"

	AuthHeaderPrefix = "bearer "

	// methods
	getHeader    = "getHeader"
	getPayload   = "getPayload"
	registration = "registration"
)

type Server struct {
	logger        *zap.Logger
	server        *http.Server
	svc           IService
	listenAddress string

	getHeaderDelay    int64
	getHeaderMaxDelay int64
	beaconGenesisTime int64

	tracer  trace.Tracer
	fluentD fluentstats.Stats
}

func New(logger *zap.Logger, svc *Service, listenAddress string, getHeaderDelay, getHeaderMaxDelay, beaconGenesisTime int64, tracer trace.Tracer, fluentD fluentstats.Stats) *Server {
	return &Server{
		logger:        logger,
		svc:           svc,
		listenAddress: listenAddress,

		getHeaderDelay:    getHeaderDelay,
		getHeaderMaxDelay: getHeaderMaxDelay,
		beaconGenesisTime: beaconGenesisTime,

		tracer:  tracer,
		fluentD: fluentD,
	}
}

func (s *Server) Start() error {
	s.server = &http.Server{
		Addr:              s.listenAddress,
		Handler:           s.InitHandler(),
		ReadTimeout:       0,
		ReadHeaderTimeout: 0,
		WriteTimeout:      0,
		IdleTimeout:       10 * time.Second,
	}
	err := s.server.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (s *Server) InitHandler() *chi.Mux {
	handler := chi.NewRouter()
	handler.Get(pathIndex, s.HandleStatus)
	handler.Get(pathStatus, s.HandleStatus)
	handler.Post(pathRegisterValidator, s.HandleRegistration)
	handler.Get(pathGetHeader, s.HandleGetHeader)
	handler.Post(pathGetPayload, s.HandleGetPayload)
	s.logger.Info("Init mev-relay-proxy")
	return handler
}

func (s *Server) Stop() {
	if s.server != nil {
		_ = s.server.Shutdown(context.Background())
	}
}

func (s *Server) HandleStatus(w http.ResponseWriter, req *http.Request) {
	parentSpan := trace.SpanFromContext(req.Context())
	parentSpanCtx := trace.ContextWithSpan(context.Background(), parentSpan)
	_, span := s.tracer.Start(parentSpanCtx, "HandleStatus")
	defer span.End()
	parentSpan.SetAttributes(
		attribute.String("req_id", "req_id"),
		attribute.String("blxr_message", "blxr_message"),
		attribute.String("client_ip", "client_ip"),
		attribute.String("resp_message", "resp_message"),
		attribute.String("tracer_id", span.SpanContext().TraceID().String()),
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{}`))
}

func (s *Server) HandleRegistration(w http.ResponseWriter, r *http.Request) {
	parentSpan := trace.SpanFromContext(r.Context())
	parentSpanCtx := trace.ContextWithSpan(context.Background(), parentSpan)
	_, span := s.tracer.Start(parentSpanCtx, "HandleRegistration")
	defer span.End()

	receivedAt := time.Now().UTC()
	clientIP := GetIPXForwardedFor(r)
	authHeader := getAuth(r)
	bodyBytes, err := io.ReadAll(r.Body)

	span.SetAttributes(
		attribute.String("req_id", "req_id"),
		attribute.String("blxr_message", "blxr_message"),
		attribute.String("client_ip", "client_ip"),
		attribute.String("resp_message", "resp_message"),
	)

	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		respondError(registration, w, toErrorResp(http.StatusInternalServerError, "", err.Error(), "", "could not read registration", ""), s.logger, nil, s.tracer)
		return
	}

	out, metaData, err := s.svc.RegisterValidator(r.Context(), receivedAt, bodyBytes, clientIP, authHeader)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		respondError(registration, w, err, s.logger, metaData, s.tracer)
		return
	}
	respondOK(registration, w, out, s.logger, metaData, s.tracer)
}

func (s *Server) HandleGetHeader(w http.ResponseWriter, r *http.Request) {
	parentSpan := trace.SpanFromContext(r.Context())
	parentSpanCtx := trace.ContextWithSpan(context.Background(), parentSpan)
	_, span := s.tracer.Start(parentSpanCtx, "HandleGetHeader")
	defer span.End()

	receivedAt := time.Now().UTC()
	slot := chi.URLParam(r, "slot")
	parentHash := chi.URLParam(r, "parent_hash")
	pubKey := chi.URLParam(r, "pubkey")
	clientIP := GetIPXForwardedFor(r)

	slotInt := s.AToI(slot)
	slotStartTime := s.GetSlotStartTime(slotInt)

	sleep, maxSleep := s.GetSleepParams(r, s.getHeaderDelay, s.getHeaderMaxDelay)

	span.SetAttributes(
		attribute.String("req_id", "req_id"),
		attribute.String("blxr_message", "blxr_message"),
		attribute.String("client_ip", "client_ip"),
		attribute.String("resp_message", "resp_message"),
		attribute.Int64("slotStartTimeUnix", slotStartTime.Unix()),
		attribute.String("slotStartTime", slotStartTime.UTC().String()),
		attribute.Int64("slot", slotInt),
		attribute.Int64("sleep", sleep),
		attribute.Int64("maxSleep", maxSleep),
		attribute.String("parentHash", parentHash),
		attribute.String("pubKey", pubKey),
		attribute.String("tracer_id", span.SpanContext().TraceID().String()),
	)

	maxSleepTime := slotStartTime.Add(time.Duration(maxSleep) * time.Millisecond)
	if time.Now().UTC().Add(time.Duration(sleep) * time.Millisecond).After(maxSleepTime) {
		time.Sleep(maxSleepTime.Sub(time.Now().UTC()))
	} else {
		time.Sleep(time.Duration(sleep) * time.Millisecond)
	}

	out, metaData, err := s.svc.GetHeader(r.Context(), receivedAt, clientIP, slot, parentHash, pubKey)
	span.AddEvent("GetHeader")
	if err != nil {
		respondError(getHeader, w, err, s.logger, metaData, s.tracer)
		return
	}
	respondOK(getHeader, w, out, s.logger, metaData, s.tracer)
}

func (s *Server) HandleGetPayload(w http.ResponseWriter, r *http.Request) {
	parentSpan := trace.SpanFromContext(r.Context())
	parentSpanCtx := trace.ContextWithSpan(context.Background(), parentSpan)
	_, span := s.tracer.Start(parentSpanCtx, "HandleGetPayload")
	defer span.End()

	receivedAt := time.Now().UTC()
	clientIP := GetIPXForwardedFor(r)

	span.SetAttributes(
		attribute.String("req_id", "req_id"),
		attribute.String("blxr_message", "blxr_message"),
		attribute.String("client_ip", "client_ip"),
		attribute.String("resp_message", "resp_message"),
		attribute.String("tracer_id", span.SpanContext().TraceID().String()),
	)

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		respondError(getPayload, w, toErrorResp(http.StatusInternalServerError, "", err.Error(), "", "could not read getPayload", ""), s.logger, nil, s.tracer)
		return
	}
	out, metaData, err := s.svc.GetPayload(r.Context(), receivedAt, bodyBytes, clientIP)
	span.AddEvent("GetPayload")
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		respondError(getPayload, w, err, s.logger, metaData, s.tracer)
		return
	}
	respondOK(getPayload, w, out, s.logger, metaData, s.tracer)
}

func respondOK(method string, w http.ResponseWriter, response any, log *zap.Logger, metaData any, tracer trace.Tracer) {
	_, span := tracer.Start(context.Background(), "respondOK-main")
	defer span.End()

	span.SetAttributes(
		attribute.String("req_id", "req_id"),
		attribute.String("blxr_message", "blxr_message"),
		attribute.String("client_ip", "client_ip"),
		attribute.String("resp_message", "resp_message"),
		attribute.String("tracer_id", span.SpanContext().TraceID().String()),
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Error("couldn't write OK response", zap.Error(err))
		http.Error(w, "", http.StatusInternalServerError)
		span.SetStatus(codes.Error, "couldn't write OK response")
		return
	}
	var meta string
	if metaData != nil {
		meta = metaData.(string)
	}
	span.End()
	log.Info(fmt.Sprintf("%s succeeded", method), zap.String("metaData", meta))
}

func respondError(method string, w http.ResponseWriter, err error, log *zap.Logger, metaData any, tracer trace.Tracer) {
	_, span := tracer.Start(context.Background(), "respondError-main")
	defer span.End()

	span.SetAttributes(
		attribute.String("req_id", "req_id"),
		attribute.String("blxr_message", "blxr_message"),
		attribute.String("client_ip", "client_ip"),
		attribute.String("resp_message", "resp_message"),
		attribute.String("tracer_id", span.SpanContext().TraceID().String()),
	)

	var meta string
	if metaData != nil {
		meta = metaData.(string)
	}
	resp, ok := err.(*ErrorResp)
	if !ok {
		log.With(zap.String("req_id", resp.BlxrMessage.reqID),
			zap.String("client_ip", resp.BlxrMessage.clientIP),
			zap.String("blxr_message", resp.BlxrMessage.msg),
			zap.String("relay_message", resp.BlxrMessage.relayMsg),
			zap.String("proxy_message", resp.BlxrMessage.proxyMsg),
			zap.String("resp_message", resp.Message),
			zap.Int("resp_code", resp.Code)).Error("failed to typecast error response", zap.String("metaData", meta))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		span.SetStatus(codes.Error, "failed to typecast error response")
		return
	}

	w.WriteHeader(resp.Code)
	log.With(zap.String("req_id", resp.BlxrMessage.reqID),
		zap.String("client_ip", resp.BlxrMessage.clientIP),
		zap.String("blxr_message", resp.BlxrMessage.msg),
		zap.String("relay_message", resp.BlxrMessage.relayMsg),
		zap.String("proxy_message", resp.BlxrMessage.proxyMsg),
		zap.String("resp_message", resp.Message),
		zap.Int("resp_code", resp.Code)).Error(fmt.Sprintf("%s failed", method), zap.String("metaData", meta))
	if resp.Message != "" && resp.Code != http.StatusNoContent { // HTTP status "No Content" implies that no message body should be included in the response.
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			log.With(zap.String("req_id", resp.BlxrMessage.reqID),
				zap.String("blxr_message", resp.BlxrMessage.msg),
				zap.String("client_ip", resp.BlxrMessage.clientIP),
				zap.String("relay_message", resp.BlxrMessage.relayMsg),
				zap.String("proxy_message", resp.BlxrMessage.proxyMsg),
				zap.String("resp_message", resp.Message),
				zap.Int("resp_code", resp.Code)).Error("couldn't write error response", zap.Error(err), zap.String("metaData", meta))
			_, _ = w.Write([]byte(``))
			span.SetStatus(codes.Error, "couldn't write error response")
			return
		}
	}
}
