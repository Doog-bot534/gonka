# Inference Performance: Task II Implementation Plan

## Prerequisite Reading

Before starting implementation, please read the following documents to understand the full context of the changes:

- The performance proposal: `proposals/inference-performance/README.md` (Task II section)
- Implementation data snapshot: `proposals/inference-performance/task-ii-data.md`
- Inference persistence flow: `inference-chain/x/inference/keeper/inference.go`
- Start/finish handlers: `inference-chain/x/inference/keeper/msg_server_start_inference.go`, `inference-chain/x/inference/keeper/msg_server_finish_inference.go`
- Existing API event listener: `decentralized-api/internal/event_listener/event_listener.go`
- API node storage pattern reference: `decentralized-api/payloadstorage/`

## How to Use This Task List

### Workflow

- **Focus on a single task**: Please work on only one task at a time to ensure clarity and quality. Avoid implementing parts of future tasks.
- **Request a review**: Once a task's implementation is complete, change its status to `[?] - Review` and wait for my confirmation.
- **Update all usages**: If a function or variable is renamed, find and update all its references throughout the codebase.
- **Build after each task**: After each task is completed, build the project to ensure there are no compilation errors.
- **Test after each section**: After completing all tasks in a section, run the corresponding tests to verify the functionality.
- **Wait for completion**: After I confirm the review, mark the task as `[x] - Finished`, add a **Result** section summarizing the changes, and then move on to the next one.

### Build & Test Commands

- **Build Inference Chain**: From the project root, run `make node-local-build`
- **Build API Node**: From the project root, run `make api-local-build`
- **Run Inference Chain Unit Tests**: From the project root, run `make node-test`
- **Run API Node Unit Tests**: From the project root, run `make api-test`

### Status Indicators

- `[ ]` **Not Started** - Task has not been initiated
- `[~]` **In Progress** - Task is currently being worked on
- `[?]` **Review** - Task completed, requires review/testing
- `[x]` **Finished** - Task completed and verified

### Task Organization

Tasks are organized by implementation area and numbered for easy reference. Dependencies are noted where critical. Complete tasks in order.

### Task Format

Each task includes:

- **What**: Clear description of work to be done
- **Where**: Specific files/locations to modify
- **Why**: Brief context of purpose when not obvious

## Task List

### Section 1: Inference Chain Hot-Path Refactor

#### 1.1 Make `SetInference` Lightweight (No Dev Stats Writes)

- **Task**: [x] - Finished Remove developer-stats writes from `SetInference`
- **What**: Refactor `SetInference` so it only persists inference state and no longer calls `SetDeveloperStats`.
- **Where**:
  - `inference-chain/x/inference/keeper/inference.go`
- **Why**: Removes heavy writes from the hot transaction path.
- **Dependencies**: None
- **Result**: `SetInference` now directly persists inference state without computing developer stats. `SetInferenceWithoutDevStatComputation` was removed and all call sites were switched to `SetInference`. Verified via `go test ./x/inference/keeper ./app -run TestDoesNotExist -count=1`.

#### 1.2 Make `StartInference` Use One Final Inference Write

- **Task**: [x] - Finished Keep only one inference persistence call in `StartInference`
- **What**: Verify/update flow so the inference is written once after all in-function mutations are complete.
- **Where**:
  - `inference-chain/x/inference/keeper/msg_server_start_inference.go`
- **Why**: Avoids duplicate writes in the common path.
- **Dependencies**: 1.1
- **Result**: `StartInference` now runs completion handling before persistence and performs one final `SetInference` call for the updated inference state.

#### 1.3 Make `FinishInference` Use One Final Inference Write

- **Task**: [x] - Finished Keep only one inference persistence call in `FinishInference`
- **What**: Ensure inference state is written once and not written again during completion handling.
- **Where**:
  - `inference-chain/x/inference/keeper/msg_server_finish_inference.go`
- **Why**: Avoids duplicate writes and keeps Finish path minimal.
- **Dependencies**: 1.1
- **Result**: `FinishInference` now runs completion handling before persistence and performs one final `SetInference` call for the updated inference state.

#### 1.4 Remove Second `SetInference` from Completion Handler

- **Task**: [x] - Finished Remove duplicate persist in `handleInferenceCompleted`
- **What**: Refactor completion handler to mutate in-memory inference/event data without performing another inference write.
- **Where**:
  - `inference-chain/x/inference/keeper/msg_server_finish_inference.go`
- **Why**: Task II explicitly requires eliminating duplicate `SetInference` execution.
- **Dependencies**: 1.3
- **Result**: Removed internal `SetInference` call from `handleInferenceCompleted`; it now updates completion side effects and mutates the passed inference in-memory only.

#### 1.5 Keep Existing Behavior for Non-Task-II Flows

- **Task**: [x] - Finished Verify non-Start/Finish flows still persist correctly
- **What**: Check `validation`, `invalidate`, and `revalidate` handlers and keep behavior unchanged unless needed for compile/runtime correctness.
- **Where**:
  - `inference-chain/x/inference/keeper/msg_server_validation.go`
  - `inference-chain/x/inference/keeper/msg_server_invalidate_inference.go`
  - `inference-chain/x/inference/keeper/msg_server_revalidate_inference.go`
- **Why**: Prevents regressions outside Task II scope.
- **Dependencies**: 1.4
- **Result**: Verified by targeted keeper tests: `go test ./x/inference/keeper -run 'TestMsgServer_Validation|TestInvalidateInference_.*|TestRevalidate_.*' -count=1`.

### Section 2: Extend `inference_finished` Event for Off-Chain Stats

#### 2.1 Add Event Attribute Builder Helper

- **Task**: [x] - Finished Create a helper that builds minimal dev-stats `inference_finished` attributes
- **What**: Implement one helper function that assembles only the attributes listed in `task-ii-data.md` section 6.
- **Where**:
  - `inference-chain/x/inference/keeper/msg_server_finish_inference.go`
- **Why**: Avoids attribute drift and simplifies tests.
- **Dependencies**: 1.4
- **Result**: Added `buildInferenceFinishedEventAttributes(inference)` helper in `msg_server_finish_inference.go`.

#### 2.2 Emit Minimal Dev-Stats Payload in `inference_finished`

- **Task**: [x] - Finished Emit minimal payload needed for dev-stats migration
- **What**: Emit only these fields: `inference_id`, `requested_by`, `model`, `status`, `epoch_id`, `prompt_token_count`, `completion_token_count`, `actual_cost_in_coins`, `start_block_timestamp`, `end_block_timestamp`.
- **Where**:
  - `inference-chain/x/inference/keeper/msg_server_finish_inference.go`
- **Why**: API node needs full data to compute/store stats off-chain.
- **Dependencies**: 2.1
- **Result**: `handleInferenceCompleted` now emits the required minimal payload through the new helper, after `epoch_id` is finalized.

#### 2.3 Keep Backward-Compatible Core Attributes

- **Task**: [x] - Finished Preserve existing `inference_id` attribute key
- **What**: Ensure old consumers continue to receive `inference_finished.inference_id` unchanged.
- **Where**:
  - `inference-chain/x/inference/keeper/msg_server_finish_inference.go`
  - `decentralized-api/internal/event_listener/event_listener.go`
- **Why**: Avoids breaking current inference validation trigger logic.
- **Dependencies**: 2.2
- **Result**: Kept the `inference_id` attribute key unchanged in `inference_finished`; API listener compatibility remains intact.

### Section 3: API Node Storage for Off-Chain Inference Stats

#### 3.1 Create Off-Chain Stats Storage Interface

- **Task**: [x] - Finished Add storage interfaces modeled after payload storage patterns
- **What**: Define interfaces and models where per-inference records (`inference_id` keyed) are the source of truth, plus aggregate read models (model/time and summary views).
- **Where**:
  - New package under `decentralized-api/` (follow `decentralized-api/payloadstorage/` structure)
- **Why**: Keeps storage implementation swappable and testable.
- **Dependencies**: 2.2
- **Result**: Added new `decentralized-api/statsstorage` package with `StatsStorage` interface and read-model structs (`InferenceRecord`, `Summary`, `ModelSummary`, debug models) for per-inference source-of-truth + aggregate reads.

#### 3.2 Implement Persistent Storage Backend

- **Task**: [x] - Finished Implement DB-backed storage for inference stats
- **What**: Add concrete storage implementation (schema, insert/update, query methods, idempotent upsert key).
- **Where**:
  - New storage files under the package from 3.1
- **Why**: Stats must survive restarts and support dashboard queries.
- **Dependencies**: 3.1
- **Result**: Implemented PostgreSQL backend (`statsstorage/postgres_storage.go`) with schema creation, idempotent `UpsertInference` by `inference_id`, per-developer time-range reads, epoch/time/model aggregate queries, debug stats queries, and `factory.go` bootstrap helper. Verified via `go test ./statsstorage -count=1` and `make api-local-build`.

#### 3.3 Wire Storage into API App Startup

- **Task**: [x] - Finished Initialize and inject new stats storage in app bootstrap
- **What**: Add storage construction and dependency wiring in startup/server initialization.
- **Where**:
  - `decentralized-api/main.go`
  - `decentralized-api/internal/server/public/server.go`
- **Why**: Makes storage available to event listener and API handlers.
- **Dependencies**: 3.2
- **Result**: Added `statsstorage.NewStatsStorage(ctx)` initialization in `main.go` with graceful nil behavior when `PGHOST` is unset, and injected storage into public server via new `WithStatsStorage(...)` option.

#### 3.4 Add Retention-Based Stats Auto-Pruning

- **Task**: [x] - Finished Add automatic pruning for old per-inference stats records
- **What**: Implement retention cleanup for stats storage with:
  - default retention = 30 days,
  - static prune cadence (daily, no interval config),
  - retention override via env (for example, `DAPI_STATS_RETENTION_DAYS`),
  - disable prune when retention is set to non-positive value.
- **Where**:
  - `decentralized-api/statsstorage/`
  - `decentralized-api/main.go` (if wrapper/lifecycle hook is needed)
- **Why**: We store one record per finished inference; without retention this grows unbounded over time.
- **Dependencies**: 3.3
- **Result**: Added managed stats wrapper (`statsstorage/managed_storage.go`) with static daily prune loop, startup prune pass, and default 30-day retention. Added `PruneOlderThan(...)` to storage interface with implementations for PostgreSQL (`DELETE ... WHERE inference_timestamp < cutoff`) and file storage (remove old record files). Factory now wraps storage with managed retention and reads `DAPI_STATS_RETENTION_DAYS` (default `30`; non-positive disables pruning).

### Section 4: API Event Listener Ingestion

#### 4.1 Parse New `inference_finished` Attributes

- **Task**: [x] - Finished Extend finished-event handling to read new payload fields
- **What**: Parse and validate all new event attributes with robust default/error handling.
- **Where**:
  - `decentralized-api/internal/event_listener/event_listener.go`
- **Why**: Converts chain events into structured records for storage.
- **Dependencies**: 2.3, 3.3
- **Result**: Added robust parser for `inference_finished` attributes with required-field checks and numeric parsing helpers.

#### 4.2 Persist Per-Inference Record from Events

- **Task**: [x] - Finished Save per-inference stats record on each finished event
- **What**: Store one normalized record per finished inference using `inference_id` as idempotency key.
- **Where**:
  - `decentralized-api/internal/event_listener/event_listener.go`
  - storage package from Section 3
- **Why**: Enables exact traceability and replay-safe ingestion.
- **Dependencies**: 4.1
- **Result**: `InferenceFinishedEventHandler` now writes parsed records to `StatsStorage.UpsertInference(...)` while preserving existing validation-sampling behavior.

#### 4.3 Provide Cumulative Aggregate Reads

- **Task**: [x] - Finished Provide aggregate reads for model/time and summary queries
- **What**: Expose cumulative aggregate query methods from storage based on per-inference source-of-truth records.
- **Where**:
  - storage package from Section 3
- **Why**: Enables dashboard stats endpoints using off-chain data.
- **Dependencies**: 4.2
- **Result**: Added `GetModelStatsByTime`, `GetSummaryByTimePeriod`, `GetSummaryByEpochsBackwards`, and developer summary/debug aggregate reads in PostgreSQL storage.

### Section 5: API Endpoint Migration (Stats)

#### 5.1 Implement Models Stats Endpoint

- **Task**: [x] - Finished Implement `GET /v1/stats/models?time_from=&time_to=`
- **What**: Add handler and route that returns per-model aggregate stats from off-chain storage (replacement for `InferencesAndTokensStatsByModels`).
- **Where**:
  - `decentralized-api/internal/server/public/server.go`
  - New stats handlers file under `decentralized-api/internal/server/public/`
- **Why**: This endpoint is first priority and needed for pricing utilization migration.
- **Dependencies**: 4.3
- **Result**: Added public endpoint route and handler (`stats_handlers.go`) backed by off-chain `StatsStorage.GetModelStatsByTime(...)`.

#### 5.2 Switch Pricing Utilization to New Models Endpoint/Storage

- **Task**: [x] - Finished Remove chain stats query dependency from pricing flow
- **What**: Update `get_pricing_handler.go` to read utilization stats from the new off-chain stats path instead of `InferencesAndTokensStatsByModels`.
- **Where**:
  - `decentralized-api/internal/server/public/get_pricing_handler.go`
- **Why**: Removes current production dependency on on-chain dev stats for pricing.
- **Dependencies**: 5.1
- **Result**: `getModelMetrics(...)` now reads model stats from off-chain storage (`s.statsStorage`) and no longer calls chain query `InferencesAndTokensStatsByModels`.

#### 5.3 Implement Developer Per-Inference Endpoint

- **Task**: [x] - Finished Implement `GET /v1/stats/developers/:developer/inferences?time_from=&time_to=`
- **What**: Add handler and route for per-inference developer records in a time range (replacement for `StatsByTimePeriodByDeveloper` semantics).
- **Where**:
  - `decentralized-api/internal/server/public/server.go`
  - New stats handlers file under `decentralized-api/internal/server/public/`
- **Why**: Explicitly required by the per-inference storage decision.
- **Dependencies**: 5.2
- **Result**: Implemented handler in `stats_handlers.go` returning per-inference developer stats in time range from `StatsStorage.GetDeveloperInferencesByTime(...)`.

#### 5.4 Implement Developer Epoch Summary Endpoint

- **Task**: [x] - Finished Implement `GET /v1/stats/developers/:developer/summary/epochs?epochs_n=`
- **What**: Add handler and route for per-developer aggregate summary over last N epochs.
- **Where**:
  - `decentralized-api/internal/server/public/server.go`
  - New stats handlers file under `decentralized-api/internal/server/public/`
- **Why**: Maintains parity with `StatsByDeveloperAndEpochsBackwards`.
- **Dependencies**: 5.3
- **Result**: Implemented handler in `stats_handlers.go` using `StatsStorage.GetSummaryByDeveloperEpochsBackwards(...)`.

#### 5.5 Implement Global Epoch Summary Endpoint

- **Task**: [x] - Finished Implement `GET /v1/stats/summary/epochs?epochs_n=`
- **What**: Add handler and route for global aggregate summary over last N epochs.
- **Where**:
  - `decentralized-api/internal/server/public/server.go`
  - New stats handlers file under `decentralized-api/internal/server/public/`
- **Why**: Maintains parity with `InferencesAndTokensStatsByEpochsBackwards`.
- **Dependencies**: 5.4
- **Result**: Implemented handler in `stats_handlers.go` using `StatsStorage.GetSummaryByEpochsBackwards(...)`.

#### 5.6 Implement Global Time Summary Endpoint

- **Task**: [x] - Finished Implement `GET /v1/stats/summary/time?time_from=&time_to=`
- **What**: Add handler and route for global aggregate summary over a time range.
- **Where**:
  - `decentralized-api/internal/server/public/server.go`
  - New stats handlers file under `decentralized-api/internal/server/public/`
- **Why**: Maintains parity with `InferencesAndTokensStatsByTimePeriod`.
- **Dependencies**: 5.5
- **Result**: Implemented handler in `stats_handlers.go` using `StatsStorage.GetSummaryByTimePeriod(...)`.

#### 5.7 Implement Debug Developer Stats Endpoint

- **Task**: [x] - Finished Implement `GET /v1/stats/debug/developers`
- **What**: Add handler and route for debug dump of by-time/by-epoch developer stats.
- **Where**:
  - `decentralized-api/internal/server/public/server.go`
  - New stats handlers file under `decentralized-api/internal/server/public/`
- **Why**: Maintains parity with `DebugStatsDeveloperStats` for diagnostics.
- **Dependencies**: 5.6
- **Result**: Implemented handler in `stats_handlers.go` using `StatsStorage.GetDebugStats(...)`, with response grouped by developer for by-time and by-epoch sections.

### Section 6: Testing and Validation

#### 6.1 Unit Tests for Chain Refactor

- **Task**: [ ] Add tests proving single-write behavior and event payload completeness
- **What**: Add/update tests for Start/Finish/completion flow to ensure no duplicate `SetInference` and expected event attributes.
- **Where**:
  - `inference-chain/x/inference/keeper/msg_server_start_inference_test.go`
  - `inference-chain/x/inference/keeper/msg_server_finish_inference_test.go`
- **Why**: Prevents regressions in hot transaction paths.
- **Dependencies**: Section 2

#### 6.2 Unit Tests for API Event Ingestion + Storage

- **Task**: [?] - Review Add tests for parsing, idempotency, and aggregate updates
- **What**: Verify event parsing, duplicate event handling, per-inference persistence, and aggregate correctness.
- **Where**:
  - `decentralized-api/internal/event_listener/event_listener_test.go`
  - storage package tests from Section 3
- **Why**: Guarantees reliable off-chain stats updates.
- **Dependencies**: Section 4
- **Result**: Added parser-focused tests in `internal/event_listener/event_listener_stats_test.go` for successful record extraction and required-field failure behavior.

#### 6.3 API Handler Tests for Stats Endpoints

- **Task**: [?] - Review Add handler tests for all new stats endpoints
- **What**: Add coverage for: models/time, developer per-inference, developer epoch summary, global epoch summary, global time summary, and debug endpoint.
- **Where**:
  - `decentralized-api/internal/server/public/`
- **Why**: Ensures endpoint contract parity and request validation behavior.
- **Dependencies**: 6.2
- **Result**: Added initial handler tests in `internal/server/public/stats_handlers_test.go` for models stats response and summary-epochs request validation path.

#### 6.4 Integration Test for End-to-End Stats Flow

- **Task**: [ ] Add an end-to-end test from chain event to API read
- **What**: Validate that a finished inference emits event -> API listener persists -> each stats endpoint returns expected updated values.
- **Where**:
  - `testermint/src/test/kotlin/`
  - `testermint/src/main/kotlin/ApplicationAPI.kt` (stats endpoint query wrappers)
- **Why**: Confirms full migration correctness across ingest and read APIs.
- **Dependencies**: 6.3
- **Reference Tests/Helpers**:
  - `testermint/src/test/kotlin/DynamicPricingTest.kt` (parallel inference load + API pricing read pattern)
  - `testermint/src/main/kotlin/InferenceTestUtils.kt` (`runParallelInferencesWithResults(...)`)
  - `testermint/src/test/kotlin/MultiModelTests.kt` (multi-inference, post-ingest assertions)
  - `testermint/src/main/kotlin/ApplicationAPI.kt` (generic public API `get(...)` request helper)

#### 6.5 Performance Check Against Task II Goal

- **Task**: [ ] Re-run Start/Finish timing after migration
- **What**: Measure transaction duration on production-like state and report delta versus baseline.
- **Where**:
  - Benchmark/profiling notes in `proposals/inference-performance/README.md` (append results)
- **Why**: Confirms Task II changes produce measurable performance improvement.
- **Dependencies**: 6.4

**Summary**: This plan implements Task II by removing heavy stats writes from the Start/Finish hot path, ensuring single inference persistence per transaction flow, and moving developer stats collection to API-node storage via minimal `inference_finished` event payload. The task sequence prioritizes small, reviewable, direct implementation steps.
