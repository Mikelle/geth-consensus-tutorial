// Part 5: CometBFT Consensus
//
// This example demonstrates BFT consensus for Geth using CometBFT:
// - ABCI (Application Blockchain Interface) integration
// - BFT consensus with instant finality
// - Multi-validator support
//
// Run: go run ./cmd/main.go

package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	abcitypes "github.com/cometbft/cometbft/abci/types"
	cmtcfg "github.com/cometbft/cometbft/config"
	cmtlog "github.com/cometbft/cometbft/libs/log"
	cmtnode "github.com/cometbft/cometbft/node"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/privval"
	"github.com/cometbft/cometbft/proxy"
	"github.com/dgraph-io/badger/v3"
	"github.com/ethereum/go-ethereum/beacon/engine"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/mikelle/geth-consensus-tutorial/05-cometbft-consensus/pkg/app"
	"github.com/mikelle/geth-consensus-tutorial/05-cometbft-consensus/pkg/ethclient"
	"github.com/urfave/cli/v2"
)

func main() {
	cliApp := &cli.App{
		Name:  "cometbft-geth",
		Usage: "CometBFT consensus layer for Geth",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "cmt-home",
				Usage:   "CometBFT home directory",
				Value:   filepath.Join(os.Getenv("HOME"), ".cometbft"),
				EnvVars: []string{"CMT_HOME"},
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
		},
		Action: runNode,
	}

	if err := cliApp.Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func runNode(c *cli.Context) error {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cmtHome := c.String("cmt-home")
	ethClientURL := c.String("eth-client-url")
	jwtSecretHex := c.String("jwt-secret")

	jwtSecret, err := hex.DecodeString(jwtSecretHex)
	if err != nil {
		return fmt.Errorf("decode JWT secret: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Connect to Geth
	logger.Info("Connecting to Geth", "url", ethClientURL)
	engineCl, err := ethclient.NewEngineClient(ctx, ethClientURL, jwtSecret)
	if err != nil {
		return fmt.Errorf("create engine client: %w", err)
	}

	// Open Badger DB for state persistence
	dbPath := filepath.Join(cmtHome, "badger")
	db, err := badger.Open(badger.DefaultOptions(dbPath))
	if err != nil {
		return fmt.Errorf("open badger db: %w", err)
	}
	defer db.Close()

	// Create ABCI application
	abciApp := app.NewGethConsensusApp(db, &engineClientAdapter{client: engineCl}, logger)

	// Load CometBFT config
	config := cmtcfg.DefaultConfig()
	config.SetRoot(cmtHome)
	config.ProxyApp = "kvstore" // Not used, we provide the app directly

	// Check if config files exist
	if _, err := os.Stat(filepath.Join(cmtHome, "config", "config.toml")); os.IsNotExist(err) {
		return fmt.Errorf("CometBFT not initialized. Run: cometbft init --home %s", cmtHome)
	}

	// Load validator key
	pv := privval.LoadFilePV(
		config.PrivValidatorKeyFile(),
		config.PrivValidatorStateFile(),
	)

	// Load node key
	nodeKey, err := p2p.LoadNodeKey(config.NodeKeyFile())
	if err != nil {
		return fmt.Errorf("load node key: %w", err)
	}

	// Create CometBFT node
	cmtLogger := cmtlog.NewTMLogger(os.Stdout)
	node, err := cmtnode.NewNode(
		config,
		pv,
		nodeKey,
		proxy.NewLocalClientCreator(abciApp),
		cmtnode.DefaultGenesisDocProviderFunc(config),
		cmtcfg.DefaultDBProvider,
		cmtnode.DefaultMetricsProvider(config.Instrumentation),
		cmtLogger,
	)
	if err != nil {
		return fmt.Errorf("create CometBFT node: %w", err)
	}

	// Start the node
	logger.Info("Starting CometBFT node", "home", cmtHome)
	if err := node.Start(); err != nil {
		return fmt.Errorf("start node: %w", err)
	}

	// Wait for shutdown
	<-ctx.Done()
	logger.Info("Shutting down...")

	if err := node.Stop(); err != nil {
		logger.Error("Error stopping node", "error", err)
	}
	node.Wait()

	return nil
}

// engineClientAdapter adapts ethclient.EngineClient to app.EngineClient interface
type engineClientAdapter struct {
	client *ethclient.EngineClient
}

func (a *engineClientAdapter) HeaderByNumber(ctx context.Context, number interface{}) (*types.Header, error) {
	return a.client.HeaderByNumber(ctx, nil)
}

func (a *engineClientAdapter) ForkchoiceUpdatedV3(ctx context.Context, state engine.ForkchoiceStateV1, attrs *engine.PayloadAttributes) (engine.ForkChoiceResponse, error) {
	return a.client.ForkchoiceUpdatedV3(ctx, state, attrs)
}

func (a *engineClientAdapter) GetPayloadV3(ctx context.Context, payloadID engine.PayloadID) (*engine.ExecutionPayloadEnvelope, error) {
	return a.client.GetPayloadV3(ctx, payloadID)
}

func (a *engineClientAdapter) NewPayloadV3(ctx context.Context, payload engine.ExecutableData, versionedHashes []common.Hash, beaconRoot *common.Hash) (engine.PayloadStatusV1, error) {
	return a.client.NewPayloadV3(ctx, payload, versionedHashes, beaconRoot)
}

// Ensure abciApp implements the ABCI interface
var _ abcitypes.Application = (*app.GethConsensusApp)(nil)
