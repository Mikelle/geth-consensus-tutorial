// Part 1: Engine API Basics
//
// This example demonstrates the fundamental Engine API operations:
// - JWT authentication
// - ForkchoiceUpdated (trigger block building)
// - GetPayload (retrieve built block)
// - NewPayload (submit block for execution)
//
// Run: go run main.go

package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"time"

	"github.com/ethereum/go-ethereum/beacon/engine"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/golang-jwt/jwt/v5"
)

func main() {
	// Configuration
	engineURL := getEnv("ETH_CLIENT_URL", "http://localhost:8551")
	jwtSecretHex := getEnv("JWT_SECRET", "688f5d737bad920bdfb2fc2f488d6b6209eebeb7b7f7710df3571de7fda67a32")

	jwtSecret, err := hex.DecodeString(jwtSecretHex)
	if err != nil {
		log.Fatalf("Failed to decode JWT secret: %v", err)
	}

	// Create Engine API client
	ctx := context.Background()
	client, err := NewEngineClient(ctx, engineURL, jwtSecret)
	if err != nil {
		log.Fatalf("Failed to create engine client: %v", err)
	}

	// Get current head from Geth
	header, err := client.HeaderByNumber(ctx, nil)
	if err != nil {
		log.Fatalf("Failed to get latest header: %v", err)
	}

	log.Printf("Current head: block=%d hash=%s", header.Number.Uint64(), header.Hash().Hex())

	// Build and finalize a block
	if err := buildBlock(ctx, client, header); err != nil {
		log.Fatalf("Failed to build block: %v", err)
	}
}

func buildBlock(ctx context.Context, client *EngineClient, currentHead *types.Header) error {
	headHash := currentHead.Hash()
	timestamp := uint64(time.Now().Unix())
	if timestamp <= currentHead.Time {
		timestamp = currentHead.Time + 1
	}

	// Step 1: ForkchoiceUpdated - trigger block building
	log.Printf("Step 1: Calling ForkchoiceUpdatedV3...")

	fcs := engine.ForkchoiceStateV1{
		HeadBlockHash:      headHash,
		SafeBlockHash:      headHash,
		FinalizedBlockHash: headHash,
	}

	attrs := &engine.PayloadAttributes{
		Timestamp:             timestamp,
		Random:                headHash, // Use head hash as RANDAO
		SuggestedFeeRecipient: common.HexToAddress("0x0000000000000000000000000000000000000000"),
		Withdrawals:           []*types.Withdrawal{},
		BeaconRoot:            &headHash,
	}

	fcResponse, err := client.ForkchoiceUpdatedV3(ctx, fcs, attrs)
	if err != nil {
		return fmt.Errorf("ForkchoiceUpdatedV3 failed: %w", err)
	}

	if fcResponse.PayloadStatus.Status != engine.VALID {
		return fmt.Errorf("unexpected status: %s", fcResponse.PayloadStatus.Status)
	}

	if fcResponse.PayloadID == nil {
		return fmt.Errorf("no payload ID returned")
	}

	log.Printf("  PayloadID: %s", fcResponse.PayloadID.String())
	log.Printf("  Status: %s", fcResponse.PayloadStatus.Status)

	// Step 2: Wait for Geth to include transactions
	log.Printf("Step 2: Waiting for block assembly (100ms)...")
	time.Sleep(100 * time.Millisecond)

	// Step 3: GetPayload - retrieve the built block
	log.Printf("Step 3: Calling GetPayloadV4...")

	payloadResp, err := client.GetPayloadV4(ctx, *fcResponse.PayloadID)
	if err != nil {
		return fmt.Errorf("GetPayloadV4 failed: %w", err)
	}

	payload := payloadResp.ExecutionPayload
	log.Printf("  Block Number: %d", payload.Number)
	log.Printf("  Block Hash: %s", payload.BlockHash.Hex())
	log.Printf("  Parent Hash: %s", payload.ParentHash.Hex())
	log.Printf("  Timestamp: %d", payload.Timestamp)
	log.Printf("  Transactions: %d", len(payload.Transactions))

	// Step 4: NewPayload - submit block for execution
	log.Printf("Step 4: Calling NewPayloadV3...")

	status, err := client.NewPayloadV3(ctx, *payload, []common.Hash{}, &headHash)
	if err != nil {
		return fmt.Errorf("NewPayloadV3 failed: %w", err)
	}

	log.Printf("  Status: %s", status.Status)

	if status.Status != engine.VALID {
		return fmt.Errorf("payload not valid: %s", status.Status)
	}

	// Step 5: Update fork choice to finalize
	log.Printf("Step 5: Finalizing block...")

	finalFcs := engine.ForkchoiceStateV1{
		HeadBlockHash:      payload.BlockHash,
		SafeBlockHash:      payload.BlockHash,
		FinalizedBlockHash: payload.BlockHash,
	}

	finalResp, err := client.ForkchoiceUpdatedV3(ctx, finalFcs, nil)
	if err != nil {
		return fmt.Errorf("final ForkchoiceUpdatedV3 failed: %w", err)
	}

	log.Printf("  Status: %s", finalResp.PayloadStatus.Status)
	log.Printf("\nBlock %d finalized successfully!", payload.Number)

	return nil
}

// EngineClient wraps the Engine API RPC client
type EngineClient struct {
	rpc *rpc.Client
}

// NewEngineClient creates a new Engine API client with JWT authentication
func NewEngineClient(ctx context.Context, endpoint string, jwtSecret []byte) (*EngineClient, error) {
	transport := &jwtRoundTripper{
		underlyingTransport: http.DefaultTransport,
		jwtSecret:           jwtSecret,
	}

	httpClient := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}

	rpcClient, err := rpc.DialOptions(ctx, endpoint, rpc.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("dial engine endpoint: %w", err)
	}

	return &EngineClient{rpc: rpcClient}, nil
}

func (c *EngineClient) HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error) {
	var header *types.Header
	err := c.rpc.CallContext(ctx, &header, "eth_getBlockByNumber", toBlockNumArg(number), false)
	if err != nil {
		return nil, err
	}
	if header == nil {
		return nil, fmt.Errorf("block not found: %s", toBlockNumArg(number))
	}
	return header, nil
}

func (c *EngineClient) ForkchoiceUpdatedV3(ctx context.Context, state engine.ForkchoiceStateV1, attrs *engine.PayloadAttributes) (engine.ForkChoiceResponse, error) {
	var resp engine.ForkChoiceResponse
	err := c.rpc.CallContext(ctx, &resp, "engine_forkchoiceUpdatedV3", state, attrs)
	return resp, err
}

func (c *EngineClient) GetPayloadV4(ctx context.Context, payloadID engine.PayloadID) (*engine.ExecutionPayloadEnvelope, error) {
	var resp engine.ExecutionPayloadEnvelope
	err := c.rpc.CallContext(ctx, &resp, "engine_getPayloadV4", payloadID)
	return &resp, err
}

func (c *EngineClient) NewPayloadV4(ctx context.Context, payload engine.ExecutableData, versionedHashes []common.Hash, beaconRoot *common.Hash, requests []hexutil.Bytes) (engine.PayloadStatusV1, error) {
	var resp engine.PayloadStatusV1
	err := c.rpc.CallContext(ctx, &resp, "engine_newPayloadV4", payload, versionedHashes, beaconRoot, requests)
	return resp, err
}

func (c *EngineClient) NewPayloadV3(ctx context.Context, payload engine.ExecutableData, versionedHashes []common.Hash, beaconRoot *common.Hash) (engine.PayloadStatusV1, error) {
	var resp engine.PayloadStatusV1
	err := c.rpc.CallContext(ctx, &resp, "engine_newPayloadV3", payload, versionedHashes, beaconRoot)
	return resp, err
}

// jwtRoundTripper adds JWT authentication to HTTP requests
type jwtRoundTripper struct {
	underlyingTransport http.RoundTripper
	jwtSecret           []byte
}

func (t *jwtRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Generate fresh token for each request
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iat": time.Now().Unix(),
	})

	tokenString, err := token.SignedString(t.jwtSecret)
	if err != nil {
		return nil, fmt.Errorf("sign JWT: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+tokenString)
	return t.underlyingTransport.RoundTrip(req)
}

func toBlockNumArg(number *big.Int) string {
	if number == nil {
		return "latest"
	}
	return hexutil.EncodeBig(number)
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
