package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/uptrace/uptrace-go/uptrace"
	"go.opentelemetry.io/otel"

	"github.com/bloXroute-Labs/mev-relay-proxy/api"
	"github.com/google/uuid"

	"time"

	relaygrpc "github.com/bloXroute-Labs/relay-grpc"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var (
	// Included in the build process
	_BuildVersion string
	_AppName      = "mev-relay-proxy"
	_SecretToken  string
	// defaults
	defaultListenAddr = getEnv("RELAY_PROXY_LISTEN_ADDR", "localhost:18551")

	listenAddr = flag.String("addr", defaultListenAddr, "mev-relay-proxy server listening address")
	//lint:ignore U1000 Ignore unused variable
	relaysGRPCURL         = flag.String("relays", fmt.Sprintf("%v:%d", "127.0.0.1", 5010), "comma seperated list of relay grpc URL")
	registrationRelaysURL = flag.String("registration-relays", fmt.Sprintf("%v:%d", "127.0.0.1", 5010), "registration relays grpc URL")
	getHeaderDelayInMS    = flag.Int("get-header-delay-ms", 300, "delay for sending the getHeader request in millisecond")
	authKey               = flag.String("auth-key", "", "account authentication key")
	nodeID                = flag.String("node-id", fmt.Sprintf("mev-relay-proxy-%v", uuid.New().String()), "unique identifier for the node")
	uptraceDSN            = flag.String("uptrace-dsn", "", "uptrace URL")
)

func main() {
	flag.Parse()

	l := newLogger(_AppName, _BuildVersion)
	ctx, cancel := context.WithCancel(context.Background())
	// init client connection
	var (
		clients             []*api.Client
		conns               []*grpc.ClientConn
		registrationClients []*api.Client
	)

	// Parse the relaysGRPCURL
	newClients, newConns := getClientsFromURLs(l, *relaysGRPCURL, conns, clients)
	defer func() {
		for _, conn := range newConns {
			conn.Close()
		}
	}()

	// Parse the registrationRelaysURL
	newRegistrationClients, newRegConns := getClientsFromURLs(l, *registrationRelaysURL, conns, registrationClients)
	defer func() {
		for _, conn := range newRegConns {
			conn.Close()
		}
	}()

	l.Info("starting mev-relay-proxy server", zap.String("listenAddr", *listenAddr), zap.String("uptraceDSN", *uptraceDSN), zap.String("nodeID", *nodeID), zap.String("authKey", *authKey), zap.String("relaysGRPCURL", *relaysGRPCURL), zap.Int("getHeaderDelayInMS", *getHeaderDelayInMS))

	// Configure OpenTelemetry with sensible defaults.
	uptrace.ConfigureOpentelemetry(
		uptrace.WithDSN(*uptraceDSN),

		uptrace.WithServiceName(_AppName),
		uptrace.WithServiceVersion(_BuildVersion),
		uptrace.WithDeploymentEnvironment(*nodeID),
	)
	// Send buffered spans and free resources.
	defer func() {
		if err := uptrace.Shutdown(ctx); err != nil {
			l.Error("failed to shutdown uptrace", zap.Error(err))
		}
	}()

	tracer := otel.Tracer("main")

	// init service and server

	svc := api.NewService(l, tracer, _BuildVersion, *authKey, *nodeID, newClients, newRegistrationClients...)

	server := api.New(l, svc, *listenAddr, *getHeaderDelayInMS, tracer)

	exit := make(chan struct{})
	go func() {
		shutdown := make(chan os.Signal, 1)
		signal.Notify(shutdown, syscall.SIGINT, syscall.SIGTERM)
		<-shutdown
		l.Warn("shutting down")
		signal.Stop(shutdown)
		cancel()
		close(exit)
	}()

	// start streaming headers
	go func(_ctx context.Context) {
		wg := new(sync.WaitGroup)
		svc.WrapStreamHeaders(_ctx, wg)
	}(context.Background())
	if err := server.Start(); err != nil {
		l.Fatal("failed to start mev-relay-proxy server", zap.Error(err))
	}
	<-exit
}
func newLogger(appName, version string) *zap.Logger {
	logLevel := zap.DebugLevel
	var zapCore zapcore.Core
	level := zap.NewAtomicLevel()
	level.SetLevel(logLevel)
	encoderCfg := zap.NewProductionEncoderConfig()
	//encoderCfg.EncodeTime = zapcore.TimeEncoderOfLayout(time.RFC3339)
	encoderCfg.EncodeTime = zapcore.TimeEncoderOfLayout(time.RFC3339Nano)
	encoder := zapcore.NewJSONEncoder(encoderCfg)
	zapCore = zapcore.NewCore(encoder, zapcore.Lock(os.Stdout), level)

	logger := zap.New(zapCore, zap.AddCaller(), zap.ErrorOutput(zapcore.Lock(os.Stderr)))
	logger = logger.With(zap.String("app", appName), zap.String("buildVersion", version))
	return logger
}

func getEnv(key string, defaultValue string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return defaultValue
}

func getClientsFromURLs(l *zap.Logger, relaysGRPCURL string, conns []*grpc.ClientConn, clients []*api.Client) ([]*api.Client, []*grpc.ClientConn) {
	// Parse the relaysGRPCURL
	relays := strings.Split(relaysGRPCURL, ",")
	// Dial each relay and store the connections
	for _, relayURL := range relays {
		conn, err := grpc.Dial(relayURL, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			// Handle error: failed to dial relay
			log.Fatalf("Failed to dial relay: %v", err)
		}
		conns = append(conns, conn)
		clients = append(clients, &api.Client{URL: relayURL, RelayClient: relaygrpc.NewRelayClient(conn)})
	}

	return clients, conns
}
