package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace/noop"

	"github.com/attestantio/go-builder-client/spec"
	relaygrpc "github.com/bloXroute-Labs/relay-grpc"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"gotest.tools/assert"
)

func TestService_RegisterValidator(t *testing.T) {

	tests := map[string]struct {
		f           func(ctx context.Context, req *relaygrpc.RegisterValidatorRequest, opts ...grpc.CallOption) (*relaygrpc.RegisterValidatorResponse, error)
		wantSuccess any
		wantErr     *ErrorResp
	}{
		"If registerValidator succeeded ": {
			f: func(ctx context.Context, req *relaygrpc.RegisterValidatorRequest, opts ...grpc.CallOption) (*relaygrpc.RegisterValidatorResponse, error) {
				return &relaygrpc.RegisterValidatorResponse{Code: 0, Message: "success"}, nil
			},
			wantSuccess: struct{}{},
			wantErr:     nil,
		},
		"If registerValidator returns error": {
			f: func(ctx context.Context, req *relaygrpc.RegisterValidatorRequest, opts ...grpc.CallOption) (*relaygrpc.RegisterValidatorResponse, error) {
				return nil, fmt.Errorf("error")
			},
			wantErr: toErrorResp(http.StatusInternalServerError, "error", "", "", ""),
		},
		"If registerValidator returns empty output": {
			f: func(ctx context.Context, req *relaygrpc.RegisterValidatorRequest, opts ...grpc.CallOption) (*relaygrpc.RegisterValidatorResponse, error) {
				return nil, nil
			},
			wantErr: toErrorResp(http.StatusInternalServerError, "failed to register", "", "", ""),
		},
		"If registerValidator returns error output": {
			f: func(ctx context.Context, req *relaygrpc.RegisterValidatorRequest, opts ...grpc.CallOption) (*relaygrpc.RegisterValidatorResponse, error) {
				return &relaygrpc.RegisterValidatorResponse{Code: 2, Message: "failed"}, nil
			},
			wantErr: toErrorResp(http.StatusInternalServerError, "failed", "", "", ""),
		},
	}
	for testName, tt := range tests {
		t.Run(testName, func(t *testing.T) {
			s := &Service{
				logger:  zap.NewNop(),
				clients: []*Client{{"", &mockRelayClient{RegisterValidatorFunc: tt.f}}},
			}
			got, _, err := s.RegisterValidator(context.Background(), time.Now(), nil, "", "")
			if err == nil {
				assert.Equal(t, got, tt.wantSuccess)
				return
			}
			assert.Equal(t, err.Error(), tt.wantErr.Error())
		})
	}
}
func TestService_getPayload(t *testing.T) {

	tests := map[string]struct {
		f           func(ctx context.Context, req *relaygrpc.GetPayloadRequest, opts ...grpc.CallOption) (*relaygrpc.GetPayloadResponse, error)
		wantSuccess []byte
		wantErr     *ErrorResp
	}{
		"If getPayload succeeded ": {
			f: func(ctx context.Context, req *relaygrpc.GetPayloadRequest, opts ...grpc.CallOption) (*relaygrpc.GetPayloadResponse, error) {
				return &relaygrpc.GetPayloadResponse{Code: 0, Message: "success", VersionedExecutionPayload: []byte(`payload`)}, nil
			},
			wantSuccess: []byte(`payload`),
			wantErr:     nil,
		},
		"If getPayload returns error": {
			f: func(ctx context.Context, req *relaygrpc.GetPayloadRequest, opts ...grpc.CallOption) (*relaygrpc.GetPayloadResponse, error) {
				return nil, fmt.Errorf("error")
			},
			wantErr: toErrorResp(http.StatusInternalServerError, "error", "", "", ""),
		},
		"If getPayload returns empty output": {
			f: func(ctx context.Context, req *relaygrpc.GetPayloadRequest, opts ...grpc.CallOption) (*relaygrpc.GetPayloadResponse, error) {
				return nil, nil
			},
			wantErr: toErrorResp(http.StatusInternalServerError, "failed to getPayload", "", "", ""),
		},
		"If getPayload returns error output": {
			f: func(ctx context.Context, req *relaygrpc.GetPayloadRequest, opts ...grpc.CallOption) (*relaygrpc.GetPayloadResponse, error) {
				return &relaygrpc.GetPayloadResponse{Code: 2, Message: "failed"}, nil
			},
			wantErr: toErrorResp(http.StatusInternalServerError, "failed", "", "", ""),
		},
	}
	for testName, tt := range tests {
		t.Run(testName, func(t *testing.T) {
			s := &Service{
				logger:  zap.NewNop(),
				clients: []*Client{{RelayClient: &mockRelayClient{GetPayloadFunc: tt.f}}},
				tracer:  noop.NewTracerProvider().Tracer(""),
			}
			got, _, err := s.GetPayload(context.Background(), time.Now(), nil, "")
			if err == nil {
				assert.Equal(t, string(got.(json.RawMessage)), string(tt.wantSuccess))
				return
			}
			assert.Equal(t, err.Error(), tt.wantErr.Error())
		})
	}
}

func TestService_StreamHeaderAndGetMethod(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := zap.NewNop()
	//l, _ := zap.NewDevelopment()
	streams := []stream{
		{Slot: uint64(66), ParentHash: "0x66", ProposerPubKey: "0x66", Value: new(big.Int).SetInt64(66666).Bytes()},
		{Slot: uint64(66), ParentHash: "0x66", ProposerPubKey: "0x66", Value: new(big.Int).SetInt64(66668).Bytes()},
		{Slot: uint64(66), ParentHash: "0x66", ProposerPubKey: "0x66", Value: new(big.Int).SetInt64(66669).Bytes(), Payload: []byte("{\"version\":\"capella\",\"data\":{\"message\":{\"header\":{\"parent_hash\":\"0xbd0ada39aca5fed393fa4bf80d45d4703e3a9aa3c0a09750879cb842f1bdfc58\",\"fee_recipient\":\"0x8dC847Af872947Ac18d5d63fA646EB65d4D99560\",\"state_root\":\"0x3047ac621434c21c527f38d676382fa10e402f742e6d47bc6e07e7a2c13fdf32\",\"receipts_root\":\"0xeaf609cff0ccedd82d3db9029455b5ce52f0d8f4b91977d9251db72bc73acf1e\",\"logs_bloom\":\"0x00310412002012002a001066840114452398001008403a4000eb341024000912221080280208300681509a0b80810070740ba236ca21371102118f89312c201653f2c03c4c009c9946a0401a5218c233c20d046090460801860442868c2610244900006086420ad0a008b28430000ac0700008e84028054400826812c09800c12a22b472491401c03040301502c100000027100d904c82e8342104e2e400808042e808402465080203811582a04808880602f610900043a000487036a08002009410c00210a21084004aa4108153430a060200a2408800190821849a5808e0084130004509222c47280a11808240800492421454021303414898f80415800774\",\"prev_randao\":\"0x21a739f58604430cef3e5c550a955bc5943bae4c3dae6a01672be878ae0a5e1b\",\"block_number\":\"10077336\",\"gas_limit\":\"30000000\",\"gas_used\":\"9696787\",\"timestamp\":\"1700496012\",\"extra_data\":\"0x506f776572656420627920626c6f58726f757465\",\"base_fee_per_gas\":\"11\",\"block_hash\":\"0xf4488a3b1fa59a3ce2e52a087ae3d7c93ff4a29f0a2df93a003b02902571cc54\",\"transactions_root\":\"0x9956dd9ece5082f2eab87c2b5d22f7ed6cf7c865cc461c20ee71bee07b031368\",\"withdrawals_root\":\"0x2f2680e6bca4d97e9a776567779f7a290766634e1fc6a0fce75ceabe239240bb\"},\"value\":\"54788421837882049\",\"pubkey\":\"0x821f2a65afb70e7f2e820a925a9b4c80a159620582c1766b1b09729fec178b11ea22abb3a51f07b288be815a1a2ff516\"},\"signature\":\"0x8e7678fb099b1489ad5d0929d48d4c5839e09f8884bf3cbbf309a5d1032f723c5b599ac00d53c3bea7b5fc70f1ac53fd0f8e6692d18ec94b8565f9fa18dc393867b43400774968f6e0f1d40282764d64a5ef5897716a2e5f9e3b667f3e49c0fe\"}}")},
		{Slot: uint64(77), ParentHash: "0x77", ProposerPubKey: "0x77", Value: new(big.Int).SetInt64(77777).Bytes()},
		{Slot: uint64(88), ParentHash: "0x88", ProposerPubKey: "0x88", Value: new(big.Int).SetInt64(88888).Bytes()},
	}
	// Set up your mock gRPC server and client.
	srv := grpc.NewServer()
	relaygrpc.RegisterRelayServer(srv, &mockRelayServer{output: streams, logger: l})

	// Create a listener and start the server.
	lis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal("Failed to create listener", zap.Error(err))
	}
	go srv.Serve(lis)
	defer srv.Stop()

	// Create a gRPC client that connects to the mock server.
	conn, err := grpc.Dial(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal("Failed to create client connection", zap.Error(err))
	}
	defer conn.Close()

	relayClient := relaygrpc.NewRelayClient(conn)
	service := NewService(l, noop.NewTracerProvider().Tracer(""), "test", "", "", &Client{lis.Addr().String(), relayClient})

	go service.StreamHeader(context.Background(), relayClient)

	server := &Server{svc: service, logger: zap.NewNop(), listenAddress: "127.0.0.1:9090"}
	go server.Start()
	defer server.Stop()

	<-time.After(time.Second * 1)

	client := http.Client{}

	tests := map[string]struct {
		in      stream
		args    []stream
		want    any
		wantErr bool
	}{
		"If key not present in the header map": {
			in:      stream{Slot: uint64(99), ParentHash: "0x00", ProposerPubKey: "0x00"},
			args:    streams,
			wantErr: true,
		},
		"If key present in the header map": {
			in:      stream{Slot: uint64(66), ParentHash: "0x66", ProposerPubKey: "0x66"},
			args:    streams,
			want:    json.RawMessage([]byte("{\"version\":\"capella\",\"data\":{\"message\":{\"header\":{\"parent_hash\":\"0xbd0ada39aca5fed393fa4bf80d45d4703e3a9aa3c0a09750879cb842f1bdfc58\",\"fee_recipient\":\"0x8dC847Af872947Ac18d5d63fA646EB65d4D99560\",\"state_root\":\"0x3047ac621434c21c527f38d676382fa10e402f742e6d47bc6e07e7a2c13fdf32\",\"receipts_root\":\"0xeaf609cff0ccedd82d3db9029455b5ce52f0d8f4b91977d9251db72bc73acf1e\",\"logs_bloom\":\"0x00310412002012002a001066840114452398001008403a4000eb341024000912221080280208300681509a0b80810070740ba236ca21371102118f89312c201653f2c03c4c009c9946a0401a5218c233c20d046090460801860442868c2610244900006086420ad0a008b28430000ac0700008e84028054400826812c09800c12a22b472491401c03040301502c100000027100d904c82e8342104e2e400808042e808402465080203811582a04808880602f610900043a000487036a08002009410c00210a21084004aa4108153430a060200a2408800190821849a5808e0084130004509222c47280a11808240800492421454021303414898f80415800774\",\"prev_randao\":\"0x21a739f58604430cef3e5c550a955bc5943bae4c3dae6a01672be878ae0a5e1b\",\"block_number\":\"10077336\",\"gas_limit\":\"30000000\",\"gas_used\":\"9696787\",\"timestamp\":\"1700496012\",\"extra_data\":\"0x506f776572656420627920626c6f58726f757465\",\"base_fee_per_gas\":\"11\",\"block_hash\":\"0xf4488a3b1fa59a3ce2e52a087ae3d7c93ff4a29f0a2df93a003b02902571cc54\",\"transactions_root\":\"0x9956dd9ece5082f2eab87c2b5d22f7ed6cf7c865cc461c20ee71bee07b031368\",\"withdrawals_root\":\"0x2f2680e6bca4d97e9a776567779f7a290766634e1fc6a0fce75ceabe239240bb\"},\"value\":\"54788421837882049\",\"pubkey\":\"0x821f2a65afb70e7f2e820a925a9b4c80a159620582c1766b1b09729fec178b11ea22abb3a51f07b288be815a1a2ff516\"},\"signature\":\"0x8e7678fb099b1489ad5d0929d48d4c5839e09f8884bf3cbbf309a5d1032f723c5b599ac00d53c3bea7b5fc70f1ac53fd0f8e6692d18ec94b8565f9fa18dc393867b43400774968f6e0f1d40282764d64a5ef5897716a2e5f9e3b667f3e49c0fe\"}}")),
			wantErr: false,
		},
	}
	for testName, tt := range tests {
		t.Run(testName, func(t *testing.T) {
			got, _, err := service.GetHeader(ctx, time.Now(), "", strconv.FormatUint(tt.in.Slot, 10), tt.in.ParentHash, tt.in.ProposerPubKey)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetHeader() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetHeader() got = %v, wantErr %v", got, tt.want)
			}
			responsePayload := new(spec.VersionedSignedBuilderBid)
			code, err := sendHTTPRequest(context.Background(), client, http.MethodGet, fmt.Sprintf("http://127.0.0.1:9090/eth/v1/builder/header/%v/%v/%v", tt.in.Slot, tt.in.ParentHash, tt.in.ProposerPubKey), "", map[string]string{}, nil, responsePayload)
			if !tt.wantErr {
				assert.NilError(t, err)
				assert.Equal(t, code, http.StatusOK)
				hash, err := responsePayload.BlockHash()
				assert.NilError(t, err)
				assert.Equal(t, hash.String(), "0xf4488a3b1fa59a3ce2e52a087ae3d7c93ff4a29f0a2df93a003b02902571cc54")
			}
		})
	}
}

type UserAgent string

func sendHTTPRequest(ctx context.Context, client http.Client, method, url string, userAgent UserAgent, headers map[string]string, payload, dst any) (code int, err error) {
	var req *http.Request

	if payload == nil {
		req, err = http.NewRequestWithContext(ctx, method, url, nil)
	} else {
		payloadBytes, err2 := json.Marshal(payload)
		if err2 != nil {
			return 0, fmt.Errorf("could not marshal request: %w", err2)
		}
		req, err = http.NewRequestWithContext(ctx, method, url, bytes.NewReader(payloadBytes))

		// Set headers
		req.Header.Add("Content-Type", "application/json")
	}
	if err != nil {
		return 0, fmt.Errorf("could not prepare request: %w", err)
	}

	// Set user agent header
	req.Header.Set("User-Agent", strings.TrimSpace(fmt.Sprintf("mev-boost/%s %s", "test-version", userAgent)))

	// Set other headers
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	// Execute request
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return resp.StatusCode, nil
	}

	if resp.StatusCode > 299 {
		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			return resp.StatusCode, fmt.Errorf("could not read error response body for status code %d: %w", resp.StatusCode, err)
		}
		return resp.StatusCode, fmt.Errorf("%w: %d / %s", errors.New("HTTP error response"), resp.StatusCode, string(bodyBytes))
	}

	if dst != nil {
		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			return resp.StatusCode, fmt.Errorf("could not read response body: %w", err)
		}
		if err := json.Unmarshal(bodyBytes, &dst); err != nil {
			return resp.StatusCode, fmt.Errorf("could not unmarshal response %s: %w", string(bodyBytes), err)
		}

	}

	return resp.StatusCode, nil
}

type mockRelayServer struct {
	relaygrpc.UnimplementedRelayServer
	output []stream
	logger *zap.Logger
}

func (m *mockRelayServer) SubmitBlock(ctx context.Context, request *relaygrpc.SubmitBlockRequest) (*relaygrpc.SubmitBlockResponse, error) {
	panic("implement me")
}

func (m *mockRelayServer) RegisterValidator(ctx context.Context, request *relaygrpc.RegisterValidatorRequest) (*relaygrpc.RegisterValidatorResponse, error) {
	panic("implement me")
}

func (m *mockRelayServer) GetHeader(ctx context.Context, request *relaygrpc.GetHeaderRequest) (*relaygrpc.GetHeaderResponse, error) {
	panic("implement me")
}

func (m *mockRelayServer) GetPayload(ctx context.Context, request *relaygrpc.GetPayloadRequest) (*relaygrpc.GetPayloadResponse, error) {
	panic("implement me")
}

func (m *mockRelayServer) StreamHeader(request *relaygrpc.StreamHeaderRequest, srv relaygrpc.Relay_StreamHeaderServer) error {
	// Simulate streaming of headers for testing.
	// send predefined headers to the client via srv.Send().
	bidStream := make(chan stream, 100)
	go func() {
		for _, s := range m.output {
			m.logger.Warn("sending stream")
			bidStream <- s
		}
		close(bidStream)
	}()
	for bid := range bidStream {
		m.logger.Warn("sending header")
		header := &relaygrpc.StreamHeaderResponse{
			Slot:       bid.Slot,
			ParentHash: bid.ParentHash,
			Pubkey:     bid.ProposerPubKey,
			Value:      bid.Value,
			Payload:    bid.Payload,
		}
		if err := srv.Send(header); err != nil {
			m.logger.Error("error sending header", zap.Error(err))
			continue
		}
	}
	return nil
}

type stream struct {
	Slot           uint64
	ParentHash     string
	ProposerPubKey string
	Value          []byte
	Payload        []byte
}

type mockRelayClient struct {
	RegisterValidatorFunc func(ctx context.Context, req *relaygrpc.RegisterValidatorRequest, opts ...grpc.CallOption) (*relaygrpc.RegisterValidatorResponse, error)
	GetPayloadFunc        func(ctx context.Context, req *relaygrpc.GetPayloadRequest, opts ...grpc.CallOption) (*relaygrpc.GetPayloadResponse, error)
	StreamHeaderFunc      func(ctx context.Context, in *relaygrpc.StreamHeaderRequest, opts ...grpc.CallOption) (relaygrpc.Relay_StreamHeaderClient, error)
}

func (m *mockRelayClient) SubmitBlock(ctx context.Context, in *relaygrpc.SubmitBlockRequest, opts ...grpc.CallOption) (*relaygrpc.SubmitBlockResponse, error) {
	panic("implement me")
}

func (m *mockRelayClient) RegisterValidator(ctx context.Context, req *relaygrpc.RegisterValidatorRequest, opts ...grpc.CallOption) (*relaygrpc.RegisterValidatorResponse, error) {
	if m.RegisterValidatorFunc != nil {
		return m.RegisterValidatorFunc(ctx, req, opts...)
	}
	return nil, nil
}
func (m *mockRelayClient) GetHeader(ctx context.Context, in *relaygrpc.GetHeaderRequest, opts ...grpc.CallOption) (*relaygrpc.GetHeaderResponse, error) {
	panic("implement me")
}

func (m *mockRelayClient) StreamHeader(ctx context.Context, in *relaygrpc.StreamHeaderRequest, opts ...grpc.CallOption) (relaygrpc.Relay_StreamHeaderClient, error) {
	if m.StreamHeaderFunc != nil {
		return m.StreamHeaderFunc(ctx, in, opts...)
	}
	return nil, nil
}

func (m *mockRelayClient) GetPayload(ctx context.Context, req *relaygrpc.GetPayloadRequest, opts ...grpc.CallOption) (*relaygrpc.GetPayloadResponse, error) {

	if m.GetPayloadFunc != nil {
		return m.GetPayloadFunc(ctx, req, opts...)
	}
	return nil, nil
}
