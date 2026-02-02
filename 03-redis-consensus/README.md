# Part 3: Redis Distributed Consensus

A distributed consensus implementation using Redis for leader election and block coordination.

## Features

- **Leader Election**: TTL-based distributed locking
- **Block Streaming**: Redis Streams for block propagation
- **State Persistence**: Redis-backed state management
- **Automatic Failover**: Leader takeover on failure

## Structure

```
03-redis-consensus/
├── cmd/
│   └── main.go           # CLI entry point
└── pkg/
    ├── ethclient/
    │   └── client.go     # Engine API client with JWT
    ├── blockbuilder/
    │   └── builder.go    # Block building with Redis publishing
    ├── redis/
    │   ├── client.go     # Redis client wrapper
    │   └── leaderelection.go  # Leader election logic
    └── state/
        └── state.go      # Redis-backed state management
```

## Run

```bash
# Start Geth and Redis (from repo root)
docker-compose up -d geth redis

# Run multiple nodes
go run ./cmd/main.go --instance-id node-1 --health-addr :8080 &
go run ./cmd/main.go --instance-id node-2 --health-addr :8081 &
go run ./cmd/main.go --instance-id node-3 --health-addr :8082 &
```

## Configuration

| Flag | Default | Description |
|------|---------|-------------|
| `--instance-id` | (required) | Unique node identifier |
| `--eth-client-url` | `http://localhost:8551` | Geth Engine API URL |
| `--jwt-secret` | (default) | 32-byte hex JWT secret |
| `--redis-addr` | `localhost:6379` | Redis server address |
| `--redis-password` | `` | Redis password |
| `--redis-stream` | `consensus:blocks` | Stream name for blocks |
| `--health-addr` | `:8080` | Health check endpoint |

## Architecture

### Leader Election

```
┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│   Node 1    │     │   Node 2    │     │   Node 3    │
│  (Leader)   │     │  (Standby)  │     │  (Standby)  │
└──────┬──────┘     └──────┬──────┘     └──────┬──────┘
       │                   │                   │
       │ SetNX lock:leader │                   │
       ├───────────────────►                   │
       │                   │ Fails (key exists)│
       │                   ├──────────────────►│
       │                   │                   │
       │    Renew TTL      │                   │
       ├──────────────────►│                   │
       │                   │                   │
```

### Block Publishing

When a block is finalized, the leader publishes to Redis Streams:

```
XADD consensus:blocks * \
  block_hash 0x... \
  block_number 123 \
  payload <base64> \
  timestamp 1234567890
```

### Failover

1. Leader fails to renew TTL (5s)
2. Lock expires
3. Standby node acquires lock via SetNX
4. New leader initializes from Geth
5. Block production continues

## Key Concepts

### TTL-Based Locking

```go
// Acquire or renew leadership
acquired, _ := redis.SetNX(ctx, "consensus:leader", instanceID, 5*time.Second)
if acquired {
    // We're the leader
}
```

### Redis Streams

Used for block propagation to followers:
- Durable message storage
- Automatic ID generation
- Consumer group support (for Part 4)

## Endpoints

- `GET /health` - Returns 200 with leader status
- `GET /metrics` - Prometheus metrics

## Testing Failover

```bash
# Check who's leader
curl localhost:8080/health  # OK (leader=true)
curl localhost:8081/health  # OK (leader=false)

# Kill the leader
kill %1

# Wait 5s for TTL to expire
sleep 5

# New leader elected
curl localhost:8081/health  # OK (leader=true)
```

## Next

Continue to [Part 4: Member Nodes](../04-member-nodes/) for horizontal scaling with PostgreSQL.
