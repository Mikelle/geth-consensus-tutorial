package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/mikelle/geth-consensus-tutorial/03-member-nodes/pkg/postgres"
)

// Server provides HTTP API for block syncing
type Server struct {
	store  *postgres.PayloadStore
	logger *slog.Logger
	server *http.Server
}

// NewServer creates a new API server
func NewServer(store *postgres.PayloadStore, addr string, logger *slog.Logger) *Server {
	s := &Server{
		store:  store,
		logger: logger,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/blocks/latest", s.handleLatestBlock)
	mux.HandleFunc("/blocks/", s.handleGetBlock)
	mux.HandleFunc("/blocks", s.handleGetBlocksAfter)

	s.server = &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	return s
}

// BlockResponse represents a block in API responses
type BlockResponse struct {
	BlockNumber  uint64 `json:"block_number"`
	BlockHash    string `json:"block_hash"`
	ParentHash   string `json:"parent_hash"`
	PayloadData  string `json:"payload_data"`
	RequestsData string `json:"requests_data"`
	Timestamp    int64  `json:"timestamp"`
}

// BlocksResponse represents multiple blocks
type BlocksResponse struct {
	Blocks []*BlockResponse `json:"blocks"`
}

func (s *Server) handleLatestBlock(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	payload, err := s.store.GetLatestPayload(r.Context())
	if err != nil {
		s.logger.Error("Failed to get latest payload", "error", err)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	resp := &BlockResponse{
		BlockNumber:  payload.BlockNumber,
		BlockHash:    payload.BlockHash,
		ParentHash:   payload.ParentHash,
		PayloadData:  payload.PayloadData,
		RequestsData: payload.RequestsData,
		Timestamp:    payload.Timestamp,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleGetBlock(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse block identifier from path: /blocks/{number or hash}
	path := r.URL.Path[len("/blocks/"):]
	if path == "" {
		http.Error(w, "block identifier required", http.StatusBadRequest)
		return
	}

	var payload *postgres.Payload
	var err error

	// Try to parse as number first
	if number, parseErr := strconv.ParseUint(path, 10, 64); parseErr == nil {
		payload, err = s.store.GetPayloadByNumber(r.Context(), number)
	} else {
		// Assume it's a hash
		payload, err = s.store.GetPayloadByHash(r.Context(), path)
	}

	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	resp := &BlockResponse{
		BlockNumber:  payload.BlockNumber,
		BlockHash:    payload.BlockHash,
		ParentHash:   payload.ParentHash,
		PayloadData:  payload.PayloadData,
		RequestsData: payload.RequestsData,
		Timestamp:    payload.Timestamp,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleGetBlocksAfter(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	afterStr := r.URL.Query().Get("after")
	if afterStr == "" {
		afterStr = "0"
	}

	after, err := strconv.ParseUint(afterStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid after parameter", http.StatusBadRequest)
		return
	}

	limitStr := r.URL.Query().Get("limit")
	if limitStr == "" {
		limitStr = "100"
	}

	limit, err := strconv.Atoi(limitStr)
	if err != nil || limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	payloads, err := s.store.GetPayloadsAfter(r.Context(), after, limit)
	if err != nil {
		s.logger.Error("Failed to get payloads", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	resp := &BlocksResponse{
		Blocks: make([]*BlockResponse, 0, len(payloads)),
	}

	for _, p := range payloads {
		resp.Blocks = append(resp.Blocks, &BlockResponse{
			BlockNumber:  p.BlockNumber,
			BlockHash:    p.BlockHash,
			ParentHash:   p.ParentHash,
			PayloadData:  p.PayloadData,
			RequestsData: p.RequestsData,
			Timestamp:    p.Timestamp,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// Start starts the API server
func (s *Server) Start() {
	s.logger.Info("API server starting", "addr", s.server.Addr)
	go func() {
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Error("API server error", "error", err)
		}
	}()
}

// Stop gracefully stops the API server
func (s *Server) Stop(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}
