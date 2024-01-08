package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/go-chi/chi/v5"

	"go.uber.org/zap"
)

// Router paths
var (
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
	logger         *zap.Logger
	server         *http.Server
	svc            IService
	listenAddress  string
	getHeaderDelay int
	tracer         trace.Tracer
}

func New(logger *zap.Logger, svc *Service, listenAddress string, getHeaderDelay int, tracer trace.Tracer) *Server {
	return &Server{
		logger:         logger,
		svc:            svc,
		listenAddress:  listenAddress,
		getHeaderDelay: getHeaderDelay,
		tracer:         tracer,
	}
}

func (s *Server) Start() error {
	s.server = &http.Server{
		Addr:              s.listenAddress,
		Handler:           s.InitHandler(),
		ReadTimeout:       1500 * time.Millisecond,
		ReadHeaderTimeout: 600 * time.Millisecond,
		WriteTimeout:      3 * time.Second,
		IdleTimeout:       3 * time.Second,
	}
	err := s.server.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (s *Server) InitHandler() *chi.Mux {
	handler := chi.NewRouter()
	handler.With(s.middleWare).Get(pathStatus, s.HandleStatus)
	handler.With(s.middleWare).Post(pathRegisterValidator, s.HandleRegistration)
	handler.With(s.middleWare).Get(pathGetHeader, s.HandleGetHeader)
	handler.With(s.middleWare).Post(pathGetPayload, s.HandleGetPayload)
	s.logger.Info("Init mev-relay-proxy")
	return handler
}

func (s *Server) middleWare(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}

func (s *Server) Stop() {
	if s.server != nil {
		_ = s.server.Shutdown(context.Background())
	}
}

func (s *Server) HandleStatus(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{}`)
}

func (s *Server) HandleRegistration(w http.ResponseWriter, r *http.Request) {
	receivedAt := time.Now().UTC()
	clientIP := GetIPXForwardedFor(r)
	authHeader := r.Header.Get("authorization")
	bodyBytes, err := io.ReadAll(r.Body)

	if err != nil {
		respondError(registration, w, toErrorResp(http.StatusInternalServerError, err.Error(), "", "could not read registration", ""), s.logger, nil)
		return
	}

	out, metaData, err := s.svc.RegisterValidator(r.Context(), receivedAt, bodyBytes, clientIP, authHeader)
	if err != nil {
		respondError(registration, w, err, s.logger, metaData)
		return
	}
	respondOK(registration, w, out, s.logger, metaData)
}

func (s *Server) HandleGetHeader(w http.ResponseWriter, r *http.Request) {
	receivedAt := time.Now().UTC()
	slot := chi.URLParam(r, "slot")
	parentHash := chi.URLParam(r, "parent_hash")
	pubKey := chi.URLParam(r, "pubkey")
	clientIP := GetIPXForwardedFor(r)
	<-time.After(time.Millisecond * time.Duration(s.getHeaderDelay))
	out, metaData, err := s.svc.GetHeader(r.Context(), receivedAt, clientIP, slot, parentHash, pubKey)
	if err != nil {
		respondError(getHeader, w, err, s.logger, metaData)
		return
	}
	respondOK(getHeader, w, out, s.logger, metaData)
}

func (s *Server) HandleGetPayload(w http.ResponseWriter, r *http.Request) {
	receivedAt := time.Now().UTC()
	clientIP := GetIPXForwardedFor(r)

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		respondError(getPayload, w, toErrorResp(http.StatusInternalServerError, err.Error(), "", "could not read getPayload", ""), s.logger, nil)
		return
	}
	out, metaData, err := s.svc.GetPayload(r.Context(), receivedAt, bodyBytes, clientIP)
	if err != nil {
		respondError(getPayload, w, err, s.logger, metaData)
		return
	}
	respondOK(getPayload, w, out, s.logger, metaData)
}

func respondOK(method string, w http.ResponseWriter, response any, log *zap.Logger, metaData any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Error("couldn't write OK response", zap.Error(err))
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	var meta string
	if metaData != nil {
		meta = metaData.(string)
	}
	log.Info(fmt.Sprintf("%s succeeded", method), zap.String("metaData", meta))
}

func respondError(method string, w http.ResponseWriter, err error, log *zap.Logger, metaData any) {
	resp := err.(*ErrorResp)
	var meta string
	if metaData != nil {
		meta = metaData.(string)
	}
	w.WriteHeader(resp.Code)
	log.With(zap.String("req_id", resp.BlxrMessage.reqID), zap.String("blxr_message", resp.BlxrMessage.msg), zap.String("client_ip", resp.BlxrMessage.clientIP), zap.String("resp_message", resp.Message), zap.Int("resp_code", resp.Code)).Error(fmt.Sprintf("%s failed", method), zap.String("metaData", meta))
	if resp.Message != "" {
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			log.With(zap.String("req_id", resp.BlxrMessage.reqID), zap.String("blxr_message", resp.BlxrMessage.msg), zap.String("client_ip", resp.BlxrMessage.clientIP), zap.String("resp_message", resp.Message), zap.Int("resp_code", resp.Code)).Error("couldn't write error response", zap.Error(err), zap.String("metaData", meta))
			http.Error(w, "", http.StatusInternalServerError)
		}
	}
}
