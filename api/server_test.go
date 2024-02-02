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

	"go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/zap"
	"gotest.tools/assert"
)

type MockService struct {
	logger                *zap.Logger
	RegisterValidatorFunc func(ctx context.Context, payload []byte, clientIP, authKey string) (interface{}, any, error)
	GetHeaderFunc         func(ctx context.Context, clientIP, slot, parentHash, pubKey string) (any, any, error)
	GetPayloadFunc        func(ctx context.Context, payload []byte, clientIP string) (any, any, error)
}

var _ IService = (*MockService)(nil)

func (m *MockService) RegisterValidator(ctx context.Context, receivedAt time.Time, payload []byte, clientIP, authKey string) (any, any, error) {
	if m.RegisterValidatorFunc != nil {
		return m.RegisterValidatorFunc(ctx, payload, clientIP, authKey)
	}
	return nil, nil, nil
}
func (m *MockService) GetHeader(ctx context.Context, receivedAt time.Time, clientIP, slot, parentHash, pubKey, authHeader string) (any, any, error) {
	if m.GetHeaderFunc != nil {
		return m.GetHeaderFunc(ctx, clientIP, slot, parentHash, pubKey)
	}
	return nil, nil, nil
}

func (m *MockService) GetPayload(ctx context.Context, receivedAt time.Time, payload []byte, clientIP string, authHeader string) (any, any, error) {
	if m.GetPayloadFunc != nil {
		return m.GetPayloadFunc(ctx, payload, clientIP)
	}
	return nil, nil, nil
}

func TestServer_HandleRegistration(t *testing.T) {
	testCases := map[string]struct {
		requestBody   []byte
		mockService   *MockService
		expectedCode  int
		expectedError string
	}{
		"When registration succeeded": {
			requestBody: []byte(`{"key": "value"}`),
			mockService: &MockService{
				logger: zap.NewNop(),
				RegisterValidatorFunc: func(ctx context.Context, payload []byte, clientIP, authKey string) (interface{}, any, error) {
					return nil, nil, nil

				},
			},
			expectedCode:  http.StatusOK,
			expectedError: "",
		},
		"When registration failed": {
			requestBody: []byte(`{"key": "value"}`),
			mockService: &MockService{
				logger: zap.NewNop(),
				RegisterValidatorFunc: func(ctx context.Context, payload []byte, clientIP, authKey string) (interface{}, any, error) {
					return nil, nil, toErrorResp(http.StatusInternalServerError, "", "failed to register", "", "failed to register", "")
				},
			},
			expectedCode:  http.StatusInternalServerError,
			expectedError: "failed to register",
		},
	}

	for testName, tc := range testCases {
		t.Run(testName, func(t *testing.T) {
			req, err := http.NewRequest("POST", "/eth/v1/builder/validators", bytes.NewBuffer(tc.requestBody))
			if err != nil {
				t.Fatal(err)
			}
			rr := httptest.NewRecorder()
			server := &Server{svc: tc.mockService, logger: zap.NewNop(), tracer: noop.NewTracerProvider().Tracer("test")}
			server.HandleRegistration(rr, req)

			assert.Equal(t, rr.Code, tc.expectedCode)
			out := new(ErrorResp)
			err = json.NewDecoder(rr.Body).Decode(out)
			assert.NilError(t, err)
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
				GetHeaderFunc: func(ctx context.Context, clientIP, slot, parentHash, pubKey string) (interface{}, any, error) {

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
				GetHeaderFunc: func(ctx context.Context, clientIP, slot, parentHash, pubKey string) (interface{}, any, error) {
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
				GetHeaderFunc: func(ctx context.Context, clientIP, slot, parentHash, pubKey string) (interface{}, any, error) {
					return nil, nil, &ErrorResp{Code: http.StatusNoContent, Message: "header value is not present for the requested key slot", BlxrMessage: BlxrMessage{
						reqID:    "dummy-id",
						msg:      "header value is not present for the requested key slot",
						clientIP: "0.0.0.0",
					}}
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
				GetPayloadFunc: func(ctx context.Context, payload []byte, clientIP string) (any, any, error) {
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
				GetPayloadFunc: func(ctx context.Context, payload []byte, clientIP string) (any, any, error) {
					return nil, nil, toErrorResp(http.StatusInternalServerError, "", "failed to getPayload", "", "failed to getPayload", "")
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
			assert.NilError(t, err)
			if tc.expectedError != "" {
				assert.Equal(t, out.Message, tc.expectedError)
			}
		})
	}
}
