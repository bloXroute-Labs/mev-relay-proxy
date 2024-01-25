package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/uptrace/uptrace-go/uptrace"
	"go.opentelemetry.io/otel"

	"github.com/bloXroute-Labs/mev-relay-proxy/api"
	"github.com/bloXroute-Labs/mev-relay-proxy/fluentstats"
	"github.com/google/uuid"

	"time"

	relaygrpc "github.com/bloXroute-Labs/relay-grpc"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
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
	relayGRPCURL       = flag.String("relay", fmt.Sprintf("%v:%d", "127.0.0.1", 5010), "relay grpc URL")
	relaysGRPCURL      = flag.String("relays", fmt.Sprintf("%v:%d", "127.0.0.1", 5010), "comma seperated list of relay grpc URL")
	getHeaderDelayInMS = flag.Int("get-header-delay-ms", 300, "delay for sending the getHeader request in millisecond")
	authKey            = flag.String("auth-key", "", "account authentication key")
	nodeID             = flag.String("node-id", fmt.Sprintf("mev-relay-proxy-%v", uuid.New().String()), "unique identifier for the node")
	uptraceDSN         = flag.String("uptrace-dsn", "", "uptrace URL")
	// fluentD
	fluentDHostFlag   = flag.String("fluentd-host", "", "fluentd host")
	beaconGenesisTime = flag.Int64("beacon-genesis-time", 1606824023, "beacon genesis time in unix timestamp")
)

func main() {
	flag.Parse()
	l := newLogger(_AppName, _BuildVersion)

	defer func() {
		if err := l.Sync(); err != nil {
			fmt.Fprintf(os.Stderr, "Error syncing log: %v\n", err)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())

	keepaliveOpts := grpc.WithKeepaliveParams(keepalive.ClientParameters{
		Time:                time.Minute,
		Timeout:             20 * time.Second,
		PermitWithoutStream: true,
	})

	// init client connection
	var (
		clients []*api.Client
		conns   []*grpc.ClientConn
	)
	urls := strings.Split(*relaysGRPCURL, ",")
	for _, url := range urls {
		conn, err := grpc.Dial(url, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock(), keepaliveOpts)
		if err != nil {
			l.Fatal("failed to create mev-relay-proxy client connection", zap.Error(err))
		}
		clients = append(clients, &api.Client{URL: url, Conn: conn, RelayClient: relaygrpc.NewRelayClient(conn)})
		conns = append(conns, conn)
	}
	defer func() {
		for _, conn := range conns {
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

	// init fluentD if enabled
	fluentLogger := fluentstats.NewStats(true, *fluentDHostFlag)

	// init service and server
	svc := api.NewService(l, tracer, _BuildVersion, *nodeID, *authKey, _SecretToken, fluentLogger, clients...)
	server := api.New(l, svc, *listenAddr, *getHeaderDelayInMS, tracer, fluentLogger, *beaconGenesisTime)

	exit := make(chan struct{})
	go func() {
		shutdown := make(chan os.Signal, 1)
		signal.Notify(shutdown, syscall.SIGINT, syscall.SIGTERM)
		<-shutdown
		l.Warn("shutting down")
		signal.Stop(shutdown)
		cancel()
		server.Stop()
		close(exit)
	}()

	// start streaming headers
	go func(_ctx context.Context) {
		wg := new(sync.WaitGroup)
		svc.StartStreamHeaders(_ctx, wg)
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
