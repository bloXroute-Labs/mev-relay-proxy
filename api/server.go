package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/attestantio/go-eth2-client/spec"
	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/bloXroute-Labs/gateway/v2/utils/syncmap"
	relayGRPC "github.com/bloXroute-Labs/relay-grpc"
	"github.com/go-chi/chi/v5"
	"github.com/holiman/uint256"
	"github.com/patrickmn/go-cache"

	"go.uber.org/zap"
)

// Router paths
var (
	pathStatus            = "/eth/v1/builder/status"
	pathRegisterValidator = "/eth/v1/builder/validators"
	pathGetHeader         = "/eth/v1/builder/header/{slot:[0-9]+}/{parent_hash:0x[a-fA-F0-9]+}/{pubkey:0x[a-fA-F0-9]+}"
	pathGetPayload        = "/eth/v1/builder/blinded_blocks"

	AuthHeaderPrefix = "bearer "

	// methods
	getHeader    = "getHeader"
	getPayload   = "getPayload"
	registration = "registration"

	// errors
	errInvalidVersion = errors.New("invalid version")
	errMissingRequest = errors.New("req is nil")
	errEmptyPayload   = errors.New("empty payload")

	// cache
	builderBidsCleanupInterval = 60 * time.Second // 5 slots
	cacheKeySeparator          = "_"
)

type Server struct {
	logger             *zap.Logger
	server             *http.Server
	svc                IService
	listenAddress      string
	getHeaderDelay     int
	builderBidsForSlot *cache.Cache
}

func New(logger *zap.Logger, svc *Service, listenAddress string, getHeaderDelay int) *Server {
	return &Server{
		logger:             logger,
		svc:                svc,
		listenAddress:      listenAddress,
		getHeaderDelay:     getHeaderDelay,
		builderBidsForSlot: cache.New(builderBidsCleanupInterval, builderBidsCleanupInterval),
	}
}

func (s *Server) Start() error {
	s.server = &http.Server{
		Addr:              s.listenAddress,
		Handler:           s.InitHandler(),
		ReadTimeout:       0,
		ReadHeaderTimeout: 0,
		WriteTimeout:      0,
		IdleTimeout:       10 * time.Second,
	}
	err := s.server.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (s *Server) InitHandler() *chi.Mux {
	handler := chi.NewRouter()
	handler.Get(pathStatus, s.HandleStatus)
	handler.Post(pathRegisterValidator, s.HandleRegistration)
	handler.Get(pathGetHeader, s.HandleGetHeader)
	handler.Post(pathGetPayload, s.HandleGetPayload)
	s.logger.Info("Init mev-relay-proxy")
	return handler
}

func (s *Server) Stop() {
	if s.server != nil {
		_ = s.server.Shutdown(context.Background())
	}
}

func (s *Server) HandleStatus(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{}`))
}

func (s *Server) HandleRegistration(w http.ResponseWriter, r *http.Request) {
	receivedAt := time.Now().UTC()
	clientIP := GetIPXForwardedFor(r)
	authHeader := r.Header.Get("authorization")
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		respondError(registration, w, toErrorResp(http.StatusInternalServerError, err.Error(), "", "", "could not read registration", ""), s.logger, nil)
		return
	}
	out, metaData, err := s.svc.RegisterValidator(r.Context(), receivedAt, bodyBytes, clientIP, authHeader)
	if err != nil {
		respondError(registration, w, err, s.logger, metaData)
		return
	}
	respondOK(registration, w, out, s.logger, metaData)
}

func (s *Server) HandleGetHeader(w http.ResponseWriter, r *http.Request) {
	receivedAt := time.Now().UTC()
	slot := chi.URLParam(r, "slot")
	parentHash := chi.URLParam(r, "parent_hash")
	pubKey := chi.URLParam(r, "pubkey")
	clientIP := GetIPXForwardedFor(r)
	<-time.After(time.Millisecond * time.Duration(s.getHeaderDelay))
	out, metaData, err := s.svc.GetHeader(r.Context(), receivedAt, clientIP, slot, parentHash, pubKey)
	if err != nil {
		respondError(getHeader, w, err, s.logger, metaData)
		return
	}
	respondOK(getHeader, w, out, s.logger, metaData)
}

func (s *Server) HandleGetPayload(w http.ResponseWriter, r *http.Request) {
	receivedAt := time.Now().UTC()
	clientIP := GetIPXForwardedFor(r)

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		respondError(getPayload, w, toErrorResp(http.StatusInternalServerError, err.Error(), "", "", "could not read getPayload", ""), s.logger, nil)
		return
	}
	out, metaData, err := s.svc.GetPayload(r.Context(), receivedAt, bodyBytes, clientIP)
	if err != nil {
		respondError(getPayload, w, err, s.logger, metaData)
		return
	}
	respondOK(getPayload, w, out, s.logger, metaData)
}

func respondOK(method string, w http.ResponseWriter, response any, log *zap.Logger, metaData any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Error("couldn't write OK response", zap.Error(err))
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	var meta string
	if metaData != nil {
		meta = metaData.(string)
	}
	log.Info(fmt.Sprintf("%s succeeded", method), zap.String("metaData", meta))
}

func respondError(method string, w http.ResponseWriter, err error, log *zap.Logger, metaData any) {
	resp := err.(*ErrorResp)
	var meta string
	if metaData != nil {
		meta = metaData.(string)
	}
	w.WriteHeader(resp.Code)

	log.With(
		zap.String("req_id", resp.BlxrMessage.reqID),
		zap.String("blxr_message", resp.BlxrMessage.msg),
		zap.String("client_ip", resp.BlxrMessage.clientIP),
		zap.Int("resp_code", resp.Code),
	).Error(fmt.Sprintf("%s failed", method), zap.String("metaData", meta))

	if resp.Message != "" {
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			log.With(
				zap.String("req_id", resp.BlxrMessage.reqID),
				zap.String("blxr_message", resp.BlxrMessage.msg),
				zap.String("message", resp.Message),
				zap.String("client_ip", resp.BlxrMessage.clientIP),
				zap.Int("resp_code", resp.Code),
			).Error("couldn't write error response", zap.Error(err), zap.String("metaData", meta))
			http.Error(w, "", http.StatusInternalServerError)
		}
	}
}

func (s *Server) keyCacheGetHeaderResponse(slot uint64, parentHash string, proposerPubkey string) string {
	return fmt.Sprintf("%d_%s_%s", slot, strings.ToLower(parentHash), strings.ToLower(proposerPubkey))
}

func (s *Server) parseKeyCacheGetHeaderResponse(cacheKey string) (slot uint64, parentHash string, proposerPubkey string, err error) {
	cacheKeyComponents := strings.Split(cacheKey, cacheKeySeparator)
	if len(cacheKeyComponents) != 3 {
		err = errors.New("invalid cache key format")
		return
	}

	slot, parseError := strconv.ParseUint(cacheKeyComponents[0], 10, 64)
	if parseError != nil {
		err = parseError
		return
	}

	parentHash = cacheKeyComponents[1]
	proposerPubkey = cacheKeyComponents[2]

	return
}

func (s *Server) StoreBuilderBid(request *relayGRPC.SubmitBlockRequest) error {
	// don't store any bids for 'GetPayloadOnly' blocks
	if request.GetPayloadOnly {
		s.logger.With(
			zap.Uint64("slot", request.BidTrace.GetSlot()),
			zap.Uint64("blockNumber", request.ExecutionPayload.GetBlockNumber()),
			zap.String("blockHash", phase0.Hash32(request.BidTrace.GetBlockHash()).String()),
			zap.String("builderPubkey", phase0.BLSPubKey(request.BidTrace.GetBuilderPubkey()).String()),
			zap.String("extraData", string(request.ExecutionPayload.GetExtraData())),
		).Info("skipping storage of builder bid for 'GetPayloadOnly' block")
		return nil
	}

	// TODO: add support for Deneb blocks to 'bloxroute/relay-grpc' repo
	submitBlockRequest := &VersionedSubmitBlockRequest{}
	submitBlockRequest.Version = spec.DataVersionCapella
	submitBlockRequest.Capella = relayGRPC.ProtoRequestToCapellaRequest(request)

	// Prepare the getHeader response
	getHeaderResponse, err := BuildGetHeaderResponse(submitBlockRequest)
	if err != nil {
		return err
	}

	slot, err := submitBlockRequest.Slot()
	if err != nil {
		return err
	}

	parentHash, err := submitBlockRequest.ParentHash()
	if err != nil {
		return err
	}

	proposerPubkey, err := submitBlockRequest.ProposerPubKey()
	if err != nil {
		return err
	}

	builderPubkey, err := submitBlockRequest.Builder()
	if err != nil {
		return err
	}

	// store the bid for builder pubkey
	keyGetHeaderResponse := s.keyCacheGetHeaderResponse(slot, parentHash.String(), proposerPubkey.String())
	wrappedGetHeaderResponse := &VersionedSignedBuilderBid{}
	wrappedGetHeaderResponse.VersionedSignedBuilderBid = *getHeaderResponse

	s.setBuilderBidForSlot(keyGetHeaderResponse, builderPubkey.String(), wrappedGetHeaderResponse)

	return nil
}

func (s *Server) GetTopBuilderBid(ctx context.Context, cacheKey string) (*VersionedSignedBuilderBid, error) {
	var builderBidsMap *syncmap.SyncMap[string, *VersionedSignedBuilderBid]
	entry, bidsMapFound := s.builderBidsForSlot.Get(cacheKey)
	if bidsMapFound {
		builderBidsMap = entry.(*syncmap.SyncMap[string, *VersionedSignedBuilderBid])
	}

	if !bidsMapFound || builderBidsMap.Size() == 0 {
		return nil, fmt.Errorf("no builder bids found for cache key %s", cacheKey)
	}

	topBid := &VersionedSignedBuilderBid{}
	topBidValue := uint256.NewInt(0)

	// search for the highest builder bid
	builderBidsMap.Range(func(builderPubkey string, bid *VersionedSignedBuilderBid) bool {
		bidValue, err := bid.Value()
		if err != nil {
			return false
		}

		if bidValue.Cmp(topBidValue) > 0 {
			topBid = bid
			topBidValue = bidValue
		}

		return true
	})

	return topBid, nil
}

func (s *Server) setBuilderBidForSlot(cacheKey string, builderPubkey string, bid *VersionedSignedBuilderBid) {
	var builderBidsMap *syncmap.SyncMap[string, *VersionedSignedBuilderBid]

	// if the cache key does not exist, create a new syncmap and store it in the cache
	if entry, bidsMapFound := s.builderBidsForSlot.Get(cacheKey); !bidsMapFound {
		builderBidsMap = syncmap.NewStringMapOf[*VersionedSignedBuilderBid]()
		s.builderBidsForSlot.Set(cacheKey, builderBidsMap, cache.DefaultExpiration)
	} else {
		// otherwise use the existing syncmap
		builderBidsMap = entry.(*syncmap.SyncMap[string, *VersionedSignedBuilderBid])
	}

	if existingBuilderBid, found := builderBidsMap.Load(builderPubkey); found {
		isBidDataError := false

		oldBidBlockHash, err := existingBuilderBid.BlockHash()
		if err != nil {
			s.logger.Warn("could not get existing block hash", zap.Error(err), zap.String("cacheKey", cacheKey), zap.String("builderPubkey", builderPubkey))
			isBidDataError = true
		}

		newBidBlockHash, err := bid.BlockHash()
		if err != nil {
			s.logger.Warn("could not get replacement block hash", zap.Error(err), zap.String("cacheKey", cacheKey), zap.String("builderPubkey", builderPubkey))
			isBidDataError = true
		}

		oldBidValue, err := existingBuilderBid.Value()
		if err != nil {
			s.logger.Warn("could not get existing bid value", zap.Error(err), zap.String("cacheKey", cacheKey), zap.String("builderPubkey", builderPubkey))
			isBidDataError = true
		}

		newBidValue, err := bid.Value()
		if err != nil {
			s.logger.Warn("could not get replacement bid value", zap.Error(err), zap.String("cacheKey", cacheKey), zap.String("builderPubkey", builderPubkey))
			isBidDataError = true
		}

		oldExtraData, err := existingBuilderBid.ExtraData()
		if err != nil {
			s.logger.Warn("could not get existing bid extra data", zap.Error(err), zap.String("cacheKey", cacheKey), zap.String("builderPubkey", builderPubkey))
			isBidDataError = true
		}

		newExtraData, err := bid.ExtraData()
		if err != nil {
			s.logger.Warn("could not get replacement bid extra data", zap.Error(err), zap.String("cacheKey", cacheKey), zap.String("builderPubkey", builderPubkey))
			isBidDataError = true
		}

		slot, parentHash, proposerPubkey, err := s.parseKeyCacheGetHeaderResponse(cacheKey)
		if err != nil {
			s.logger.With(
				zap.String("cacheKey", cacheKey),
				zap.String("builderPubkey", builderPubkey),
				zap.Error(err),
			).Error("could not parse cache key")
		}

		// if this block is canceling another higher-value block from the same builder,
		// log getHeader cancellation data to fluentd to be loaded into the generic table
		if !isBidDataError && newBidValue.Cmp(oldBidValue) < 0 && s.svc.NodeID() != "" {
			go func() {
				stat := blockReplacedStatsRecord{
					Slot:                   slot,
					ParentHash:             parentHash,
					ProposerPubkey:         proposerPubkey,
					BuilderPubkey:          builderPubkey,
					ReplacedBlockHash:      oldBidBlockHash.String(),
					ReplacedBlockValue:     oldBidValue.Dec(),
					ReplacedBlockETHValue:  WeiToEth(oldBidValue.Dec()),
					ReplacedBlockExtraData: DecodeExtraData(oldExtraData),
					NewBlockHash:           newBidBlockHash.String(),
					NewBlockValue:          newBidValue.Dec(),
					NewBlockEthValue:       WeiToEth(newBidValue.Dec()),
					NewBlockExtraData:      DecodeExtraData(newExtraData),
					ReplacementTime:        time.Now().UTC(),
				}
				// log the cancellation data
				s.logger.Info("logging block replacement data", zap.Any("stat", stat))
			}()
		}
	}

	builderBidsMap.Store(builderPubkey, bid)
}

func (s *Server) getBuilderBidForSlot(cacheKey string, builderPubkey string) (*VersionedSignedBuilderBid, bool) {
	if entry, bidsMapFound := s.builderBidsForSlot.Get(cacheKey); bidsMapFound {
		builderBidsMap := entry.(*syncmap.SyncMap[string, *VersionedSignedBuilderBid])
		builderBid, found := builderBidsMap.Load(builderPubkey)
		return builderBid, found
	}
	return nil, false
}
