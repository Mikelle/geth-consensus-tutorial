package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/mikelle/geth-consensus-tutorial/03-member-nodes/pkg/postgres"
)

// Syncer synchronizes blocks from a leader node
type Syncer struct {
	leaderURL   string
	store       *postgres.PayloadStore
	logger      *slog.Logger
	httpClient  *http.Client
	lastSynced  atomic.Uint64
	syncedCount atomic.Uint64
}

// NewSyncer creates a new block syncer
func NewSyncer(leaderURL string, store *postgres.PayloadStore, logger *slog.Logger) *Syncer {
	return &Syncer{
		leaderURL: leaderURL,
		store:     store,
		logger:    logger,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// BlockResponse matches the API response format
type BlockResponse struct {
	BlockNumber uint64 `json:"block_number"`
	BlockHash   string `json:"block_hash"`
	ParentHash  string `json:"parent_hash"`
	PayloadData string `json:"payload_data"`
	Timestamp   int64  `json:"timestamp"`
}

// BlocksResponse for batch fetching
type BlocksResponse struct {
	Blocks []*BlockResponse `json:"blocks"`
}

// Start begins the sync loop
func (s *Syncer) Start(ctx context.Context) {
	// Initialize from existing data
	if latest, err := s.store.GetLatestPayload(ctx); err == nil {
		s.lastSynced.Store(latest.BlockNumber)
		s.logger.Info("Resuming sync", "lastBlock", latest.BlockNumber)
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
		payload := &postgres.Payload{
			BlockNumber: block.BlockNumber,
			BlockHash:   block.BlockHash,
			ParentHash:  block.ParentHash,
			PayloadData: block.PayloadData,
			Timestamp:   block.Timestamp,
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

// GetLastSynced returns the last synced block number
func (s *Syncer) GetLastSynced() uint64 {
	return s.lastSynced.Load()
}

// GetSyncedCount returns total synced blocks
func (s *Syncer) GetSyncedCount() uint64 {
	return s.syncedCount.Load()
}
