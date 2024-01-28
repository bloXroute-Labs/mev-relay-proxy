package api

import (
	"time"
)

const (
	statsGRPCCompression              = "stats.grpc_compression"
	statsRegionalRelayBidCancellation = "stats.regional_relay_bid_cancellation"
)

type compressionStats struct {
	Slot            int64     `json:"slot"`
	BlockHash       string    `json:"block_hash"`
	Role            string    `json:"role"`
	StartTime       time.Time `json:"start_time"`
	Success         bool      `json:"success"`
	CompressionRate float64   `json:"compression_rate"`
	Duration        int64     `json:"duration"`
	NodeId          string    `json:"node_id"`
	NumTxs          int       `json:"num_txs"`
	NumShortIds     int       `json:"num_short_ids"`
	SizeBefore      uint64    `json:"size_before"`
	SizeAfter       uint64    `json:"size_after"`
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
