# PoC V2 Offchain (2.0.2) - Task Plan

## Prerequisite Reading

- The step-by-step rollout spec: `proposals/poc-v2/poc-v2-offchain.md` (Section 3: Version 2.0.2)
- Existing 2.0.1 implementation: `proposals/poc-v2/poc-v2-offchain-2.0.1-todo.md`
- Branch existing implementation: `proposals/poc-v2/poc-v2-changes.md`

## How to Use This Task List

### Workflow

- **Focus on a single task**: implement one task at a time; don’t start future tasks early.
- **Update all usages**: if you rename/add a function/type, update all call sites.
- **Request a review**: once a task is done, mark it as `[?] - Review` and wait for confirmation.
- **Build after each task**: ensure both API and (where relevant) mlnode integration still build.

## Task List (2.0.2 only)

### Section 1: Ingest Peer Results (`POST /v2/poc/results`)

#### 1.1 Update Storage for Peer Results & Address Separation
- **Task**: [x] Update PoC storage to handle peer summaries and partition files by address
- **What**:
  - **Shared Entity**: Use `PoCBatchesGeneratedRecord` for both local and peer results.
  - **File Storage Layout**: Change record path to `records/{blockHeight}/{address}/{timestamp_nanos}.json`.
  - **Logic Update**: Update `StoreGeneratedRecord` (or add `StorePeerRecord`):
    - If "local" (has artifacts): compute hash/amount incrementally (existing logic).
    - If "peer" (no artifacts, provided hash/amount): trust and store the provided `Amount` and `Hash` (update state to match latest peer claim).
  - **Postgres**: Ensure schema supports this (likely fine, just need to allow inserting records with empty nonces but non-zero amount).
- **Where**: `decentralized-api/pocstorage/file_storage.go`, `decentralized-api/pocstorage/postgres_storage.go`
- **Why**: User requested merging local/peer batches into a single entity and separating FS by address.

#### 1.2 Implement `POST /v2/poc/results`
- **Task**: [x] Create the component to receive peer broadcasts
- **What**:
  - New Endpoint: `POST /v2/poc/results`.
  - Body: JSON with `block_height`, `address`, list of `nodes` (`node_id`, `model`, `amount`, `hash`).
  - **Validation**:
    - Verify `X-TA-Signature` matches the `address`.
    - Verify `address` is a currently active validator.
    - Allow only for the *latest known* PoC `block_height`.
  - **Behavior**:
    - Convert input to `PoCBatchesGeneratedRecord` (one per node/result).
    - Call storage to persist (using the "peer" logic from 1.1).
    - **Optimization**: The spec mentions "single POST connection... sequential results". Can use Keep-Alive.
  - **Note**: "Ignore storing signature" for now (per user instruction).
- **Where**:
  - `decentralized-api/internal/server/public/post_poc_results_v2_handler.go` (new)
  - `decentralized-api/internal/server/public/server.go` (route)
- **Why**: This is how we hear from the network.

### Section 2: Broadcast Results (Gossip)

#### 2.1 FD Limit Management
- **Task**: [x] Configure system FD limits for high peer counts
- **What**:
  - When epoch data is cached, check `RLIMIT_NOFILE`.
  - Attempt to raise soft limit to `5 * validator_count`.
  - Expose the effective limit in `GET /v1/versions` for debugging.
- **Where**: `decentralized-api/internal/epoch_group_cache.go`, `decentralized-api/internal/system_limits.go`, `decentralized-api/internal/server/public/app_info_handlers.go`
- **Why**: Broadcasting to thousands of peers requires many open sockets.

#### 2.2 Result Broadcaster Component
- **Task**: [x] Implement the broadcasting engine
- **What**:
  - **Trigger**: When local `post_poc_batches_generated_v2` successfully processes a batch.
  - **Action**: Broadcast summary to **all other active participants**.
- **Where**: `decentralized-api/internal/poc/broadcaster_v2.go` (new)
- **Why**: We must propagate our work to the network.
- **Completed Subtasks**:
  - [x] Create `decentralized-api/internal/poc/broadcaster_v2.go`.
  - [x] Integrate into `decentralized-api/internal/server/mlnode/post_poc_batches_generated_v2_handler.go`.
  - [x] Use same signature scheme as payload responses (keyring-backed `AccountSigner` + `calculations.Sign`).

#### 2.3 Optimization: Batching & Aggregation
- **Task**: [x] Implement batching (100ms buffer) and aggregation
- **What**:
  - Refactor `ResultBroadcaster` to be a background worker.
  - Buffer incoming records and flush every 100ms.
  - Aggregate updates (keep latest `amount`/`hash` per `node_id`).
  - Send single payload with multiple nodes to peers.
- **Where**: `decentralized-api/internal/poc/broadcaster_v2.go`

#### 2.4 Optimization: Duration Logic
- **Task**: [x] Continuous Broadcasting
- **What**:
  - Ensure broadcaster continues running/heartbeating.
  - (Implemented via background worker in `Start` method).
- **Where**: `decentralized-api/internal/poc/broadcaster_v2.go`

### Section 3: Visualization

#### 3.1 Update `GET /v2/poc/results`
- **Task**: [x] Show peer results in debug endpoint
- **What**:
  - Extend the existing `GET` handler to include peer data.
  - The storage `ListGeneratedRecords` should now naturally return peer records (since they are in the same table/fs layout).
  - Group by `address` in the response.
- **Where**: `decentralized-api/internal/server/public/get_poc_results_v2_handler.go`
- **Why**: To verify network convergence.
- **Notes**: Completed via Section 1.1 storage unification. Existing handler logic (`aggregatePoCResultsV2Participants`) inherently supports multiple participants.

### Section 4: Operational Validation

#### 4.1 Integration Test / Validation Script
- **Task**: [x] Verify network convergence
- **What**: Script or manual test steps to:
  1. Launch multiple API nodes.
  2. Run PoC.
  3. Query `GET /v2/poc/results` on all nodes.
  4. Assert consistency.
- **Completed**: Implemented `decentralized-api/internal/poc/network_convergence_test.go` which simulates a multi-node network (Alice/Bob) and verifies successful broadcast, aggregation, and receipt of results.

### Section 5: Optimization & Cleanup (Post-Verification)

#### 5.1 Optimization: Shared Participant Cache
- **Task**: [x] Refactor and move Participant Cache to Internal
- **What**:
  - **Issue**: Need efficient caching for both active set and participant details without circular dependencies or duplication.
  - **Solution**:
    -   Split caching into `internal/epoch_group_cache.go` (Active Set) and `internal/participants_list_cache.go` (All Participants).
    -   Internalize cache creation in `Server` and `Broadcaster` to avoid dependency injection complexity.
  - **Usage**:
    -   `ResultBroadcaster`: Intersect Active Set (EpochCache) with Connection Info (ListCache).
    -   `POST /results`: Verify self is active (EpochCache) and get sender PubKey (ListCache).
    -   Cache participant pubkeys and grantee pubkeys together per epoch; use lazy fetch on demand.
- **Where**: `decentralized-api/internal/participants_list_cache.go` (new), `decentralized-api/internal/epoch_group_cache.go`

#### 5.2 Optimization: Batch Storage for Peer Results
- **Task**: [x] Implement Batch Storage in `PoCStorage`
- **What**:
  - **Issue**: `POST /results` iterates nodes and calls `StoreGeneratedRecord` sequentially (N txs per request). High overhead/contention.
  - **Optimization**: Implement `StoreGeneratedRecordsBatch`.
  - **Logic**:
    1. Sort inputs by `node_id` (prevent deadlocks).
    2. Single `SELECT ... FOR UPDATE` (bulk lock).
    3. Calculate diffs in memory.
    4. Single Bulk `INSERT`.
- **Where**: `decentralized-api/pocstorage/storage.go`, `decentralized-api/pocstorage/postgres_storage.go`, `decentralized-api/internal/server/public/post_poc_results_v2_handler.go`

#### 5.3 Optimization: Active Validator Verification
- **Task**: [x] Enforce Active Validator Status on Result Ingestion & Broadcast
- **What**:
  - **Inbound (Receiver)**: `POST /results` must check if the **receiving node** (self) is an active validator in the **current epoch**.
    - *Impl*: `post_poc_results_v2_handler.go` uses `EpochGroupDataCache.IsActiveParticipant` for local address.
  - **Outbound (Sender)**: `ResultBroadcaster` must only broadcast to peers that are **active validators**.
    - *Impl*: `broadcaster_v2.go` gets Active Set from `EpochGroupDataCache`, then gets connection info from `ParticipantsListCache`.
  - **Refactor**: Legacy `participant/cache.go` was removed in favor of these internal caches.
- **Where**: `decentralized-api/internal/server/public/post_poc_results_v2_handler.go`, `decentralized-api/internal/poc/broadcaster_v2.go`, `decentralized-api/internal/participants_list_cache.go`
