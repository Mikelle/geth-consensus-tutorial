# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

A tutorial repository for building custom consensus layers for go-ethereum (Geth). Each subdirectory is a self-contained Go module that progressively builds on the previous part:

- `01-engine-api/` - Minimal Engine API client (JWT auth, ForkchoiceUpdated, GetPayload, NewPayload)
- `02-single-node/` - Production-ready single-node consensus with retry logic and health checks
- `03-redis-consensus/` - Distributed consensus using Redis for leader election and block streaming
- `04-member-nodes/` - Horizontally scalable system with PostgreSQL persistence and HTTP sync API
- `05-cometbft-consensus/` - Byzantine fault tolerant consensus using CometBFT's ABCI interface

## Common Commands

### Infrastructure
```bash
# Start all services (Geth, Redis, PostgreSQL)
docker-compose up -d

# Start specific services
docker-compose up -d geth redis postgres
```

### Running Each Part
```bash
# Part 1: Engine API basics
cd 01-engine-api && go run main.go

# Part 2: Single node
cd 02-single-node && go run ./cmd/main.go --instance-id node-1

# Part 3: Redis consensus (multiple terminals)
cd 03-redis-consensus && go run ./cmd/main.go --instance-id node-1

# Part 4: Member nodes
cd 04-member-nodes && go run ./cmd/main.go leader --instance-id leader-1
cd 04-member-nodes && go run ./cmd/main.go follower --instance-id member-1

# Part 5: CometBFT (requires: cometbft init --home ~/.cometbft)
cd 05-cometbft-consensus && go run ./cmd/main.go --cmt-home ~/.cometbft
```

## Architecture

### Engine API Communication Pattern
All parts communicate with Geth using the same two-phase block building pattern:
1. `ForkchoiceUpdatedV3` with `PayloadAttributes` - triggers block building, returns `PayloadID`
2. `GetPayloadV4` - retrieves the built `ExecutionPayload`
3. `NewPayloadV4` - submits block to Geth for execution
4. `ForkchoiceUpdatedV3` without attributes - sets the new chain head

### Package Structure (Parts 2-4)
Each Go module follows the same structure:
- `cmd/main.go` - CLI entry point using urfave/cli
- `pkg/ethclient/client.go` - Engine API client with JWT authentication
- `pkg/blockbuilder/builder.go` - Block building logic
- `pkg/state/state.go` - State management (in-memory, Redis-backed, or PostgreSQL)

### Part 5 CometBFT ABCI Methods
The CometBFT integration implements these ABCI methods:
- `PrepareProposal` - Proposer builds block via Engine API
- `ProcessProposal` - Validators verify proposed block
- `FinalizeBlock` - Execute block via NewPayload after consensus

## Key Environment Variables
| Variable | Default | Description |
|----------|---------|-------------|
| `ETH_CLIENT_URL` | `http://localhost:8551` | Geth Engine API endpoint |
| `JWT_SECRET` | (see docker-compose) | 32-byte hex-encoded secret |
| `REDIS_ADDR` | `localhost:6379` | Redis address |
| `POSTGRES_DSN` | (see docker-compose) | PostgreSQL connection string |

## Dependencies

- Go 1.21+
- Docker & Docker Compose
- CometBFT v0.38.11 (Part 5 only): `go install github.com/cometbft/cometbft/cmd/cometbft@v0.38.11`

## Commit Convention

Use conventional commits: `feat`, `fix`, `refactor`, `docs`, `chore`, `test`

Examples:
- `feat: add redis leader election`
- `fix: handle jwt token expiration`
- `docs: update part 3 readme`
