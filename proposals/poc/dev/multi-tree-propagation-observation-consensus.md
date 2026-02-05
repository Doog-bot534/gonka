# Multi-Tree Propagation - Observation & Consensus Layer

## Status

**Implemented** - Unit tests and integration tests pass.

## Overview

Builds on the multi-tree propagation system (see `multi-tree-propagation.md` and `multi-tree-propagation-phase1.md`) to add a consensus mechanism. Instead of each node submitting its own local artifact count on-chain, validators first exchange observations of what they saw, then compute a majority-agreed count and submit that.

---

## Problem

In the base propagation system, each participant publishes headers with their artifact count and then submits that count on-chain independently. If different validators see different counts (due to propagation delays or network issues), there's no agreement on the canonical count. A node could also lie about its count with no way for others to contest.

## Solution

Three new concepts layered on top of header propagation:

1. **First Arrival Tracking** - Each node records the timestamp and count from the first header it receives per participant per PoC height.

2. **Observation Broadcasting** - At a computed block height, each validator signs and broadcasts its full arrival map to all peers via the overlay trees.

3. **Consensus Calculation** - For each participant, find the highest count where a majority of validators saw at least that count before a deadline.

On-chain commits now use the consensus-agreed count.

---

## Data Structures

### ArrivalInfo

Recorded when a node first receives a header from a participant:

```go
type ArrivalInfo struct {
    Time  int64   // Unix timestamp (ms) when first heard
    Count uint32  // Count in first header received
}
```

Once stored, these never change. The node also stores its own arrival when it publishes its own header.

### FirstArrivalObservation

Signed attestation of what a validator saw:

```go
type FirstArrivalObservation struct {
    ValidatorAddress string                 // Who created this observation
    PocHeight        int64                  // PoC stage
    Arrivals         map[string]ArrivalInfo // participant -> first arrival
    Timestamp        int64                  // When this observation was created
    Signature        []byte                 // ed25519 signature
}
```

Signed over a canonical byte serialization of all fields (participants sorted alphabetically). Verified on receipt using the validator's public key from chain state.

---

## Consensus Algorithm

`CalculateConsensus()` in `consensus.go`:

```
Input:
  - observations: []FirstArrivalObservation (from all validators)
  - bundles: []BundleHeader (published headers for this height)
  - deadline: int64 (timestamp cutoff)

For each participant:
  1. Get all published headers, sorted by count ascending
  2. For each header count (low to high):
     - Count how many validators saw >= that count before the deadline
     - If count >= majority (n/2 + 1), record it as the agreed count
  3. The highest count that still has majority agreement wins

Output: map[participant]ConsensusResult{AgreedCount, TotalValidators, AgreeingCount}
```

The agreed count is always <= the actual published count, since it requires majority attestation.

### Deadline Derivation

When computing consensus for on-chain commits, the deadline is derived from the observations themselves: the minimum `Timestamp` across all received observations. This avoids the block-height-to-wallclock-time conversion problem.

---

## Commit Flow

Previously: `maybeSubmitCommit()` published headers and submitted on-chain commits in the same method.

Now split into two phases:

### Phase 1: Header Publishing (during PoCGenerate / PoCGenerateWindDown)

`maybePublishHeaders()`:
- Publishes headers and proofs via propagation trees
- Stores own arrival time
- Tracks last published state to avoid redundant publishes
- Runs every tick during PoCGenerate phase

### Phase 2: Consensus Commit (during exchange window)

`maybeSubmitConsensusCommit()`:
- Queries `ConsensusCalculator` for the agreed count for self
- If consensus > 0, submits `MsgPoCV2StoreCommit` with the agreed count
- If no consensus yet, logs and retries on next tick
- Once submitted, marks height as done (no duplicate submissions)
- If propagation is disabled, skips committing entirely

---

## Observation Broadcasting Timing

Computed in `new_block_dispatcher.go`:

```go
observationBroadcastHeight := PoCExchangeDeadline - DefaultObservationBuffer(10)
```

Capped at `EndOfPoCGeneration - 1` (must fire before generation phase ends) and floored at `PocStartBlockHeight + 1` (must be after generation starts).

In production (17280 blocks/epoch), this gives ~50 seconds between observation broadcast and the exchange deadline.

### Broadcast Path

Each validator:
1. Collects all first arrivals from cache
2. Signs the observation
3. Stores own observation locally
4. Sends to all tree roots (except self)
5. Sends to own children in trees where it is root

Receivers verify the signature, store the observation, and forward to their children.

---

## Storage

### New BundleStorage Interface Methods

```go
StoreFirstArrival(ctx, participant, pocHeight, arrivalTime, count) error
GetFirstArrival(ctx, participant, pocHeight) (ArrivalInfo, error)
GetAllFirstArrivals(ctx, pocHeight) (map[string]ArrivalInfo, error)
StoreObservation(ctx, obs FirstArrivalObservation) error
GetObservations(ctx, pocHeight) ([]FirstArrivalObservation, error)
```

Implemented in all three backends:

- **FileBundleStorage**: JSON files (`arrivals.json`, `observations.json`), atomic writes via temp+rename
- **PostgresBundleStorage**: Two new tables (below), in-memory cache backed by DB
- **HybridBundleStorage**: Tries Postgres first, falls back to file

### Postgres Schema

```sql
CREATE TABLE poc_first_arrivals (
    instance TEXT NOT NULL,
    participant TEXT NOT NULL,
    poc_height BIGINT NOT NULL,
    arrival_time BIGINT NOT NULL,
    arrival_count INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (instance, participant, poc_height)
);

CREATE TABLE poc_observations (
    instance TEXT NOT NULL,
    validator_address TEXT NOT NULL,
    poc_height BIGINT NOT NULL,
    arrivals JSONB NOT NULL,
    timestamp BIGINT NOT NULL,
    signature BYTEA NOT NULL,
    PRIMARY KEY (instance, validator_address, poc_height)
);
```

---

## HTTP Endpoints

### Protocol (node-to-node)

| Method | Path | Purpose |
|--------|------|---------|
| `POST` | `/v1/propagation/observation` | Receive observation from peer |

### Diagnostic (query only)

| Method | Path | Purpose |
|--------|------|---------|
| `GET` | `/v1/propagation/first-arrivals/:poc_height` | Query local arrival records |
| `GET` | `/v1/propagation/observations/:poc_height` | Query stored observations |
| `GET` | `/v1/propagation/consensus/:poc_height?deadline=X` | Calculate consensus on demand |

The diagnostic endpoints are not used by the protocol. They exist for integration testing and runtime inspection.

---

## Transport

New `ObservationSender` interface alongside existing `Sender`:

```go
type ObservationSender interface {
    SendObservation(to string, obs FirstArrivalObservation) error
}
```

Implemented by `HTTPTransport`, `MockTransport`, and `PerParticipantSender`.

The `ReceiverHandler` interface gains `OnObservation()`:

```go
type ReceiverHandler interface {
    OnHeader(h BundleHeader, treeIdx int, from string) error
    OnProofs(bundleID [32]byte, proofs []ProofItem, from string) error
    OnObservation(obs FirstArrivalObservation, from string) error
}
```

### Deduplication

The receiver tracks three separate processed-message maps:
- `processedHeaders` - by bundle ID
- `processedProofs` - by bundle ID
- `processedObservations` - by `SHA256(validatorAddress || pocHeight)`

All cleared at epoch boundary via `ClearProcessedState()`.

---

## Tests

### Unit Tests

`consensus_test.go` - Tests for `CalculateConsensus()`:
- Single validator trivially agrees with itself
- Three validators with majority agreement
- Mixed arrival times with deadline filtering
- No arrivals yields zero consensus
- Multiple count levels (selects highest with majority)

### Integration Tests

`PropagationTests.kt` - Two new test cases:

**First arrival time tracking**: 3-node cluster, verifies all nodes record arrival times for other participants, verifies times are static (don't change on re-query).

**Consensus calculation**: 3-node cluster, verifies observations are broadcast and received, consensus produces positive counts bounded by actual artifact counts, results are present across all nodes.

---

## Known Limitations

**No canonical observation set**: Different validators may compute consensus over different subsets of observations if some observations arrive late. In practice, the 50-second window in production is generous for small payloads to converge. For guaranteed correctness, observation commitments would need to be anchored on-chain (see below).

**No fallback**: If consensus doesn't form before the exchange window closes, the participant misses that epoch. There's no fallback to submitting the local count.

### Future: On-Chain Observation Anchoring

To guarantee all validators compute over the same observation set, each validator could post a hash of their observation on-chain during the exchange window. The chain would define the canonical set. Validators would then compute consensus using exactly the observations whose hashes appeared on-chain. The bulk observation data stays off-chain, fetched from peers using the hash as key.

---

## Related Documents

- `multi-tree-propagation.md` - Architecture overview
- `multi-tree-propagation-phase1.md` - Phase 1 implementation (trees, headers, transport)
- `offchain.md` - Off-chain PoC artifacts proposal
