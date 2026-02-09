# Part 3: Member Nodes Architecture

A distributed consensus system with Redis leader election, PostgreSQL persistence, and member nodes that sync blocks from the leader and execute them on their own Geth — making each member a full execution replica.

## Features

- **Leader Mode**: Full consensus with Redis leader election, Geth block production, and PostgreSQL storage
- **Member Mode**: Sync blocks from leader, execute on local Geth via Engine API
- **PostgreSQL Storage**: Durable payload persistence (leader and each member have their own instance)
- **HTTP API**: Block sync endpoints for member nodes
- **Horizontal Scaling**: Add execution replicas that can serve RPC queries independently

## Structure

```
03-member-nodes/
├── cmd/
│   └── main.go           # CLI entry point
└── pkg/
    ├── ethclient/
    │   └── client.go     # Engine API client with JWT
    ├── blockbuilder/
    │   └── builder.go    # Block building with PostgreSQL
    ├── redis/
    │   ├── client.go     # Redis client wrapper (includes Lua scripts)
    │   └── leaderelection.go
    ├── postgres/
    │   └── store.go      # PostgreSQL payload store
    ├── api/
    │   └── server.go     # HTTP API for block sync
    ├── sync/
    │   └── syncer.go     # Member node sync + Geth execution
    └── state/
        └── state.go      # State management
```

## Run

```bash
# Start infrastructure (from repo root)
docker compose up -d geth geth-member redis postgres postgres-member

# Run leader node
go run ./cmd/main.go --instance-id leader-1 --mode leader \
  --health-addr :8080 --api-addr :8090

# Run member node (with its own Geth and PostgreSQL)
go run ./cmd/main.go --instance-id member-1 --mode member \
  --leader-url http://localhost:8090 \
  --eth-client-url http://localhost:8552 \
  --postgres-url "postgres://postgres:postgres@localhost:5433/consensus?sslmode=disable" \
  --health-addr :8081
```

## Configuration

### Leader Mode Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--instance-id` | (required) | Unique node identifier |
| `--mode` | `leader` | Node mode |
| `--eth-client-url` | `http://localhost:8551` | Geth Engine API URL |
| `--redis-addr` | `localhost:6379` | Redis server address |
| `--postgres-url` | (see below) | PostgreSQL connection URL |
| `--api-addr` | `:8090` | Block sync API address |

### Member Mode Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--instance-id` | (required) | Unique node identifier |
| `--mode` | `member` | Set to `member` |
| `--leader-url` | `http://localhost:8090` | Leader API URL |
| `--eth-client-url` | `http://localhost:8551` | Local Geth Engine API URL |
| `--postgres-url` | (see below) | PostgreSQL connection URL |

## Architecture

### System Overview

```
                    ┌─────────────────────────────────────┐
                    │              Leader                 │
                    │  ┌──────────┐    ┌──────────────┐  │
                    │  │ Engine   │───►│ Block        │  │
                    │  │ Client   │    │ Builder      │  │
                    │  └──────────┘    └──────┬───────┘  │
                    │                         │          │
                    │  ┌──────────┐    ┌──────▼───────┐  │
                    │  │ Leader   │    │ PostgreSQL   │  │
                    │  │ Election │    │ Store        │  │
                    │  └────┬─────┘    └──────┬───────┘  │
                    │       │                 │          │
                    │       ▼                 ▼          │
                    │  ┌──────────┐    ┌──────────────┐  │
                    │  │ Redis    │    │ HTTP API     │  │
                    │  │          │    │ :8090        │  │
                    │  └──────────┘    └──────┬───────┘  │
                    └─────────────────────────┼──────────┘
                                              │
               ┌──────────────────────────────┼──────────────────────────────┐
               │                              │                              │
               ▼                              ▼                              ▼
        ┌─────────────┐               ┌─────────────┐               ┌─────────────┐
        │  Member 1   │               │  Member 2   │               │  Member N   │
        │  ┌───────┐  │               │  ┌───────┐  │               │  ┌───────┐  │
        │  │Syncer │  │               │  │Syncer │  │               │  │Syncer │  │
        │  └───┬───┘  │               │  └───┬───┘  │               │  └───┬───┘  │
        │      ▼      │               │      ▼      │               │      ▼      │
        │  ┌───────┐  │               │  ┌───────┐  │               │  ┌───────┐  │
        │  │ Geth  │  │               │  │ Geth  │  │               │  │ Geth  │  │
        │  └───┬───┘  │               │  └───┬───┘  │               │  └───┬───┘  │
        │      ▼      │               │      ▼      │               │      ▼      │
        │  ┌───────┐  │               │  ┌───────┐  │               │  ┌───────┐  │
        │  │  PG   │  │               │  │  PG   │  │               │  │  PG   │  │
        │  └───────┘  │               │  └───────┘  │               │  └───────┘  │
        └─────────────┘               └─────────────┘               └─────────────┘
```

### HTTP API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/blocks/latest` | GET | Get latest block |
| `/blocks/{number}` | GET | Get block by number |
| `/blocks/{hash}` | GET | Get block by hash |
| `/blocks?after=N&limit=M` | GET | Get blocks after N (limit capped at 1000) |

### Database Schema

```sql
CREATE TABLE payloads (
    block_number BIGINT PRIMARY KEY,
    block_hash TEXT NOT NULL UNIQUE,
    parent_hash TEXT NOT NULL,
    payload_data TEXT NOT NULL,
    requests_data TEXT NOT NULL DEFAULT '',
    timestamp BIGINT NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW()
);
```

## Key Concepts

### Leader-Member Separation

**Leader responsibilities:**
- Produce blocks via Engine API
- Store payloads in PostgreSQL
- Publish to Redis streams
- Serve sync API

**Member responsibilities:**
- Poll leader API for new blocks
- Execute blocks on local Geth (NewPayloadV4 + ForkchoiceUpdatedV3)
- Store payloads in local PostgreSQL
- Serve RPC queries (eth_call, eth_getBalance, etc.)

### Sync Protocol

Members poll the leader with exponential backoff on failures (200ms–30s):

```
GET /blocks?after=1000&limit=100
```

Response:
```json
{
  "blocks": [
    {
      "block_number": 1001,
      "block_hash": "0x...",
      "parent_hash": "0x...",
      "payload_data": "base64...",
      "requests_data": "base64...",
      "timestamp": 1700000000
    }
  ]
}
```

### Scaling Patterns

1. **Read Scaling**: Add member nodes — each is a full execution replica
2. **Write Scaling**: Leader handles all writes
3. **Geographic Distribution**: Members in different regions

## Endpoints

### Leader
- `GET /health` - Health with leader status
- `GET /metrics` - Prometheus metrics
- `GET /blocks/*` - Block sync API

### Member
- `GET /health` - Health with sync status
- `GET /metrics` - Prometheus metrics

## Testing

```bash
# Check leader health
curl localhost:8080/health
# OK (mode=leader, isLeader=true)

# Check member health
curl localhost:8081/health
# OK (mode=member, lastSynced=1234, totalSynced=500)

# Get latest block from leader API
curl localhost:8090/blocks/latest

# Get blocks after number
curl "localhost:8090/blocks?after=100&limit=10"
```

## Production Considerations

1. **PostgreSQL**: Use connection pooling (PgBouncer)
2. **Redis**: Use Redis Cluster for HA
3. **Load Balancing**: Put members behind a load balancer
4. **Monitoring**: Track sync lag on members
5. **Backups**: Regular PostgreSQL backups

## Complete System

Running the full system:

```bash
# Start infrastructure
docker compose up -d geth geth-member redis postgres postgres-member

# Start multiple leaders (only one will be active via Redis election)
go run ./cmd/main.go --instance-id leader-1 --mode leader &
go run ./cmd/main.go --instance-id leader-2 --mode leader \
  --health-addr :8082 --api-addr :8092 &

# Start member node
go run ./cmd/main.go --instance-id member-1 --mode member \
  --eth-client-url http://localhost:8552 \
  --postgres-url "postgres://postgres:postgres@localhost:5433/consensus?sslmode=disable" \
  --health-addr :8081 &
```
