package api

import (
	"time"
)

const (
	statsRelayProxyGetHeader       = "stats.relay-proxy-getHeader"
	statsRelayProxyBidCancellation = "stats.relay-proxy-bid-cancellation"
	statsRelayProxyGetPayload      = "stats.relay-proxy-getPayload"

	typeRelayProxyGetHeader  = "relay_proxy_provided_header"
	typeRelayProxyGetPayload = "relay_proxy_provided_payload"
)

type getHeaderStatsRecord struct {
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
}

type getPayloadStatsRecord struct {
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
}

type blockReplacedStatsRecord struct {
	Slot                   uint64    `json:"slot"`
	ParentHash             string    `json:"parent_hash"`
	ProposerPubkey         string    `json:"proposer_pubkey"`
	BuilderPubkey          string    `json:"builder_pubkey"`
	ReplacedBlockHash      string    `json:"replaced_block_hash"`
	ReplacedBlockValue     string    `json:"replaced_block_value"`
	ReplacedBlockETHValue  string    `json:"replaced_block_eth_value"`
	ReplacedBlockExtraData string    `json:"replaced_block_extra_data"`
	NewBlockHash           string    `json:"new_block_hash"`
	NewBlockValue          string    `json:"new_block_value"`
	NewBlockEthValue       string    `json:"new_block_eth_value"`
	NewBlockExtraData      string    `json:"new_block_extra_data"`
	ReplacementTime        time.Time `json:"replacement_time"`
}
