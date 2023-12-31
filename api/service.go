package api

import (
	"context"
	"encoding/json"
	"fmt"
	"google.golang.org/grpc/metadata"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/bloXroute-Labs/gateway/v2/utils/syncmap"
	relaygrpc "github.com/bloXroute-Labs/relay-grpc"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
)

type IService interface {
	RegisterValidator(ctx context.Context, receivedAt time.Time, payload []byte, clientIP, authHeader string) (any, any, error)
	GetHeader(ctx context.Context, receivedAt time.Time, clientIP, slot, parentHash, pubKey string) (any, any, error)
	GetPayload(ctx context.Context, receivedAt time.Time, payload []byte, clientIP string) (any, any, error)
}
type Service struct {
	logger  *zap.Logger
	version string // build version
	headers *syncmap.SyncMap[string, []*Header]
	clients []*Client
	//TODO: add flag to receive node id
	nodeID string // UUID
	//slotCleanUpCh chan uint64
	authKey string
}

type Client struct {
	URL string
	relaygrpc.RelayClient
}

type Header struct {
	Value     []byte // block value
	Payload   []byte // blinded block
	BlockHash string
}

func NewService(logger *zap.Logger, version string, nodeID string, authKey string, clients ...*Client) *Service {
	return &Service{
		logger:  logger,
		version: version,
		clients: clients,
		headers: syncmap.NewStringMapOf[[]*Header](),
		nodeID:  nodeID,
		authKey: authKey,
	}
}

func (s *Service) RegisterValidator(ctx context.Context, receivedAt time.Time, payload []byte, clientIP string, authHeader string) (any, any, error) {
	id := uuid.NewString()
	s.logger.Info("received",
		zap.String("method", "registerValidator"),
		zap.String("clientIP", clientIP),
		zap.String("reqID", id),
		zap.Time("receivedAt", receivedAt),
	)
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", s.authKey)
	req := &relaygrpc.RegisterValidatorRequest{
		ReqId:      id,
		Payload:    payload,
		ClientIp:   clientIP,
		NodeId:     s.nodeID,
		Version:    s.version,
		ReceivedAt: timestamppb.New(receivedAt),
		AuthHeader: authHeader,
	}
	var (
		err error
		out *relaygrpc.RegisterValidatorResponse
	)
	for _, client := range s.clients {
		out, err = client.RegisterValidator(ctx, req)
		if err != nil {
			err = toErrorResp(http.StatusInternalServerError, err.Error(), id, "relay returned error", clientIP)
			continue
		}
		if out == nil {
			err = toErrorResp(http.StatusInternalServerError, "failed to register", id, "empty response from relay", clientIP)
			continue
		}
		if out.Code != uint32(codes.OK) {
			err = toErrorResp(http.StatusBadRequest, out.Message, id, "relay returned failure response code", clientIP)
			continue
		}
	}
	if err != nil {
		return nil, nil, err
	}
	return struct{}{}, nil, nil
}

func (s *Service) WrapStreamHeaders(ctx context.Context, wg *sync.WaitGroup) {
	for _, client := range s.clients {
		wg.Add(1)
		go func(_ctx context.Context, c *Client) {
			defer wg.Done()
			s.WrapStreamHeader(_ctx, c)
		}(ctx, client)
	}
	wg.Wait()
}

func (s *Service) WrapStreamHeader(ctx context.Context, client *Client) {
	for {
		select {
		case <-ctx.Done():
			s.logger.Warn("Stream header context cancelled")
			return
		default:
			if _, err := s.StreamHeader(ctx, client); err != nil {
				s.logger.Warn("Failed to stream header. Sleeping 1 second and then reconnecting", zap.String("url", client.URL), zap.Error(err))
			} else {
				s.logger.Warn("Stream header stopped.  Sleeping 1 second and then reconnecting", zap.String("url", client.URL))
			}
			time.Sleep(1 * time.Second)
		}
	}
}

func (s *Service) StreamHeader(ctx context.Context, client relaygrpc.RelayClient) (*relaygrpc.StreamHeaderResponse, error) {
	id := uuid.NewString()
	nodeID := fmt.Sprintf("%v-%v", s.nodeID, id)
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", s.authKey)
	stream, err := client.StreamHeader(ctx, &relaygrpc.StreamHeaderRequest{
		ReqId:   id,
		NodeId:  nodeID,
		Version: s.version,
	})
	s.logger.Info("streaming headers", zap.String("nodeID", nodeID))
	if err != nil {
		s.logger.Warn("failed to stream header", zap.Error(err), zap.String("nodeID", nodeID), zap.String("reqID", id))
		return nil, err
	}
	for {
		header, err := stream.Recv()
		if err == io.EOF {
			s.logger.Warn("stream received EOF", zap.Error(err), zap.String("nodeID", nodeID), zap.String("reqID", id))
			break
		}
		if err != nil {
			s.logger.Warn("failed to receive stream, disconnecting the stream", zap.Error(err), zap.String("nodeID", nodeID), zap.String("reqID", id))
			break
		}
		// Added empty streaming as a temporary workaround to maintain streaming alive
		// TODO: this need to be handled by adding settings for keep alive params on both server and client
		if header.GetBlockHash() == "" {
			s.logger.Debug("received empty stream", zap.String("nodeID", nodeID))
			continue
		}
		k := fmt.Sprintf("slot-%v-parentHash-%v-pubKey-%v", header.GetSlot(), header.GetParentHash(), header.GetPubkey())
		s.logger.Info("received header",
			zap.String("reqID", id),
			zap.Uint64("slot", header.GetSlot()),
			zap.String("parentHash", header.GetParentHash()),
			zap.String("blockHash", header.GetBlockHash()),
			zap.String("blockValue", new(big.Int).SetBytes(header.GetValue()).String()),
			zap.String("pubKey", header.GetPubkey()),
			zap.String("nodeID", nodeID),
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
	}
	s.logger.Warn("closing connection", zap.String("nodeID", nodeID), zap.String("reqID", id))
	return nil, nil
}

func (s *Service) GetHeader(ctx context.Context, receivedAt time.Time, clientIP, slot, parentHash, pubKey string) (any, any, error) {
	k := fmt.Sprintf("slot-%v-parentHash-%v-pubKey-%v", slot, parentHash, pubKey)
	id := uuid.NewString()
	s.logger.Info("received",
		zap.String("method", "getHeader"),
		zap.Time("receivedAt", receivedAt),
		zap.String("clientIP", clientIP),
		zap.String("key", k),
		zap.String("reqID", id),
	)
	defer func() {
		go func() {
			// cleanup old slots/headers
			time.AfterFunc(time.Second*30, func() {
				s.logger.Info("cleanup old slot", zap.String("key", k))
				s.headers.Delete(k)
			})
		}()
	}()

	val := new(big.Int)
	index := 0
	//TODO: currently storing all the header values for the particular slot
	// can be stored only the higher values to avoid looping through all the values
	if headers, ok := s.headers.Load(k); ok {
		for i, header := range headers {
			hVal := new(big.Int).SetBytes(header.Value)
			if hVal.Cmp(val) == 1 {
				val.Set(hVal)
				index = i
			}
		}
		out := headers[index]

		return json.RawMessage(out.Payload), fmt.Sprintf("%v-blockHash-%v-value-%v", k, out.BlockHash, val.String()), nil
	}
	return nil, k, toErrorResp(http.StatusNoContent, "", id, fmt.Sprintf("header value is not present for the requested key %v", k), clientIP)
}

func (s *Service) GetPayload(ctx context.Context, receivedAt time.Time, payload []byte, clientIP string) (any, any, error) {
	id := uuid.NewString()
	s.logger.Info("received",
		zap.String("method", "getPayload"),
		zap.String("clientIP", clientIP),
		zap.String("reqID", id),
		zap.Time("receivedAt", receivedAt),
	)
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", s.authKey)
	req := &relaygrpc.GetPayloadRequest{
		ReqId:      id,
		Payload:    payload,
		ClientIp:   clientIP,
		Version:    s.version,
		ReceivedAt: timestamppb.New(receivedAt),
	}
	var (
		err  error
		out  *relaygrpc.GetPayloadResponse
		meta string
	)
	for _, client := range s.clients {
		out, err = client.GetPayload(ctx, req)
		if err != nil {
			err = toErrorResp(http.StatusInternalServerError, err.Error(), id, "relay returned error", clientIP)
			continue
		}
		if out == nil {
			err = toErrorResp(http.StatusInternalServerError, "failed to getPayload", id, "empty response from relay", clientIP)
			continue
		}
		meta = fmt.Sprintf("slot-%v-parentHash-%v-pubKey-%v-blockHash-%v-proposerIndex-%v", out.GetSlot(), out.GetParentHash(), out.GetPubkey(), out.GetBlockHash(), out.GetProposerIndex())
		if out.Code != uint32(codes.OK) {
			err = toErrorResp(http.StatusBadRequest, out.Message, id, "relay returned failure response code", clientIP)
			continue
		}
		// Return early if one of the node return success
		return json.RawMessage(out.GetVersionedExecutionPayload()), meta, nil
	}
	if err != nil {
		return nil, meta, err
	}
	// execution never reaches here
	return json.RawMessage(out.GetVersionedExecutionPayload()), meta, nil
}

type ErrorResp struct {
	Code        int    `json:"code"`
	Message     string `json:"message"`
	BlxrMessage BlxrMessage
}

type BlxrMessage struct {
	reqID    string
	msg      string
	clientIP string
}

func (e *ErrorResp) Error() string {
	return e.Message
}

func toErrorResp(code int, message, reqID, blxrMessage, clientIP string) *ErrorResp {
	return &ErrorResp{
		Code:    code,
		Message: message,
		BlxrMessage: BlxrMessage{
			reqID:    reqID,
			msg:      blxrMessage,
			clientIP: clientIP,
		},
	}
}
