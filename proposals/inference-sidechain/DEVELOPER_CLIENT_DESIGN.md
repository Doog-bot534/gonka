# Developer-Side v2 Client Design

## Short Answer

Yes, this is possible.

The current proposal and `V2EscrowFlowTest` already imply a clear developer-side state machine. In practice, the existing `DeveloperChainController` in the test is already the kernel of such a client: it allocates per-escrow sequence numbers, chooses recipients deterministically, builds chain deltas, tracks receiver acknowledgements, verifies executor proofs, and queues `FinishInference` / `MissedInference` messages for the next block.

The important caveat is this:

- a single opaque `client.send(request): result | err` API is good for normal usage,
- but it is not enough by itself for all tests,
- to cover negative and protocol-level cases, the same client must also support injected dependencies and test-only overrides.

So the right design is not "one huge method with hidden internals". The right design is:

1. a small public client API for normal developer usage,
2. a reusable per-escrow state engine inside it,
3. injectable transport / resolver / signer / storage dependencies,
4. a test harness or test overrides layer built on the same engine.

## Why This Fits The Current Flow

From the proposal and test flow, the developer-side client is responsible for all of the following:

- creating or attaching to an escrow,
- remembering `escrow_id`, `epoch_id`, and `model_id`,
- allocating monotonically increasing `sequence` values per escrow,
- deterministically selecting responsible participants from `escrow_id + sequence`,
- building `developer_chain_delta`,
- tracking `latest_block_sequence` per receiver,
- supporting overlap/parallel in-flight requests,
- hashing request and response payloads,
- signing developer blocks,
- verifying executor proofs,
- retrying the same logical request through responsible participants,
- collecting signed relay failures,
- appending `MissedInference` on quorum,
- maintaining deterministic developer-side state and `state_hash`.

That is exactly the kind of logic that belongs in a dedicated client library.

## Recommended Shape

Use a two-level design.

### 1. Top-Level Client

This object owns shared dependencies and can create or attach to multiple escrows.

Suggested shape:

```kotlin
interface InferenceV2Client {
    suspend fun createEscrow(request: CreateEscrowRequest): EscrowSessionClient
    suspend fun attachEscrow(context: EscrowContext): EscrowSessionClient
}
```

### 2. Per-Escrow Session Client

This object owns the mutex, chain state, acknowledgement map, pending finish/missed messages, and sequence allocation for one escrow.

Suggested shape:

```kotlin
interface EscrowSessionClient {
    suspend fun complete(
        request: InferenceRequestPayload,
        options: RequestOptions = RequestOptions(),
    ): ClientOutcome<ClientResult<OpenAIResponse>>

    suspend fun stream(
        request: InferenceRequestPayload,
        options: RequestOptions = RequestOptions(),
    ): ClientOutcome<ClientStreamResult>

    suspend fun retry(
        handle: RetryHandle,
        options: RetryOptions = RetryOptions(),
    ): ClientOutcome<RetryResult>

    fun snapshot(): EscrowClientSnapshot
}
```

This split matches the protocol well, because sequence and chain continuity are escrow-scoped, not global.

The public API should not expose manual lifecycle methods like:

- `recordFinishInference(...)`
- `recordMissedInference(...)`

Those should be internal consequences of protocol flow:

- `FinishInference` should be recorded automatically when a non-streaming request succeeds.
- For streaming, the client should own the terminal proof/hash path and record `FinishInference` automatically when the managed stream reaches normal completion.
- `MissedInference` should be queued automatically when the client has enough valid signed relay-failure artifacts to satisfy quorum.

## Recommended Internal Components

Internally, the client should be composed from small parts.

### Core Engine

This is the extracted version of the current `DeveloperChainController`.

Responsibilities:

- reserve next sequence,
- choose intended recipient,
- build signed block and `developer_chain_delta`,
- track `acknowledgedByRecipient`,
- record `FinishInference`,
- record `MissedInference`,
- maintain deterministic state and `state_hash`.

Suggested name:

- `EscrowChainEngine`

### Transport Layer

Responsibilities:

- send non-streaming v2 requests,
- open streaming v2 requests,
- make raw calls when tests need exact status/body access,
- support timeouts and cancellation,
- allow participant-targeted sends.

For streaming, the client should prefer a managed stream wrapper over returning only the raw HTTP stream.
That wrapper should:

- observe all streamed bytes/events,
- capture the terminal executor proof event,
- compute final `response_payload_hash`,
- record `FinishInference` automatically on normal completion,
- avoid recording `FinishInference` when the stream is abandoned before terminal completion.

Suggested name:

- `V2ParticipantTransport`

### Participant Snapshot / Routing Layer

Responsibilities:

- resolve eligible participants for the escrow/model/epoch,
- expose weights, inference URLs, signer pubkeys, and signer authorization info,
- deterministically select responsible participants.

Suggested names:

- `ParticipantSnapshotProvider`
- `ResponsibleParticipantSelector`

### Persistence Layer

Responsibilities:

- persist per-escrow chain state,
- persist per-recipient latest acknowledged block sequence,
- persist pending finish/missed messages,
- optionally persist payloads later for Step 19.

Suggested name:

- `EscrowChainStateStore`

### Verification Layer

Responsibilities:

- compute request/response hashes,
- verify executor proof signatures,
- verify relay-error artifact signatures,
- verify `MissedInference` quorum inputs.

Suggested names:

- `PayloadHasher`
- `ExecutorProofVerifier`
- `RelayArtifactVerifier`

### Canonical Signing Contract

Developer block signing is especially sensitive to canonical serialization details.

Requirements:

- The client must use exactly the same canonical byte layout that API/executor validation expects for developer-chain messages and signed blocks.
- Signature-critical fields must remain in canonical order even when they are empty or optional.
- The client must not "simplify" canonical serialization by dropping empty fields that are still part of the signed byte sequence.
- If possible, canonical message hashing/signing code should be shared between the developer client and validation side. If it is not shared, it must be kept bit-for-bit identical.

Why this matters:

- A request can look structurally valid and still be rejected as `developer_chain_delta block signature is invalid` if the client omits or reorders even one signed field.
- This applies not only to obvious fields like `request_payload_hash` and `status`, but also to optional fields such as `missed_inference_evidence` when those fields are part of canonical message bytes.

## Parameters The Client Should Have

The easiest way to keep this maintainable is to separate parameters into:

- constructor parameters,
- escrow-session parameters,
- per-request parameters,
- test-only parameters.

## Constructor Parameters

These should be required when building the top-level client.

```kotlin
data class InferenceV2ClientConfig(
    val developerAddress: String,
    val chainId: String,
    val developerSigner: DeveloperSigner,
    val escrowGateway: EscrowGateway,
    val participantSnapshotProvider: ParticipantSnapshotProvider,
    val transport: V2ParticipantTransport,
    val stateStore: EscrowChainStateStore,
    val clock: Clock,
    val payloadHasher: PayloadHasher,
    val executorProofVerifier: ExecutorProofVerifier,
    val relayArtifactVerifier: RelayArtifactVerifier,
    val responsibleParticipantCountProvider: ResponsibleParticipantCountProvider,
    val retryPolicy: RetryPolicy = RetryPolicy.default(),
    val logger: ClientLogger = NoopClientLogger,
    val testKit: DeveloperClientTestKit? = null,
)
```

### What Each Constructor Parameter Is For

- `developerAddress`
  Used for escrow ownership, request headers, and signer identity.

- `chainId`
  Required for developer block signing.

- `developerSigner`
  Signs developer blocks. This should be an interface, not a concrete node wrapper.

- `escrowGateway`
  Creates escrow on-chain and returns `escrow_id` plus `epoch_id`.

- `participantSnapshotProvider`
  Returns participant metadata needed for deterministic routing and proof validation.

- `transport`
  Sends requests to chosen participants and exposes both standard and raw/streaming paths.

- `stateStore`
  Holds per-escrow chain state. In tests this can be in-memory; in production it can become durable later.

- `clock`
  Needed so timestamps are deterministic in tests.

- `payloadHasher`
  Keeps request/response hashing consistent across developer client and API node.

- `executorProofVerifier`
  Verifies Step 14.2 / 14.3 executor proof before `FinishInference` is appended.

- `relayArtifactVerifier`
  Verifies Step 16 signed relay failures before they count toward quorum.

- `responsibleParticipantCountProvider`
  Avoids hardcoding `N`; proposal says it should come from network params.

- `retryPolicy`
  Controls timeout, relay fallback, and fanout behavior.

- `logger`
  Optional, but useful for debugging multi-hop retry cases.

- `testKit`
  Optional bundle for overrides, probes, and fault injection. This is the key to making the same client usable in all tests.

## Escrow-Session Parameters

These belong to the per-escrow session object, not to each request.

```kotlin
data class EscrowContext(
    val escrowId: String,
    val epochId: Long,
    val modelId: String,
)
```

Recommended session-internal state:

- `latestReservedSequence`
- `chainBlocks`
- `deterministicState`
- `pendingFinishMessagesByRequestSequence`
- `pendingMissedMessagesByRequestSequence`
- `acknowledgedByRecipient`
- one mutex per escrow

## Per-Request Parameters

The public request surface should stay small.

```kotlin
data class RequestOptions(
    val timeout: Duration? = null,
    val leaderTimeout: Duration? = null,
    val preferredInitialRecipient: String? = null,
    val sendToAllResponsible: Boolean = false,
    val allowFanoutOnTimeout: Boolean = true,
    val includeDiagnostics: Boolean = false,
    val overrides: RequestTestOverrides? = null,
)
```

Recommended retry options:

```kotlin
data class RetryOptions(
    val preferredInitialRecipient: String? = null,
    val resendToAllResponsible: Boolean = false,
    val includeDiagnostics: Boolean = false,
)
```

### Minimum Request Inputs

For normal usage, the client should only need:

- `InferenceRequestPayload`
- optionally `RequestOptions`

Everything else should be internal:

- sequence allocation,
- request id formation,
- participant selection,
- chain delta formation,
- request/response hashing,
- proof verification,
- finish/missed persistence.

### Why `preferredInitialRecipient` Is Useful

This is not for normal application code.
It is useful for tests like:

- send through a non-intended responsible participant,
- start from follower instead of intended executor,
- force a relay path deterministically.

`preferredInitialRecipient` should mean "first hop only".
It should not mean fanout.

### What Fanout Means

Fanout should mean:

- send the same logical request to all responsible participants for that `escrow_id + sequence`,
- not broadcast to all participants in the entire network.

That matches the Step 11 retry/fallback model in the proposal.

## Result Shape

Returning only raw OpenAI payload is too small for tests.

Recommended top-level outcome wrapper:

```kotlin
sealed interface ClientOutcome<out T>

data class ClientSuccess<T>(
    val result: T,
) : ClientOutcome<T>

data class ClientFailure(
    val error: ClientError,
    val retryHandle: RetryHandle? = null,
    val relayArtifacts: List<RelayErrorArtifact> = emptyList(),
    val missedInferenceQueued: Boolean = false,
    val attempts: List<AttemptTrace> = emptyList(),
) : ClientOutcome<Nothing>
```

Recommended retry handle:

```kotlin
data class RetryHandle(
    val escrowId: String,
    val sequence: Long,
    val requestId: String,
    val responsibleParticipants: List<String>,
    val requestPayloadHash: String,
    val stream: Boolean,
    val requestStateRef: String,
)
```

`RetryHandle` should identify the same logical request, not store a finalized transport body.
In particular, it should not require callers to hold a prebuilt `developer_chain_delta` JSON envelope for replay.

On retry, the controller may need to rebuild the recipient-specific transport envelope because:

- `base_block_sequence` can differ per recipient,
- the included `blocks[]` can differ per recipient,
- the current recipient may acknowledge a different latest known chain tip than a previous recipient.

So the handle should preserve logical request identity and immutable request facts, for example:

- same `escrowId`,
- same `sequence`,
- same `requestId`,
- same `requestPayloadHash`,
- same stream/non-stream mode,
- same reserved request state via `requestStateRef`.

Then `retry(handle, ...)` can ask the internal controller to regenerate the correct `developer_chain_delta` and final request body for the selected recipient.

Recommended retry result:

```kotlin
sealed interface RetryResult

data class RetryCompletion(
    val result: ClientResult<OpenAIResponse>,
) : RetryResult

data class RetryStream(
    val result: ClientStreamResult,
) : RetryResult
```

Recommended non-streaming result:

```kotlin
data class ClientResult<T>(
    val value: T,
    val requestId: String,
    val sequence: Long,
    val escrowId: String,
    val latestBlockSequence: Long,
    val recipientAddress: String,
    val executorAddress: String?,
    val executorSignerAddress: String? = null,
    val executorSignerPubKey: String? = null,
    val executorSignature: String? = null,
    val responsePayloadHash: String? = null,
    val attempts: List<AttemptTrace> = emptyList(),
)
```

Recommended streaming result:

```kotlin
data class ClientStreamResult(
    val requestId: String,
    val sequence: Long,
    val escrowId: String,
    val latestBlockSequence: Long,
    val recipientAddress: String,
    val stream: ManagedStreamHandle,
)
```

This keeps the client usable for normal code while still giving tests enough metadata to assert routing and replay behavior.

The important point is that errors should return a `RetryHandle`, not just a bare `sequence`.
Returning only `sequence` is too weak because safe replay depends on preserving the same logical request state, not just reusing the same number.
The final recipient-specific envelope should still be rebuilt internally by the controller on resend.

## What Must Stay Internal

These should not be required from the caller on every request:

- `escrow_id` header construction,
- `sequence`,
- `epoch_id`,
- `developer_chain_delta`,
- `base_block_sequence`,
- developer block signature,
- `request_payload_hash`,
- `response_payload_hash`,
- executor proof verification,
- `MissedInference` evidence serialization.

If callers must pass these every time, the client is not actually doing the developer-side job.

The caller also should not have to construct `RetryHandle` values manually.
Those should be created by the client only after a logical request has already been prepared internally.
The caller also should not have to build resend envelopes manually; retries should go back through the controller so it can compute the correct recipient-specific delta.
The caller also should not manually decide when to write `MissedInference`; that should be driven by validated relay artifacts and quorum rules inside the client.

## Testability Requirements

This is the most important part.

If the goal is to use the client in all tests, including negative tests, then the client must expose controlled hooks for fault injection and observation.

## Required Test Hooks

Recommended test-only interfaces:

```kotlin
data class DeveloperClientTestKit(
    val planMutator: ((ReservedPlan) -> ReservedPlan)? = null,
    val transportInterceptor: TransportInterceptor? = null,
    val responseMutator: ((TransportResponse) -> TransportResponse)? = null,
    val snapshotOverride: (() -> ParticipantSnapshot)? = null,
    val clockOverride: Clock? = null,
    val eventListener: ClientEventListener? = null,
)
```

Recommended per-request test overrides:

```kotlin
data class RequestTestOverrides(
    val forceBaseBlockSequence: Long? = null,
    val forceSequence: Long? = null,
    val forceResponsibleRecipients: List<String>? = null,
    val forceStateHash: String? = null,
    val tamperRequestPayloadHash: String? = null,
    val tamperResponsePayloadHash: String? = null,
    val disableAutoRecordFinish: Boolean = false,
    val fanoutMode: FanoutMode? = null,
)
```

## Why These Test Hooks Are Needed

Without them, the following cases become hard or impossible to test through the client:

- stale `base_block_sequence` rejection,
- overlap mismatch rejection,
- tampered executor proof rejection,
- same logical request sent through all responsible participants,
- send through non-intended participant first,
- leader disconnect and follower replay,
- relay failure artifact aggregation,
- forced `MissedInference`,
- mismatched `state_hash`,
- conflicting signed blocks for escrow invalidation.

In other words:

- normal tests should use `complete()` / `stream()`,
- protocol-negative tests should use the same client core with overrides,
- they should not need to rebuild request envelopes by hand unless they are specifically testing serialization itself.

One important boundary:

- positive streaming completion should not require a test hook to record `FinishInference`,
- but negative tests may still need hooks to inject tampered proof data or mutated chain envelopes.

## Retry And Non-Response Behavior

The client can support participant non-response cleanly, but it needs explicit policy.

Recommended retry policy fields:

```kotlin
data class RetryPolicy(
    val leaderTimeout: Duration,
    val relayTimeout: Duration,
    val allowFanoutOnTimeout: Boolean,
    val retryableFailureCodes: Set<String>,
    val requireDeterministicFailureAgreement: Boolean,
)
```

When fanout is enabled by policy, it should mean fanout to all responsible participants for that logical request.

Recommended behavior:

1. Internally prepare one logical request identity once.
2. Send first to intended executor, unless request options override the first recipient.
3. If `sendToAllResponsible = true`, immediately fan out to all responsible participants for that request identity.
4. If the initial send fails after the logical request was prepared, return a `RetryHandle` unless auto-fanout succeeds internally.
5. `retry(handle, ...)` must preserve the same logical request identity while letting the controller rebuild the recipient-specific envelope, either:
   - to the intended executor,
   - to one preferred responsible participant,
   - or to all responsible participants.
6. Deduplicate results by `request_id`.
7. Verify proof on success and append `FinishInference`.
8. If enough valid signed relay errors are collected, queue `MissedInference` internally and append it automatically in the next block.
9. Failure results should surface collected relay artifacts and whether `MissedInference` was queued, but the caller should not need to invoke a separate public method to record it.

This directly matches Steps 11, 13, and 16.

## Mapping To Existing Tests

The proposed client can cover the current `V2EscrowFlowTest` scenarios if designed this way.

- Smoke flow across 3 participants:
  plain `complete()`

- Parallel overlapping requests:
  concurrent `complete()` / `stream()` on the same `EscrowSessionClient`

- Non-intended participant first:
  `preferredInitialRecipient`

- Retry through all responsible participants:
  `sendToAllResponsible = true` or `retry(handle, resendToAllResponsible = true)`

- Invalid executor proof:
  `responseMutator`

- Stale continuity:
  `forceBaseBlockSequence`

- Relay signed-error quorum:
  transport interceptor plus artifact verification

- Mismatched state hash / conflicting block:
  `planMutator` or explicit `forceStateHash`

That means the answer is "yes", but only if we preserve a thin testing seam.

## Recommendation

I recommend implementing:

1. `EscrowChainEngine`
2. `EscrowSessionClient`
3. `InferenceV2Client`
4. `V2ParticipantTransport`
5. `DeveloperClientTestKit`

in that order.

The first step should be extracting the logic currently embedded in `DeveloperChainController` into `EscrowChainEngine`, because that is already the closest thing to the future developer-side client.

## Final Recommendation On API Size

For production usage, keep the public API small:

- `createEscrow(...)`
- `attachEscrow(...)`
- `complete(request, options)`
- `stream(request, options)`
- `retry(handle, options)`

The implementation may still have internal prepare/dispatch phases, but they should not be public API.

For testing, do not expand the public API endlessly.
Instead, inject:

- transport,
- snapshot provider,
- clock,
- signer,
- state store,
- test overrides / event hooks.

That gives you one reusable client for all happy-path and failure-path tests, while keeping normal developer usage simple.
