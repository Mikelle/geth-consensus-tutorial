# Part 1: Engine API Basics

This example demonstrates the fundamental Engine API operations:

- JWT authentication for secure communication with Geth
- `ForkchoiceUpdatedV3` - Set chain head and trigger block building
- `GetPayloadV4` - Retrieve a built block
- `NewPayloadV4` - Submit a block for execution

## Run

```bash
# Start Geth first (from repo root)
docker compose up -d geth

# Run the example
go run main.go
```

## Expected Output

```
Current head: block=0 hash=0x...
Step 1: Calling ForkchoiceUpdatedV3...
  PayloadID: 0x...
  Status: VALID
Step 2: Waiting for block assembly (100ms)...
Step 3: Calling GetPayloadV4...
  Block Number: 1
  Block Hash: 0x...
  Parent Hash: 0x...
  Timestamp: 1234567890
  Transactions: 0
Step 4: Calling NewPayloadV4...
  Status: VALID
Step 5: Finalizing block...
  Status: VALID

Block 1 finalized successfully!
```

## Key Concepts

### JWT Authentication

Geth's Engine API requires JWT authentication. The token is regenerated per request with a fresh timestamp:

```go
token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
    "iat": time.Now().Unix(),
})
```

### Two-Phase Block Building

1. **Propose**: `ForkchoiceUpdatedV3` with `PayloadAttributes` returns a `PayloadID`
2. **Wait**: Give Geth time to include transactions
3. **Retrieve**: `GetPayloadV4` returns the built `ExecutionPayload`
4. **Execute**: `NewPayloadV4` submits the block to Geth for execution
5. **Finalize**: `ForkchoiceUpdatedV3` without attributes sets the new head

## Next

Continue to [Part 2: Single Node Consensus](../02-single-node/) for a complete implementation.
