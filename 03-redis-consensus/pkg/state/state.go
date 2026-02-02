package state

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/mikelle/geth-consensus-tutorial/03-redis-consensus/pkg/redis"
)

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
	BlockHeight uint64 `json:"block_height"`
	BlockHash   []byte `json:"block_hash"`
	BlockTime   uint64 `json:"block_time"`
}

// BlockBuildState tracks the current block building state
type BlockBuildState struct {
	CurrentStep      BuildStep `json:"current_step"`
	PayloadID        string    `json:"payload_id"`
	ExecutionPayload string    `json:"execution_payload"`
	Requests         string    `json:"requests"` // EIP-7685 requests
}

// StateManager interface for state operations
type StateManager interface {
	SaveBlockState(ctx context.Context, state *BlockBuildState) error
	GetBlockBuildState(ctx context.Context) BlockBuildState
	ResetBlockState(ctx context.Context) error
}

const (
	stateKeyPrefix = "consensus:state:"
	stateTTL       = 5 * time.Minute
)

// RedisStateManager implements Redis-backed state management
type RedisStateManager struct {
	client     *redis.Client
	instanceID string
	mu         sync.RWMutex
	localState *BlockBuildState // Local cache
}

// NewRedisStateManager creates a new RedisStateManager
func NewRedisStateManager(client *redis.Client, instanceID string) *RedisStateManager {
	return &RedisStateManager{
		client:     client,
		instanceID: instanceID,
		localState: &BlockBuildState{CurrentStep: StepBuildBlock},
	}
}

func (m *RedisStateManager) stateKey() string {
	return stateKeyPrefix + m.instanceID
}

// SaveBlockState saves the block state to Redis
func (m *RedisStateManager) SaveBlockState(ctx context.Context, state *BlockBuildState) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	if err := m.client.Set(ctx, m.stateKey(), string(data), stateTTL); err != nil {
		return fmt.Errorf("save state to redis: %w", err)
	}

	m.localState = state
	return nil
}

// GetBlockBuildState retrieves the current block build state
func (m *RedisStateManager) GetBlockBuildState(ctx context.Context) BlockBuildState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Try to get from Redis first
	data, err := m.client.Get(ctx, m.stateKey())
	if err == nil && data != "" {
		var state BlockBuildState
		if json.Unmarshal([]byte(data), &state) == nil {
			return state
		}
	}

	// Fall back to local state
	if m.localState == nil {
		return BlockBuildState{CurrentStep: StepBuildBlock}
	}
	return *m.localState
}

// ResetBlockState resets the block build state
func (m *RedisStateManager) ResetBlockState(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.localState = &BlockBuildState{CurrentStep: StepBuildBlock}

	data, _ := json.Marshal(m.localState)
	return m.client.Set(ctx, m.stateKey(), string(data), stateTTL)
}

// LocalStateManager implements in-memory state management (for followers)
type LocalStateManager struct {
	mu              sync.RWMutex
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
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blockBuildState = state
	return nil
}

// GetBlockBuildState retrieves the current block build state
func (m *LocalStateManager) GetBlockBuildState(_ context.Context) BlockBuildState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.blockBuildState == nil {
		return BlockBuildState{CurrentStep: StepBuildBlock}
	}
	return *m.blockBuildState
}

// ResetBlockState resets the block build state
func (m *LocalStateManager) ResetBlockState(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blockBuildState = &BlockBuildState{
		CurrentStep: StepBuildBlock,
	}
	return nil
}
