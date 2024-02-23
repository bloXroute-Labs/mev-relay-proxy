package api

import (
	"encoding/base64"
	"encoding/json"
	"net/url"

	"fmt"
	eth2Api "github.com/attestantio/go-eth2-client/api"
	eth2ApiV1Capella "github.com/attestantio/go-eth2-client/api/v1/capella"
	eth2ApiV1Deneb "github.com/attestantio/go-eth2-client/api/v1/deneb"
	"github.com/attestantio/go-eth2-client/spec"
	"net/http"
	"strconv"
	"strings"
	"time"
)

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
func ParseURL(r *http.Request) (*url.URL, error) {
	u, err := url.QueryUnescape(r.URL.String())
	if err != nil {
		return r.URL, fmt.Errorf("failed to decode url: %v, reason %v", r.URL.String(), err)
	}
	urlParsed, err := url.Parse(u)
	if err != nil {
		return r.URL, fmt.Errorf("failed to parse decoded url: %v, reason %v", r.URL.String(), err)
	}
	return urlParsed, nil
}

func GetAuth(r *http.Request, parsedURL *url.URL) string {
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" {
		return authHeader
	}

	// fallback to query param
	return parsedURL.Query().Get("auth")
}

func DecodeAuth(in string) (string, string, error) {
	decodedBytes, err := base64.StdEncoding.DecodeString(in)
	if err != nil {
		return "", "", fmt.Errorf("failed to decode auth header")
	}
	data := strings.Split(string(decodedBytes), ":")
	if len(data) != 2 {
		return "", "", fmt.Errorf("invalid auth header")
	}
	return data[0], data[1], nil
}

// GetSleepParams returns the sleep time and max sleep time from the request
func GetSleepParams(parsedURL *url.URL, delayInMs, maxDelayInMs int64) (int64, int64) {

	sleepTime, sleepMax := delayInMs, maxDelayInMs

	sleep := parsedURL.Query().Get("sleep")
	if sleep != "" {
		sleepTime = AToI(sleep)
	}

	maxSleep := parsedURL.Query().Get("max_sleep")
	if maxSleep != "" {
		sleepMax = AToI(maxSleep)
	}

	return sleepTime, sleepMax
}

// AToI converts a string to an int64
func AToI(value string) int64 {
	i, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0
	}
	return i
}

// GetSlotStartTime returns the time of the start of the slot
func GetSlotStartTime(beaconGenesisTime, slot int64) time.Time {
	return time.Unix(beaconGenesisTime+(int64(slot)*12), 0).UTC()
}

type VersionedSignedBlindedBeaconBlock struct {
	eth2Api.VersionedSignedBlindedBeaconBlock
}

func (r *VersionedSignedBlindedBeaconBlock) MarshalJSON() ([]byte, error) {
	switch r.Version { //nolint:exhaustive
	case spec.DataVersionCapella:
		return json.Marshal(r.Capella)
	case spec.DataVersionDeneb:
		return json.Marshal(r.Deneb)
	default:
		return nil, fmt.Errorf("%s is not supported", r.Version)
	}
}

func (r *VersionedSignedBlindedBeaconBlock) UnmarshalJSON(input []byte) error {
	var err error

	denebBlock := new(eth2ApiV1Deneb.SignedBlindedBeaconBlock)
	if err = json.Unmarshal(input, denebBlock); err == nil {
		r.Version = spec.DataVersionDeneb
		r.Deneb = denebBlock
		return nil
	}

	capellaBlock := new(eth2ApiV1Capella.SignedBlindedBeaconBlock)
	if err = json.Unmarshal(input, capellaBlock); err == nil {
		r.Version = spec.DataVersionCapella
		r.Capella = capellaBlock
		return nil
	}
	return fmt.Errorf("failed to unmarshal SignedBlindedBeaconBlock : %v", err)
}
