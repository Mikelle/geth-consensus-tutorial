# Custom Geth Consensus Tutorial

Build a custom consensus layer for go-ethereum (Geth) from scratch. This tutorial accompanies the blog series on writing custom consensus mechanisms.

## Blog Series

1. [Writing Custom Consensus for Geth: A Practical Guide](https://mikelle.github.io/blog/custom-geth-consensus) - Engine API fundamentals
2. Single Node Consensus: Building a Complete Implementation - Production-ready single node *(coming soon)*
3. Scaling to Distributed Consensus with Redis - Fault tolerance with leader election *(coming soon)*
4. Member Nodes: Horizontal Scaling for Consensus - PostgreSQL-based scaling *(coming soon)*
5. CometBFT Integration: BFT Finality for Geth - Byzantine fault tolerance *(coming soon)*

## Repository Structure

```
├── 01-engine-api/        # Part 1: Minimal Engine API client
├── 02-single-node/       # Part 2: Complete single-node consensus
├── 03-redis-consensus/   # Part 3: Distributed consensus with Redis
├── 04-member-nodes/      # Part 4: Member nodes with PostgreSQL
├── 05-cometbft-consensus/# Part 5: CometBFT BFT consensus
└── docker-compose.yml    # Run Geth + Redis + PostgreSQL locally
```

Each directory is self-contained and progressively builds on the previous part.

## Quick Start

### Prerequisites

- Go 1.24+
- Docker & Docker Compose
- Make (optional)

### Run the Infrastructure

```bash
# Start Geth, Redis, and PostgreSQL
docker compose up -d

# Wait for Geth to initialize (~10 seconds)
docker compose logs -f geth
```

### Run Each Part

```bash
# Part 1: Engine API basics
cd 01-engine-api
go run main.go

# Part 2: Single node consensus
cd 02-single-node
go run ./cmd/main.go --instance-id node-1

# Part 3: Redis consensus (run multiple terminals)
cd 03-redis-consensus
go run ./cmd/main.go --instance-id node-1  # Terminal 1
go run ./cmd/main.go --instance-id node-2  # Terminal 2

# Part 4: Member nodes
cd 04-member-nodes
go run ./cmd/main.go leader --instance-id leader-1   # Terminal 1
go run ./cmd/main.go follower --instance-id member-1 # Terminal 2

# Part 5: CometBFT consensus (requires CometBFT installed)
cometbft init --home ~/.cometbft  # Initialize once
cd 05-cometbft-consensus
go run ./cmd/main.go --cmt-home ~/.cometbft
```

## Architecture Overview

### Part 1: Engine API

```
┌─────────────────┐
│  Your Code      │
│  (main.go)      │
└────────┬────────┘
         │ Engine API (HTTP + JWT)
┌────────▼────────┐
│      Geth       │
└─────────────────┘
```

### Part 2: Single Node

```
┌─────────────────────────────────────┐
│          SingleNodeApp              │
│  ┌────────────┐  ┌───────────────┐  │
│  │BlockBuilder│  │ StateManager  │  │
│  └────────────┘  └───────────────┘  │
└────────────────┬────────────────────┘
                 │
         ┌───────▼───────┐
         │     Geth      │
         └───────────────┘
```

### Part 3: Redis Consensus

```
┌──────────┐     ┌──────────┐
│  Node 1  │     │  Node 2  │
│ (Leader) │     │(Follower)│
└────┬─────┘     └────┬─────┘
     │                │
     └───────┬────────┘
             │
     ┌───────▼───────┐
     │    Redis      │
     │  • Election   │
     │  • Streams    │
     └───────────────┘
```

### Part 4: Member Nodes

```
┌─────────────────────────┐
│     Leader Node         │
│  ┌──────┐  ┌─────────┐  │
│  │ Geth │  │Postgres │  │
│  └──────┘  └────┬────┘  │
└─────────────────┼───────┘
                  │
        ┌─────────┴─────────┐
        ▼                   ▼
   ┌─────────┐         ┌─────────┐
   │Member 1 │         │Member 2 │
   │ + Geth  │         │ + Geth  │
   └─────────┘         └─────────┘
```

### Part 5: CometBFT Consensus

```
┌───────────────────────────────────────────────────────────────┐
│                    CometBFT P2P Network                        │
│  ┌─────────────┐   ┌─────────────┐   ┌─────────────┐         │
│  │ Validator 1 │   │ Validator 2 │   │ Validator 3 │         │
│  │  CometBFT   │◄─►│  CometBFT   │◄─►│  CometBFT   │         │
│  └──────┬──────┘   └──────┬──────┘   └──────┬──────┘         │
└─────────┼─────────────────┼─────────────────┼─────────────────┘
          │ ABCI            │ ABCI            │ ABCI
   ┌──────▼──────┐   ┌──────▼──────┐   ┌──────▼──────┐
   │   App + DB  │   │   App + DB  │   │   App + DB  │
   └──────┬──────┘   └──────┬──────┘   └──────┬──────┘
          │ Engine          │ Engine          │ Engine
   ┌──────▼──────┐   ┌──────▼──────┐   ┌──────▼──────┐
   │    Geth 1   │   │    Geth 2   │   │    Geth 3   │
   └─────────────┘   └─────────────┘   └─────────────┘
```

## Key Concepts

| Concept | Description |
|---------|-------------|
| Engine API | HTTP/JSON-RPC interface for consensus-execution communication |
| JWT Auth | Stateless authentication for Engine API requests |
| ForkchoiceUpdated | Set chain head and trigger block building |
| GetPayload | Retrieve a built block from Geth |
| NewPayload | Submit a block for execution |
| Leader Election | TTL-based distributed lock using Redis |
| Consumer Groups | Redis Streams for exactly-once message delivery |

## Configuration

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `ETH_CLIENT_URL` | `http://localhost:8551` | Geth Engine API endpoint |
| `JWT_SECRET` | (see docker compose) | 32-byte hex-encoded secret |
| `REDIS_ADDR` | `localhost:6379` | Redis address |
| `POSTGRES_DSN` | (see docker compose) | PostgreSQL connection string |

### Geth Genesis

The included `genesis.json` creates a local PoS-ready chain with:
- Chain ID: 1337
- Pre-funded test account: `0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266` (Foundry default)
- Instant block times (controlled by consensus layer)

## Production Notes

This tutorial code is simplified for learning. For production, consider:

- **TLS**: Enable HTTPS for Engine API and inter-node communication
- **Secrets Management**: Use Vault or similar for JWT secrets
- **Monitoring**: Add comprehensive Prometheus metrics
- **Rate Limiting**: Protect API endpoints
- **Circuit Breakers**: Handle Geth unavailability gracefully

## Production Implementation

This tutorial is based on the [mev-commit consensus layer](https://github.com/primev/mev-commit/tree/main/cl), which powers encrypted preconfirmations on Ethereum.

## License

MIT
