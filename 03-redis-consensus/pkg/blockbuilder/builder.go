package blockbuilder

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/ethereum/go-ethereum/beacon/engine"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/mikelle/geth-consensus-tutorial/03-redis-consensus/pkg/redis"
	"github.com/mikelle/geth-consensus-tutorial/03-redis-consensus/pkg/state"
	"github.com/vmihailenco/msgpack/v5"
)

var ErrEmptyBlock = errors.New("empty block skipped")

const maxAttempts = 10

// EngineClient interface for Engine API operations
type EngineClient interface {
	ForkchoiceUpdatedV3(ctx context.Context, state engine.ForkchoiceStateV1, attrs *engine.PayloadAttributes) (engine.ForkChoiceResponse, error)
	GetPayloadV5(ctx context.Context, payloadID engine.PayloadID) (*engine.ExecutionPayloadEnvelope, error)
	NewPayloadV4(ctx context.Context, payload engine.ExecutableData, versionedHashes []common.Hash, beaconRoot *common.Hash, requests [][]byte) (engine.PayloadStatusV1, error)
	HeaderByNumber(ctx context.Context, number interface{}) (*types.Header, error)
}

// BlockBuilder orchestrates block building with Redis publishing
type BlockBuilder struct {
	stateManager          state.StateManager
	engineCl              EngineClient
	redisClient           *redis.Client
	logger                *slog.Logger
	buildDelay            time.Duration
	buildEmptyBlocksDelay time.Duration
	feeRecipient          common.Address
	executionHead         *state.ExecutionHead
}

// NewBlockBuilder creates a new BlockBuilder
func NewBlockBuilder(
	stateManager state.StateManager,
	engineCl EngineClient,
	redisClient *redis.Client,
	logger *slog.Logger,
	buildDelay, buildEmptyBlocksDelay time.Duration,
	feeRecipient string,
) *BlockBuilder {
	return &BlockBuilder{
		stateManager:          stateManager,
		engineCl:              engineCl,
		redisClient:           redisClient,
		logger:                logger,
		buildDelay:            buildDelay,
		buildEmptyBlocksDelay: buildEmptyBlocksDelay,
		feeRecipient:          common.HexToAddress(feeRecipient),
	}
}

// GetPayload builds a new block
func (bb *BlockBuilder) GetPayload(ctx context.Context) error {
	// Initialize from Geth if needed
	if bb.executionHead == nil {
		if err := bb.SetExecutionHeadFromRPC(ctx); err != nil {
			return fmt.Errorf("set execution head: %w", err)
		}
	}

	// Calculate timestamp
	ts := uint64(time.Now().UnixMilli())
	if ts <= bb.executionHead.BlockTime {
		ts = bb.executionHead.BlockTime + 1
	}

	// Start build with retry
	var payloadID *engine.PayloadID
	err := retryWithBackoff(ctx, maxAttempts, bb.logger, func() error {
		headHash := common.BytesToHash(bb.executionHead.BlockHash)

		fcs := engine.ForkchoiceStateV1{
			HeadBlockHash:      headHash,
			SafeBlockHash:      headHash,
			FinalizedBlockHash: headHash,
		}

		attrs := &engine.PayloadAttributes{
			Timestamp:             ts,
			Random:                headHash,
			SuggestedFeeRecipient: bb.feeRecipient,
			Withdrawals:           []*types.Withdrawal{},
			BeaconRoot:            &headHash,
		}

		response, err := bb.engineCl.ForkchoiceUpdatedV3(ctx, fcs, attrs)
		if err != nil {
			return err
		}
		if response.PayloadStatus.Status != engine.VALID {
			return backoff.Permanent(fmt.Errorf("invalid status: %s", response.PayloadStatus.Status))
		}
		if response.PayloadID == nil {
			return backoff.Permanent(errors.New("no payload ID returned"))
		}

		payloadID = response.PayloadID
		return nil
	})
	if err != nil {
		return fmt.Errorf("start build: %w", err)
	}

	// Wait for transactions
	time.Sleep(bb.buildDelay)

	// Get the built payload
	var payloadResp *engine.ExecutionPayloadEnvelope
	err = retryWithBackoff(ctx, maxAttempts, bb.logger, func() error {
		var err error
		payloadResp, err = bb.engineCl.GetPayloadV5(ctx, *payloadID)
		return err
	})
	if err != nil {
		return fmt.Errorf("get payload: %w", err)
	}

	// Serialize and save state
	payloadData, err := msgpack.Marshal(payloadResp.ExecutionPayload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	requestsData, err := msgpack.Marshal(payloadResp.Requests)
	if err != nil {
		return fmt.Errorf("marshal requests: %w", err)
	}

	encodedPayload := base64.StdEncoding.EncodeToString(payloadData)
	encodedRequests := base64.StdEncoding.EncodeToString(requestsData)

	return bb.stateManager.SaveBlockState(ctx, &state.BlockBuildState{
		CurrentStep:      state.StepFinalizeBlock,
		PayloadID:        payloadID.String(),
		ExecutionPayload: encodedPayload,
		Requests:         encodedRequests,
	})
}

// FinalizeBlock finalizes a built block and publishes to Redis
func (bb *BlockBuilder) FinalizeBlock(ctx context.Context, payloadIDStr, executionPayloadStr, requestsStr string) error {
	if payloadIDStr == "" || executionPayloadStr == "" {
		return errors.New("missing payload data")
	}

	// Decode payload
	payloadBytes, err := base64.StdEncoding.DecodeString(executionPayloadStr)
	if err != nil {
		return fmt.Errorf("decode payload: %w", err)
	}

	var payload engine.ExecutableData
	if err := msgpack.Unmarshal(payloadBytes, &payload); err != nil {
		return fmt.Errorf("unmarshal payload: %w", err)
	}

	// Decode requests
	var requests [][]byte
	if requestsStr != "" {
		requestsBytes, err := base64.StdEncoding.DecodeString(requestsStr)
		if err != nil {
			return fmt.Errorf("decode requests: %w", err)
		}
		if err := msgpack.Unmarshal(requestsBytes, &requests); err != nil {
			return fmt.Errorf("unmarshal requests: %w", err)
		}
	}

	// Validate
	if err := bb.validatePayload(payload); err != nil {
		return fmt.Errorf("validate payload: %w", err)
	}

	// Push to Geth
	parentHash := common.BytesToHash(bb.executionHead.BlockHash)
	err = retryWithBackoff(ctx, maxAttempts, bb.logger, func() error {
		status, err := bb.engineCl.NewPayloadV4(ctx, payload, []common.Hash{}, &parentHash, requests)
		if err != nil {
			return err
		}
		if status.Status == engine.INVALID {
			msg := "unknown"
			if status.ValidationError != nil {
				msg = *status.ValidationError
			}
			return backoff.Permanent(fmt.Errorf("invalid: %s", msg))
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("new payload: %w", err)
	}

	// Update fork choice
	fcs := engine.ForkchoiceStateV1{
		HeadBlockHash:      payload.BlockHash,
		SafeBlockHash:      payload.BlockHash,
		FinalizedBlockHash: payload.BlockHash,
	}

	err = retryWithBackoff(ctx, maxAttempts, bb.logger, func() error {
		_, err := bb.engineCl.ForkchoiceUpdatedV3(ctx, fcs, nil)
		return err
	})
	if err != nil {
		return fmt.Errorf("update fork choice: %w", err)
	}

	// Publish to Redis stream
	if bb.redisClient != nil {
		if err := bb.redisClient.PublishBlock(ctx, payload.BlockHash.Hex(), executionPayloadStr, payload.Number); err != nil {
			bb.logger.Warn("Failed to publish block to Redis", "error", err)
			// Don't fail the block - local finalization succeeded
		}
	}

	// Update local head
	bb.executionHead = &state.ExecutionHead{
		BlockHeight: payload.Number,
		BlockHash:   payload.BlockHash[:],
		BlockTime:   payload.Timestamp,
	}

	bb.logger.Info("Block finalized",
		"height", payload.Number,
		"hash", payload.BlockHash.Hex(),
		"txs", len(payload.Transactions))

	return nil
}

func (bb *BlockBuilder) validatePayload(payload engine.ExecutableData) error {
	expectedHeight := bb.executionHead.BlockHeight + 1
	if payload.Number != expectedHeight {
		return fmt.Errorf("invalid height: got %d, expected %d", payload.Number, expectedHeight)
	}

	expectedParent := common.BytesToHash(bb.executionHead.BlockHash)
	if payload.ParentHash != expectedParent {
		return fmt.Errorf("invalid parent hash")
	}

	if payload.Timestamp <= bb.executionHead.BlockTime {
		return fmt.Errorf("invalid timestamp")
	}

	return nil
}

// SetExecutionHeadFromRPC initializes execution head from Geth
func (bb *BlockBuilder) SetExecutionHeadFromRPC(ctx context.Context) error {
	return retryWithBackoff(ctx, maxAttempts, bb.logger, func() error {
		header, err := bb.engineCl.HeaderByNumber(ctx, nil)
		if err != nil {
			return err
		}
		bb.executionHead = &state.ExecutionHead{
			BlockHeight: header.Number.Uint64(),
			BlockHash:   header.Hash().Bytes(),
			BlockTime:   header.Time,
		}
		bb.logger.Info("Initialized from Geth",
			"height", bb.executionHead.BlockHeight,
			"hash", common.BytesToHash(bb.executionHead.BlockHash).Hex())
		return nil
	})
}

// GetExecutionHead returns the current execution head
func (bb *BlockBuilder) GetExecutionHead() *state.ExecutionHead {
	return bb.executionHead
}

func retryWithBackoff(ctx context.Context, maxAttempts uint64, logger *slog.Logger, operation func() error) error {
	eb := backoff.NewExponentialBackOff()
	eb.InitialInterval = 200 * time.Millisecond
	eb.MaxInterval = 30 * time.Second

	b := backoff.WithMaxRetries(eb, maxAttempts)

	return backoff.Retry(func() error {
		select {
		case <-ctx.Done():
			return backoff.Permanent(ctx.Err())
		default:
			if err := operation(); err != nil {
				logger.Warn("Operation failed, retrying", "error", err)
				return err
			}
			return nil
		}
	}, backoff.WithContext(b, ctx))
}
