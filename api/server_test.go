package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	capellaBuilderAPI "github.com/attestantio/go-builder-client/api/capella"
	v1 "github.com/attestantio/go-builder-client/api/v1"
	builderSpec "github.com/attestantio/go-builder-client/spec"
	"github.com/attestantio/go-eth2-client/spec"
	"github.com/attestantio/go-eth2-client/spec/capella"
	"github.com/bloXroute-Labs/gateway/v2/utils/syncmap"
	"github.com/gorilla/websocket"
	"github.com/patrickmn/go-cache"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel"
	"go.uber.org/zap"
)

const (
	testHttpListenAddr                      = "localhost:18550"
	testGrpcHost                            = "localhost:5005"
	testGrpcPort                            = "5005"
	testMaxHeaderBytes                      = 4000
	testGatewayAuthKey                      = ""
	testNodeID                              = ""
	testGetPayloadOnlyBuilderPubkeys        = ""
	testWhitelistBuilderPubkeys             = ""
	testFluentDHost                         = ""
	testGatewayHost                         = ""
	testGatewayPort                         = ""
	testRelayHost                           = ""
	testRelayAuthKey                        = ""
	testAuthHeader                          = "testAuthHeader"
	testTxHex                               = "f9016d8209198522ecb25c0083010c5594d37bbe5744d730a1d98d8dc97c42f0ca46ad714680b90104574da7170000000000000000000000007ab6a85bc53c09c02f698f0e4225f0440b4987fe000000000000000000000000dac17f958d2ee523a2206206994597c13d831ec700000000000000000000000000000000000000000000000000000000154712e5000000000000000000000000000000000000000000000000000000000000008000000000000000000000000000000000000000000000000000000000000000444f55543a363143343130424234393436463732363630464538303044303434373534463433464630333845443335433833333035393243443046323439354238454631440000000000000000000000000000000000000000000000000000000026a0bdfb256fe98bffb040be2c10d623cc53f70786a49da47b70ed3ba38a87f4a05fa072973574cec4f19ada708b73c382ccc4de957b35686a70d0481905d74625ce04\n"
	testParentHash                          = "0x736f7e43b0b9d840d9152e7a5426f8d876a33f3ba52300bbd2923c9c7d5b62dc"
	testProposerPubkey                      = "0x932c01d6fdb20b882983a74fd0d140015e7d6f6ca783209aad778507e62dc92143238484257f9aa39fb90f01f6b5ecdf"
	testBuilderPubkey1                      = "0xae147691b534e441597cbd6a479aaa662126ca8426457737e895ae33251ea048d2d94bd18800999d6b04b1ace560be50"
	testBuilderPubkey2                      = "0xb9451d2bc9d8c82d88da94da16ea30435dfc9c0de14bf7eb2a5bd3c0774a9a99c3136914679d9a11d214bec1e46f55a0"
	testBuilderPubkey3                      = "0x8265169a40b57b91ad258817217f3edeeb3d648a4a61ef0bc125b2c23ac72be1c31dff632a2aa5d58b86203d4c3e5c43"
	testSlot1                        uint64 = 1
	testSlot2                        uint64 = 2
	testValueLow                     int64  = 1
	testValueMedium                  int64  = 10
	testValueHigh                    int64  = 100

	bloxrouteExtraDataHex = "0x506f776572656420627920626c6f58726f757465"
)

var (
	tracer  = otel.Tracer("main")
	testLog = logrus.WithField("module", "regional-relay-testing")

	submitBlockHTTPClient = &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   10 * time.Second,
	}

	submitBlockWebsocketClient = websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: true},
	}

	testBlockHash1 = [32]byte{0x0000000000000000000000000000000000000000000000000000000000000001}
	testBlockHash2 = [32]byte{0x0000000000000000000000000000000000000000000000000000000000000002}
	testBlockHash3 = [32]byte{0x0000000000000000000000000000000000000000000000000000000000000003}

	//blockMissingBidTrace         = &capellaBuilderAPI.SubmitBlockRequest{ExecutionPayload: &capella.ExecutionPayload{}}
	blockMissingBidTrace = &builderSpec.VersionedSubmitBlockRequest{
		Version:   spec.DataVersionCapella,
		Bellatrix: nil,
		Capella:   &capellaBuilderAPI.SubmitBlockRequest{ExecutionPayload: &capella.ExecutionPayload{}},
		Deneb:     nil,
	}
	blockMissingExecutionPayload = &builderSpec.VersionedSubmitBlockRequest{
		Version:   spec.DataVersionCapella,
		Bellatrix: nil,
		Capella:   &capellaBuilderAPI.SubmitBlockRequest{Message: &v1.BidTrace{}},
		Deneb:     nil,
	}

	testVersionedSignedBuilderBid1 = &VersionedSignedBuilderBid{}
	testVersionedSignedBuilderBid2 = &VersionedSignedBuilderBid{}
)

type MockService struct {
	logger                *zap.Logger
	RegisterValidatorFunc func(ctx context.Context, payload []byte, clientIP, authKey string) (interface{}, any, error)
	GetHeaderFunc         func(ctx context.Context, clientIP, slot, parentHash, pubKey string) (any, any, error)
	GetPayloadFunc        func(ctx context.Context, payload []byte, clientIP string) (any, any, error)
	NodeIDFunc            func() string
}

var _ IService = (*MockService)(nil)

func (m *MockService) RegisterValidator(ctx context.Context, receivedAt time.Time, payload []byte, clientIP, authKey string) (any, any, error) {
	if m.RegisterValidatorFunc != nil {
		return m.RegisterValidatorFunc(ctx, payload, clientIP, authKey)
	}
	return nil, nil, nil
}
func (m *MockService) GetHeader(ctx context.Context, receivedAt time.Time, clientIP, slot, parentHash, pubKey string) (any, any, error) {
	if m.GetHeaderFunc != nil {
		return m.GetHeaderFunc(ctx, clientIP, slot, parentHash, pubKey)
	}
	return nil, nil, nil
}

func (m *MockService) GetPayload(ctx context.Context, receivedAt time.Time, payload []byte, clientIP string) (any, any, error) {
	if m.GetPayloadFunc != nil {
		return m.GetPayloadFunc(ctx, payload, clientIP)
	}
	return nil, nil, nil
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
			server := &Server{svc: tc.mockService, logger: zap.NewNop()}
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
	}

	for testName, tc := range testCases {
		t.Run(testName, func(t *testing.T) {
			req, err := http.NewRequest("GET", fmt.Sprintf("/eth/v1/builder/header/%s/%s/%s", tc.slot, tc.parentHash, tc.pubKey), nil)
			if err != nil {
				t.Fatal(err)
			}
			rr := httptest.NewRecorder()
			server := &Server{svc: tc.mockService, logger: zap.NewNop()}

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
			server := &Server{svc: tc.mockService, logger: zap.NewNop()}
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

func TestGetBuilderBidForSlot(t *testing.T) {
	server := &Server{
		svc: &MockService{
			logger: zap.NewNop(),
		},
		builderBidsForSlot: cache.New(5*time.Minute, 10*time.Minute),
		logger:             zap.NewNop(),
	}

	// set up builder bids map for test slot
	cacheKey := server.keyCacheGetHeaderResponse(testSlot1, testParentHash, testProposerPubkey)
	builderBidsMap := syncmap.NewStringMapOf[*VersionedSignedBuilderBid]()
	builderBidsMap.Store(testBuilderPubkey1, testVersionedSignedBuilderBid1)
	server.builderBidsForSlot.Set(cacheKey, builderBidsMap, cache.DefaultExpiration)

	// test builder bid found
	bid, found := server.getBuilderBidForSlot(cacheKey, testBuilderPubkey1)
	assert.Equal(t, bid, testVersionedSignedBuilderBid1)
	assert.True(t, found)

	// test builder bid not found (unknown builder pubkey)
	bid, found = server.getBuilderBidForSlot(cacheKey, "unknown")
	assert.Nil(t, bid)
	assert.False(t, found)

	// test builder bid not found (unknown cache key)
	cacheKey = server.keyCacheGetHeaderResponse(testSlot1, testParentHash, "unknown")
	bid, found = server.getBuilderBidForSlot(cacheKey, testBuilderPubkey1)
	assert.Nil(t, bid)
	assert.False(t, found)
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
