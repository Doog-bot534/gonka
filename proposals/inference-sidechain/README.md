# Proposal: Inference Sidechain (v2 Access Flow)

## Status

Draft

## Goal

Introduce a new inference request flow that:

- works in parallel with existing v1 completion flow,
- removes the Transfer Agent dependency for v2 requests,
- removes per-request on-chain messages for v2 inference execution,
- keeps access control and participant assignment deterministic and verifiable.

This document defines implementation steps (1-23). Steps 1-15 are implemented in the current milestone; steps 16-23 are planned next.

## Non-Goals (for this stage)

- Replace or remove v1 completion flow.
- Finalize settlement/slashing logic for v2 responses.
- Define long-term storage backends (this proposal allows local storage first).

## Step 1: Add v2 Completion Endpoint in `decentralized-api`

### Step 1 Summary

Add a new v2 chat-completions API endpoint (`/v2/chat/completions`) that allows developers to request inference directly, without Transfer Agent and without creating on-chain messages per inference request.

### Step 1 Requirements

- Add a v2 endpoint (`/v2/chat/completions`) in `decentralized-api`.
- Keep v1 endpoint and behavior unchanged.
- v1 and v2 must run in parallel in the same deployment.
- v2 request handling must not require Transfer Agent routing.
- v2 request handling must not broadcast on-chain inference messages.

### Notes

- v2 is a new flow, not a migration cut-over.
- Existing clients remain compatible with v1.

## Step 2: On-Chain Access via `MsgCreateEscrow(model_id)`

### Step 2 Summary

A developer must create an escrow on-chain to get permission for v2 API usage. Chain stores access intent and emits events that `decentralized-api` indexes locally. Every v2 request must include `escrow_id` in headers so authorization can be resolved from escrow state.

### Step 2 Requirements

- Add/extend `MsgCreateEscrow` to include `model_id` (no client-provided `escrow_id`).
- Chain allocates `escrow_id` from a deterministic global sequence and returns/emits it.
- On successful message handling, store an escrow access record keyed by `escrow_id`, containing:
  - `developer_address` (escrow owner authorized for v2),
  - `model_id`.
- Require `escrow_id` in v2 request headers (for example, `X-Escrow-Id`).
- Emit an event with enough fields for off-chain indexing.
- `decentralized-api` listens for these events and maintains a local in-memory access index keyed by `escrow_id` (non-persistent for now).
- v2 endpoint must resolve access by `escrow_id` and allow only when requester identity matches the stored `developer_address` (and `model_id` when provided in request).

### Suggested Event Fields

- `event_type`: `escrow_created`
- `developer_address`
- `developer_pubkey`
- `escrow_id`
- `model_id`
- `block_height`
- `tx_hash`

### Access Rule (v2)

Allow request only when:

- request headers include `escrow_id`,
- `escrow_id` exists in local index,
- requester identity matches record `developer_address`,
- `model_id` check passes when request includes/targets model.

## Step 3: Deterministic Participant Selection with `escrow_id + sequence`

### Step 3 Summary

Each v2 request must include headers that allow both developer and executor to independently compute who is responsible for that request.

### Step 3 Requirements

- Add to request headers:
  - `escrow_id` (already required in Step 2, reused here for routing seed)
  - `sequence` (added in Step 3, strictly increasing per escrow)
- Define deterministic participant selection function:
  - Input seed derived from `escrow_id + sequence`.
  - Select `N` participants, where `N` is a network parameter (default `4`).
  - `N` should be sourced from dedicated inference-v2 params (not participant-access params).
  - Selection is random-like but weighted by participant power.
  - Given same chain state and same inputs, all parties must derive identical result.

### Determinism Constraints

- Use canonical seed derivation, for example:
  - `seed = H(escrow_id || ":" || sequence)`
- Use a deterministic ordering of eligible participants before sampling.
- Use chain data snapshot rules that avoid ambiguity (for example, latest finalized epoch/state at request validation time).
- Ensure no hidden runtime entropy is used.

### Outcome

Both developer and executor can independently identify the responsible participant set for each request (`escrow_id`, `sequence`) without extra coordination.

## Step 4: Developer Inference Chain Block Format (No Signatures Yet)

### Step 4 Summary

Define the minimal chain-block format that the API node accepts for v2 request history continuity, without signatures/hashes at this stage.

### Step 4 Requirements

- API node must define block structure:
  - `block_sequence`
  - `messages[]`
- API node must define accepted message types:
  - `StartInference { request_id, model_id, timestamp }`
  - `FinishInference { request_id, status, timestamp }`
- `StartInference` must not include a separate request sequence field; `block_sequence` is the sequence source of truth.
- Each block must contain exactly one `StartInference` message.
- API node must check continuity only by `block_sequence` monotonic progression.
- Developer-side requirement (external dependency): caller must build blocks using this exact format.

## Step 5: Request Transport for Chain Delta (Without Modifying `OpenAiRequest`)

### Step 5 Summary

Define how the API node receives chain-delta data alongside inference payload without changing `OpenAiRequest`.

### Step 5 Requirements

- API node endpoint must accept a v2 envelope that carries:
  - unchanged `openai_request`,
  - `developer_chain_delta` (blocks since last successful checkpoint).
- API node must keep chain fields outside `OpenAiRequest` schema.
- API node must require each request to include:
  - `base_block_sequence` (last block sequence previously acknowledged by this receiver; sender includes its latest acknowledged value),
  - `blocks[]` (new blocks),
  - `latest_block_sequence` (tip after appended blocks).
- API node must enforce explicit size limits, serialization rules, and rejection codes for malformed chain delta payload.
- Developer-side requirement (external dependency): caller must send only delta blocks since last successful checkpoint.

## Step 6: API Node Chain Validation, Storage, and Response Gate

### Step 6 Summary

The API node on a responsible participant must validate and record chain updates before processing the current request.

### Step 6 Requirements

- API node keeps local chains keyed by developer (and escrow scope), in-memory first.
- On each request, API node validates:
  - `base_block_sequence` matches receiver's stored latest sequence for this developer chain,
  - continuity from known tip (`base_block_sequence`),
  - strict monotonic `block_sequence`,
  - exactly one `StartInference` per block,
  - latest received block corresponds to current request via `StartInference.request_id`.
- API node appends/records accepted blocks before inference processing starts.
- Request processing must not start if chain-delta validation/storage fails.
- API node returns checkpoint metadata (`latest_block_sequence`) for next request delta.

## Step 7: Testermint End-to-End Developer Chain Flow

### Step 7 Summary

Extend Testermint flow so developer side actually builds and sends chain blocks, and validate end-to-end behavior.

### Step 7 Requirements

- Update `V2EscrowFlowTest` (and helpers) so developer forms `developer_chain_delta` blocks for each request.
- Ensure generated blocks use monotonically increasing `block_sequence`.
- Ensure each generated block includes exactly one `StartInference`.
- Send envelope payload (`openai_request` + `developer_chain_delta`) in v2 requests.
- Ensure developer/test client persists receiver-acknowledged `latest_block_sequence` and uses it as next `base_block_sequence`.
- Validate end-to-end that API node accepts valid chain deltas and rejects invalid continuity.

## Step 8: Parallel Overlap Acceptance (In-Flight Requests)

### Step 8 Summary

Allow developer requests to run in parallel so a later request can reach an executor before a previous request is acknowledged, while still preserving deterministic chain consistency.

### Step 8 Requirements

- Support overlapping in-flight requests for the same developer/escrow chain.
- A request with a stale client checkpoint (because prior request is still in-flight) must be accepted when overlap is consistent with already-known chain state for that receiver.
- Developer client should use a fast, mutexed per-escrow chain controller function for parallel request creation:
  - input should include precomputed `request_payload_hash` (not full `openai_request`),
  - inside lock: allocate `sequence = previous + 1`, deterministically pick recipient from `escrow_id + sequence`, append `StartInference`, build `developer_chain_delta`, return `{sequence, recipient, developer_chain_delta}`,
  - release lock before network I/O.
- Define idempotent replay behavior keyed by `request_id`:
  - identical replays are accepted (or treated as already accepted),
  - conflicting replays are rejected deterministically.
- Sequence/replay protection must not permanently consume a sequence when pre-processing validation fails.
- Add Testermint parallel tests where request `n+1` is sent before request `n` acknowledgment and both complete successfully.

## Step 9: Add `StartInference.request_payload_hash` and Validate

### Step 9 Summary

Bind each `StartInference` to the exact OpenAI request payload via hash, so the API node can verify request integrity.

### Step 9 Requirements

- Extend `StartInference` with `request_payload_hash`.
- Define OpenAI payload hashing rules (algorithm + exact byte payload used in `openai_request`) and use one shared implementation across developer and API node.
- API node must validate `request_payload_hash` against the received `openai_request` before request execution starts.
- Reject mismatches with a deterministic validation error.
- Add positive/negative tests for hash validation.

## Step 10: Add `FinishInference.response_payload_hash` and Append on Developer Side

### Step 10 Summary

After receiving inference output, the developer forms `FinishInference` with response payload hash and appends it to the developer chain.

### Step 10 Requirements

- Extend `FinishInference` with `response_payload_hash`.
- Developer client must hash the received response payload and create `FinishInference { request_id, status, timestamp, response_payload_hash }`.
- Developer client must persist this `FinishInference` in its local chain state.
- API node must validate `FinishInference` format and its linkage to `request_id`.
- Add end-to-end tests showing `FinishInference` is produced and later transmitted in chain deltas.

## Step 11: Multi-Executor Retry, Relay to Intended Executor, and Streaming/Failure Semantics

### Step 11 Summary

If intended executor is unavailable or slow, developer can retry through all responsible participants; non-intended participants must relay to intended executor and return either the intended response (including streaming) or a standardized failure.

### Step 11 Requirements

- When intended executor does not respond in time, developer may send the same request to all `N` responsible participants.
- Non-intended responsible participants must forward (relay) to the intended executor for that request, not execute independently.
- Relay path must preserve response behavior:
  - non-streaming responses are returned as standard completion payloads,
  - streaming responses are proxied as streaming responses end-to-end.
- If intended executor is unreachable or fails, relay must return a standardized, deterministic failure response.
- Participants must deduplicate by `request_id` to prevent duplicate execution/results under retries/fanout.
- Add Testermint coverage for:
  - intended executor timeout,
  - successful relayed completion,
  - successful relayed streaming,
  - deterministic failure when intended executor cannot be reached.

### Step 11 End-to-End Test Plan

Add dedicated Testermint E2E tests (separate from Step 7 smoke test) that validate retry/relay behavior for one logical request (`escrow_id + sequence`) sent through multiple responsible participants.

- **Scenario A: Relay success (non-streaming)**
  - Configure one request where client first contacts a non-intended responsible participant.
  - Verify non-intended participant relays to intended executor and returns a valid completion response.
  - Assert response payload and request identity (`request_id`) are unchanged across relay path.
- **Scenario B: Relay success (streaming)**
  - Send a streamed request through non-intended responsible participant.
  - Verify stream is proxied from intended executor end-to-end.
  - Assert stream framing/chunk order and terminal completion behavior are preserved.
- **Scenario C: Intended executor timeout, fallback over all N**
  - Make intended executor slow/unavailable.
  - Send the same logical request (same `sequence`, same payload) through all responsible participants.
  - Verify one of the paths eventually returns successful response when intended executor recovers, or all return deterministic failure if it stays unavailable.
- **Scenario D: Deterministic failure**
  - Keep intended executor unavailable for entire timeout window.
  - Verify every responsible participant returns the same standardized failure class/code.
- **Scenario E: Duplicate/replay safety**
  - Replay identical request (`request_id` + payload hash) after acceptance.
  - Verify duplicate is handled idempotently (no conflicting second execution path) and result is consistent.

### Step 11 E2E Assertions

- Intended executor for a request is deterministic from `escrow_id + sequence` and validated against latest `StartInference`.
- Non-intended participants do not execute the request independently; they relay to intended executor.
- Relay behavior is protocol-preserving for both non-streaming and streaming responses.
- Retries/fanout with same logical request are deduplicated by identity and do not create inconsistent chain state.
- Failure responses are deterministic and actionable for client retry policy.

## Step 12: Relay-Side Developer Chain Ingestion and Validation

### Step 12 Summary

When a non-intended participant relays a request, it must still process the attached developer chain delta exactly as an executor would: validate it, store unknown blocks, and keep local chain state consistent.

### Step 12 Requirements

- Relay participants must always parse and validate attached `developer_chain_delta`, even when they are not the intended executor.
- Relay participants must append/store previously unknown valid blocks from the request before forwarding.
- Relay participants must reject malformed/invalid chain deltas deterministically using the same validation rules as direct executor handling.
- Relay participants must preserve overlap semantics from Step 8 (matching overlap accepted, mismatch rejected).
- Relay participants must return updated checkpoint metadata (`latest_block_sequence`) based on local accepted state.

## Step 13: Leader/Client Disconnect Resilience for Same Request Identity

### Step 13 Summary

If developer disconnects early, or if the leader relay/client connection drops, intended execution must continue in background so active/new requests with the same `request_id` can still receive completion/replay.

### Step 13 Requirements

- If leader client disconnects during streaming/non-streaming execution, intended executor must continue processing request to completion.
- The final result (or deterministic failure) must be retained for replay to subsequent requests with same `request_id`.
- Duplicate in-flight requests must be able to attach/replay regardless of whether original leader connection is still open.
- Add E2E tests for:
  - leader disconnect mid-stream with follower receiving remaining stream/replay,
  - leader disconnect in non-streaming path with follower receiving completed result.

## Step 14.1: Developer Block Signatures

### Step 14.1 Summary

Add developer signatures on every developer block so the chain history is cryptographically attributable to the developer identity.

### Step 14.1 Requirements

- Signature algorithm/type must be the same as existing v2 signature validation path: Cosmos SDK `secp256k1` signatures.
- Signature/public key encoding should stay consistent with existing APIs (`base64`-encoded pubkeys and signatures).
- Add `escrow_id` and developer `signature` to each developer block. Do not include developer address/pubkey in block payload; derive signer identity from stored escrow access record by `escrow_id`.
- Use domain-separated signing payload `v2_dev_block_sig_v1` to prevent cross-context replay.
- Define canonical block hashing/signing flow (best option):
  - compute canonical message bytes for each message in the block,
  - compute per-message hashes `message_hash_i = sha256(canonical_message_i_bytes)`,
  - compute ordered block message hash `block_messages_hash = sha256(message_hash_0 || message_hash_1 || ... || message_hash_n)`,
  - sign preimage fields: `chain_id`, `escrow_id`, `block_sequence`, `block_messages_hash`.
- `request_id` should not be a separate signature field for block signatures; it is covered inside canonical message bytes and therefore inside `block_messages_hash`.
- Sign `sha256(canonical_preimage_bytes)` with `secp256k1`.
- Executor/relay nodes must validate developer block signatures before accepting/appending blocks and recompute `block_messages_hash` from block messages as part of verification.
- Reject requests deterministically when developer signatures are missing/invalid (separate error classes for missing signature, malformed encoding, signer pubkey unavailable, and cryptographic mismatch).
- Add tests for positive and negative developer-signature paths (wrong key, wrong block-messages hash, wrong block `escrow_id`, malformed signature bytes, missing signature fields).

## Step 14.2: Executor `FinishInference` Signatures and Transport

### Step 14.2 Summary

Add a minimal executor proof that binds the final `response_payload_hash` to the developer-signed request block, and return that proof to the developer for persistence and verification.

### Step 14.2 Requirements

- Keep the proof minimal: executor signs only what is strictly needed to prove linkage between request and response.
- Signature algorithm/type should match existing v2 signature path: Cosmos SDK `secp256k1` with base64-encoded signature bytes.
- Use domain-separated signing payload `v2_exec_finish_sig_v1`.
- Define canonical signing preimage bytes including only:
  - `developer_request_block_signature`,
  - `response_payload_hash`.
- Sign `sha256(canonical_preimage_bytes)` with executor `secp256k1`.
- Rationale for minimal fields: `developer_request_block_signature` already commits request context (`chain_id`, `escrow_id`, `block_sequence`, and canonical block messages), so these fields do not need to be re-signed in Step 14.2.
- Return proof metadata to developer with response:
  - non-streaming responses: return `executor_address`, `executor_signer_address`, `executor_signer_pubkey`, and `executor_signature` in response headers,
  - streaming responses: return `executor_address`, `executor_signer_address`, `executor_signer_pubkey`, and `executor_signature` in terminal stream event (or equivalent terminal proof channel), since final `response_payload_hash` is known at stream end.
- Developer must verify executor signature against executor pubkey/address and locally recomputed `response_payload_hash`.
- Add tests for positive and negative executor-proof paths (wrong key, wrong response hash, malformed signature bytes, missing proof fields).

## Step 14.3: Developer Ingestion of Executor Proof and Executor-Side `FinishInference` Proof Validation

### Step 14.3 Summary

Consume Step 14.2 proof metadata on the developer side, persist it into `FinishInference`, and enforce proof verification on API/executor nodes when `FinishInference` messages are received in later chain deltas.

### Step 14.3 Requirements

- Developer client must parse executor proof metadata from v2 responses:
  - non-streaming: `X-V2-Executor-Address`, `X-V2-Executor-Signature`,
  - streaming: terminal `v2_executor_proof` SSE event payload.
- Developer must resolve executor pubkey by executor address (source of truth: participant query endpoint, e.g. `GET /v1/participants/:address`, backed by chain data).
- Developer must verify proof signature against locally recomputed `response_payload_hash` and the request block signature before appending `FinishInference`.
- Extend developer-chain `FinishInference` payload to carry executor proof fields (exact schema names to be finalized).
- API/executor and relay nodes must validate `FinishInference` executor proof on ingest:
  - verify `executor_address` corresponds to a known participant pubkey,
  - verify signature over Step 14.2 canonical payload (`developer_request_block_signature`, `response_payload_hash`),
  - reject malformed/missing/invalid proof deterministically.
- Add tests (unit + E2E) for:
  - valid proof ingestion and replay,
  - wrong executor key,
  - wrong `response_payload_hash`,
  - malformed/missing proof fields,
  - streaming terminal-proof ingestion path.

## Step 15: Conflicting Signed Block Detection and On-Chain Escrow Invalidation

### Step 15 Summary

Detect developer equivocation for the same block identity and finalize invalidation on-chain via `MsgInvalidateEscrow` carrying conflict evidence.

Status: implemented.

### Step 15 Requirements

- Define conflict identity key explicitly as `(developer_address, escrow_id, block_sequence)`.
- Detect conflict when two distinct canonical block payload hashes exist for the same conflict identity and both carry valid developer signatures.
- Treat exact duplicates (same canonical hash/signature) as idempotent replay, not conflict evidence.
- Construct deterministic conflict evidence containing:
  - conflict identity key,
  - both canonical payload hashes,
  - both conflicting signed block payloads (or canonical byte/hash references),
  - both signatures and signer identities.
- The intended executor (or node that detects conflict first) must submit on-chain `MsgInvalidateEscrow` with conflict evidence.
- Chain must validate evidence deterministically and transition escrow state to `INVALIDATED_CONFLICT` on success.
- After invalidation, all new requests for that escrow must be rejected deterministically on both intended and relay paths.
- Define in-flight behavior explicitly: requests accepted before invalidation may complete; all subsequent requests are rejected.
- Add tests for:
  - conflict detection from two different signed blocks with same identity,
  - successful on-chain `MsgInvalidateEscrow` with valid evidence,
  - rejection of `MsgInvalidateEscrow` with malformed/insufficient evidence,
  - non-conflict idempotent replay,
  - deterministic rejection after invalidation from both intended and relay participants.

## Step 16: Relay Non-Response Signed Errors and `MissedInference` Quorum

### Step 16 Summary

When relay to intended executor fails or times out, return a signed relay-error artifact. Developer aggregates these signed errors and, once majority-by-weight threshold is met, appends a `MissedInference` message in the next block.

### Step 16 Requirements

- Define deterministic relay failure conditions that produce a relay error artifact (timeout, transport failure, explicit unavailable response).
- Relay node must return an error payload signed by relay signer, containing at minimum:
  - `escrow_id`,
  - `request_id`,
  - intended executor identity,
  - relay identity,
  - failure reason code/class.
- Developer must collect signed relay-error artifacts for a request and verify signatures/participant authorization before counting them.
- Define quorum rule as majority-by-weight over responsible participants for the request.
- Once quorum is reached, developer must append `MissedInference` message to the next developer block for that `request_id`.
- API/executor/relay ingest must validate `MissedInference` message structure and quorum evidence deterministically.
- Add tests for:
  - insufficient signed-error weight (no `MissedInference` allowed),
  - sufficient signed-error weight (valid `MissedInference` accepted),
  - malformed/forged relay-error signatures rejected.

## Step 17: Deterministic Executor State and Per-Block State Hash

### Step 17 Summary

Introduce deterministic state tracking on developer and executor sides for per-executor processed inferences, input/output token totals, and missed inferences, with each block committing a post-apply `state_hash`.

### Step 17 Requirements

- Define deterministic state schema maintained by both developer and executors, including:
  - per-executor count of processed inferences,
  - per-executor input token totals,
  - per-executor output token totals,
  - per-executor missed-inference count.
- Define message application rules (`StartInference`, `FinishInference`, `MissedInference`) and deterministic ordering semantics.
- After applying all messages in a block, compute canonical `state_hash` and include it in block payload/signing domain.
- Ingest validation must recompute post-apply state and reject blocks whose `state_hash` mismatches.
- Add tests for deterministic state convergence across developer and executor nodes and mismatch rejection behavior.

## Step 18: Streaming Replay Hub Hardening (Backpressure and Lifecycle)

### Step 18 Summary

Streaming replay now supports delivering buffered history + live tail to duplicate callers of the same logical request. The next step is production hardening for memory safety and slow-consumer behavior.

### Step 18 Requirements

- Add explicit backpressure policy for stream subscribers (bounded buffers, deterministic drop/close behavior for slow consumers).
- Add TTL/eviction for completed stream replay entries to prevent unbounded memory growth.
- Add limits/guardrails for per-request replay buffer size and total replay-cache footprint.
- Add observability: metrics/logs for subscriber count, dropped subscribers, replay-buffer sizes, and eviction counts.
- Add tests for slow subscribers, many subscribers, and replay-cache cleanup/eviction behavior.

## Step 19: Persist v2 Request/Response Payloads Locally (v1-Parity)

### Step 19 Summary

Add local payload persistence for v2 requests/responses, mirroring existing v1 payload-storage behavior to improve post-mortem debugging, verification workflows, and replay diagnostics.

### Step 19 Requirements

- Persist v2 `openai_request` payload and final response payload locally using the same storage conventions used for v1 payload storage.
- Key persisted payloads by deterministic request identity (at minimum `request_id`, with enough scope metadata for uniqueness).
- Ensure both intended-executor and relay paths produce consistent payload records for completed requests.
- Persist deterministic failure payload/metadata for failed requests where response payload is unavailable.
- Add retention/cleanup policy and bounds consistent with v1 storage expectations.
- Add tests validating payload persistence for:
  - successful non-streaming v2 requests,
  - successful streaming v2 requests,
  - deterministic failure cases.

## Step 20: Epoch-Pinned v2 Routing and Model-Group Resolution

### Step 20 Summary

Eliminate epoch-boundary ambiguity by assigning each escrow to a fixed `epoch_id` at creation time, and always using that escrow epoch for model-group lookup and participant URL resolution.

### Step 20 Requirements

- Add `epoch_id` to `escrow_created` event and store it in API-side escrow access record (`escrow_id -> epoch_id`).
- API must use escrow-assigned `epoch_id` (not tracker `LatestEpoch`) to resolve:
  - model epoch group data used for deterministic responsible-participant selection,
  - active participant inference URL cache lookups for relay/forwarding paths.
- Keep deterministic participant draw inputs unchanged (`escrow_id + sequence`) while changing only which epoch/model group is used as the candidate set.
- Retries/replays for an escrow must always use the escrow-assigned `epoch_id` implicitly (no per-request epoch switching).
- Optional request `epoch_id` transport (if provided by clients) must match escrow-assigned `epoch_id` or be rejected deterministically.
- Testermint should resolve and pin escrow epoch once per fixture and keep it consistent for overlap/retry/relay scenarios.
- Add tests for:
  - boundary transition safety (request formed near epoch switch still resolves correct model group),
  - escrow event contains `epoch_id` and API stores it in escrow access index,
  - optional mismatched request `epoch_id` is rejected when provided.

## Step 21: Escrow Lifecycle Closure and Settlement (TBD)

### Step 21 Summary

Define end-of-lifecycle behavior for v2 escrows so `MsgCreateEscrow`-opened access does not remain indefinitely valid after its intended epoch window, and add an explicit settlement message flow.

### Step 21 Requirements

- Add escrow-expiration/closure semantics tied to epoch progression (escrow opened by `MsgCreateEscrow` must be closed after epoch end under defined rules).
- Introduce `MsgSettleEscrow` (name/fields to be finalized) for explicit settlement/finalization.
- Define deterministic behavior for late requests after escrow closure (reject path, error shape, and replay semantics).
- Define accounting hooks for settlement outcomes (success/failure/timeout cases), with exact on-chain state transitions to be specified later.
- Add tests (to be detailed later) covering:
  - escrow closure at/after epoch boundary,
  - settlement success path,
  - settlement rejection for invalid state transitions.

## Step 22: Active-Participant Signer Snapshot Refactor (Future)

### Step 22 Summary

Refactor signer authorization lookup so warm/cold signer material needed for v2 executor-proof verification is served from epoch-pinned active-participant snapshots instead of ad-hoc authz queries at request time.

### Step 22 Requirements

- Extend active-participant epoch snapshots to include authorized signer set for each participant (cold + warm signer addresses/pubkeys), with message-type scope.
- Keep epoch-pinned routing semantics: signer authorization must resolve against the escrow-assigned epoch snapshot.
- Replace per-request signer authz queries in v2 `FinishInference` proof validation with snapshot-backed lookup (with deterministic fallback policy if snapshot data is missing).
- Ensure relay and intended-executor nodes use the same snapshot-backed signer-authority source.
- Add tests for:
  - warm-key signer proof accepted via snapshot data,
  - unauthorized signer rejected deterministically,
  - epoch-boundary replay still validates against escrow-pinned epoch signer set.

## Step 23: gRPC ActiveParticipants + Authorized Signer Set Query

### Step 23 Summary

Replace raw store-key reads for active participant metadata with a typed gRPC query that returns participant inference metadata and authorized signer keys in one response.

### Step 23 Requirements

- Add chain query endpoint `QueryActiveParticipants(epoch_id)` (or equivalent) to return active participants for a target epoch via gRPC.
- Include in response:
  - participant address/index,
  - inference URL,
  - validator (cold) pubkey,
  - authorized signer set for `MsgFinishInference` (address + pubkey; cold + warm keys).
- Update `decentralized-api` epoch cache prewarm to use this gRPC query instead of raw `QueryByKey` for active participants.
- Remove dependency on passing chain RPC URL into pubkey/URL prewarm lookup paths once gRPC response is the source of truth.
- Add tests for:
  - parity with existing active participant routing behavior,
  - cold-key and warm-key signer proof validation using gRPC-provided signer set,
  - deterministic failure when signer is missing from authorized set.

## Step-to-Test Mapping (Current Status)

- **Step 1**: v2 endpoint added in parallel with v1; covered by `post_chat_v2_handler` tests and existing v1 tests in same service.
- **Step 2**: escrow authorization path covered by:
  - chain tests for `MsgCreateEscrow`,
  - API tests that reject unauthorized/mismatched escrow access.
- **Step 3**: deterministic responsible-participant selection covered by:
  - deterministic selection unit tests,
  - handler tests rejecting non-responsible participant.
- **Step 4-6**: chain-delta envelope/validation/storage covered by API tests for:
  - required envelope fields,
  - monotonic `block_sequence` checks,
  - `base_block_sequence` mismatch rejection,
  - current-request linkage checks.
- **Step 7**: `V2EscrowFlowTest` passed with:
  - 3-participant cluster (`genesis` + `join1` + `join2`),
  - developer creates escrow on-chain,
  - 10 successful v2 requests with deterministic executor routing by `escrow_id + sequence`,
  - negative check for stale `base_block_sequence` rejection.
- **Step 8**: overlap acceptance and replay safety implemented in `decentralized-api`:
  - stale `base_block_sequence` is accepted when overlapping blocks exactly match receiver-local chain state,
  - overlap mismatch is rejected deterministically (`developer_chain_delta overlap does not match receiver chain`),
  - identical replay with same sequence is accepted when it does not advance chain,
  - non-increasing sequence is rejected only when replay attempts to advance chain,
  - pre-validation failures do not consume sequence (same sequence can be retried with corrected payload).
- **Step 8 tests added**:
  - API unit coverage in `post_chat_v2_handler_test` for matching-overlap acceptance, mismatch rejection, identical replay, and no-sequence-burn retry path.
  - Testermint scenario `developer sends overlapping v2 streaming requests in parallel` passed (12 concurrent requests with 5s mock response delay), with overlap acceptance visible in logs (`V2 chain overlap accepted`).
- **Step 9**: request-payload hash binding implemented:
  - `StartInference.request_payload_hash` is required and validated against `openai_request` in API pre-processing,
  - hash uses SHA-256 over exact `openai_request` bytes (no canonicalization rewrite),
  - API unit tests cover positive path, missing hash, and mismatch rejection.
- **Step 10 (implemented)**:
  - Testermint developer chain controller records `FinishInference { request_id, status, response_payload_hash }` after non-streaming responses,
  - queued `FinishInference` messages are appended to the next newly formed block (with exactly one `StartInference`),
  - API validates `FinishInference` format (`status`, `response_payload_hash`) and linkage (`request_id` must reference a known prior `StartInference`) before execution.
- **Step 11 (implemented)**:
  - relay behavior for non-intended responsible participants is covered in API tests and Testermint flows,
  - Testermint includes retry/fanout of same logical request through all responsible participants,
  - Testermint includes relayed streaming request path via non-intended participant.
- **Step 12 (implemented)**:
  - relay participants ingest and validate `developer_chain_delta` before forwarding,
  - relay-side overlap mismatch rejection is covered by Testermint scenario `relay participant ingests chain and rejects overlap mismatch before forwarding`.
- **Step 13 (implemented)**:
  - intended execution continues when caller connection is cancelled (execution uses non-cancelled context),
  - streaming leader path is decoupled from client socket via background stream pump + replay hub,
  - dedupe replays completed deterministic failures for later duplicates instead of returning generic "already processed without replayable response",
  - Testermint `leader disconnect mid-stream and follower still receives replay` passed,
  - Testermint `leader timeout on non-streaming and follower still gets completed replay` passed.
- **Step 14.1 (implemented)**:
  - developer blocks now carry `escrow_id` and `signature` (no signer address/pubkey in block),
  - API validates domain-separated block signatures over canonical `block_messages_hash` and block identity (`chain_id`, `escrow_id`, `block_sequence`),
  - signer identity is derived from stored escrow access (`escrow_id -> developer_address`) and developer pubkey resolution, not from block fields,
  - API rejects deterministic signature failures for missing signatures, malformed encodings, signer pubkey unavailable, and cryptographic mismatch,
  - API unit tests include positive path coverage plus negative developer-signature paths,
  - Testermint developer-chain builders now sign blocks for v2 requests (compile verification: `testClasses`).
- **Step 14.2 (implemented)**:
  - API creates domain-separated executor proof signatures over `sha256(v2_exec_finish_sig_v1 || developer_request_block_signature || response_payload_hash)`,
  - non-streaming responses now return `X-V2-Executor-Address`, `X-V2-Executor-Signer-Address`, `X-V2-Executor-Signer-PubKey`, and `X-V2-Executor-Signature` headers,
  - streaming responses now include terminal SSE event `event: v2_executor_proof` with `executor_address`, `executor_signer_address`, `executor_signer_pubkey`, and `executor_signature`,
  - API unit tests cover proof transport on non-streaming and streaming paths,
  - API unit tests include executor-proof signature validation helper coverage for valid and negative cases (wrong key, wrong response hash, malformed signature, missing fields).
- **Step 14.3 (implemented)**:
  - developer-side non-streaming ingestion now persists executor proof fields in `FinishInference` messages (`executor_address`, `executor_signer_address`, `executor_signer_pubkey`, `executor_signature`) with local proof-signature verification before append,
  - streaming developer path now ingests terminal `v2_executor_proof` SSE event and verifies proof signature against locally recomputed stream payload hash in Testermint flow,
  - API/executor/relay chain-delta validation now enforces executor proof presence and verifies signatures against request-linked start block signature and `response_payload_hash`,
  - signer authorization is enforced against authz-backed signer lookup (`MsgFinishInference` scope) when available.
- **Step 15 (implemented)**:
  - overlap mismatch detection now surfaces conflicting block evidence (same `escrow_id` + `block_sequence`, different signed block),
  - intended executor submits on-chain `MsgInvalidateEscrow` with conflicting block hashes, block message hashes, and signatures,
  - chain recomputes canonical conflict hashes and verifies both developer signatures before setting invalidated state,
  - chain emits `escrow_invalidated` event, and decentralized-api nodes ingest this event to mark escrow invalidated locally,
  - all nodes reject subsequent requests for invalidated escrow with deterministic conflict error (`Escrow is invalidated due to conflicting signed blocks`),
  - Testermint E2E scenario added for conflict -> on-chain invalidation -> cluster-wide rejection.
- **Step 22 (planned)**:
  - move signer-authorization source from per-request authz lookup to epoch-pinned active-participant signer snapshots for deterministic warm-key verification across replays/boundaries.
- **Step 23 (planned)**:
  - add typed gRPC query for active participants with authorized signer sets and migrate cache prewarm off raw store-key queries.

## End-to-End v2 Flow (Initial)

1. Developer sends `MsgCreateEscrow(model_id)` on-chain.
2. Chain persists escrow mapping and emits `escrow_created` event.
3. `decentralized-api` ingests event and updates local in-memory access index.
4. Developer sends a request to `/v2/chat/completions` with `escrow_id` and `sequence` headers.
5. API verifies developer access from local index.
6. API computes deterministic responsible participants (`N` from network params, default `4`) from `escrow_id + sequence`.
7. Request is handled through v2 execution path (parallel to v1).

## Implementation Checklist

- [x] Add proposal-specific v2 endpoint contract and request header schema.
- [x] Implement `MsgCreateEscrow(model_id)` changes in chain module.
- [x] Add escrow-created event emission and tests.
- [x] Implement `decentralized-api` event listener/indexer for escrow records.
- [x] Gate v2 endpoint by local escrow access records.
- [x] Implement deterministic weighted participant selector with network param `N` (default `4`).
- [x] Define and implement inference-chain block format (`block_sequence` + messages only).
- [x] Add v2 request envelope carrying `openai_request` + chain delta.
- [x] Implement API-node pre-processing chain validation/storage (`block_sequence` continuity only).
- [x] Enforce exactly one `StartInference` per block and use `block_sequence` as sequence source.
- [x] Update Testermint to form developer chain and validate end-to-end behavior.
- [x] Step 8: accept overlapping in-flight requests (parallel request support in API + unit tests).
- [x] Step 9: add/validate `StartInference.request_payload_hash`.
- [x] Step 10: add/propagate `FinishInference.response_payload_hash`.
- [x] Step 11: implement multi-executor retry + relay + streaming/failure semantics.
- [x] Step 12: relay-side developer chain ingestion/validation and unknown-block append.
- [x] Step 13: leader/client disconnect resilience and replay continuity for same `request_id`.
- [x] Step 14.1: add/validate developer block signatures.
- [x] Step 14.2: add/validate executor `FinishInference` signatures and response transport.
- [x] Step 14.3: ingest executor proofs into `FinishInference` and validate on executor/relay ingest.
- [x] Step 15: conflicting signed block evidence + on-chain `MsgInvalidateEscrow`.
- [ ] Step 16: signed relay non-response errors + majority-by-weight `MissedInference`.
- [ ] Step 17: deterministic per-executor state accounting + per-block `state_hash`.
- [ ] Step 18: streaming replay hub hardening (backpressure, limits, and cache eviction).
- [ ] Step 19: persist v2 request/response payloads locally (v1-parity behavior).
- [ ] Step 20: pin v2 routing to explicit `epoch_id` for epoch/model-group lookups.
- [ ] Step 21: close `MsgCreateEscrow` lifecycle after epoch end and add `MsgSettleEscrow`.
- [ ] Step 22: refactor signer authority into epoch-pinned active-participant snapshots.
- [ ] Step 23: migrate active-participant/signer prewarm to typed gRPC query response.
- [ ] Add tests proving:
  - [ ] v1 and v2 parallel operation,
  - [x] v2 authorization enforcement,
  - [x] deterministic participant resolution for identical inputs,
  - [x] overlap acceptance under parallel in-flight requests (API unit tests),
  - [x] overlap acceptance under parallel in-flight requests (Testermint E2E execution run),
  - [x] payload-hash validation on start/finish messages,
  - [x] relay/stream/failure behavior for executor fallback.
  - [x] relay-side chain-delta ingestion and checkpoint updates.
  - [x] disconnect resilience with successful replay for same `request_id`.
  - [x] developer block-signature validation (API unit tests + Testermint signing path compile coverage).
  - [x] signature validation for executor finish messages.
  - [x] developer ingestion and persistence of executor proof fields in `FinishInference` (non-streaming path).
  - [x] executor/relay validation of received `FinishInference` executor proofs.
  - [x] streaming terminal-proof ingestion and signature verification in Testermint stream flow.
  - [x] deterministic escrow invalidation on conflicting signed blocks.
  - [x] on-chain rejection for malformed/invalid conflict evidence (hash/signature mismatch).
  - [ ] relay signed-error quorum path resulting in valid `MissedInference`.
  - [ ] deterministic state-hash convergence across developer/executor nodes.
  - [ ] streaming replay backpressure handling and cleanup/eviction behavior.
  - [ ] epoch-pinned v2 routing correctness across epoch boundaries.
  - [ ] escrow closure/settlement lifecycle correctness after epoch end.

## Testermint Smoke Test Instructions (v2 Escrow + 10 Requests)

Validation coverage for this smoke test is described in **Step-to-Test Mapping (Current Status)** above.

### How to Run

1. Rebuild local test-net images (do it each time before you run test):

   ```bash
   cd local-test-net
   ./stop-rebuild.sh
   ```

2. Wait until all Docker builds/containers from that command are ready.

3. Run just this test:

   ```bash
   cd testermint
   ./gradlew :test --tests "V2EscrowFlowTest.developer uses v2 api with escrow across 3 participants" -i --console=plain -Dtest.logging.events=passed,skipped,failed -DexcludeTags=unstable,exclude
   ```

4. Check logs for flow markers and routing decisions:

   ```bash
   rg "V2_ESCROW_TEST|escrow_created|/v2/chat/completions" logs
   ```

## Open Questions for Next Steps

- Sequence source of truth: API-managed counter vs developer-provided monotonic sequence with replay checks.
- Exact weighted-sampling algorithm (weighted shuffle vs without-replacement sampling method).
- Failure handling when one or more selected participants are unavailable.
- Settlement, rewards, and dispute/invalidation flow for v2 responses.
