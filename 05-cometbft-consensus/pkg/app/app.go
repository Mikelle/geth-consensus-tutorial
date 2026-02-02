package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	abcitypes "github.com/cometbft/cometbft/abci/types"
	"github.com/dgraph-io/badger/v3"
	"github.com/ethereum/go-ethereum/beacon/engine"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

const (
	keyExecutionHead = "execution_head"
	defaultBuildDelay = 300 * time.Millisecond
)

// EngineClient interface for Engine API operations
type EngineClient interface {
	HeaderByNumber(ctx context.Context, number interface{}) (*types.Header, error)
	ForkchoiceUpdatedV3(ctx context.Context, state engine.ForkchoiceStateV1, attrs *engine.PayloadAttributes) (engine.ForkChoiceResponse, error)
	GetPayloadV3(ctx context.Context, payloadID engine.PayloadID) (*engine.ExecutionPayloadEnvelope, error)
	NewPayloadV3(ctx context.Context, payload engine.ExecutableData, versionedHashes []common.Hash, beaconRoot *common.Hash) (engine.PayloadStatusV1, error)
}

// ExecutionHead tracks the current chain head
type ExecutionHead struct {
	BlockHeight uint64      `json:"block_height"`
	BlockHash   common.Hash `json:"block_hash"`
	BlockTime   uint64      `json:"block_time"`
}

// MsgExecutionPayload wraps the execution payload for CometBFT transactions
type MsgExecutionPayload struct {
	ExecutionPayload *engine.ExecutableData `json:"execution_payload"`
}

// GethConsensusApp implements the CometBFT ABCI application
// that drives Geth block production via the Engine API
type GethConsensusApp struct {
	db         *badger.DB
	engineCl   EngineClient
	logger     *slog.Logger
	buildDelay time.Duration

	// Current execution head (cached)
	execHead *ExecutionHead
}

// NewGethConsensusApp creates a new ABCI application
func NewGethConsensusApp(db *badger.DB, engineCl EngineClient, logger *slog.Logger) *GethConsensusApp {
	return &GethConsensusApp{
		db:         db,
		engineCl:   engineCl,
		logger:     logger,
		buildDelay: defaultBuildDelay,
	}
}

// Info returns application info - called on startup
func (app *GethConsensusApp) Info(ctx context.Context, req *abcitypes.RequestInfo) (*abcitypes.ResponseInfo, error) {
	app.logger.Info("ABCI Info called")

	// Load execution head from DB
	execHead, err := app.loadExecutionHead()
	if err != nil {
		app.logger.Warn("No execution head in DB, will initialize on first block")
		return &abcitypes.ResponseInfo{
			LastBlockHeight: 0,
		}, nil
	}

	app.execHead = execHead
	app.logger.Info("Loaded execution head",
		"height", execHead.BlockHeight,
		"hash", execHead.BlockHash.Hex())

	return &abcitypes.ResponseInfo{
		LastBlockHeight:  int64(execHead.BlockHeight),
		LastBlockAppHash: execHead.BlockHash.Bytes(),
	}, nil
}

// InitChain initializes the chain - called once at genesis
func (app *GethConsensusApp) InitChain(ctx context.Context, req *abcitypes.RequestInitChain) (*abcitypes.ResponseInitChain, error) {
	app.logger.Info("ABCI InitChain called", "chainID", req.ChainId)

	// Initialize execution head from Geth
	header, err := app.engineCl.HeaderByNumber(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("get genesis header: %w", err)
	}
	if header == nil {
		return nil, fmt.Errorf("got nil header from Geth")
	}

	app.execHead = &ExecutionHead{
		BlockHeight: header.Number.Uint64(),
		BlockHash:   header.Hash(),
		BlockTime:   header.Time,
	}

	if err := app.saveExecutionHead(app.execHead); err != nil {
		return nil, fmt.Errorf("save execution head: %w", err)
	}

	app.logger.Info("Initialized from Geth genesis",
		"height", app.execHead.BlockHeight,
		"hash", app.execHead.BlockHash.Hex())

	return &abcitypes.ResponseInitChain{}, nil
}

// PrepareProposal builds a new block when this node is the proposer
func (app *GethConsensusApp) PrepareProposal(ctx context.Context, req *abcitypes.RequestPrepareProposal) (*abcitypes.ResponsePrepareProposal, error) {
	app.logger.Info("PrepareProposal called", "height", req.Height)

	if app.execHead == nil {
		// Initialize from Geth if needed
		header, err := app.engineCl.HeaderByNumber(ctx, nil)
		if err != nil {
			return nil, fmt.Errorf("get latest header: %w", err)
		}
		app.execHead = &ExecutionHead{
			BlockHeight: header.Number.Uint64(),
			BlockHash:   header.Hash(),
			BlockTime:   header.Time,
		}
	}

	// Build the next block
	payload, err := app.buildBlock(ctx, req.Time.Unix())
	if err != nil {
		return nil, fmt.Errorf("build block: %w", err)
	}

	// Wrap payload in our message format
	msg := MsgExecutionPayload{ExecutionPayload: payload}
	txBytes, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	app.logger.Info("Prepared proposal",
		"blockNumber", payload.Number,
		"blockHash", payload.BlockHash.Hex(),
		"txCount", len(payload.Transactions))

	return &abcitypes.ResponsePrepareProposal{
		Txs: [][]byte{txBytes},
	}, nil
}

// ProcessProposal validates a proposed block
func (app *GethConsensusApp) ProcessProposal(ctx context.Context, req *abcitypes.RequestProcessProposal) (*abcitypes.ResponseProcessProposal, error) {
	app.logger.Info("ProcessProposal called", "height", req.Height)

	if len(req.Txs) == 0 {
		return &abcitypes.ResponseProcessProposal{Status: abcitypes.ResponseProcessProposal_REJECT}, nil
	}

	// Parse and validate the payload
	var msg MsgExecutionPayload
	if err := json.Unmarshal(req.Txs[0], &msg); err != nil {
		app.logger.Error("Failed to unmarshal proposal", "error", err)
		return &abcitypes.ResponseProcessProposal{Status: abcitypes.ResponseProcessProposal_REJECT}, nil
	}

	if msg.ExecutionPayload == nil {
		return &abcitypes.ResponseProcessProposal{Status: abcitypes.ResponseProcessProposal_REJECT}, nil
	}

	// Validate the payload
	if err := app.validatePayload(msg.ExecutionPayload); err != nil {
		app.logger.Error("Invalid payload", "error", err)
		return &abcitypes.ResponseProcessProposal{Status: abcitypes.ResponseProcessProposal_REJECT}, nil
	}

	return &abcitypes.ResponseProcessProposal{Status: abcitypes.ResponseProcessProposal_ACCEPT}, nil
}

// FinalizeBlock executes and commits a block
func (app *GethConsensusApp) FinalizeBlock(ctx context.Context, req *abcitypes.RequestFinalizeBlock) (*abcitypes.ResponseFinalizeBlock, error) {
	app.logger.Info("FinalizeBlock called", "height", req.Height)

	if len(req.Txs) == 0 {
		return nil, fmt.Errorf("no transactions in block")
	}

	// Parse the payload
	var msg MsgExecutionPayload
	if err := json.Unmarshal(req.Txs[0], &msg); err != nil {
		return nil, fmt.Errorf("unmarshal payload: %w", err)
	}

	payload := msg.ExecutionPayload

	// Submit to Geth via NewPayload
	parentHash := app.execHead.BlockHash
	status, err := app.engineCl.NewPayloadV3(ctx, *payload, []common.Hash{}, &parentHash)
	if err != nil {
		return nil, fmt.Errorf("new payload: %w", err)
	}

	if status.Status == engine.INVALID {
		msg := "unknown"
		if status.ValidationError != nil {
			msg = *status.ValidationError
		}
		return nil, fmt.Errorf("payload invalid: %s", msg)
	}

	// Update forkchoice to set the new head
	fcs := engine.ForkchoiceStateV1{
		HeadBlockHash:      payload.BlockHash,
		SafeBlockHash:      payload.BlockHash,
		FinalizedBlockHash: payload.BlockHash,
	}

	_, err = app.engineCl.ForkchoiceUpdatedV3(ctx, fcs, nil)
	if err != nil {
		return nil, fmt.Errorf("forkchoice update: %w", err)
	}

	// Update local state
	app.execHead = &ExecutionHead{
		BlockHeight: payload.Number,
		BlockHash:   payload.BlockHash,
		BlockTime:   payload.Timestamp,
	}

	if err := app.saveExecutionHead(app.execHead); err != nil {
		return nil, fmt.Errorf("save execution head: %w", err)
	}

	app.logger.Info("Block finalized",
		"height", payload.Number,
		"hash", payload.BlockHash.Hex(),
		"txCount", len(payload.Transactions))

	// Return one TxResult per transaction in the block
	txResults := make([]*abcitypes.ExecTxResult, len(req.Txs))
	for i := range req.Txs {
		txResults[i] = &abcitypes.ExecTxResult{Code: 0}
	}

	return &abcitypes.ResponseFinalizeBlock{
		AppHash:   payload.BlockHash.Bytes(),
		TxResults: txResults,
	}, nil
}

// Commit persists state to disk
func (app *GethConsensusApp) Commit(ctx context.Context, req *abcitypes.RequestCommit) (*abcitypes.ResponseCommit, error) {
	return &abcitypes.ResponseCommit{}, nil
}

// CheckTx validates a transaction for the mempool (not used in our case)
func (app *GethConsensusApp) CheckTx(ctx context.Context, req *abcitypes.RequestCheckTx) (*abcitypes.ResponseCheckTx, error) {
	return &abcitypes.ResponseCheckTx{Code: 0}, nil
}

// Query handles ABCI queries
func (app *GethConsensusApp) Query(ctx context.Context, req *abcitypes.RequestQuery) (*abcitypes.ResponseQuery, error) {
	return &abcitypes.ResponseQuery{}, nil
}

// buildBlock triggers Geth to build a new block
func (app *GethConsensusApp) buildBlock(ctx context.Context, timestamp int64) (*engine.ExecutableData, error) {
	headHash := app.execHead.BlockHash
	ts := uint64(timestamp)
	if ts <= app.execHead.BlockTime {
		ts = app.execHead.BlockTime + 1
	}

	// Start block building
	fcs := engine.ForkchoiceStateV1{
		HeadBlockHash:      headHash,
		SafeBlockHash:      headHash,
		FinalizedBlockHash: headHash,
	}

	attrs := &engine.PayloadAttributes{
		Timestamp:             ts,
		Random:                headHash, // Use parent hash as RANDAO
		SuggestedFeeRecipient: common.Address{},
		Withdrawals:           []*types.Withdrawal{},
		BeaconRoot:            &headHash,
	}

	response, err := app.engineCl.ForkchoiceUpdatedV3(ctx, fcs, attrs)
	if err != nil {
		return nil, fmt.Errorf("forkchoice updated: %w", err)
	}

	if response.PayloadStatus.Status != engine.VALID {
		return nil, fmt.Errorf("invalid status: %s", response.PayloadStatus.Status)
	}

	if response.PayloadID == nil {
		return nil, fmt.Errorf("no payload ID returned")
	}

	// Wait for Geth to build the block
	time.Sleep(app.buildDelay)

	// Retrieve the built payload
	payloadResp, err := app.engineCl.GetPayloadV3(ctx, *response.PayloadID)
	if err != nil {
		return nil, fmt.Errorf("get payload: %w", err)
	}

	return payloadResp.ExecutionPayload, nil
}

// validatePayload checks that the payload is valid
func (app *GethConsensusApp) validatePayload(payload *engine.ExecutableData) error {
	if app.execHead == nil {
		return nil // Skip validation on first block
	}

	expectedHeight := app.execHead.BlockHeight + 1
	if payload.Number != expectedHeight {
		return fmt.Errorf("invalid height: got %d, expected %d", payload.Number, expectedHeight)
	}

	if payload.ParentHash != app.execHead.BlockHash {
		return fmt.Errorf("invalid parent hash")
	}

	if payload.Timestamp <= app.execHead.BlockTime {
		return fmt.Errorf("invalid timestamp")
	}

	return nil
}

// loadExecutionHead loads the execution head from the database
func (app *GethConsensusApp) loadExecutionHead() (*ExecutionHead, error) {
	var head ExecutionHead
	err := app.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(keyExecutionHead))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			return json.Unmarshal(val, &head)
		})
	})
	if err != nil {
		return nil, err
	}
	return &head, nil
}

// saveExecutionHead persists the execution head to the database
func (app *GethConsensusApp) saveExecutionHead(head *ExecutionHead) error {
	data, err := json.Marshal(head)
	if err != nil {
		return err
	}
	return app.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(keyExecutionHead), data)
	})
}

// Required ABCI methods (stubs for methods we don't use)

func (app *GethConsensusApp) ListSnapshots(ctx context.Context, req *abcitypes.RequestListSnapshots) (*abcitypes.ResponseListSnapshots, error) {
	return &abcitypes.ResponseListSnapshots{}, nil
}

func (app *GethConsensusApp) OfferSnapshot(ctx context.Context, req *abcitypes.RequestOfferSnapshot) (*abcitypes.ResponseOfferSnapshot, error) {
	return &abcitypes.ResponseOfferSnapshot{}, nil
}

func (app *GethConsensusApp) LoadSnapshotChunk(ctx context.Context, req *abcitypes.RequestLoadSnapshotChunk) (*abcitypes.ResponseLoadSnapshotChunk, error) {
	return &abcitypes.ResponseLoadSnapshotChunk{}, nil
}

func (app *GethConsensusApp) ApplySnapshotChunk(ctx context.Context, req *abcitypes.RequestApplySnapshotChunk) (*abcitypes.ResponseApplySnapshotChunk, error) {
	return &abcitypes.ResponseApplySnapshotChunk{}, nil
}

func (app *GethConsensusApp) ExtendVote(ctx context.Context, req *abcitypes.RequestExtendVote) (*abcitypes.ResponseExtendVote, error) {
	return &abcitypes.ResponseExtendVote{}, nil
}

func (app *GethConsensusApp) VerifyVoteExtension(ctx context.Context, req *abcitypes.RequestVerifyVoteExtension) (*abcitypes.ResponseVerifyVoteExtension, error) {
	return &abcitypes.ResponseVerifyVoteExtension{Status: abcitypes.ResponseVerifyVoteExtension_ACCEPT}, nil
}
