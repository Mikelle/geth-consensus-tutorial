# Part 5: CometBFT Consensus - BFT Finality for Geth

This part demonstrates how to integrate **CometBFT** (formerly Tendermint) as a Byzantine Fault Tolerant (BFT) consensus layer for Geth, providing instant finality and multi-validator support.

## Why CometBFT + Geth?

The previous parts of this tutorial showed simpler consensus mechanisms:
- **Part 2**: Single node (no fault tolerance)
- **Parts 3-4**: Redis-based leader election (crash fault tolerant)

CometBFT provides **Byzantine Fault Tolerance (BFT)**, meaning the network can tolerate up to 1/3 of validators being malicious or faulty while still reaching consensus. This is critical for production blockchain systems.

### Key Benefits

| Feature | Redis Consensus | CometBFT |
|---------|----------------|----------|
| Fault Tolerance | Crash only | Byzantine (malicious actors) |
| Finality | Probabilistic | Instant (single slot) |
| Validators | Single leader | Multi-validator voting |
| Network | Trusted | Untrusted |

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                    CometBFT Consensus                            │
│  ┌──────────┐   ┌──────────┐   ┌──────────┐   ┌──────────┐     │
│  │Validator1│   │Validator2│   │Validator3│   │Validator4│     │
│  └────┬─────┘   └────┬─────┘   └────┬─────┘   └────┬─────┘     │
│       │              │              │              │            │
│       └──────────────┴──────┬───────┴──────────────┘            │
│                             │                                   │
│                      P2P Gossip Network                         │
└─────────────────────────────┼───────────────────────────────────┘
                              │
                     ABCI (Local Socket)
                              │
┌─────────────────────────────▼───────────────────────────────────┐
│                    ABCI Application                              │
│  ┌─────────────────────────────────────────────────────────┐   │
│  │  GethConsensusApp                                        │   │
│  │  ├─ PrepareProposal() → Build block via Engine API      │   │
│  │  ├─ ProcessProposal() → Validate proposed block         │   │
│  │  ├─ FinalizeBlock()   → Execute via NewPayload          │   │
│  │  └─ Commit()          → Persist state                   │   │
│  └─────────────────────────────────────────────────────────┘   │
│                              │                                  │
│                     Engine API (HTTP + JWT)                     │
└──────────────────────────────┼──────────────────────────────────┘
                               │
┌──────────────────────────────▼──────────────────────────────────┐
│                         Geth                                     │
│  ├─ Block Builder        (Assembles transactions)               │
│  ├─ State Machine        (Executes EVM)                         │
│  └─ Storage              (Persists chain)                       │
└─────────────────────────────────────────────────────────────────┘
```

## CometBFT Consensus Flow

CometBFT uses a three-phase commit protocol:

```
Height H
    │
    ▼
┌─────────────────────────────────────────────────────────────┐
│  PROPOSE PHASE                                               │
│  ┌─────────────────────────────────────────────────────────┐│
│  │ Proposer calls PrepareProposal()                        ││
│  │   1. ForkchoiceUpdatedV3 (start building)               ││
│  │   2. Wait 300ms for transactions                        ││
│  │   3. GetPayloadV5 (retrieve built block)                ││
│  │   4. Wrap payload in CometBFT transaction               ││
│  └─────────────────────────────────────────────────────────┘│
└─────────────────────────────────────────────────────────────┘
    │
    ▼
┌─────────────────────────────────────────────────────────────┐
│  PREVOTE PHASE                                               │
│  ┌─────────────────────────────────────────────────────────┐│
│  │ All validators call ProcessProposal()                   ││
│  │   • Verify parent hash matches local head               ││
│  │   • Verify block height is sequential                   ││
│  │   • Verify timestamp is valid                           ││
│  │   • Vote ACCEPT or REJECT                               ││
│  └─────────────────────────────────────────────────────────┘│
└─────────────────────────────────────────────────────────────┘
    │
    ▼
┌─────────────────────────────────────────────────────────────┐
│  PRECOMMIT PHASE                                             │
│  ┌─────────────────────────────────────────────────────────┐│
│  │ Validators commit if >2/3 prevoted                      ││
│  │   • Sign precommit message                              ││
│  │   • Broadcast to network                                ││
│  └─────────────────────────────────────────────────────────┘│
└─────────────────────────────────────────────────────────────┘
    │
    ▼
┌─────────────────────────────────────────────────────────────┐
│  FINALIZATION                                                │
│  ┌─────────────────────────────────────────────────────────┐│
│  │ All nodes call FinalizeBlock()                          ││
│  │   1. NewPayloadV4 (submit to Geth)                      ││
│  │   2. ForkchoiceUpdatedV3 (set as head)                  ││
│  │   3. Save execution head to DB                          ││
│  │   4. Block is FINAL - no reorgs possible                ││
│  └─────────────────────────────────────────────────────────┘│
└─────────────────────────────────────────────────────────────┘
    │
    ▼
Height H+1
```

## ABCI Interface

The **Application Blockchain Interface (ABCI)** is CometBFT's protocol for communicating with application logic. Our app implements these key methods:

### PrepareProposal

Called when this node is the block proposer:

```go
func (app *GethConsensusApp) PrepareProposal(ctx context.Context, req *abcitypes.RequestPrepareProposal) (*abcitypes.ResponsePrepareProposal, error) {
    // 1. Start block building via ForkchoiceUpdatedV3
    response, _ := app.engineCl.ForkchoiceUpdatedV3(ctx, fcs, &attrs)

    // 2. Wait for Geth to assemble transactions
    time.Sleep(300 * time.Millisecond)

    // 3. Retrieve the built payload
    payload, _ := app.engineCl.GetPayloadV5(ctx, *response.PayloadID)

    // 4. Wrap in CometBFT transaction format
    return &abcitypes.ResponsePrepareProposal{
        Txs: [][]byte{payloadJSON},
    }, nil
}
```

### ProcessProposal

Called by all validators to validate a proposed block:

```go
func (app *GethConsensusApp) ProcessProposal(ctx context.Context, req *abcitypes.RequestProcessProposal) (*abcitypes.ResponseProcessProposal, error) {
    // Parse the proposed payload
    var msg MsgExecutionPayload
    json.Unmarshal(req.Txs[0], &msg)

    // Validate against local state
    if msg.ExecutionPayload.ParentHash != app.execHead.BlockHash {
        return REJECT
    }
    if msg.ExecutionPayload.Number != app.execHead.BlockHeight + 1 {
        return REJECT
    }

    return ACCEPT
}
```

### FinalizeBlock

Called after consensus is reached to execute the block:

```go
func (app *GethConsensusApp) FinalizeBlock(ctx context.Context, req *abcitypes.RequestFinalizeBlock) (*abcitypes.ResponseFinalizeBlock, error) {
    // 1. Submit payload to Geth
    status, _ := app.engineCl.NewPayloadV4(ctx, payload, hashes, beaconRoot)

    // 2. Update fork choice
    fcs := engine.ForkchoiceStateV1{
        HeadBlockHash:      payload.BlockHash,
        SafeBlockHash:      payload.BlockHash,
        FinalizedBlockHash: payload.BlockHash,  // Instant finality!
    }
    app.engineCl.ForkchoiceUpdatedV3(ctx, fcs, nil)

    // 3. Update local state
    app.execHead = &ExecutionHead{
        BlockHeight: payload.Number,
        BlockHash:   payload.BlockHash,
    }

    // 4. Return result for each transaction
    txResults := make([]*abcitypes.ExecTxResult, len(req.Txs))
    for i := range req.Txs {
        txResults[i] = &abcitypes.ExecTxResult{Code: 0}
    }

    return &abcitypes.ResponseFinalizeBlock{
        AppHash:   payload.BlockHash.Bytes(),
        TxResults: txResults,  // Required by CometBFT
    }, nil
}
```

## Installation

### Prerequisites

```bash
# Install CometBFT
go install github.com/cometbft/cometbft/cmd/cometbft@v0.38.11

# Verify installation
cometbft version
```

### Initialize CometBFT

```bash
# Initialize a single validator for testing
cometbft init --home ~/.cometbft

# This creates:
# ~/.cometbft/config/config.toml       - Node configuration
# ~/.cometbft/config/genesis.json      - Genesis with validator set
# ~/.cometbft/config/priv_validator_key.json  - Validator signing key
# ~/.cometbft/config/node_key.json     - P2P identity
```

## Running

### Start Geth

```bash
# From repo root
docker-compose up -d geth
```

### Start CometBFT + App

```bash
cd 04-cometbft-consensus
go run ./cmd/main.go \
  --cmt-home ~/.cometbft \
  --eth-client-url http://localhost:8551
```

### Expected Output

```
time=... level=INFO msg="Connecting to Geth" url=http://localhost:8551
time=... level=INFO msg="Starting CometBFT node" home=/Users/you/.cometbft
time=... level=INFO msg="ABCI Info called"
time=... level=INFO msg="ABCI InitChain called" chainID=test-chain
time=... level=INFO msg="Initialized from Geth genesis" height=0 hash=0x...

# First block
time=... level=INFO msg="PrepareProposal called" height=1
time=... level=INFO msg="Prepared proposal" blockNumber=1 blockHash=0x... txCount=0
time=... level=INFO msg="ProcessProposal called" height=1
time=... level=INFO msg="FinalizeBlock called" height=1
time=... level=INFO msg="Block finalized" height=1 hash=0x...

# Subsequent blocks...
time=... level=INFO msg="PrepareProposal called" height=2
...
```

## Multi-Validator Setup

For a production-like setup with multiple validators:

### 1. Generate Validator Keys

```bash
# Generate keys for each validator
for i in {0..3}; do
  cometbft init --home ~/.cometbft-node$i
done
```

### 2. Create Shared Genesis

```json
{
  "genesis_time": "2024-01-01T00:00:00.000000Z",
  "chain_id": "geth-consensus",
  "validators": [
    {
      "address": "VALIDATOR_0_ADDRESS",
      "pub_key": {"type": "tendermint/PubKeyEd25519", "value": "..."},
      "power": "100"
    },
    {
      "address": "VALIDATOR_1_ADDRESS",
      "pub_key": {"type": "tendermint/PubKeyEd25519", "value": "..."},
      "power": "100"
    },
    // ... more validators
  ],
  "consensus_params": {
    "block": {
      "max_bytes": "22020096",
      "max_gas": "-1"
    },
    "evidence": {
      "max_age_num_blocks": "100000",
      "max_age_duration": "172800000000000"
    },
    "validator": {
      "pub_key_types": ["ed25519"]
    }
  }
}
```

### 3. Configure P2P

In each node's `config.toml`:

```toml
[p2p]
persistent_peers = "node0_id@node0:26656,node1_id@node1:26656,..."
```

### 4. Start All Nodes

Each node needs:
- Its own CometBFT instance
- Its own Geth instance (or shared if using remote Engine API)

```bash
# Node 0
./cometbft-geth --cmt-home ~/.cometbft-node0 --eth-client-url http://geth0:8551

# Node 1
./cometbft-geth --cmt-home ~/.cometbft-node1 --eth-client-url http://geth1:8551
```

## Key Concepts

### Instant Finality

Unlike Ethereum's probabilistic finality (where blocks can be reorged), CometBFT provides **instant finality**:

- Once a block is committed, it cannot be reverted
- No need to wait for confirmations
- Enables immediate transaction finality

This is achieved because:
1. >2/3 of validators must sign each block
2. Validators cannot sign conflicting blocks (slashing)
3. BFT guarantees safety with up to 1/3 Byzantine validators

### Execution vs Consensus Separation

This architecture mirrors Ethereum's post-merge design:

| Layer | Component | Responsibility |
|-------|-----------|----------------|
| Consensus | CometBFT | Block ordering, finality, validator voting |
| Execution | Geth | Transaction execution, state transitions |
| Bridge | Engine API | Communication between layers |

### Payload Lifecycle

```
PrepareProposal           ProcessProposal           FinalizeBlock
     │                          │                        │
     ▼                          ▼                        ▼
┌─────────────┐           ┌──────────┐           ┌──────────────┐
│Build Payload│    ──▶    │ Validate │    ──▶    │   Execute    │
│(Geth builds)│           │ (Check   │           │ (NewPayload) │
│             │           │  hashes) │           │              │
└─────────────┘           └──────────┘           └──────────────┘
```

## Comparison with Ethereum Beacon Chain

| Aspect | This Implementation | Ethereum Beacon Chain |
|--------|--------------------|-----------------------|
| Consensus | CometBFT (Tendermint) | Casper FFG + LMD GHOST |
| Finality | Every block | Every 2 epochs (~12.8 min) |
| Validators | Configurable | 32 ETH stake required |
| Block Time | Configurable | 12 seconds |
| Engine API | V4/V5 (Prague/Osaka) | V4/V5 (Prague/Osaka) |

## Production Considerations

### 1. Validator Security

```bash
# Use a hardware security module (HSM) for validator keys
# Or remote signer like tmkms
```

### 2. Monitoring

Add Prometheus metrics for:
- Block production latency
- Consensus round duration
- Validator participation
- Engine API call latency

### 3. High Availability

- Run multiple sentries per validator
- Use secure validator network topology
- Implement key rotation procedures

### 4. Genesis Coordination

All validators must:
- Start with identical genesis
- Have synchronized Geth state
- Use matching chain configuration

## Next Steps

- **Vote Extensions**: Extend CometBFT votes with custom data (e.g., preconfirmations)
- **State Sync**: Enable fast node synchronization
- **Light Clients**: Support lightweight verification
- **MEV Protection**: Integrate encrypted mempools

## References

- [CometBFT Documentation](https://docs.cometbft.com/)
- [ABCI Specification](https://github.com/cometbft/cometbft/blob/main/spec/abci/abci++_basic_concepts.md)
- [Ethereum Engine API](https://github.com/ethereum/execution-apis/tree/main/src/engine)
- [mev-commit-geth-cl](https://github.com/primev/mev-commit) - Production implementation

## License

MIT
