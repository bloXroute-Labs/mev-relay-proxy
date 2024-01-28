package api

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/ethereum/go-ethereum/common/hexutil"
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
