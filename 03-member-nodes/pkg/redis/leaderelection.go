package redis

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const (
	leaderKey     = "consensus:leader"
	defaultLeaseTTL = 5 * time.Second
	renewInterval = 2 * time.Second
)

// LeaderElection manages leader election using Redis
type LeaderElection struct {
	client     *Client
	instanceID string
	leaseTTL   time.Duration
	logger     *slog.Logger

	mu       sync.RWMutex
	isLeader bool
	cancel   context.CancelFunc
}

// NewLeaderElection creates a new leader election manager
func NewLeaderElection(client *Client, instanceID string, logger *slog.Logger) *LeaderElection {
	return &LeaderElection{
		client:     client,
		instanceID: instanceID,
		leaseTTL:   defaultLeaseTTL,
		logger:     logger,
	}
}

// Start begins the leader election process
func (le *LeaderElection) Start(ctx context.Context) {
	ctx, le.cancel = context.WithCancel(ctx)

	go le.electionLoop(ctx)
}

// Stop stops the leader election process
func (le *LeaderElection) Stop() {
	if le.cancel != nil {
		le.cancel()
	}

	// Release leadership if we have it
	le.mu.Lock()
	wasLeader := le.isLeader
	le.isLeader = false
	le.mu.Unlock()

	if wasLeader {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		le.client.Del(ctx, leaderKey)
	}
}

// IsLeader returns whether this instance is the leader
func (le *LeaderElection) IsLeader() bool {
	le.mu.RLock()
	defer le.mu.RUnlock()
	return le.isLeader
}

func (le *LeaderElection) electionLoop(ctx context.Context) {
	ticker := time.NewTicker(renewInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			le.tryAcquireOrRenew(ctx)
		}
	}
}

func (le *LeaderElection) tryAcquireOrRenew(ctx context.Context) {
	le.mu.Lock()
	defer le.mu.Unlock()

	// Try to acquire the lock
	acquired, err := le.client.SetNX(ctx, leaderKey, le.instanceID, le.leaseTTL)
	if err != nil {
		le.logger.Error("Failed to acquire leadership", "error", err)
		le.isLeader = false
		return
	}

	if acquired {
		if !le.isLeader {
			le.logger.Info("Became leader", "instanceID", le.instanceID)
		}
		le.isLeader = true
		return
	}

	// Check if we're still the leader and renew
	currentLeader, err := le.client.Get(ctx, leaderKey)
	if err != nil {
		le.logger.Warn("Failed to check current leader", "error", err)
		le.isLeader = false
		return
	}

	if currentLeader == le.instanceID {
		// Renew the lease
		if err := le.client.Set(ctx, leaderKey, le.instanceID, le.leaseTTL); err != nil {
			le.logger.Warn("Failed to renew lease", "error", err)
			le.isLeader = false
			return
		}
		le.isLeader = true
	} else {
		if le.isLeader {
			le.logger.Info("Lost leadership", "newLeader", currentLeader)
		}
		le.isLeader = false
	}
}
