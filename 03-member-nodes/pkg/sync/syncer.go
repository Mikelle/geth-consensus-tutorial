package sync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/beacon/engine"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/mikelle/geth-consensus-tutorial/03-member-nodes/pkg/postgres"
	"github.com/vmihailenco/msgpack/v5"
)

// ExecutionEngine is the subset of Engine API methods needed by the syncer.
type ExecutionEngine interface {
	NewPayloadV4(ctx context.Context, payload engine.ExecutableData, versionedHashes []common.Hash, beaconRoot *common.Hash, requests [][]byte) (engine.PayloadStatusV1, error)
	ForkchoiceUpdatedV3(ctx context.Context, state engine.ForkchoiceStateV1, attrs *engine.PayloadAttributes) (engine.ForkChoiceResponse, error)
	HeaderByNumber(ctx context.Context, number interface{}) (*types.Header, error)
}

// Syncer synchronizes blocks from a leader node
type Syncer struct {
	leaderURL   string
	store       *postgres.PayloadStore
	engine      ExecutionEngine
	logger      *slog.Logger
	httpClient  *http.Client
	lastSynced  atomic.Uint64
	syncedCount atomic.Uint64
}

// NewSyncer creates a new block syncer
func NewSyncer(leaderURL string, store *postgres.PayloadStore, engine ExecutionEngine, logger *slog.Logger) *Syncer {
	return &Syncer{
		leaderURL: leaderURL,
		store:     store,
		engine:    engine,
		logger:    logger,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// BlockResponse matches the API response format
type BlockResponse struct {
	BlockNumber  uint64 `json:"block_number"`
	BlockHash    string `json:"block_hash"`
	ParentHash   string `json:"parent_hash"`
	PayloadData  string `json:"payload_data"`
	RequestsData string `json:"requests_data"`
	Timestamp    int64  `json:"timestamp"`
}

// BlocksResponse for batch fetching
type BlocksResponse struct {
	Blocks []*BlockResponse `json:"blocks"`
}

// Start begins the sync loop
func (s *Syncer) Start(ctx context.Context) {
	// If we have a local Geth, use its head as the starting point
	if s.engine != nil {
		header, err := s.engine.HeaderByNumber(ctx, nil)
		if err == nil {
			s.lastSynced.Store(header.Number.Uint64())
			s.logger.Info("Resuming sync from Geth head", "block", header.Number.Uint64())
		}
	}

	// Fall back to PostgreSQL head
	if s.lastSynced.Load() == 0 {
		if latest, err := s.store.GetLatestPayload(ctx); err == nil {
			s.lastSynced.Store(latest.BlockNumber)
			s.logger.Info("Resuming sync from PostgreSQL", "lastBlock", latest.BlockNumber)
		}
	}

	go s.syncLoop(ctx)
}

func (s *Syncer) syncLoop(ctx context.Context) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.syncBatch(ctx); err != nil {
				s.logger.Warn("Sync batch failed", "error", err)
			}
		}
	}
}

func (s *Syncer) syncBatch(ctx context.Context) error {
	lastSynced := s.lastSynced.Load()

	url := fmt.Sprintf("%s/blocks?after=%d&limit=100", s.leaderURL, lastSynced)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var blocksResp BlocksResponse
	if err := json.NewDecoder(resp.Body).Decode(&blocksResp); err != nil {
		return err
	}

	for _, block := range blocksResp.Blocks {
		// Execute on local Geth if available
		if s.engine != nil {
			if err := s.executeBlock(ctx, block); err != nil {
				return fmt.Errorf("execute block %d: %w", block.BlockNumber, err)
			}
		}

		// Store in PostgreSQL
		payload := &postgres.Payload{
			BlockNumber:  block.BlockNumber,
			BlockHash:    block.BlockHash,
			ParentHash:   block.ParentHash,
			PayloadData:  block.PayloadData,
			RequestsData: block.RequestsData,
			Timestamp:    block.Timestamp,
		}

		if err := s.store.SavePayload(ctx, payload); err != nil {
			return fmt.Errorf("save payload %d: %w", block.BlockNumber, err)
		}

		s.lastSynced.Store(block.BlockNumber)
		s.syncedCount.Add(1)

		s.logger.Debug("Synced block",
			"number", block.BlockNumber,
			"hash", block.BlockHash[:16]+"...")
	}

	if len(blocksResp.Blocks) > 0 {
		s.logger.Info("Synced blocks",
			"count", len(blocksResp.Blocks),
			"latest", blocksResp.Blocks[len(blocksResp.Blocks)-1].BlockNumber)
	}

	return nil
}

func (s *Syncer) executeBlock(ctx context.Context, block *BlockResponse) error {
	// Decode execution payload
	payloadBytes, err := base64.StdEncoding.DecodeString(block.PayloadData)
	if err != nil {
		return fmt.Errorf("decode payload: %w", err)
	}

	var execPayload engine.ExecutableData
	if err := msgpack.Unmarshal(payloadBytes, &execPayload); err != nil {
		return fmt.Errorf("unmarshal payload: %w", err)
	}

	// Decode requests
	var requests [][]byte
	if block.RequestsData != "" {
		requestsBytes, err := base64.StdEncoding.DecodeString(block.RequestsData)
		if err != nil {
			return fmt.Errorf("decode requests: %w", err)
		}
		if err := msgpack.Unmarshal(requestsBytes, &requests); err != nil {
			return fmt.Errorf("unmarshal requests: %w", err)
		}
	}

	// NewPayloadV4: push the block to Geth
	parentHash := common.HexToHash(block.ParentHash)
	status, err := s.engine.NewPayloadV4(ctx, execPayload, []common.Hash{}, &parentHash, requests)
	if err != nil {
		return fmt.Errorf("NewPayloadV4: %w", err)
	}
	if status.Status == engine.INVALID {
		msg := "unknown"
		if status.ValidationError != nil {
			msg = *status.ValidationError
		}
		return fmt.Errorf("payload invalid: %s", msg)
	}

	// ForkchoiceUpdatedV3: set the new head
	fcs := engine.ForkchoiceStateV1{
		HeadBlockHash:      execPayload.BlockHash,
		SafeBlockHash:      execPayload.BlockHash,
		FinalizedBlockHash: execPayload.BlockHash,
	}
	if _, err := s.engine.ForkchoiceUpdatedV3(ctx, fcs, nil); err != nil {
		return fmt.Errorf("ForkchoiceUpdatedV3: %w", err)
	}

	return nil
}

// GetLastSynced returns the last synced block number
func (s *Syncer) GetLastSynced() uint64 {
	return s.lastSynced.Load()
}

// GetSyncedCount returns total synced blocks
func (s *Syncer) GetSyncedCount() uint64 {
	return s.syncedCount.Load()
}
