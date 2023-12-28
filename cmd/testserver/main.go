package main

import (
	"context"
	"log"
	"math/big"
	"net"
	"os"
	"os/signal"
	"syscall"

	relaygrpc "github.com/bloXroute-Labs/relay-grpc"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/grpclog"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/reflection"
)

func main() {
	l, _ := zap.NewDevelopment()

	// Create a listener and start the server.
	lis, err := net.Listen("tcp", ":9090")
	if err != nil {
		l.Fatal("Failed to create listener", zap.Error(err))
	}
	l.Info("listen address", zap.String("address", lis.Addr().String()))
	srv := grpc.NewServer(grpc.UnaryInterceptor(registerClientConnectionInterceptor(l)))
	relaygrpc.RegisterRelayServer(srv, &mockRelayServer{logger: l})
	reflection.Register(srv)
	//lint:ignore SA1019  intentionally used setLogger v1
	grpclog.SetLogger(log.New(os.Stdout, "grpc: ", log.LstdFlags))
	exit := make(chan struct{})
	go func() {
		shutdown := make(chan os.Signal, 1)
		signal.Notify(shutdown, syscall.SIGINT, syscall.SIGTERM)
		<-shutdown
		l.Warn("shutting down")
		signal.Stop(shutdown)
		srv.Serve(lis)
		close(exit)
	}()
	l.Info("starting server ...")
	if err := srv.Serve(lis); err != nil {
		l.Fatal("failed to start relay mev-relay-proxy server", zap.Error(err))
	}
	<-exit
}
func registerClientConnectionInterceptor(logger *zap.Logger) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler) (interface{}, error) {

		// Log the client connection
		p, _ := peer.FromContext(ctx)
		logger.Info("Client connected", zap.String("client_address", p.Addr.String()))

		return handler(ctx, req)
	}
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
	return &relaygrpc.RegisterValidatorResponse{Code: 0, Message: "success"}, nil
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
	m.output = []stream{
		{Slot: uint64(66), ParentHash: "ph_66", ProposerPubKey: "pk_66", Value: new(big.Int).SetInt64(66666).Bytes()},
		{Slot: uint64(66), ParentHash: "ph_66", ProposerPubKey: "pk_66", Value: new(big.Int).SetInt64(66668).Bytes()},
		{Slot: uint64(66), ParentHash: "ph_66", ProposerPubKey: "pk_66", Value: new(big.Int).SetInt64(66669).Bytes(), Payload: []byte(`happy life`)},
		{Slot: uint64(77), ParentHash: "ph_77", ProposerPubKey: "pk_77", Value: new(big.Int).SetInt64(77777).Bytes()},
		{Slot: uint64(88), ParentHash: "ph_88", ProposerPubKey: "pk_88", Value: new(big.Int).SetInt64(88888).Bytes()},
	}

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
