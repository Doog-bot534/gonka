# Multi-Tree Propagation - Phase 2 Implementation Status

## Overview

Phase 2 implements the full multi-tree propagation system for distributing PoC commit metadata (headers) and proofs across validators. Trees are rebuilt each epoch using weighted participants from the previous epoch, enabling efficient gossip-style propagation.

Tree roots submit on-chain `MsgTreeRootCommit` messages containing per-participant artifact counts observed via propagation. The chain computes consensus from these tree root commits (majority of trees agreeing on >= count), and participants use the agreed count for their `MsgPoCV2StoreCommit`.

**Goal**: Enable off-chain distribution of PoC artifacts through deterministic, weighted propagation trees, with on-chain consensus via tree root commits.

---

## Completed

### 2.1 Tree Building

| Item | Status | Details |
|------|--------|---------|
| `BuildTrees()` function | Done | `poc/propagation/trees.go:30` |
| `BuildTreesWithWeights()` function | Done | `poc/propagation/trees.go:40` |
| Weighted deterministic shuffle | Done | Higher weights → closer to root |
| Tree structure (Node, Tree) | Done | Parent/children/siblings tracking |
| `TreeManager` | Done | `poc/propagation/tree_manager.go` |
| `RebuildTreesForEpoch()` | Done | Fetches weights from previous epoch |
| Tree rebuild on epoch start | Done | `new_block_dispatcher.go:363-389` |

**Tree Structure**:
- **Node**: Address, Index, Parent, Children, Siblings
- **Tree**: Index, Shuffled addresses, Fanout, Nodes map, Root
- Multiple trees per epoch (configurable, typically 3)
- Fanout per node (configurable, typically 3)
- Deterministic shuffle using `sha256(blockHash + treeIndex)` as seed

### 2.2 Data Types

| Type | Status | Details |
|------|--------|---------|
| `BundleHeader` | Done | `poc/propagation/bundle.go:17` |
| `ProofItem` | Done | `poc/propagation/storage.go:14` |
| `ArrivalInfo` | Done | Time + Count tracking |
| Header signing/verification | Done | Ed25519 signatures |

**BundleHeader Fields**:
```go
BundleID    [32]byte  // sha256(participant + pocHeight + rootHash + count + version)
Participant string    // Validator address
PubKey      string    // Base64-encoded Ed25519 public key
PocHeight   int64     // PoC epoch height
RootHash    []byte    // Merkle root of artifacts
Count       uint32    // Number of artifacts
CreatedAt   int64     // Unix timestamp
Signature   []byte    // Ed25519 signature
```

**ProofItem Fields**:
```go
LeafIndex   uint32     // Index in merkle tree
NonceValue  int32      // Nonce for artifact
VectorBytes string     // Base64-encoded vector data
Proof       []string   // Merkle proof path (hex-encoded hashes)
```

### 2.3 Bundler (Publisher)

| Item | Status | Details |
|------|--------|---------|
| `Bundler` struct | Done | `poc/propagation/bundler.go:14` |
| `Publish()` - header publishing | Done | Signs and sends headers |
| `PublishProofs()` - proof publishing | Done | Sends proofs to network |
| `sendHeader()` - multi-tree dispatch | Done | Sends to all tree roots |
| `sendProofs()` - multi-tree dispatch | Done | Sends proofs to all tree roots |
| Root broadcast optimization | Done | If root, send directly to children |
| Own arrival tracking | Done | `StoreOwnArrival()` |

**Publishing Flow**:
1. **Generate Bundle**:
   - Create `BundleID` from participant + pocHeight + rootHash + count
   - Sign header with Ed25519 private key
   - Store in local cache

2. **Send to Tree Roots**:
   - For each tree, send header to root (with tree index)
   - Skip if self is root (will broadcast via receiver)
   - Parallel dispatch to all roots

3. **Root Broadcast**:
   - If self is root in any tree, broadcast to children in that tree
   - Each child receives with correct tree index

### 2.4 Receiver (Forwarder)

| Item | Status | Details |
|------|--------|---------|
| `Receiver` struct | Done | `poc/propagation/receiver.go:15` |
| `OnHeader()` - receive headers | Done | Verify, store, forward |
| `OnProofs()` - receive proofs | Done | Store, forward |
| Signature verification | Done | Ed25519 verification |
| Duplicate detection | Done | `processedHeaders`, `processedProofs` |
| `forwardHeaderAllTrees()` | Done | Forward to children in all trees |
| `forwardProofsAllTrees()` | Done | Forward proofs to children |
| First-arrival time recording | Done | Records when header first received |

**Receiving Flow**:
1. **Verify Message**:
   - Check signature against participant's public key
   - Validate BundleID for headers
   - Check for duplicates (already processed)

2. **Store Locally**:
   - Store header/proofs in cache
   - Record first-arrival time for headers
   - Mark as processed

3. **Forward to Children**:
   - For each tree where self has children
   - Send to all children in that tree
   - Parallel dispatch
   - Skip sender for proofs (avoid loops)

### 2.5 Transport Layer

| Item | Status | Details |
|------|--------|---------|
| `HTTPTransport` | Done | `poc/propagation/http_transport.go:17` |
| `SendHeader()` | Done | POST to `/v1/propagation/header` |
| `SendProofs()` | Done | POST to `/v1/propagation/proofs` |
| Local delivery optimization | Done | Calls handler directly for self |
| HTTP handlers | Done | `HandleHeaderHTTP`, `HandleProofsHTTP` |
| Timeout configuration | Done | 10s for headers, 30s for proofs |
| Participant URL mapping | Done | `SetParticipantURLs()` |

**HTTP Endpoints**:
- `POST /v1/propagation/header` - Receive bundle headers
- `POST /v1/propagation/proofs` - Receive proofs

**Message Format**:
```json
// HeaderMessage
{
  "tree_idx": 0,
  "header": { /* BundleHeader */ },
  "from": "validator_address"
}

// ProofsMessage
{
  "bundle_id": "hex_encoded_32_bytes",
  "proofs": [ /* ProofItem[] */ ],
  "from": "validator_address"
}
```

### 2.6 Storage Layer

| Item | Status | Details |
|------|--------|---------|
| `BundleStorage` interface | Done | `poc/propagation/storage.go:21` |
| `Cache` wrapper | Done | `poc/propagation/cache.go` |
| Postgres storage | Done | `postgres_bundle_storage.go` |
| File storage | Done | `file_bundle_storage.go` |
| Hybrid storage | Done | `hybrid_bundle_storage.go` |
| Header storage | Done | `StoreHeader()`, `GetHeader()` |
| Proof storage | Done | `StoreProofs()`, `GetProofs()` |
| First-arrival storage | Done | `StoreFirstArrival()`, `GetFirstArrival()` |

### 2.7 Integration

| Item | Status | Details |
|------|--------|---------|
| Tree rebuild on epoch start | Done | `new_block_dispatcher.go:363-389` |
| `SetTrees()` on receiver | Done | Updates trees for new epoch |
| `SetTrees()` on bundler | Done | Updates trees for new epoch |
| `ClearProcessedState()` | Done | Clears duplicate tracking on epoch change |
| Participant URL population | Done | Maps addresses to HTTP endpoints |
| Header publishing in CommitWorker | Done | `commit_worker.go:181` |
| Tree root commit submission | Done | `commit_worker.go:225` |
| On-chain consensus query | Done | `query_poc_consensus.go` |

### 2.8 Tree Root Commit Consensus

Replaces the previous off-chain observation-based consensus (removed).

| Item | Status | Details |
|------|--------|---------|
| `MsgTreeRootCommit` handler | Done | `keeper/msg_server_tree_root_commit.go` |
| `PoCConsensus` query | Done | `keeper/query_poc_consensus.go` |
| `TreeRootCommits` collection | Done | `keeper/keeper.go` |
| `PoCCommitPhase` epoch stage | Done | `types/epoch_context.go` |
| `maybeSubmitTreeRootCommits()` | Done | `commit_worker.go:225` |
| `maybeSubmitConsensusCommit()` | Done | `commit_worker.go:285` |
| Fallback to local count | Done | When propagation disabled or no peers |

**On-Chain Consensus Algorithm** (`query_poc_consensus.go`):

For each participant, find the highest count where a majority of tree roots reported >= that count:

```
Input: all MsgTreeRootCommit entries for the PoC height
totalTrees = number of tree root commits
requiredAgreement = totalTrees / 2 + 1

For each participant:
  1. Collect all counts reported by different tree roots
  2. Get unique counts, sorted ascending
  3. For each target count (low to high):
     - Count how many trees reported >= that count for this participant
     - If treesAgreeing >= requiredAgreement, record as agreed count
  4. The highest count with majority agreement wins

Output: map[participant] -> {agreedCount, totalValidators, agreeingCount}
```

---

## File Summary

### Files Modified/Created

```
decentralized-api/poc/propagation/
  - trees.go (4.11 KB)                    # Tree building with weighted shuffle
  - tree_manager.go (2.78 KB)             # Epoch-based tree management
  - bundler.go (10.71 KB)                 # Message publishing
  - receiver.go (10.55 KB)                # Message receiving and forwarding
  - http_transport.go (8.03 KB)           # HTTP transport layer
  - transport.go (431 B)                  # Transport interfaces
  - bundle.go (3.62 KB)                   # BundleHeader type and signing
  - types.go (3.86 KB)                    # FirstArrivalObservation type
  - storage.go (1.52 KB)                  # Storage interfaces
  - cache.go (1.84 KB)                    # Cache wrapper
  - postgres_bundle_storage.go (10.92 KB) # Postgres implementation
  - file_bundle_storage.go (11.41 KB)     # File-based implementation
  - hybrid_bundle_storage.go (6.33 KB)    # Hybrid storage
  - consensus.go (3.09 KB)                # Consensus calculation
  - factory.go (2.37 KB)                  # Component factory

decentralized-api/internal/event_listener/new_block_dispatcher.go
  - Tree rebuild on epoch start

decentralized-api/poc/commit_worker.go
  - maybePublishHeaders(): publish headers via propagation trees
  - maybeSubmitTreeRootCommits(): submit MsgTreeRootCommit when root
  - maybeSubmitConsensusCommit(): query PoCConsensus, commit at agreed count

inference-chain/x/inference/keeper/
  - msg_server_tree_root_commit.go: MsgTreeRootCommit handler
  - query_poc_consensus.go: PoCConsensus query using tree root commits
  - keeper.go: TreeRootCommits collection (Map[Pair[height,treeIndex], TreeRootCommit])

inference-chain/x/inference/types/
  - epoch_context.go: StartOfPoCCommit(), EndOfPoCCommit(), PoCCommitPhase
  - epoch_stages.go: PoCCommitStart/PoCCommitEnd in EpochStages
```

---

## Behavior Summary

### Tree Building

**When**: At start of each PoC epoch (`IsStartOfPocStage`)

**Process**:
1. `TreeManager.RebuildTreesForEpoch()` called with current epoch index
2. Fetches validation weights from **previous epoch** via `GetEpochGroupData()`
3. Creates weighted participants: `WeightedParticipant{Address, Weight}`
4. For each tree (0 to N-1):
   - Generates seed: `sha256(blockHash + treeIndex)`
   - Performs weighted deterministic shuffle (higher weight → earlier in list)
   - Builds tree structure with configured fanout
5. Updates `Receiver.SetTrees()` and `Bundler.SetTrees()`
6. Clears processed state to accept new messages
7. Populates participant URL mappings

**Weighted Shuffle**:
- Base score = weight
- Random component = `rand(seed) * weight * 0.3` (30% randomness)
- Total score = base + random
- Sort by score descending
- Same seed produces identical order on all nodes

### Header Propagation

**Publisher Flow** (Bundler):
1. Participant completes PoC artifacts
2. Calls `bundler.Publish(pocHeight, participant, pubKey, count, rootHash)`
3. Creates `BundleHeader` with bundleID and signature
4. Stores own arrival time
5. Sends to all tree roots (if not root)
6. If root in any tree, sends directly to children

**Forwarder Flow** (Receiver):
1. Receives `OnHeader(header, treeIdx, from)` via HTTP or local
2. Verifies signature against participant's public key
3. Validates bundleID matches computed ID
4. Checks for duplicates (skip if already processed)
5. Stores header in cache
6. Records first-arrival time for this participant
7. For each tree where self has children:
   - Send header to all children with tree index
8. Parallel dispatch with error logging

**Result**: All validators receive all headers within seconds

### Proof Propagation

**Publisher Flow**:
1. Participant generates merkle proofs for artifacts
2. Calls `bundler.PublishProofs(bundleID, proofs)`
3. Sends proofs to all tree roots
4. If root, sends directly to children

**Forwarder Flow**:
1. Receives `OnProofs(bundleID, proofs, from)` via HTTP
2. Checks for duplicates (skip if already processed)
3. Stores proofs in cache (async)
4. For each tree where self has children:
   - Send proofs to children (skip sender)
   - Track forwarded recipients to avoid duplicates
5. Parallel dispatch

### Tree Root Commit Submission

**When**: During exchange window (PoCGenerate + PoCGenerateWindDown + IsPoCExchangeWindow)

Tree roots collect first-arrival data from headers propagated through their tree and submit it on-chain.

**Flow** (`maybeSubmitTreeRootCommits`):
1. Check if this node is root in any propagation tree (`TreeManager.IsRootInAnyTree`)
2. Get all first arrivals from propagation cache for this PoC height
3. Wait until own arrival is present (ensures own artifacts are generated)
4. Build `TreeRootCommitEntry` list: participant + count for each arrival with count > 0
5. For each tree where this node is root, submit `MsgTreeRootCommit` with entries
6. Track submitted trees to avoid duplicates

**Chain-Side Validation** (`msg_server_tree_root_commit.go`):
- V2 must be enabled (or migration tracking mode)
- Entries must not be empty
- Tree index must be 0..15
- Must be within PoC exchange window
- One commit per tree index per PoC height (no overwrites)

### Consensus Commit

**When**: During PoCCommitPhase (PoCExchangeDeadline+1 to StartOfPoCValidation-1) or exchange window

**Flow** (`maybeSubmitConsensusCommit`):
1. If propagation disabled or no peers: submit `MsgPoCV2StoreCommit` with local count (fallback)
2. If propagation enabled: query chain's `PoCConsensus` endpoint
3. Find own agreed count from consensus response
4. If agreed count > 0:
   - Get root hash at the agreed count from local artifact store
   - Submit `MsgPoCV2StoreCommit` with agreed count and matching root hash
   - Re-publish proofs at consensus count via propagation if count differs from local
5. If no consensus yet: retry on next tick

---

## Propagation Guarantees

| Property | Guarantee |
|----------|-----------|
| **Determinism** | All nodes build identical trees from same blockHash + epoch data |
| **Redundancy** | Multiple trees provide multiple paths (failure tolerance) |
| **Efficiency** | Fanout limits hops (fanout=3, depth=log₃(N)) |
| **Weighting** | Higher stake validators closer to root (better propagation) |
| **Deduplication** | Receivers track processed IDs (no duplicate processing) |
| **Integrity** | Ed25519 signatures prevent tampering |
| **First-arrival** | Timing preserved at first reception (not forwarding time) |
| **On-chain consensus** | Tree root commits provide verifiable, deterministic consensus |

---

## Configuration

| Parameter | Default | Description |
|-----------|---------|-------------|
| `numTrees` | 3 | Number of propagation trees per epoch |
| `fanout` | 3 | Children per node in each tree |

**Example**: 100 validators, 3 trees, fanout 3
- Tree depth: ~5 hops
- Propagation time: ~5 * 100ms = 500ms
- Redundancy: 3x paths
- On-chain: 3 `MsgTreeRootCommit` messages per epoch (one per tree root)

---

## Epoch Phase Timeline

```
PoCGenerate         → Artifacts generated, headers published via trees
PoCGenerateWindDown → Tree roots submit MsgTreeRootCommit on-chain
PoCCommit (NEW)     → Nodes query PoCConsensus, submit MsgPoCV2StoreCommit at agreed count
PoCValidate         → Weight distribution, cross-validation of artifacts
Inference           → Normal inference operations
```

`PoCCommitPhase` spans from `PoCExchangeDeadline + 1` to `StartOfPoCValidation - 1`.

---

## Related Documents

- `multi-tree-propagation.md` - Architecture overview
- `multi-tree-propagation-phase1.md` - Phase 1 (core tree implementation)
- `multi-tree-propagation-analysis.md` - Security analysis and simulation results
- `offchain.md` - PoC V2 off-chain artifacts proposal
