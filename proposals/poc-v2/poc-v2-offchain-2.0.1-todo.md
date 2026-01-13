# PoC V2 Offchain (2.0.1) - Task Plan

## Prerequisite Reading

- The step-by-step rollout spec: `proposals/poc-v2/poc-v2-offchain.md`
- Existing PoC (current production flow):
  - PoC start orchestration: `decentralized-api/internal/event_listener/new_block_dispatcher.go` (`(*OnNewBlockDispatcher).handlePhaseTransitions`)
  - Broker PoC start command: `decentralized-api/broker/state_commands.go` (`StartPocCommand.Execute`)
  - ML node PoC start call: `decentralized-api/broker/node_worker_commands.go` (`StartPoCNodeCommand.Execute`)
  - ML node client PoC init: `decentralized-api/mlnodeclient/poc.go` (`Client.InitGenerate`, `BuildInitDto`)
  - ML node → API callback (generated batches): `decentralized-api/internal/server/mlnode/post_generated_batches_handler.go` (`(*Server).postGeneratedBatches`)
- Existing API “version” endpoint (we will extend it): `decentralized-api/internal/server/public/app_info_handlers.go` (`(*Server).getVersions`)
- Existing storage backends we will mirror/reuse patterns from (no SQLite): `decentralized-api/payloadstorage/*` (notably `payloadstorage/file_storage.go`, `payloadstorage/postgres_storage.go`, optionally `payloadstorage/hybrid_storage.go` and `payloadstorage/managed_storage.go`)

## How to Use This Task List

### Workflow

- **Focus on a single task**: implement one task at a time; don’t start future tasks early.
- **Update all usages**: if you rename/add a function/type, update all call sites.
- **Request a review**: once a task is done, mark it as `[?] - Review` and wait for confirmation.
- **Build after each task**: ensure both API and (where relevant) mlnode integration still build.

### Build & Test Commands

- **Build API Node**: from repo root, run `make api-local-build`
- **Build Inference Chain**: from repo root, run `make node-local-build` (should remain unaffected, but keep it green)
- **API tests**: from repo root, run `make api-test`

### Status Indicators

- `[ ]` Not Started
- `[~]` In Progress
- `[?]` Review
- `[x]` Finished

### Task Format

Each task includes:

- **What**
- **Where**
- **Why**
- **Similar to** (when we add a new flow mirroring an existing one)

## Task List (2.0.1 only)

### Section 1: API Protocol Versioning (Discovery + Header)

#### 1.1 Add `poc_api_version` field to `GET /v1/versions`

- **Task**: [x] Extend versions response with a dedicated `poc_api_version` field
- **What**: Add a new JSON field (e.g. `"poc_api_version": "2.0.1"`) to the `/v1/versions` response so peers can discover PoC offchain compatibility without overloading existing fields.
- **Where**: `decentralized-api/internal/server/public/app_info_handlers.go` (`(*Server).getVersions`)
- **Why**: Supports rollout gating described in `proposals/poc-v2/poc-v2-offchain.md` (via `X-API-Version`).
- **Result**: Added top-level `poc_api_version` to `/v1/versions` response (constant `pocApiVersion`) in `decentralized-api/internal/server/public/app_info_handlers.go`.

#### 1.2 Add `X-API-Version` handling for new v2 endpoints (soft gating)

- **Task**: [x] Validate/record `X-API-Version` for the new `/v2/*` PoC endpoints
- **What**: For `/v2/poc/*` endpoints, enforce that the caller provides `X-API-Version: 2.0.1` (or log/warn for now, depending on rollout preference).
- **Where**:
  - Route registration: `decentralized-api/internal/server/public/server.go` (`NewServer` router setup)
  - New middleware (recommended): `decentralized-api/internal/server/middleware/*` (add a PoC-version-gating middleware)
- **Why**: Prevents mixed-version peers from calling incompatible endpoints.
- **Similar to**: Request validation patterns already used in `decentralized-api/internal/server/public/post_chat_handler.go` (timestamp/signature checks), but this is a simpler header gate.
- **Result**: Added `utils.XApiVersionHeader` in `decentralized-api/utils/api_headers.go`, added `middleware.RequireApiVersion(...)` in `decentralized-api/internal/server/middleware/api_version.go`, and registered a gated `/v2/` group in `decentralized-api/internal/server/public/server.go`.

### Section 2: PoC V2 Storage Layer (same backends as payload storage: files/Postgres)

#### 2.1 Introduce `pocstorage` interface + file/Postgres implementations

- **Task**: [x] Add a dedicated storage module for PoC V2 data
- **What**:
  - Define a storage interface for PoC runs and PoC batch results (persisted per `block_height`).
  - Implement file-backed storage and Postgres-backed storage (schema + CRUD) using the same configuration patterns as inference payload storage.
- **Where** (new module):
  - `decentralized-api/pocstorage/storage.go` (new interface)
  - `decentralized-api/pocstorage/file_storage.go` (new file implementation)
  - `decentralized-api/pocstorage/postgres_storage.go` (new Postgres implementation)
- **Why**: `proposals/poc-v2/poc-v2-offchain.md` requires “store PoC in local DB” and “store each result as separate record”.
- **Similar to**:
  - Interface + managed wrapper patterns: `decentralized-api/payloadstorage/storage.go`, `decentralized-api/payloadstorage/managed_storage.go`
  - File backend: `decentralized-api/payloadstorage/file_storage.go`
  - Postgres backend: `decentralized-api/payloadstorage/postgres_storage.go`
  - Optional hybrid selection: `decentralized-api/payloadstorage/hybrid_storage.go`
- **Result**: Added `decentralized-api/pocstorage/` with `PoCStorage` interface + minimal functional file and Postgres implementations (file layout and Postgres libpq-env connection pattern mirror `payloadstorage`).

#### 2.2 Define PoC V2 schema (tables + indexes) for Postgres backend

- **Task**: [x] Add tables for PoC runs and generated nonce batches (v2 format)
- **What** (minimum recommended tables):
  - `poc_runs`: keyed by `block_height`, fields: `epoch_length`, `block_hash`, `block_time`, `duration`, `frequency`, `interrupted_time`
  - `poc_batches_generated`: keyed by `(block_height, address, node_id, received_at)` (or a UUID), fields: `model`, `amount`, `hash`, `time_since_block`, `nonces` (serialized), plus any signature/metadata needed later
- **Where**: `decentralized-api/pocstorage/postgres_storage.go` (schema DDL + migrations/ensureSchema)
- **Why**: Enables the `/v2/poc/results` debug endpoint and correctness checks.
- **Result**: Implemented `poc_runs` and a partitioned `poc_batches_generated` table (partitioned by `block_height`) with per-block partitions created on-demand via `(*pocstorage.PostgresStorage).ensurePartition`, plus indexes for `(block_height, address)`, `(block_height, node_id)`, `(block_height, model)`.

#### 2.3 PoC V2 hashing + “amount” computation

- **Task**: [x] Define how `amount` and `hash` are computed for the new Nonces format
- **What**:
  - Define and implement deterministic hashing over “all nonces received so far” per `(block_height, address, node_id, model)`.
  - Define `amount` as “current count of nonces” (per spec).
  - Add persisted per-mlnode rolling state so we do **not** reread all previous batches on each ingest.
- **Where**:
  - Hashing helpers: `decentralized-api/pocstorage/hash.go` (`computeBatchHash`, `computeRollingHash`)
  - Interface contract: `decentralized-api/pocstorage/storage.go` (`PoCStorage.StoreGeneratedRecord` computes amount/hash)
  - File backend state: `decentralized-api/pocstorage/file_storage.go` (`state/` folder per block height)
  - Postgres backend state: `decentralized-api/pocstorage/postgres_storage.go` (`poc_mlnode_state` table + transactional update)
- **Why**: The spec requires storing `amount` + `hash` per received result record.
- **Similar to**: Hashing utilities in `decentralized-api/payloadstorage/*` and `decentralized-api/utils/*` (e.g., SHA helpers).
- **Result**: Implemented rolling per-mlnode aggregate state keyed by `(block_height, address, node_id, model)`. Each new emission updates state (amount/hash) and stores the emission record with the computed snapshot; Postgres uses a transaction with `SELECT ... FOR UPDATE` + upsert state + insert record.

### Section 3: New V2 API Endpoints (Start + Results)

#### 3.1 Add `POST /v2/poc/start`

- **Task**: [x] Implement the v2 PoC start endpoint
- **What**:
  - Accept request body fields from `proposals/poc-v2/poc-v2-offchain.md` (block_height, epoch_length, block_hash, block_time, duration, frequency, batch_size, params.model/seq_len/k_dim).
  - Persist a new PoC run record; if an old PoC is active, mark interruptions as described (set `interrupted_time`).
    - Legacy/v1 PoC detection is broker-controlled (node PoC statuses).
    - Prior v2 PoC detection is storage-controlled (latest stored v2 run still active).
  - Trigger ML node generation for all local ML nodes (see Section 4).
- **Where**:
  - Route registration: `decentralized-api/internal/server/public/server.go`
  - Handler implementation (new): `decentralized-api/internal/server/public/post_poc_start_v2_handler.go` (recommended file name)
  - Method signature: `(*public.Server).postPoCStartV2(...)` (new function)
- **Why**: This is the entrypoint for 2.0.1 testing rollout.
- **Result**: Added `/v2/poc/start` route on the gated `/v2/` group in `decentralized-api/internal/server/public/server.go` and implemented `(*Server).postPoCStartV2` in `decentralized-api/internal/server/public/post_poc_start_v2_handler.go` to validate/store `pocstorage.PoCRun` and mark interruptions when either (a) the broker indicates legacy/v1 PoC activity, or (b) a previous stored v2 run is still active (the previous v2 run is then marked interrupted). Added `pocstorage.NewPoCStorage` factory and wired `pocStorage` into `public.Server` from `decentralized-api/main.go`.

#### 3.2 Add temporary signature gate for `POST /v2/poc/start`

- **Task**: [x] Enforce the temporary “hardcoded pubkey via X-TA-Signature” gate
- **What**: Validate that the request is authorized by verifying `X-TA-Signature` against a hardcoded public key (temporary).
- **Where**:
  - New helper (recommended): `decentralized-api/internal/server/public/poc_v2_auth.go`
  - Hooked from: `(*public.Server).postPoCStartV2(...)`
- **Why**: Matches the 2.0.1 testing constraint in `proposals/poc-v2/poc-v2-offchain.md`.
- **Similar to**:
  - Signature validation patterns in `decentralized-api/internal/server/public/utils.go` / `post_chat_handler.go` (uses `calculations.ValidateSignature*`)
  - Header constant: `decentralized-api/utils/api_headers.go` (`XTASignatureHeader`)
- **Result**: Implemented PoC v2 start signature validation in `decentralized-api/internal/server/public/poc_v2_auth.go` and enforced it in `decentralized-api/internal/server/public/post_poc_start_v2_handler.go` by validating `X-TA-Signature` over the canonicalized request body hash.

#### 3.3 Add `GET /v2/poc/results?block_height=...`

- **Task**: [x] Implement the PoCResult debug endpoint
- **What**:
  - Return the latest PoC run by default, or resolve by `block_height` (closest previous if exact not found).
  - Return per-node results: `time_since_block`, nonce `amount`, `hash`, and `nonces` payloads (as stored).
- **Where**:
  - Route registration: `decentralized-api/internal/server/public/server.go`
  - Handler implementation (new): `decentralized-api/internal/server/public/get_poc_results_v2_handler.go`
  - Method signature: `(*public.Server).getPoCResultsV2(...)` (new function)
- **Why**: Required for 2.0.1 validation/testing.
- **Similar to**: Existing query handlers like `decentralized-api/internal/server/public/get_poc_batches_handler.go` (`(*Server).getPoCBatches`).
- **Result**: Added `GET /v2/poc/results` route in `decentralized-api/internal/server/public/server.go` and implemented `(*Server).getPoCResultsV2` in `decentralized-api/internal/server/public/get_poc_results_v2_handler.go`, including the temporary `X-TA-Signature` gate and grouping results by participant → node_id/model → ordered emissions.

#### 3.4 Interrupt active v2 PoC run when legacy/v1 PoC starts (broker-driven)

- **Task**: [x] When the broker starts legacy PoC, mark any active v2 PoC run as interrupted
- **What**:
  - Whenever the chain-driven flow transitions into legacy/v1 PoC generation (i.e., right before `broker.NewStartPocCommand()` is enqueued), check `pocstorage` for an active v2 run.
  - If a v2 run is active (not finished and not interrupted), call `pocStorage.MarkInterrupted(...)` with `time.Now().UTC()`.
- **Where**:
  - Transition point(s): `decentralized-api/internal/event_listener/new_block_dispatcher.go` (`(*OnNewBlockDispatcher).handlePhaseTransitions`) near `broker.NewStartPocCommand()`
  - Wiring: add `pocstorage.PoCStorage` to `internal/event_listener/new_block_dispatcher.go` (`OnNewBlockDispatcher` + constructors) and `internal/event_listener/event_listener.go` (`NewEventListener`), and pass from `decentralized-api/main.go` where `pocStore` is constructed.
- **Why**: Ensures the “interruption” semantics are symmetric (v2 interrupts when legacy is active, and legacy interrupts an active v2 run), as described in `proposals/poc-v2/poc-v2-offchain.md`.
- **Result**: Wired `pocstorage.PoCStorage` into `decentralized-api/internal/event_listener/new_block_dispatcher.go` (dispatcher struct + `NewOnNewBlockDispatcher*` constructors) and `decentralized-api/internal/event_listener/event_listener.go` (`NewEventListener`), passed `pocStore` from `decentralized-api/main.go`, and added `(*OnNewBlockDispatcher).interruptActiveV2PoCIfAny()` which is invoked immediately before enqueuing `broker.NewStartPocCommand()` to mark the latest active v2 run interrupted via `pocStorage.MarkInterrupted(...)`.

### Section 4: ML Node Trigger (API → ML node `/init/generate`)

#### 4.1 Add a v2 ML node client request type and sender

- **Task**: [x] Add v2 request structs for `/init/generate`
- **What**:
  - Define `PoCInitGenerateRequest` and nested `PoCParamsModel` in Go (matching the spec in `proposals/poc-v2/poc-v2-offchain.md`).
  - Add a client method that POSTs to ML node `POST /init/generate` **without going through broker commands** (this is a direct HTTP call via `mlnodeclient`).
- **Where**:
  - New file recommended: `decentralized-api/mlnodeclient/poc_v2.go`
  - New method on `mlnodeclient.Client`: `InitGenerateV2(ctx, req)` (new function)
- **Why**: `POST /v2/poc/start` must trigger ML node generation with this schema.
- **Similar to**:
  - `decentralized-api/mlnodeclient/poc.go` (`InitDto`, `Client.InitGenerate`, `BuildInitDto`)
- **Result**:
  - Added `PoCInitGenerateRequestV2` + `PoCParamsV2` and `(*mlnodeclient.Client).InitGenerateV2(...)` in `decentralized-api/mlnodeclient/poc_v2.go` (POSTs to `/init/generate`).
  - Extended `decentralized-api/mlnodeclient/interface.go` with `InitGenerateV2(...)` and updated `decentralized-api/mlnodeclient/mock.go` to support v2 calls (`InitGenerateV2Called`, `LastInitDtoV2`).

#### 4.2 Wire ML node triggering from `POST /v2/poc/start`

- **Task**: [x] Trigger all local ML nodes with the v2 init request
- **What**:
  - Enumerate local nodes via `(*broker.Broker).GetNodes()` **read-only**.
  - For each node, create a versioned ML node client via `(*broker.Broker).NewNodeClient(&node)` and call `InitGenerateV2(...)`.
  - **Do not** enqueue any broker commands (no `nodeBroker.QueueMessage(...)`) and **do not** mutate broker node state (no changes to `NodeState.IntendedStatus/CurrentStatus/Poc*Status/LockCount`).
  - Trigger calls concurrently with **bounded parallelism** (worker pool / semaphore) to avoid spawning unbounded goroutines and to avoid overwhelming local resources.
  - The v2 init request body must include:
    - `block_hash`, `block_height`, `batch_size`, `params.model/seq_len/k_dim`
    - plus: `public_key` (this participant pubkey), `node_id` (node num), `node_count` (total nodes)
    - and defaults: `group_id=0`, `n_groups=1` (unless specified later)
- **Where**:
  - `decentralized-api/internal/server/public/post_poc_start_v2_handler.go` (`(*Server).postPoCStartV2`)
  - Broker helpers if needed: `decentralized-api/broker/*` (e.g., node enumeration)
- **Why**: This is the primary behavior under test in 2.0.1.
- **Result**:
  - Added read-only participant pubkey accessor `(*broker.Broker).GetParticipantPubKey()` in `decentralized-api/broker/broker.go`.
  - Wired PoC v2 start to trigger v2 mlnode init generation (bounded parallelism + per-node timeout) in `decentralized-api/internal/server/public/post_poc_start_v2_handler.go` via `(*Server).triggerPoCV2InitGenerate(...)`, without using `nodeBroker.QueueMessage(...)` and without touching broker node state.

#### 4.3 Add `StopPowV2` and stop generation on all ML nodes after `duration` seconds

- **Task**: [x] Add ML node v2 “stop generation” call and schedule it from `/v2/poc/start`
- **What**:
  - Add an ML node client method `StopPowV2(ctx)` that calls the mlnode v2 stop-generation endpoint (exact path should match mlnode implementation / the upstream pattern).
  - In `POST /v2/poc/start`, after successfully launching v2 generation on ML nodes, schedule a best-effort stop fanout to **all** local ML nodes after `duration` seconds.
    - This should be **independent of broker state** (read-only node enumeration only; do not enqueue broker commands; do not mutate broker node state).
    - The stop fanout should be concurrent with bounded parallelism and per-node timeouts (same pattern as `triggerPoCV2InitGenerate`).
    - When the timer fires, re-check storage to avoid stopping an already-ended or interrupted run:
      - If the stored run for that `block_height` is already interrupted or already “past end time”, do nothing.
      - If a newer v2 run exists, do nothing (avoid stopping the wrong run).
    - Log per-node failures, but do not fail the original `/v2/poc/start` request (this is a background best-effort cleanup).
- **Where**:
  - ML node client interface: `decentralized-api/mlnodeclient/interface.go` (add `StopPowV2`)
  - ML node client impl: `decentralized-api/mlnodeclient/client.go` (implement `StopPowV2`, define the endpoint path constant similar to `PoCV2InitGeneratePath`)
  - ML node client mock: `decentralized-api/mlnodeclient/mock.go` (support `StopPowV2` for tests)
  - Scheduler/wiring: `decentralized-api/internal/server/public/post_poc_start_v2_handler.go`
    - likely add helper like `(*Server).schedulePoCV2Stop(...)` and call it only when `started == true`
  - Optional: if needed for clean shutdown, store timer handles on `public.Server` and cancel them on server stop (future-proofing; can be skipped for 2.0.1).
- **Why**:
  - Keeps ML nodes from running v2 generation indefinitely if there is no later stage transition yet (2.0.1 is API-triggered and offchain).
  - Matches the intent of the upstream v2 flow where generation is explicitly stopped before/at validation transitions.
- **Similar to**:
  - Upstream v2 orchestrator “stop all nodes” helper: `decentralized-api/internal/pocv2/node_orchestrator_v2.go` (`(*NodePoCOrchestratorV2Impl).stopGenerationOnAllNodes`, which calls `nodeClient.StopPowV2(ctx)` for each node)
- **Result**:
  - Added `StopPowV2(ctx)` to `decentralized-api/mlnodeclient/interface.go` and implemented it in `decentralized-api/mlnodeclient/poc_init_v2.go` (POST `"/api/v1/inference/pow/stop"`).
  - Updated `decentralized-api/mlnodeclient/mock.go` with `StopPowV2` support (`StopPowV2Called`, `StopPowV2Error`).
  - Wired a background timer from `decentralized-api/internal/server/public/post_poc_start_v2_handler.go` to fan out `StopPowV2` to all local ML nodes after `run.DurationSeconds`, with safety checks against `pocstorage.GetLatestRun()` to avoid stopping a newer or interrupted run.

#### 4.4 Trigger PoC v2 only on ML nodes that support the requested model

- **Task**: [x] Filter ML nodes by `run.params.model` before triggering PoC v2 start/stop
- **What**:
  - Only start PoC v2 on ML nodes that are configured to support the requested model (per-node `models` configuration and/or broker epoch model data).
  - Ensure `node_count` in the v2 init request reflects **only the eligible nodes** (not all nodes).
  - Apply the same eligibility filter to the scheduled `StopPowV2` fanout (stop only nodes that could have been started for that model).
- **Where**:
  - Triggering: `decentralized-api/internal/server/public/post_poc_start_v2_handler.go` (`(*Server).triggerPoCV2InitGenerate`)
  - Stop fanout: `decentralized-api/internal/server/public/post_poc_start_v2_handler.go` (`(*Server).stopPoCV2OnAllNodes`)
  - Node model source:
    - Local config: `decentralized-api/broker/broker.go` (`type Node struct { Models map[string]... }`)
    - Epoch models (optional signal): broker `NodeState.EpochModels`
- **Why**: Different ML nodes can host different models; a PoC run for `model=X` must only involve ML nodes that can run `model=X`.
- **Result**: Added `nodeSupportsModel(...)` helper and filtered both start and stop fanouts by `run.Params.Model` (checks `Node.Models` and `NodeState.EpochModels`). Updated `node_count` to use the eligible-node count.

#### 4.5 Trigger PoC v2 only on ML nodes that have the requested model *loaded* (INFERENCE model)

- **Task**: [x] Restrict PoC v2 to nodes where the requested model matches the broker’s INFERENCE model selection for that node
- **What**:
  - Compute which model the broker would load when switching a node to INFERENCE using the same selection logic as `InferenceUpNodeCommand`.
  - Use that computed model id to decide PoC v2 eligibility: PoC v2 for `model=X` only runs on nodes where:
    - `CurrentStatus == INFERENCE`, and
    - `SelectedInferenceModelIDForNode(node) == X` (mirrors inference-up selection)
- **Where**:
  - Model-selection helper:
    - `decentralized-api/broker/broker.go` (`(*Broker).SelectedInferenceModelIDForNode`)
  - Consume it for PoC v2 filtering:
    - `decentralized-api/internal/server/public/post_poc_start_v2_handler.go` (`(*Server).nodeSupportsModelForPoCV2`)
- **Why**: PoC v2 should only run for models that are already loaded and active on the node (the same model that inference-up chose/loaded).
- **Result**: Removed stateful `InferenceModelID` tracking and instead implemented `(*Broker).SelectedInferenceModelIDForNode(...)` (mirrors `InferenceUpNodeCommand` selection logic). Updated PoC v2 eligibility to require `CurrentStatus == INFERENCE` and `SelectedInferenceModelIDForNode(node) == run.Params.Model`.
  - **Note**: As currently implemented, `SelectedInferenceModelIDForNode(...)` uses **only** `NodeState.EpochModels`. If `EpochModels` are missing/empty for a node, the node is treated as ineligible for PoC v2 (no governance-model fallback in this selection helper).

### Section 5: ML Node → API Callback (New Nonce Format)

#### 5.1 Add `POST /v2/poc-artifacts/generated` on the ML callback server

- **Task**: [x] Add the v2 callback endpoint for ML nodes
- **What**:
  - Add a new route on the ML callback server to receive the new nonce format described in `proposals/poc-v2/poc-v2-offchain.md`.
  - Persist each received batch as a separate record (and update aggregates like `amount`/`hash`).
- **Where**:
  - Route registration: `decentralized-api/internal/server/mlnode/server.go` (add `e.POST("/v2/poc-artifacts/generated", ...)` and/or group equivalent)
  - Handler implementation (new): `decentralized-api/internal/server/mlnode/post_poc_batches_generated_v2_handler.go`
  - Method signature: `(*mlnode.Server).postGeneratedArtifactsV2(...)` (new function)
- **Why**: ML nodes will push results periodically (5s frequency); this is the ingestion path.
- **Similar to**:
  - Existing v1 handler: `decentralized-api/internal/server/mlnode/post_generated_batches_handler.go` (`(*Server).postGeneratedBatches`)
- **Result**: Wired `pocstorage.PoCStorage` into the ML callback server (`decentralized-api/internal/server/mlnode/server.go` + `decentralized-api/main.go`), added `POST /v2/poc-artifacts/generated` (and `/mlnode/v2/poc-artifacts/generated`) routing, and implemented `(*mlnode.Server).postGeneratedArtifactsV2` in `decentralized-api/internal/server/mlnode/post_poc_batches_generated_v2_handler.go` to validate the run, convert `public_key`→address, map `node_id` via broker when possible, decode `vector_b64`, and persist via `pocStore.StoreGeneratedRecord(...)`.

### Section 6: Tests and Operational Validation (2.0.1)

#### 6.1 Storage tests

- **Task**: [ ] Add unit tests for PoC V2 storage (schema + CRUD)
- **Where**: `decentralized-api/pocstorage/*_test.go`
- **Similar to**: `decentralized-api/payloadstorage/postgres_storage_test.go`

#### 6.2 Handler tests

- **Task**: [ ] Add basic handler tests for `/v2/poc/start`, `/v2/poc/results`, `/v2/poc-artifacts/generated`
- **Where**:
  - `decentralized-api/internal/server/public/*_test.go`
  - `decentralized-api/internal/server/mlnode/*_test.go`
- **Why**: Lock in request/response shapes and prevent regressions.

#### 6.3 Re-check PoC v2 init grouping parameters (`group_id`, `n_groups`)

- **Task**: [ ] Verify `group_id` / `n_groups` semantics and defaults against mlnode implementation
- **What**:
  - Confirm whether mlnode PoC v2 `/init/generate` actually uses grouping and how it interprets `group_id` and `n_groups`.
  - Confirm whether the defaults we send (`group_id=0`, `n_groups=1`) are correct for the “old PoC equivalent” behavior.
  - If needed, update:
    - Request schema in `decentralized-api/mlnodeclient/poc_v2.go` (`PoCInitGenerateRequestV2`)
    - Builder logic in `decentralized-api/internal/server/public/post_poc_start_v2_handler.go` (`triggerPoCV2InitGenerate`)
    - And the spec in `proposals/poc-v2/poc-v2-offchain.md` / this task plan.
- **Where**:
  - Source of truth: mlnode `POST /init/generate` handler (`@router.post("/init/generate")`)
  - Client: `decentralized-api/mlnodeclient/poc_v2.go`
  - Trigger: `decentralized-api/internal/server/public/post_poc_start_v2_handler.go`
