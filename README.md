# Custom Geth Consensus Tutorial

Build a custom consensus layer for go-ethereum (Geth) from scratch. This tutorial accompanies the blog series on writing custom consensus mechanisms.

## Blog Series

1. [Writing Custom Consensus for Geth: A Practical Guide](https://mikelle.github.io/blog/custom-geth-consensus) - Engine API fundamentals
2. [Single Node Consensus: Building a Complete Implementation](https://mikelle.github.io/blog/single-node-consensus) - Production-ready single node
3. [Distributed Consensus with Redis, PostgreSQL, and Member Nodes](https://mikelle.github.io/blog/redis-distributed-consensus) - Leader election, storage, and horizontal scaling
4. [CometBFT Integration: BFT Finality for Geth](https://mikelle.github.io/blog/cometbft-geth-consensus) - Byzantine fault tolerance

## Repository Structure

```
├── 01-engine-api/          # Part 1: Minimal Engine API client
├── 02-single-node/         # Part 2: Complete single-node consensus
├── 03-member-nodes/        # Part 3: Distributed consensus with member nodes
├── 04-cometbft-consensus/  # Part 4: CometBFT BFT consensus
└── docker-compose.yml      # Run Geth + Redis + PostgreSQL locally
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

# Part 3: Member nodes (leader + members)
cd 03-member-nodes
go run ./cmd/main.go --instance-id leader-1 --mode leader                        # Terminal 1
go run ./cmd/main.go --instance-id member-1 --mode member \
  --eth-client-url http://localhost:8552 \
  --postgres-url "postgres://postgres:postgres@localhost:5433/consensus?sslmode=disable" \
  --health-addr :8081                                                             # Terminal 2

# Part 4: CometBFT consensus (requires CometBFT installed)
cometbft init --home ~/.cometbft  # Initialize once
cd 04-cometbft-consensus
go run ./cmd/main.go --cmt-home ~/.cometbft
```

## Architecture Overview

### Part 1: Engine API

```mermaid
flowchart TD
    Code["Your Code<br/>(main.go)"] -->|"Engine API (HTTP + JWT)"| Geth["Geth"]
```

### Part 2: Single Node

```mermaid
flowchart TD
    subgraph App["SingleNodeApp"]
        BlockBuilder["BlockBuilder"]
        StateManager["StateManager"]
    end
    App --> Geth["Geth"]
```

### Part 3: Member Nodes

```mermaid
flowchart TD
    subgraph Leader["Leader Node"]
        Geth["Geth"]
        Redis["Redis"]
        PG[("PG")]
    end
    Leader --> M1["Member 1<br/>Syncer + Geth + PG"]
    Leader --> M2["Member 2<br/>Syncer + Geth + PG"]
    Leader --> M3["Member 3<br/>Syncer + Geth + PG"]
```

### Part 4: CometBFT Consensus

```mermaid
flowchart TD
    subgraph P2P["CometBFT P2P Network"]
        V1["Validator 1<br/>CometBFT"] --- V2["Validator 2<br/>CometBFT"] --- V3["Validator 3<br/>CometBFT"]
    end
    V1 -->|"ABCI"| A1["App + DB"]
    V2 -->|"ABCI"| A2["App + DB"]
    V3 -->|"ABCI"| A3["App + DB"]
    A1 -->|"Engine"| G1["Geth 1"]
    A2 -->|"Engine"| G2["Geth 2"]
    A3 -->|"Engine"| G3["Geth 3"]
```

## Key Concepts

| Concept | Description |
|---------|-------------|
| Engine API | HTTP/JSON-RPC interface for consensus-execution communication |
| JWT Auth | Stateless authentication for Engine API requests |
| ForkchoiceUpdated | Set chain head and trigger block building |
| GetPayload | Retrieve a built block from Geth |
| NewPayload | Submit a block for execution |
| Leader Election | TTL-based distributed lock using Redis with atomic Lua scripts |

## Configuration

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `ETH_CLIENT_URL` | `http://localhost:8551` | Geth Engine API endpoint |
| `JWT_SECRET` | (see docker compose) | 32-byte hex-encoded secret |
| `REDIS_ADDR` | `localhost:6379` | Redis address |
| `POSTGRES_URL` | (see docker compose) | PostgreSQL connection string |

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

## License

MIT
