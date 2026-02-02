package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/beacon/engine"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/mikelle/geth-consensus-tutorial/04-member-nodes/pkg/api"
	"github.com/mikelle/geth-consensus-tutorial/04-member-nodes/pkg/blockbuilder"
	"github.com/mikelle/geth-consensus-tutorial/04-member-nodes/pkg/ethclient"
	"github.com/mikelle/geth-consensus-tutorial/04-member-nodes/pkg/postgres"
	redisclient "github.com/mikelle/geth-consensus-tutorial/04-member-nodes/pkg/redis"
	"github.com/mikelle/geth-consensus-tutorial/04-member-nodes/pkg/state"
	syncpkg "github.com/mikelle/geth-consensus-tutorial/04-member-nodes/pkg/sync"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/urfave/cli/v2"
)

// Config holds application configuration
type Config struct {
	InstanceID               string
	Mode                     string // "leader" or "member"
	EthClientURL             string
	JWTSecret                string
	PriorityFeeRecipient     string
	EVMBuildDelay            time.Duration
	EVMBuildDelayEmptyBlocks time.Duration
	TxPoolPollingInterval    time.Duration
	HealthAddr               string
	APIAddr                  string
	RedisAddr                string
	RedisPassword            string
	RedisStream              string
	PostgresURL              string
	LeaderURL                string
}

// MemberNodesApp orchestrates the full consensus system
type MemberNodesApp struct {
	logger           *slog.Logger
	cfg              Config
	blockBuilder     *blockbuilder.BlockBuilder
	stateManager     state.StateManager
	redisClient      *redisclient.Client
	leaderElection   *redisclient.LeaderElection
	payloadStore     *postgres.PayloadStore
	apiServer        *api.Server
	syncer           *syncpkg.Syncer
	appCtx           context.Context
	cancel           context.CancelFunc
	wg               sync.WaitGroup
	runLoopStopped   chan struct{}
	connectionRefused bool
	connMu           sync.Mutex
}

func main() {
	app := &cli.App{
		Name:  "member-nodes",
		Usage: "Scalable consensus with leader election and member sync",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "instance-id",
				Usage:    "Unique node identifier",
				Required: true,
				EnvVars:  []string{"INSTANCE_ID"},
			},
			&cli.StringFlag{
				Name:    "mode",
				Usage:   "Node mode: leader or member",
				Value:   "leader",
				EnvVars: []string{"NODE_MODE"},
			},
			&cli.StringFlag{
				Name:    "eth-client-url",
				Usage:   "Geth Engine API URL",
				Value:   "http://localhost:8551",
				EnvVars: []string{"ETH_CLIENT_URL"},
			},
			&cli.StringFlag{
				Name:    "jwt-secret",
				Usage:   "Hex-encoded 32-byte JWT secret",
				Value:   "688f5d737bad920bdfb2fc2f488d6b6209eebeb7b7f7710df3571de7fda67a32",
				EnvVars: []string{"JWT_SECRET"},
			},
			&cli.StringFlag{
				Name:    "priority-fee-recipient",
				Usage:   "Address to receive priority fees",
				Value:   "0x0000000000000000000000000000000000000000",
				EnvVars: []string{"PRIORITY_FEE_RECIPIENT"},
			},
			&cli.DurationFlag{
				Name:    "evm-build-delay",
				Usage:   "Delay after ForkchoiceUpdated before GetPayload",
				Value:   1 * time.Millisecond,
				EnvVars: []string{"EVM_BUILD_DELAY"},
			},
			&cli.DurationFlag{
				Name:    "evm-build-delay-empty-block",
				Usage:   "Minimum time between empty blocks",
				Value:   1 * time.Minute,
				EnvVars: []string{"EVM_BUILD_DELAY_EMPTY_BLOCK"},
			},
			&cli.DurationFlag{
				Name:    "tx-pool-polling-interval",
				Usage:   "Poll interval when mempool is empty",
				Value:   5 * time.Millisecond,
				EnvVars: []string{"TX_POOL_POLLING_INTERVAL"},
			},
			&cli.StringFlag{
				Name:    "health-addr",
				Usage:   "Health check endpoint address",
				Value:   ":8080",
				EnvVars: []string{"HEALTH_ADDR"},
			},
			&cli.StringFlag{
				Name:    "api-addr",
				Usage:   "Block sync API address",
				Value:   ":8090",
				EnvVars: []string{"API_ADDR"},
			},
			&cli.StringFlag{
				Name:    "redis-addr",
				Usage:   "Redis server address",
				Value:   "localhost:6379",
				EnvVars: []string{"REDIS_ADDR"},
			},
			&cli.StringFlag{
				Name:    "redis-password",
				Usage:   "Redis password",
				Value:   "",
				EnvVars: []string{"REDIS_PASSWORD"},
			},
			&cli.StringFlag{
				Name:    "redis-stream",
				Usage:   "Redis stream name for blocks",
				Value:   "consensus:blocks",
				EnvVars: []string{"REDIS_STREAM"},
			},
			&cli.StringFlag{
				Name:    "postgres-url",
				Usage:   "PostgreSQL connection URL",
				Value:   "postgres://postgres:postgres@localhost:5432/consensus?sslmode=disable",
				EnvVars: []string{"POSTGRES_URL"},
			},
			&cli.StringFlag{
				Name:    "leader-url",
				Usage:   "Leader API URL for member sync",
				Value:   "http://localhost:8090",
				EnvVars: []string{"LEADER_URL"},
			},
		},
		Action: runNode,
	}

	if err := app.Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func runNode(c *cli.Context) error {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg := Config{
		InstanceID:               c.String("instance-id"),
		Mode:                     c.String("mode"),
		EthClientURL:             c.String("eth-client-url"),
		JWTSecret:                c.String("jwt-secret"),
		PriorityFeeRecipient:     c.String("priority-fee-recipient"),
		EVMBuildDelay:            c.Duration("evm-build-delay"),
		EVMBuildDelayEmptyBlocks: c.Duration("evm-build-delay-empty-block"),
		TxPoolPollingInterval:    c.Duration("tx-pool-polling-interval"),
		HealthAddr:               c.String("health-addr"),
		APIAddr:                  c.String("api-addr"),
		RedisAddr:                c.String("redis-addr"),
		RedisPassword:            c.String("redis-password"),
		RedisStream:              c.String("redis-stream"),
		PostgresURL:              c.String("postgres-url"),
		LeaderURL:                c.String("leader-url"),
	}

	logger.Info("Starting consensus node", "instanceID", cfg.InstanceID, "mode", cfg.Mode)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	app, err := NewMemberNodesApp(ctx, cfg, logger)
	if err != nil {
		return err
	}

	app.Start()
	<-ctx.Done()
	app.Stop()

	return nil
}

// NewMemberNodesApp creates a new MemberNodesApp
func NewMemberNodesApp(parentCtx context.Context, cfg Config, logger *slog.Logger) (*MemberNodesApp, error) {
	ctx, cancel := context.WithCancel(parentCtx)

	// Connect to PostgreSQL
	payloadStore, err := postgres.NewPayloadStore(ctx, cfg.PostgresURL)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}

	app := &MemberNodesApp{
		logger:         logger,
		cfg:            cfg,
		payloadStore:   payloadStore,
		appCtx:         ctx,
		cancel:         cancel,
		runLoopStopped: make(chan struct{}),
	}

	if cfg.Mode == "member" {
		// Member mode: just sync from leader
		app.syncer = syncpkg.NewSyncer(cfg.LeaderURL, payloadStore, logger.With("component", "Syncer"))
	} else {
		// Leader mode: full consensus setup
		redisCl, err := redisclient.NewClient(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisStream)
		if err != nil {
			cancel()
			payloadStore.Close()
			return nil, fmt.Errorf("connect to redis: %w", err)
		}
		app.redisClient = redisCl

		jwtBytes, err := hex.DecodeString(cfg.JWTSecret)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("decode JWT secret: %w", err)
		}

		engineCl, err := ethclient.NewEngineClient(ctx, cfg.EthClientURL, jwtBytes)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("create engine client: %w", err)
		}

		stateMgr := state.NewRedisStateManager(redisCl, cfg.InstanceID)
		app.stateManager = stateMgr

		leaderElection := redisclient.NewLeaderElection(redisCl, cfg.InstanceID, logger.With("component", "LeaderElection"))
		app.leaderElection = leaderElection

		engineAdapter := &engineClientAdapter{client: engineCl}

		bb := blockbuilder.NewBlockBuilder(
			stateMgr,
			engineAdapter,
			redisCl,
			payloadStore,
			logger.With("component", "BlockBuilder"),
			cfg.EVMBuildDelay,
			cfg.EVMBuildDelayEmptyBlocks,
			cfg.PriorityFeeRecipient,
		)
		app.blockBuilder = bb

		// API server for member sync
		app.apiServer = api.NewServer(payloadStore, cfg.APIAddr, logger.With("component", "API"))
	}

	return app, nil
}

// Start begins the node
func (app *MemberNodesApp) Start() {
	if app.cfg.Mode == "member" {
		app.startMember()
	} else {
		app.startLeader()
	}
}

func (app *MemberNodesApp) startLeader() {
	// Start leader election
	app.leaderElection.Start(app.appCtx)

	// Start API server
	app.apiServer.Start()

	// Health server
	app.wg.Add(1)
	go func() {
		defer app.wg.Done()
		mux := http.NewServeMux()
		mux.HandleFunc("/health", app.healthHandler)
		mux.Handle("/metrics", promhttp.Handler())

		server := &http.Server{Addr: app.cfg.HealthAddr, Handler: mux}
		app.logger.Info("Health endpoint listening", "addr", app.cfg.HealthAddr)

		go func() {
			<-app.appCtx.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			server.Shutdown(ctx)
		}()

		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			app.logger.Error("Health server error", "error", err)
		}
	}()

	// Block production loop
	app.wg.Add(1)
	go func() {
		defer app.wg.Done()
		defer close(app.runLoopStopped)
		app.runLeaderLoop()
	}()
}

func (app *MemberNodesApp) startMember() {
	// Start syncer
	app.syncer.Start(app.appCtx)

	// Health server
	app.wg.Add(1)
	go func() {
		defer app.wg.Done()
		mux := http.NewServeMux()
		mux.HandleFunc("/health", app.memberHealthHandler)
		mux.Handle("/metrics", promhttp.Handler())

		server := &http.Server{Addr: app.cfg.HealthAddr, Handler: mux}
		app.logger.Info("Member health endpoint listening", "addr", app.cfg.HealthAddr)

		go func() {
			<-app.appCtx.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			server.Shutdown(ctx)
		}()

		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			app.logger.Error("Health server error", "error", err)
		}
	}()

	app.logger.Info("Member node started", "leaderURL", app.cfg.LeaderURL)
}

func (app *MemberNodesApp) runLeaderLoop() {
	app.logger.Info("Block production started", "instanceID", app.cfg.InstanceID)
	app.stateManager.ResetBlockState(app.appCtx)

	for {
		select {
		case <-app.appCtx.Done():
			app.logger.Info("Block production stopping")
			return
		default:
			// Only produce blocks if we're the leader
			if !app.leaderElection.IsLeader() {
				time.Sleep(100 * time.Millisecond)
				continue
			}

			err := app.produceBlock()
			app.setConnectionStatus(err)

			if errors.Is(err, blockbuilder.ErrEmptyBlock) {
				time.Sleep(app.cfg.TxPoolPollingInterval)
				continue
			}
			if err != nil {
				app.logger.Error("Block production failed", "error", err)
			}

			app.stateManager.ResetBlockState(app.appCtx)
		}
	}
}

func (app *MemberNodesApp) produceBlock() error {
	// Phase 1: Build
	if err := app.blockBuilder.GetPayload(app.appCtx); err != nil {
		return err
	}

	// Get state
	currentState := app.stateManager.GetBlockBuildState(app.appCtx)

	// Phase 2: Finalize
	return app.blockBuilder.FinalizeBlock(app.appCtx, currentState.PayloadID, currentState.ExecutionPayload)
}

func (app *MemberNodesApp) healthHandler(w http.ResponseWriter, r *http.Request) {
	if err := app.appCtx.Err(); err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}

	app.connMu.Lock()
	refused := app.connectionRefused
	app.connMu.Unlock()

	if refused {
		http.Error(w, "ethereum unavailable", http.StatusServiceUnavailable)
		return
	}

	select {
	case <-app.runLoopStopped:
		http.Error(w, "run loop stopped", http.StatusServiceUnavailable)
		return
	default:
	}

	isLeader := app.leaderElection.IsLeader()
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "OK (mode=leader, isLeader=%v)", isLeader)
}

func (app *MemberNodesApp) memberHealthHandler(w http.ResponseWriter, r *http.Request) {
	if err := app.appCtx.Err(); err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}

	lastSynced := app.syncer.GetLastSynced()
	syncedCount := app.syncer.GetSyncedCount()

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "OK (mode=member, lastSynced=%d, totalSynced=%d)", lastSynced, syncedCount)
}

func (app *MemberNodesApp) setConnectionStatus(err error) {
	app.connMu.Lock()
	defer app.connMu.Unlock()

	if err == nil {
		app.connectionRefused = false
		return
	}

	if strings.Contains(err.Error(), "connection refused") {
		app.connectionRefused = true
		app.logger.Warn("Geth connection refused")
	}
}

// Stop gracefully stops the application
func (app *MemberNodesApp) Stop() {
	app.logger.Info("Stopping...")

	if app.leaderElection != nil {
		app.leaderElection.Stop()
	}

	app.cancel()

	if app.apiServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		app.apiServer.Stop(ctx)
		cancel()
	}

	done := make(chan struct{})
	go func() {
		app.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		app.logger.Info("Shutdown complete")
	case <-time.After(5 * time.Second):
		app.logger.Warn("Shutdown timed out")
	}

	if app.redisClient != nil {
		app.redisClient.Close()
	}
	app.payloadStore.Close()
}

// engineClientAdapter adapts ethclient.EngineClient to blockbuilder.EngineClient interface
type engineClientAdapter struct {
	client *ethclient.EngineClient
}

func (a *engineClientAdapter) ForkchoiceUpdatedV3(ctx context.Context, state engine.ForkchoiceStateV1, attrs *engine.PayloadAttributes) (engine.ForkChoiceResponse, error) {
	return a.client.ForkchoiceUpdatedV3(ctx, state, attrs)
}

func (a *engineClientAdapter) GetPayloadV4(ctx context.Context, payloadID engine.PayloadID) (*engine.ExecutionPayloadEnvelope, error) {
	return a.client.GetPayloadV4(ctx, payloadID)
}

func (a *engineClientAdapter) NewPayloadV3(ctx context.Context, payload engine.ExecutableData, hashes []common.Hash, root *common.Hash) (engine.PayloadStatusV1, error) {
	return a.client.NewPayloadV3(ctx, payload, hashes, root)
}

func (a *engineClientAdapter) HeaderByNumber(ctx context.Context, number interface{}) (*types.Header, error) {
	var n *big.Int
	if number != nil {
		if v, ok := number.(*big.Int); ok {
			n = v
		}
	}
	return a.client.HeaderByNumber(ctx, n)
}

func (a *engineClientAdapter) GetMempoolStatus(ctx context.Context) (*blockbuilder.MempoolStatus, error) {
	status, err := a.client.GetMempoolStatus(ctx)
	if err != nil || status == nil {
		return nil, err
	}
	return &blockbuilder.MempoolStatus{
		Pending: uint64(status.Pending),
		Queued:  uint64(status.Queued),
	}, nil
}
