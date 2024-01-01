package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/uptrace/uptrace-go/uptrace"
	"go.opentelemetry.io/otel"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

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
	// defaults
	defaultListenAddr = getEnv("RELAY_PROXY_LISTEN_ADDR", "localhost:18551")

	listenAddr = flag.String("addr", defaultListenAddr, "mev-relay-proxy server listening address")
	//lint:ignore U1000 Ignore unused variable
	relayGRPCURL       = flag.String("relay", fmt.Sprintf("%v:%d", "127.0.0.1", 5010), "relay grpc URL")
	relaysGRPCURL      = flag.String("relays", fmt.Sprintf("%v:%d", "127.0.0.1", 5010), "comma seperated list of relay grpc URL")
	getHeaderDelayInMS = flag.Int("get-header-delay-ms", 300, "delay for sending the getHeader request in millisecond")
	nodeID             = flag.String("node-id", fmt.Sprintf("mev-relay-proxy-%v", uuid.New().String()), "unique identifier for the node")
	uptraceDSN         = flag.String("uptrace-dsn", "", "uptrace URL")
	fluentdDSN         = flag.String("fluentd-dsn", "", "fluentd URL")
)

func main() {
	flag.Parse()
	l := newLogger(_AppName, _BuildVersion)
	ctx, cancel := context.WithCancel(context.Background())
	// init client connection
	var (
		clients []*api.Client
		conns   []*grpc.ClientConn
	)
	urls := strings.Split(*relaysGRPCURL, ",")
	for _, url := range urls {
		conn, err := grpc.Dial(url, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			l.Fatal("failed to create mev-relay-proxy client connection", zap.Error(err))
		}
		clients = append(clients, &api.Client{URL: url, RelayClient: relaygrpc.NewRelayClient(conn)})
		conns = append(conns, conn)
	}
	defer func() {
		for _, conn := range conns {
			conn.Close()
		}
	}()

	// Configure OpenTelemetry with sensible defaults.
	uptrace.ConfigureOpentelemetry(
		uptrace.WithDSN(*uptraceDSN),

		uptrace.WithServiceName("mev-boost-relay"),
		uptrace.WithServiceVersion("1.0.0"),
		uptrace.WithDeploymentEnvironment(*fluentdDSN),
	)
	// Send buffered spans and free resources.
	defer func() {
		if err := uptrace.Shutdown(ctx); err != nil {
			l.Error("failed to shutdown uptrace", zap.Error(err))
		}
	}()

	tracer := otel.Tracer("main")

	// init service and server
	svc := api.NewService(l, nil, _BuildVersion, *nodeID, clients...)
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
	}(ctx)
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
