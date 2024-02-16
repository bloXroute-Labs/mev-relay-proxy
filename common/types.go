package common

import (
	"encoding/json"
	"errors"
	"fmt"
	eth2Api "github.com/attestantio/go-eth2-client/api"
	eth2ApiV1Capella "github.com/attestantio/go-eth2-client/api/v1/capella"
	eth2ApiV1Deneb "github.com/attestantio/go-eth2-client/api/v1/deneb"
	"github.com/attestantio/go-eth2-client/spec"
	"github.com/attestantio/go-eth2-client/spec/phase0"
	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/flashbots/go-boost-utils/ssz"
	boostSsz "github.com/flashbots/go-boost-utils/ssz"
	boostTypes "github.com/flashbots/go-boost-utils/types"
	"os"
)

var (
	ErrUnknownNetwork = errors.New("unknown network")
	ErrInvalidVersion = errors.New("invalid version")
	ErrDataMissing    = errors.New("data missing")

	EthNetworkHolesky = "holesky"
	EthNetworkSepolia = "sepolia"
	EthNetworkGoerli  = "goerli"
	EthNetworkMainnet = "mainnet"
	EthNetworkCustom  = "custom"

	GenesisForkVersionHolesky = "0x01017000"

	GenesisValidatorsRootHolesky = "0x9143aa7c615a7f7115e2b6aac319c03529df8242ae705fba9df39b79c59fa8b1"

	BellatrixForkVersionHolesky = "0x03017000"

	CapellaForkVersionHolesky = "0x04017000"
	CapellaForkVersionSepolia = "0x90000072"
	CapellaForkVersionGoerli  = "0x03001020"
	CapellaForkVersionMainnet = "0x03000000"

	DenebForkVersionHolesky = "0x05017000"
	DenebForkVersionSepolia = "0x90000073"
	DenebForkVersionGoerli  = "0x04001020"
	DenebForkVersionMainnet = "0x04000000"
)

type EthNetworkDetails struct {
	Name                     string
	GenesisForkVersionHex    string
	GenesisValidatorsRootHex string
	BellatrixForkVersionHex  string
	CapellaForkVersionHex    string
	DenebForkVersionHex      string

	DomainBuilder                 phase0.Domain
	DomainBeaconProposerBellatrix phase0.Domain
	DomainBeaconProposerCapella   phase0.Domain
	DomainBeaconProposerDeneb     phase0.Domain
}

func NewEthNetworkDetails(networkName string) (ret *EthNetworkDetails, err error) {
	var genesisForkVersion string
	var genesisValidatorsRoot string
	var bellatrixForkVersion string
	var capellaForkVersion string
	var denebForkVersion string
	var domainBuilder phase0.Domain
	var domainBeaconProposerBellatrix phase0.Domain
	var domainBeaconProposerCapella phase0.Domain
	var domainBeaconProposerDeneb phase0.Domain

	switch networkName {
	case EthNetworkHolesky:
		genesisForkVersion = GenesisForkVersionHolesky
		genesisValidatorsRoot = GenesisValidatorsRootHolesky
		bellatrixForkVersion = BellatrixForkVersionHolesky
		capellaForkVersion = CapellaForkVersionHolesky
		denebForkVersion = DenebForkVersionHolesky
	case EthNetworkSepolia:
		genesisForkVersion = boostTypes.GenesisForkVersionSepolia
		genesisValidatorsRoot = boostTypes.GenesisValidatorsRootSepolia
		bellatrixForkVersion = boostTypes.BellatrixForkVersionSepolia
		capellaForkVersion = CapellaForkVersionSepolia
		denebForkVersion = DenebForkVersionSepolia
	case EthNetworkGoerli:
		genesisForkVersion = boostTypes.GenesisForkVersionGoerli
		genesisValidatorsRoot = boostTypes.GenesisValidatorsRootGoerli
		bellatrixForkVersion = boostTypes.BellatrixForkVersionGoerli
		capellaForkVersion = CapellaForkVersionGoerli
		denebForkVersion = DenebForkVersionGoerli
	case EthNetworkMainnet:
		genesisForkVersion = boostTypes.GenesisForkVersionMainnet
		genesisValidatorsRoot = boostTypes.GenesisValidatorsRootMainnet
		bellatrixForkVersion = boostTypes.BellatrixForkVersionMainnet
		capellaForkVersion = CapellaForkVersionMainnet
		denebForkVersion = DenebForkVersionMainnet
	case EthNetworkCustom:
		genesisForkVersion = os.Getenv("GENESIS_FORK_VERSION")
		genesisValidatorsRoot = os.Getenv("GENESIS_VALIDATORS_ROOT")
		bellatrixForkVersion = os.Getenv("BELLATRIX_FORK_VERSION")
		capellaForkVersion = os.Getenv("CAPELLA_FORK_VERSION")
		denebForkVersion = os.Getenv("DENEB_FORK_VERSION")
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnknownNetwork, networkName)
	}

	domainBuilder, err = ComputeDomain(boostSsz.DomainTypeAppBuilder, genesisForkVersion, phase0.Root{}.String())
	if err != nil {
		return nil, err
	}

	domainBeaconProposerBellatrix, err = ComputeDomain(boostSsz.DomainTypeBeaconProposer, bellatrixForkVersion, genesisValidatorsRoot)
	if err != nil {
		return nil, err
	}

	domainBeaconProposerCapella, err = ComputeDomain(boostSsz.DomainTypeBeaconProposer, capellaForkVersion, genesisValidatorsRoot)
	if err != nil {
		return nil, err
	}

	domainBeaconProposerDeneb, err = ComputeDomain(boostSsz.DomainTypeBeaconProposer, denebForkVersion, genesisValidatorsRoot)
	if err != nil {
		return nil, err
	}

	return &EthNetworkDetails{
		Name:                          networkName,
		GenesisForkVersionHex:         genesisForkVersion,
		GenesisValidatorsRootHex:      genesisValidatorsRoot,
		BellatrixForkVersionHex:       bellatrixForkVersion,
		CapellaForkVersionHex:         capellaForkVersion,
		DenebForkVersionHex:           denebForkVersion,
		DomainBuilder:                 domainBuilder,
		DomainBeaconProposerBellatrix: domainBeaconProposerBellatrix,
		DomainBeaconProposerCapella:   domainBeaconProposerCapella,
		DomainBeaconProposerDeneb:     domainBeaconProposerDeneb,
	}, nil
}

func (e *EthNetworkDetails) String() string {
	return fmt.Sprintf(
		`EthNetworkDetails{
	Name: %s, 
	GenesisForkVersionHex: %s, 
	GenesisValidatorsRootHex: %s,
	BellatrixForkVersionHex: %s, 
	CapellaForkVersionHex: %s, 
	DenebForkVersionHex: %s,
	DomainBuilder: %x, 
	DomainBeaconProposerBellatrix: %x, 
	DomainBeaconProposerCapella: %x, 
	DomainBeaconProposerDeneb: %x
}`,
		e.Name,
		e.GenesisForkVersionHex,
		e.GenesisValidatorsRootHex,
		e.BellatrixForkVersionHex,
		e.CapellaForkVersionHex,
		e.DenebForkVersionHex,
		e.DomainBuilder,
		e.DomainBeaconProposerBellatrix,
		e.DomainBeaconProposerCapella,
		e.DomainBeaconProposerDeneb)
}

// ComputeDomain computes the signing domain
func ComputeDomain(domainType phase0.DomainType, forkVersionHex, genesisValidatorsRootHex string) (domain phase0.Domain, err error) {
	genesisValidatorsRoot := phase0.Root(ethcommon.HexToHash(genesisValidatorsRootHex))
	forkVersionBytes, err := hexutil.Decode(forkVersionHex)
	if err != nil || len(forkVersionBytes) != 4 {
		return domain, errors.New("invalid fork version")
	}
	var forkVersion [4]byte
	copy(forkVersion[:], forkVersionBytes[:4])
	return ssz.ComputeDomain(domainType, forkVersion, genesisValidatorsRoot), nil
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
	return fmt.Errorf("failed to unmarshal SignedBlindedBeaconBlock %v", err)
}

// ExecutionParentHash returns the parent hash of the beacon block.
func (r *VersionedSignedBlindedBeaconBlock) ExecutionParentHash() (phase0.Hash32, error) {
	switch r.Version {
	case spec.DataVersionBellatrix:
		if r.Bellatrix == nil ||
			r.Bellatrix.Message == nil ||
			r.Bellatrix.Message.Body == nil ||
			r.Bellatrix.Message.Body.ExecutionPayloadHeader == nil {
			return phase0.Hash32{}, ErrDataMissing
		}

		return r.Bellatrix.Message.Body.ExecutionPayloadHeader.ParentHash, nil
	case spec.DataVersionCapella:
		if r.Capella == nil ||
			r.Capella.Message == nil ||
			r.Capella.Message.Body == nil ||
			r.Capella.Message.Body.ExecutionPayloadHeader == nil {
			return phase0.Hash32{}, ErrDataMissing
		}

		return r.Capella.Message.Body.ExecutionPayloadHeader.ParentHash, nil
	case spec.DataVersionDeneb:
		if r.Deneb == nil ||
			r.Deneb.Message == nil ||
			r.Deneb.Message.Body == nil ||
			r.Deneb.Message.Body.ExecutionPayloadHeader == nil {
			return phase0.Hash32{}, ErrDataMissing
		}

		return r.Deneb.Message.Body.ExecutionPayloadHeader.ParentHash, nil
	default:
		return phase0.Hash32{}, fmt.Errorf("%s is not supported", r.Version)
	}
}
