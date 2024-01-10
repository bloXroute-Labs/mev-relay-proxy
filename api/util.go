package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bloXroute-Labs/mev-relay-proxy/stats"
	"github.com/urfave/cli/v2"
)

var (
	FluentdHostFlag = &cli.StringFlag{
		Name:    "fluentd-host",
		Usage:   "fluentd host",
		Aliases: []string{"fh"},
		Value:   "172.17.0.1",
		Hidden:  true,
	}
	FluentdFlag = &cli.BoolFlag{
		Name:   "fluentd",
		Usage:  "sends logs records to fluentD",
		Value:  true,
		Hidden: true,
	}
)

// decodeJSON reads JSON from io.Reader and decodes it into a struct
//
//lint:ignore U1000  intentionally unused in this file
func decodeJSON(r io.Reader, dst any) error {
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(dst); err != nil {
		return err
	}
	return nil
}

// decodeAndCloseJSON reads JSON from io.ReadCloser, decodes it into a struct and
// closes the reader
//
//lint:ignore U1000  intentionally unused in this file
func decodeJSONAndClose(r io.ReadCloser, dst any) error {
	defer r.Close()
	return decodeJSON(r, dst)
}

func GetIPXForwardedFor(r *http.Request, fluentDStats stats.Stats) string {
	forwarded := r.Header.Get("X-Forwarded-For")
	if forwarded != "" {
		if strings.Contains(forwarded, ",") { // return first entry of list of IPs
			ip := strings.Split(forwarded, ",")[0]
			// Log to FluentD
			fluentDStats.LogToFluentD(stats.Record{
				Type: "IPForwarded",
				Data: ip,
			}, time.Now(), "utils.GetIPXForwardedFor")
			return ip
		}
		// Log to FluentD
		fluentDStats.LogToFluentD(stats.Record{
			Type: "IPForwarded",
			Data: forwarded,
		}, time.Now(), "utils.GetIPXForwardedFor")
		return forwarded
	}
	// Log to FluentD
	fluentDStats.LogToFluentD(stats.Record{
		Type: "IPForwarded",
		Data: r.RemoteAddr,
	}, time.Now(), "utils.GetIPXForwardedFor")
	return r.RemoteAddr
}
