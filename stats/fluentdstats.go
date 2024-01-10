package stats

import (
	"time"

	"github.com/ethereum/go-ethereum/log"
	"github.com/fluent/fluent-logger-golang/fluent"
)

const (
	// DateFormat is an example to date time string format
	DateFormat = "2006-01-02T15:04:05.000000"
)

// Record represents a bloxroute style stat type record
type Record struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

// LogRecord represents a log message to be sent to FluentD
type LogRecord struct {
	Level     string `json:"level"`
	Name      string `json:"name"`
	Msg       Record `json:"msg"`
	Timestamp string `json:"timestamp"`
}

// Stats is used to generate STATS records
type Stats interface {
	LogToFluentD(record Record, ts time.Time, logName string)
}

// NoStats is used to generate empty stats
type NoStats struct {
}

// LogToFluentD implements Stats.
func (NoStats) LogToFluentD(record Record, ts time.Time, logName string) {
	panic("unimplemented")
}

// FluentdStats struct that represents fluentd stats info
type FluentdStats struct {
	FluentD *fluent.Fluent
}

// NewStats is used to create transaction STATS logger
func NewStats(fluentDEnabled bool, fluentDHost string) Stats {
	if !fluentDEnabled {
		return NoStats{}
	}

	return newStats(fluentDHost)
}

// LogToFluentD log info to the fluentd
func (s FluentdStats) LogToFluentD(record Record, ts time.Time, logName string) {
	d := LogRecord{
		Level:     "STATS",
		Name:      logName,
		Msg:       record,
		Timestamp: ts.Format(DateFormat),
	}

	err := s.FluentD.EncodeAndPostData("bx.eth.builder.go.log", ts, d)
	if err != nil {
		log.Error("Error sending message to fluentD", "err", err)
	}
}

const (
	BLXRRecvBndlSucess         = "BLXR-recvBndl-ss-submittedBundle"
	BLXRWorkerBlockBundlesInfo = "BLXR-worker-block-bundles-info"
)

func newStats(fluentdHost string) Stats {
	fluentLogger, err := fluent.New(fluent.Config{
		FluentHost:    fluentdHost,
		FluentPort:    24224,
		MarshalAsJSON: true,
		Async:         true,
	})

	if err != nil {
		log.Error("Error connecting to fluentD %v", err)
		return NoStats{}
	}
	return FluentdStats{
		FluentD: fluentLogger,
	}
}
