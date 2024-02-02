package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
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

func GetIPXForwardedFor(r *http.Request) string {
	forwarded := r.Header.Get("X-Forwarded-For")
	if forwarded != "" {
		if strings.Contains(forwarded, ",") { // return first entry of list of IPs
			return strings.Split(forwarded, ",")[0]
		}
		return forwarded
	}
	return r.RemoteAddr
}

func getAuth(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" {
		return authHeader
	}

	// fallback to query param
	return r.URL.Query().Get("auth")
}

func DecodeAuth(in string) (string, string, error) {
	decodedBytes, err := base64.StdEncoding.DecodeString(in)
	if err != nil {
		return "", "", errors.New("failed to decode auth header")
	}
	data := strings.Split(string(decodedBytes), ":")
	if len(data) != 2 {
		return "", "", errors.New("invalid auth header")
	}
	return data[0], data[1], nil
}

// GetSleepParams returns the sleep time and max sleep time from the request
func (s *Server) GetSleepParams(r *http.Request, delayInMs, maxDelayInMs int64) (int64, int64) {

	sleepTime, sleepMax := delayInMs, maxDelayInMs

	sleep := r.URL.Query().Get("sleep")
	if sleep != "" {
		sleepTime = s.AToI(sleep)
	}

	maxSleep := r.URL.Query().Get("max_sleep")
	if maxSleep != "" {
		sleepMax = s.AToI(maxSleep)
	}

	return sleepTime, sleepMax
}

// AToI converts a string to an int64
func (s *Server) AToI(value string) int64 {
	i, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		s.logger.Error("failed to parse int", zap.String("value", value), zap.Error(err))
		return 0
	}
	return int64(i)
}

// GetSlotStartTime returns the time of the start of the slot
func GetSlotStartTime(beaconGenesisTime, slot int64) time.Time {
	return time.Unix(beaconGenesisTime+(int64(slot)*12), 0).UTC()
}
