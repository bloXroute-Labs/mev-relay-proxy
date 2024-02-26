package api

import (
	"github.com/bloXroute-Labs/mev-relay-proxy/fluentstats"
	"time"
)

const (
	StatsRelayProxyGetHeader  = "relay-proxy-getHeader"
	StatsRelayProxyGetPayload = "relay-proxy-getPayload"

	TypeRelayProxyGetHeader  = "relay_proxy_provided_header"
	TypeRelayProxyGetPayload = "relay_proxy_provided_payload"
)

type GetHeaderStatsRecord struct {
	RequestReceivedAt        time.Time     `json:"request_received_at"`
	FetchGetHeaderStartTime  string        `json:"fetch_get_header_start_time"`
	FetchGetHeaderDurationMS int64         `json:"fetch_get_header_duration_ms"`
	Duration                 time.Duration `json:"duration"`
	MsIntoSlot               int64         `json:"ms_into_slot"`
	ParentHash               string        `json:"parent_hash"`
	PubKey                   string        `json:"pub_key"`
	BlockHash                string        `json:"block_hash"`
	ReqID                    string        `json:"req_id"`
	ClientIP                 string        `json:"client_ip"`
	BlockValue               string        `json:"block_value"`
	Succeeded                bool          `json:"succeeded"`
	NodeID                   string        `json:"node_id"`
	Slot                     int64         `json:"slot"`
	AccountID                string        `json:"account_id"`
	ValidatorID              string        `json:"validator_id"`
}

type GetPayloadStatsRecord struct {
	RequestReceivedAt time.Time     `json:"request_received_at"`
	Duration          time.Duration `json:"duration"`
	SlotStartTime     time.Time     `json:"slot_start_time"`
	MsIntoSlot        int64         `json:"ms_into_slot"`
	Slot              uint64        `json:"slot"`
	ParentHash        string        `json:"parent_hash"`
	PubKey            string        `json:"pub_key"`
	BlockHash         string        `json:"block_hash"`
	ReqID             string        `json:"req_id"`
	ClientIP          string        `json:"client_ip"`
	Succeeded         bool          `json:"succeeded"`
	NodeID            string        `json:"node_id"`
	AccountID         string        `json:"account_id"`
	ValidatorID       string        `json:"validator_id"`
}

type StatsRecord struct {
	Record    fluentstats.Record
	Timestamp time.Time
	NodeID    string
	Type      string
}
