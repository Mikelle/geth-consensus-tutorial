# Part 2: Single Node Consensus

A complete, production-ready single-node consensus implementation with:

- Retry logic with exponential backoff
- Health checks for orchestration
- Graceful shutdown
- Prometheus metrics
- Configuration management

## Structure

```
02-single-node/
├── cmd/
│   └── main.go           # CLI entry point
└── pkg/
    ├── ethclient/
    │   └── client.go     # Engine API client with JWT
    ├── blockbuilder/
    │   └── builder.go    # Block building logic
    └── state/
        └── state.go      # In-memory state management
```

## Run

```bash
# Start Geth (from repo root)
docker-compose up -d geth

# Run the node
go run ./cmd/main.go --instance-id node-1
```

## Configuration

| Flag | Default | Description |
|------|---------|-------------|
| `--instance-id` | (required) | Unique node identifier |
| `--eth-client-url` | `http://localhost:8551` | Geth Engine API URL |
| `--jwt-secret` | (default) | 32-byte hex JWT secret |
| `--priority-fee-recipient` | `0x0...` | Fee recipient address |
| `--evm-build-delay` | `1ms` | Delay for block assembly |
| `--evm-build-delay-empty-block` | `1m` | Min time between empty blocks |
| `--health-addr` | `:8080` | Health check endpoint |

## Endpoints

- `GET /health` - Returns 200 if healthy
- `GET /metrics` - Prometheus metrics

## Key Concepts

### State Machine

```
┌──────────────┐  GetPayload()  ┌─────────────────┐
│ StepBuildBlock │ ────────────► │ StepFinalizeBlock │
└──────────────┘                └─────────────────┘
        ▲                                │
        └────────────────────────────────┘
                  FinalizeBlock()
```

### Retry Logic

Uses exponential backoff with:
- Initial interval: 200ms
- Max interval: 30s
- Max attempts: 10

Permanent errors (e.g., INVALID status) are not retried.

## Next

Continue to [Part 3: Redis Consensus](../03-redis-consensus/) for distributed fault tolerance.
