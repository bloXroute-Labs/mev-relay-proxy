package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/zap"
)

type MockService struct {
	logger                *zap.Logger
	RegisterValidatorFunc func(ctx context.Context, payload []byte, clientIP, authKey, validatorID string) (interface{}, *LogMetric, error)
	GetHeaderFunc         func(ctx context.Context, clientIP, slot, parentHash, pubKey, authHeader, validatorID string) (any, *LogMetric, error)
	GetPayloadFunc        func(ctx context.Context, payload []byte, clientIP, authHeader, validatorID string) (any, *LogMetric, error)
	NodeIDFunc            func() string
}

var _ IService = (*MockService)(nil)

func (m *MockService) RegisterValidator(ctx context.Context, receivedAt time.Time, payload []byte, clientIP, authHeader, validatorID string) (any, *LogMetric, error) {
	if m.RegisterValidatorFunc != nil {
		return m.RegisterValidatorFunc(ctx, payload, clientIP, authHeader, validatorID)
	}
	return nil, new(LogMetric), nil
}
func (m *MockService) GetHeader(ctx context.Context, receivedAt time.Time, clientIP, slot, parentHash, pubKey, authHeader, validatorID string) (any, *LogMetric, error) {
	if m.GetHeaderFunc != nil {
		return m.GetHeaderFunc(ctx, clientIP, slot, parentHash, pubKey, authHeader, validatorID)
	}
	return nil, new(LogMetric), nil
}

func (m *MockService) GetPayload(ctx context.Context, receivedAt time.Time, payload []byte, clientIP, authHeader, validatorID string) (any, *LogMetric, error) {
	if m.GetPayloadFunc != nil {
		return m.GetPayloadFunc(ctx, payload, clientIP, authHeader, validatorID)
	}
	return nil, new(LogMetric), nil
}

func (m *MockService) NodeID() string {
	if m.NodeIDFunc != nil {
		return m.NodeIDFunc()
	}
	return ""
}

func TestServer_HandleRegistration(t *testing.T) {
	testCases := map[string]struct {
		requestBody   []byte
		url           string
		mockService   *MockService
		expectedCode  int
		expectedError string
	}{
		"When registration succeeded": {
			requestBody: []byte(`{"key": "value"}`),
			url:         "/eth/v1/builder/validators?id=VG&auth=YmY1YzVkMWItNzAzMC00ZjA1LTlhYzMtMjE3MDk1ZTlkMmI2OjFmNGIwZjU5ZGYwNDM1MWQ2ZWRkOGUxYjU2ZTk3MTNh",
			mockService: &MockService{
				logger: zap.NewNop(),
				RegisterValidatorFunc: func(ctx context.Context, payload []byte, clientIP, authKey, validatorID string) (interface{}, *LogMetric, error) {
					return nil, nil, nil

				},
			},
			expectedCode:  http.StatusOK,
			expectedError: "",
		},
		"Registration should succeed when url contains escape char": {
			requestBody: []byte(`{"key": "value"}`),
			url:         "/eth/v1/builder/validators?id=VG%26auth=YmY1YzVkMWItNzAzMC00ZjA1LTlhYzMtMjE3MDk1ZTlkMmI2OjFmNGIwZjU5ZGYwNDM1MWQ2ZWRkOGUxYjU2ZTk3MTNh%26sleep=600%26max_sleep=1200",
			mockService: &MockService{
				logger: zap.NewNop(),
				RegisterValidatorFunc: func(ctx context.Context, payload []byte, clientIP, authKey, validatorID string) (interface{}, *LogMetric, error) {
					return nil, nil, nil

				},
			},
			expectedCode:  http.StatusOK,
			expectedError: "",
		},
		"When registration failed": {
			requestBody: []byte(`{"key": "value"}`),
			url:         "/eth/v1/builder/validators",
			mockService: &MockService{
				logger: zap.NewNop(),
				RegisterValidatorFunc: func(ctx context.Context, payload []byte, clientIP, authKey, validatorID string) (interface{}, *LogMetric, error) {
					return nil, nil, toErrorResp(http.StatusInternalServerError, "failed to register")
				},
			},
			expectedCode:  http.StatusInternalServerError,
			expectedError: "failed to register",
		},
	}

	for testName, tc := range testCases {
		t.Run(testName, func(t *testing.T) {
			req, err := http.NewRequest("POST", tc.url, bytes.NewBuffer(tc.requestBody))
			if err != nil {
				t.Fatal(err)
			}
			rr := httptest.NewRecorder()
			server := &Server{svc: tc.mockService, logger: zap.NewNop(), tracer: noop.NewTracerProvider().Tracer("test")}
			server.HandleRegistration(rr, req)

			assert.Equal(t, rr.Code, tc.expectedCode)
			out := new(ErrorResp)
			err = json.NewDecoder(rr.Body).Decode(out)
			assert.NoError(t, err)
			if tc.expectedError != "" {
				assert.Equal(t, out.Message, tc.expectedError)
			}
		})
	}
}

func TestServer_HandleGetHeader(t *testing.T) {
	testCases := map[string]struct {
		slot           string
		parentHash     string
		pubKey         string
		mockService    *MockService
		expectedCode   int
		expectedOutput string
	}{
		"when getHeader succeeded": {
			slot:       "123",
			parentHash: "ph123",
			pubKey:     "pk123",
			mockService: &MockService{
				logger: zap.NewNop(),
				GetHeaderFunc: func(ctx context.Context, clientIP, slot, parentHash, pubKey, authHeader, validatorID string) (interface{}, *LogMetric, error) {

					return "getHeader", nil, nil
				},
			},
			expectedCode:   http.StatusOK,
			expectedOutput: "getHeader",
		},
		"when getHeader failed": {
			slot:       "456",
			parentHash: "ph456",
			pubKey:     "pk456",
			mockService: &MockService{
				logger: zap.NewNop(),
				GetHeaderFunc: func(ctx context.Context, clientIP, slot, parentHash, pubKey, authHeader, validatorID string) (interface{}, *LogMetric, error) {
					return nil, nil, &ErrorResp{Code: http.StatusNoContent}
				},
			},
			expectedCode:   http.StatusNoContent,
			expectedOutput: "",
		},
		"when getHeader failed with requested header not found": {
			slot:       "456",
			parentHash: "ph456",
			pubKey:     "pk456",
			mockService: &MockService{
				logger: zap.NewNop(),
				GetHeaderFunc: func(ctx context.Context, clientIP, slot, parentHash, pubKey, authHeader, validatorID string) (interface{}, *LogMetric, error) {
					return nil, nil, &ErrorResp{Code: http.StatusNoContent, Message: "header value is not present for the requested key slot"}
				},
			},
			expectedCode:   http.StatusNoContent,
			expectedOutput: "",
		},
	}

	for testName, tc := range testCases {
		t.Run(testName, func(t *testing.T) {
			req, err := http.NewRequest("GET", fmt.Sprintf("/eth/v1/builder/header/%s/%s/%s", tc.slot, tc.parentHash, tc.pubKey), nil)
			if err != nil {
				t.Fatal(err)
			}
			rr := httptest.NewRecorder()
			server := &Server{svc: tc.mockService, logger: zap.NewNop(), tracer: noop.NewTracerProvider().Tracer("test")}

			server.HandleGetHeader(rr, req)

			assert.Equal(t, rr.Code, tc.expectedCode)
			if tc.expectedOutput != "" {
				out := strings.TrimSpace(rr.Body.String())
				out = strings.Trim(out, "\"")
				assert.Equal(t, out, tc.expectedOutput)
				return
			}
			assert.Equal(t, rr.Body.String(), tc.expectedOutput)
		})
	}
}

func TestServer_HandleGetPayload(t *testing.T) {
	testCases := map[string]struct {
		requestBody   []byte
		mockService   *MockService
		expectedCode  int
		expectedError string
	}{
		"When getPayload succeeded": {
			requestBody: []byte(`{"key": "value"}`),
			mockService: &MockService{
				logger: zap.NewNop(),
				GetPayloadFunc: func(ctx context.Context, payload []byte, clientIP, authHeader, validatorID string) (any, *LogMetric, error) {
					return nil, nil, nil
				},
			},
			expectedCode:  http.StatusOK,
			expectedError: "",
		},
		"When getPayload failed": {
			requestBody: []byte(`{"key": "value"}`),
			mockService: &MockService{
				logger: zap.NewNop(),
				GetPayloadFunc: func(ctx context.Context, payload []byte, clientIP, authHeader, validatorID string) (any, *LogMetric, error) {
					return nil, nil, toErrorResp(http.StatusInternalServerError, "failed to getPayload")
				},
			},
			expectedCode:  http.StatusInternalServerError,
			expectedError: "failed to getPayload",
		},
	}

	for testName, tc := range testCases {
		t.Run(testName, func(t *testing.T) {
			req, err := http.NewRequest("POST", "/eth/v1/builder/blinded_blocks", bytes.NewBuffer(tc.requestBody))
			if err != nil {
				t.Fatal(err)
			}
			rr := httptest.NewRecorder()
			server := &Server{svc: tc.mockService, logger: zap.NewNop(), tracer: noop.NewTracerProvider().Tracer("test")}
			server.HandleGetPayload(rr, req)

			assert.Equal(t, rr.Code, tc.expectedCode)
			out := new(ErrorResp)
			err = json.NewDecoder(rr.Body).Decode(out)
			assert.NoError(t, err)
			if tc.expectedError != "" {
				assert.Equal(t, out.Message, tc.expectedError)
			}
		})
	}
}

func TestNodeID(t *testing.T) {
	server := &Server{
		svc: &MockService{
			logger: zap.NewNop(),
			NodeIDFunc: func() string {
				return testNodeID
			},
		},
		logger: zap.NewNop(),
	}

	assert.Equal(t, server.svc.NodeID(), testNodeID)
}
