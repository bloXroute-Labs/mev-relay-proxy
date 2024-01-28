package api

import (
	"fmt"

	builderApi "github.com/attestantio/go-builder-client/api"
	builderApiCapella "github.com/attestantio/go-builder-client/api/capella"
	builderApiDeneb "github.com/attestantio/go-builder-client/api/deneb"
	builderSpec "github.com/attestantio/go-builder-client/spec"
	"github.com/attestantio/go-eth2-client/spec"
	"github.com/attestantio/go-eth2-client/spec/phase0"
	eth2UtilDeneb "github.com/attestantio/go-eth2-client/util/deneb"
	"github.com/flashbots/go-boost-utils/utils"
	"github.com/pkg/errors"
)

func BuildGetHeaderResponse(payload *VersionedSubmitBlockRequest) (*builderSpec.VersionedSignedBuilderBid, error) {
	if payload == nil {
		return nil, errMissingRequest
	}

	versionedPayload := &builderApi.VersionedExecutionPayload{Version: payload.Version}
	switch payload.Version {
	case spec.DataVersionCapella:
		versionedPayload.Capella = payload.Capella.ExecutionPayload
		header, err := utils.PayloadToPayloadHeader(versionedPayload)
		if err != nil {
			return nil, err
		}
		signedBuilderBid, err := BuilderBlockRequestToSignedBuilderBid(payload, header)
		if err != nil {
			return nil, err
		}
		return &builderSpec.VersionedSignedBuilderBid{
			Version: spec.DataVersionCapella,
			Capella: signedBuilderBid.Capella,
		}, nil
	case spec.DataVersionDeneb:
		versionedPayload.Deneb = payload.Deneb.ExecutionPayload
		header, err := utils.PayloadToPayloadHeader(versionedPayload)
		if err != nil {
			return nil, err
		}
		signedBuilderBid, err := BuilderBlockRequestToSignedBuilderBid(payload, header)
		if err != nil {
			return nil, err
		}
		return &builderSpec.VersionedSignedBuilderBid{
			Version: spec.DataVersionDeneb,
			Deneb:   signedBuilderBid.Deneb,
		}, nil
	case spec.DataVersionUnknown, spec.DataVersionPhase0, spec.DataVersionAltair, spec.DataVersionBellatrix:
		return nil, errInvalidVersion
	default:
		return nil, errEmptyPayload
	}
}

func BuilderBlockRequestToSignedBuilderBid(payload *VersionedSubmitBlockRequest, header *builderApi.VersionedExecutionPayloadHeader) (*builderSpec.VersionedSignedBuilderBid, error) {
	value, err := payload.Value()
	if err != nil {
		return nil, err
	}

	builderPubkey, err := payload.Builder()
	if err != nil {
		return nil, err
	}

	signature, err := payload.Signature()
	if err != nil {
		return nil, err
	}

	switch payload.Version {
	case spec.DataVersionCapella:
		builderBid := builderApiCapella.BuilderBid{
			Value:  value,
			Header: header.Capella,
			Pubkey: builderPubkey,
		}

		return &builderSpec.VersionedSignedBuilderBid{
			Version: spec.DataVersionCapella,
			Capella: &builderApiCapella.SignedBuilderBid{
				Message:   &builderBid,
				Signature: signature,
			},
		}, nil
	case spec.DataVersionDeneb:
		var blobRoots []phase0.Root
		for i, blob := range payload.Deneb.BlobsBundle.Blobs {
			blobRootHelper := eth2UtilDeneb.BeaconBlockBlob{Blob: blob}
			root, err := blobRootHelper.HashTreeRoot()
			if err != nil {
				return nil, errors.Wrap(err, fmt.Sprintf("failed to calculate blob root at blob index %d", i))
			}
			blobRoots = append(blobRoots, root)
		}

		builderBid := builderApiDeneb.BuilderBid{
			Header:             header.Deneb,
			BlobKZGCommitments: payload.Deneb.BlobsBundle.Commitments,
			Value:              value,
			Pubkey:             builderPubkey,
		}

		return &builderSpec.VersionedSignedBuilderBid{
			Version: spec.DataVersionDeneb,
			Deneb: &builderApiDeneb.SignedBuilderBid{
				Message:   &builderBid,
				Signature: signature,
			},
		}, nil
	case spec.DataVersionUnknown, spec.DataVersionPhase0, spec.DataVersionAltair, spec.DataVersionBellatrix:
		fallthrough
	default:
		return nil, errors.Wrap(errInvalidVersion, fmt.Sprintf("%s is not supported", payload.Version.String()))
	}
}
