package state

import "context"

// BuildStep represents the current step in block building
type BuildStep int

const (
	StepBuildBlock BuildStep = iota
	StepFinalizeBlock
)

func (s BuildStep) String() string {
	switch s {
	case StepBuildBlock:
		return "BuildBlock"
	case StepFinalizeBlock:
		return "FinalizeBlock"
	default:
		return "Unknown"
	}
}

// ExecutionHead tracks the current chain head
type ExecutionHead struct {
	BlockHeight uint64
	BlockHash   []byte
	BlockTime   uint64
}

// BlockBuildState tracks the current block building state
type BlockBuildState struct {
	CurrentStep      BuildStep
	PayloadID        string
	ExecutionPayload string // Base64-encoded msgpack
	Requests         string // Base64-encoded msgpack (EIP-7685 requests)
}

// StateManager interface for state operations
type StateManager interface {
	SaveBlockState(ctx context.Context, state *BlockBuildState) error
	GetBlockBuildState(ctx context.Context) BlockBuildState
	ResetBlockState(ctx context.Context) error
}

// LocalStateManager implements in-memory state management
type LocalStateManager struct {
	blockBuildState *BlockBuildState
}

// NewLocalStateManager creates a new LocalStateManager
func NewLocalStateManager() *LocalStateManager {
	return &LocalStateManager{
		blockBuildState: &BlockBuildState{
			CurrentStep: StepBuildBlock,
		},
	}
}

// SaveBlockState saves the block state
func (m *LocalStateManager) SaveBlockState(_ context.Context, state *BlockBuildState) error {
	m.blockBuildState = state
	return nil
}

// GetBlockBuildState retrieves the current block build state
func (m *LocalStateManager) GetBlockBuildState(_ context.Context) BlockBuildState {
	if m.blockBuildState == nil {
		return BlockBuildState{CurrentStep: StepBuildBlock}
	}
	return *m.blockBuildState
}

// ResetBlockState resets the block build state
func (m *LocalStateManager) ResetBlockState(_ context.Context) error {
	m.blockBuildState = &BlockBuildState{
		CurrentStep: StepBuildBlock,
	}
	return nil
}
