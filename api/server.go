package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/bloXroute-Labs/mev-relay-proxy/fluentstats"
	"github.com/go-chi/chi/v5"
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
	_, span := s.tracer.Start(parentSpanCtx, "handleStatus")
	defer span.End()
	span.SetAttributes(
		attribute.String("reqHost", req.Host),
		attribute.String("method", req.Method),
		attribute.String("remoteAddr", req.RemoteAddr),
		attribute.String("requestURI", req.RequestURI),
		attribute.String("authHeader", getAuth(req)),
		attribute.String("traceID", span.SpanContext().TraceID().String()),
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{}`))
}

func (s *Server) HandleRegistration(w http.ResponseWriter, r *http.Request) {
	parentSpan := trace.SpanFromContext(r.Context())
	parentSpanCtx := trace.ContextWithSpan(context.Background(), parentSpan)
	_, span := s.tracer.Start(parentSpanCtx, "handleRegistration")
	defer span.End()

	receivedAt := time.Now().UTC()
	clientIP := GetIPXForwardedFor(r)
	authHeader := getAuth(r)

	logMetric := NewLogMetric(
		[]zap.Field{
			zap.String("reqHost", r.Host),
			zap.String("method", r.Method),
			zap.String("clientIP", clientIP),
			zap.String("remoteAddr", r.RemoteAddr),
			zap.String("requestURI", r.RequestURI),
			zap.String("authHeader", authHeader),
			zap.String("traceID", span.SpanContext().TraceID().String()),
		},
		[]attribute.KeyValue{
			attribute.String("reqHost", r.Host),
			attribute.String("method", r.Method),
			attribute.String("clientIP", clientIP),
			attribute.String("remoteAddr", r.RemoteAddr),
			attribute.String("requestURI", r.RequestURI),
			attribute.String("authHeader", authHeader),
			attribute.String("traceID", span.SpanContext().TraceID().String()),
		},
	)
	span.SetAttributes(logMetric.attributes...)
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		logMetric.String("proxyError", err.Error())
		logMetric.Error(errors.New("could not read registration"))
		respondError(registration, w, toErrorResp(http.StatusInternalServerError, "could not read registration"), s.logger, s.tracer, logMetric)
		return
	}

	out, lm, err := s.svc.RegisterValidator(r.Context(), receivedAt, bodyBytes, clientIP, authHeader)
	logMetric.Merge(lm)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		respondError(registration, w, err, s.logger, s.tracer, logMetric)
		return
	}

	respondOK(registration, w, out, s.logger, s.tracer, logMetric)
}

func (s *Server) HandleGetHeader(w http.ResponseWriter, r *http.Request) {
	parentSpan := trace.SpanFromContext(r.Context())
	parentSpanCtx := trace.ContextWithSpan(context.Background(), parentSpan)
	_, span := s.tracer.Start(parentSpanCtx, "handleGetHeader")
	defer span.End()

	receivedAt := time.Now().UTC()
	slot := chi.URLParam(r, "slot")
	parentHash := chi.URLParam(r, "parent_hash")
	pubKey := chi.URLParam(r, "pubkey")
	clientIP := GetIPXForwardedFor(r)
	authHeader := getAuth(r)
	slotInt := s.AToI(slot)
	slotStartTime := GetSlotStartTime(s.beaconGenesisTime, slotInt)

	sleep, maxSleep := s.GetSleepParams(r, s.getHeaderDelay, s.getHeaderMaxDelay)
	logMetric := NewLogMetric(
		[]zap.Field{
			zap.String("reqHost", r.Host),
			zap.String("method", r.Method),
			zap.String("remoteAddr", r.RemoteAddr),
			zap.String("requestURI", r.RequestURI),
			zap.String("clientIP", clientIP),
			zap.String("authHeader", authHeader),
			zap.String("traceID", span.SpanContext().TraceID().String()),
			zap.Int64("slotStartTimeUnix", slotStartTime.Unix()),
			zap.String("slotStartTime", slotStartTime.UTC().String()),
			zap.Int64("slot", slotInt),
			zap.Int64("sleep", sleep),
			zap.Int64("maxSleep", maxSleep),
			zap.String("parentHash", parentHash),
			zap.String("pubKey", pubKey),
			zap.String("traceID", span.SpanContext().TraceID().String()),
		},
		[]attribute.KeyValue{
			attribute.String("reqHost", r.Host),
			attribute.String("method", r.Method),
			attribute.String("clientIP", clientIP),
			attribute.String("remoteAddr", r.RemoteAddr),
			attribute.String("requestURI", r.RequestURI),
			attribute.String("authHeader", authHeader),
			attribute.Int64("slotStartTimeUnix", slotStartTime.Unix()),
			attribute.String("slotStartTime", slotStartTime.UTC().String()),
			attribute.Int64("slot", slotInt),
			attribute.Int64("sleep", sleep),
			attribute.Int64("maxSleep", maxSleep),
			attribute.String("parentHash", parentHash),
			attribute.String("pubKey", pubKey),
			attribute.String("traceID", span.SpanContext().TraceID().String()),
		},
	)
	span.SetAttributes(logMetric.attributes...)

	maxSleepTime := slotStartTime.Add(time.Duration(maxSleep) * time.Millisecond)
	if time.Now().UTC().Add(time.Duration(sleep) * time.Millisecond).After(maxSleepTime) {
		time.Sleep(maxSleepTime.Sub(time.Now().UTC()))
	} else {
		time.Sleep(time.Duration(sleep) * time.Millisecond)
	}

	span.AddEvent("getHeader")
	out, lm, err := s.svc.GetHeader(r.Context(), receivedAt, clientIP, slot, parentHash, pubKey, authHeader)
	logMetric.Merge(lm)
	if err != nil {
		respondError(getHeader, w, err, s.logger, s.tracer, logMetric)
		return
	}
	respondOK(getHeader, w, out, s.logger, s.tracer, logMetric)
}

func (s *Server) HandleGetPayload(w http.ResponseWriter, r *http.Request) {
	parentSpan := trace.SpanFromContext(r.Context())
	parentSpanCtx := trace.ContextWithSpan(context.Background(), parentSpan)
	_, span := s.tracer.Start(parentSpanCtx, "handleGetPayload")
	defer span.End()

	receivedAt := time.Now().UTC()
	clientIP := GetIPXForwardedFor(r)
	authHeader := getAuth(r)
	logMetric := NewLogMetric(
		[]zap.Field{
			zap.String("reqHost", r.Host),
			zap.String("method", r.Method),
			zap.String("remoteAddr", r.RemoteAddr),
			zap.String("requestURI", r.RequestURI),
			zap.String("authHeader", authHeader),
			zap.String("clientIP", clientIP),
			zap.String("traceID", span.SpanContext().TraceID().String()),
		},
		[]attribute.KeyValue{
			attribute.String("reqHost", r.Host),
			attribute.String("method", r.Method),
			attribute.String("clientIP", clientIP),
			attribute.String("remoteAddr", r.RemoteAddr),
			attribute.String("requestURI", r.RequestURI),
			attribute.String("authHeader", authHeader),
			attribute.String("traceID", span.SpanContext().TraceID().String()),
		},
	)
	span.SetAttributes(logMetric.attributes...)

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		logMetric.String("proxyError", err.Error())
		logMetric.Error(errors.New("could not read registration"))
		respondError(getPayload, w, toErrorResp(http.StatusInternalServerError, "could not read registration"), s.logger, s.tracer, logMetric)
		return
	}
	span.AddEvent("getPayload")
	out, lm, err := s.svc.GetPayload(r.Context(), receivedAt, bodyBytes, clientIP, authHeader)
	logMetric.Merge(lm)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		respondError(getPayload, w, err, s.logger, s.tracer, logMetric)
		return
	}
	respondOK(getPayload, w, out, s.logger, s.tracer, logMetric)
}
func respondOK(method string, w http.ResponseWriter, response any, log *zap.Logger, tracer trace.Tracer, logMetric *LogMetric) {
	_, span := tracer.Start(context.Background(), "respondOK-main")
	defer span.End()
	logMetric.Attributes(
		attribute.String("method", method),
		attribute.Int("responseCode", 200),
		attribute.String("traceID", span.SpanContext().TraceID().String()),
	)
	span.SetAttributes(logMetric.attributes...)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		span.SetStatus(codes.Error, "couldn't write OK response")
		log.With(logMetric.fields...).Error("couldn't write OK response", zap.Error(err))
		http.Error(w, "", http.StatusInternalServerError)

		span.End()
		return
	}
	log.With(zap.String("method", method)).With(logMetric.fields...).Info(fmt.Sprintf("%s succeeded", method))

}

func respondError(method string, w http.ResponseWriter, err error, log *zap.Logger, tracer trace.Tracer, logMetric *LogMetric) {

	_, span := tracer.Start(context.Background(), "respondError-main")
	defer span.End()
	logMetric.Attributes(
		attribute.String("method", method),
		attribute.String("err", err.Error()),
		attribute.String("traceID", span.SpanContext().TraceID().String()),
	)
	span.SetAttributes(logMetric.attributes...)

	resp, ok := err.(*ErrorResp)
	span.SetAttributes(attribute.Int("responseCode", resp.ErrorCode()))
	if !ok {
		log.With(zap.String("method", method)).With(logMetric.fields...).Error("failed to typecast error response")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		span.SetStatus(codes.Error, "failed to typecast error response")
		return
	}
	w.WriteHeader(resp.Code)
	log.With(zap.String("method", method)).With(logMetric.fields...).Error(fmt.Sprintf("%s failed", method))
	if resp.Message != "" && resp.Code != http.StatusNoContent { // HTTP status "No Content" implies that no message body should be included in the response.
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			span.SetStatus(codes.Error, "couldn't write error response")
			log.With(zap.String("method", method)).With(logMetric.fields...).Error("couldn't write error response", zap.Error(err))
			_, _ = w.Write([]byte(``))
			return
		}
	}
}
