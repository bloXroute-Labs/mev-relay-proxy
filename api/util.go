package api

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"

	"fmt"
	eth2Api "github.com/attestantio/go-eth2-client/api"
	eth2ApiV1Capella "github.com/attestantio/go-eth2-client/api/v1/capella"
	eth2ApiV1Deneb "github.com/attestantio/go-eth2-client/api/v1/deneb"
	"github.com/attestantio/go-eth2-client/spec"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common/hexutil"

	"go.uber.org/zap"
)

const (
	weiToEthSignificantDigits = 18
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
		return "", "", fmt.Errorf("failed to decode auth header")
	}
	data := strings.Split(string(decodedBytes), ":")
	if len(data) != 2 {
		return "", "", fmt.Errorf("invalid auth header")
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
func WeiToEth(valueString string) string {
	numDigits := len(valueString)
	missing := int(math.Max(0, float64((weiToEthSignificantDigits+1)-numDigits)))
	prefix := "0000000000000000000"[:missing]
	ethValue := prefix + valueString
	decimalIndex := len(ethValue) - weiToEthSignificantDigits
	return ethValue[:decimalIndex] + "." + ethValue[decimalIndex:]
}

// DecodeExtraData returns a decoded string from block ExtraData
func DecodeExtraData(extraData []byte) string {
	extraDataString := hexutil.Bytes(extraData).String()
	decodedExtraData, err := hex.DecodeString(strings.TrimPrefix(extraDataString, "0x"))
	if err != nil {
		return ""
	}
	return string(decodedExtraData)
}
