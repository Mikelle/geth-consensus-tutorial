# Part 4: Member Nodes Architecture

A horizontally scalable consensus system with PostgreSQL persistence and HTTP-based member synchronization.

## Features

- **Leader Mode**: Full consensus with Redis and PostgreSQL
- **Member Mode**: Lightweight sync from leader via HTTP API
- **PostgreSQL Storage**: Durable payload persistence
- **HTTP API**: Block sync endpoints for member nodes
- **Horizontal Scaling**: Add read replicas without touching consensus

## Structure

```
04-member-nodes/
├── cmd/
│   └── main.go           # CLI entry point
└── pkg/
    ├── ethclient/
    │   └── client.go     # Engine API client with JWT
    ├── blockbuilder/
    │   └── builder.go    # Block building with PostgreSQL
    ├── redis/
    │   ├── client.go     # Redis client wrapper
    │   └── leaderelection.go
    ├── postgres/
    │   └── store.go      # PostgreSQL payload store
    ├── api/
    │   └── server.go     # HTTP API for block sync
    ├── sync/
    │   └── syncer.go     # Member node sync logic
    └── state/
        └── state.go      # State management
```

## Run

```bash
# Start infrastructure (from repo root)
docker-compose up -d geth redis postgres

# Run leader node
go run ./cmd/main.go --instance-id leader-1 --mode leader \
  --health-addr :8080 --api-addr :8090

# Run member nodes
go run ./cmd/main.go --instance-id member-1 --mode member \
  --leader-url http://localhost:8090 --health-addr :8081

go run ./cmd/main.go --instance-id member-2 --mode member \
  --leader-url http://localhost:8090 --health-addr :8082
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
        │  │ Local │  │               │  │ Local │  │               │  │ Local │  │
        │  │ PG    │  │               │  │ PG    │  │               │  │ PG    │  │
        │  └───────┘  │               │  └───────┘  │               │  └───────┘  │
        └─────────────┘               └─────────────┘               └─────────────┘
```

### HTTP API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/blocks/latest` | GET | Get latest block |
| `/blocks/{number}` | GET | Get block by number |
| `/blocks/{hash}` | GET | Get block by hash |
| `/blocks?after=N&limit=M` | GET | Get blocks after N |

### Database Schema

```sql
CREATE TABLE payloads (
    block_number BIGINT PRIMARY KEY,
    block_hash TEXT NOT NULL UNIQUE,
    parent_hash TEXT NOT NULL,
    payload_data TEXT NOT NULL,
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
- Store payloads locally
- Serve read queries

### Sync Protocol

Members poll the leader:

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
      "payload_data": "base64..."
    }
  ]
}
```

### Scaling Patterns

1. **Read Scaling**: Add member nodes
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
docker-compose up -d geth redis postgres

# Start multiple leaders (only one will be active)
./bin/member-nodes --instance-id leader-1 --mode leader &
./bin/member-nodes --instance-id leader-2 --mode leader &

# Start member nodes
./bin/member-nodes --instance-id member-1 --mode member &
./bin/member-nodes --instance-id member-2 --mode member &
./bin/member-nodes --instance-id member-3 --mode member &
```
