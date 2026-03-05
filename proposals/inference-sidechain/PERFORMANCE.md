# v2 Handler Performance and Reliability Improvements

Status: Proposed

This document lists concrete improvements to `post_chat_v2_handler`, `v2_chain_delta` (developer chain store), and `v2_request_dedupe` (request deduper) identified during review of the Step 8–10 implementation.

## 1. Per-Chain Locking in Developer Chain Store

**Current**: `v2DeveloperChainStore` uses a single global `sync.Mutex` for all chains. Unrelated escrows block each other under concurrent load.

**Proposed**: Use per-chain (per `chainKey`) locking via a `sync.Map` of `*sync.Mutex` or a sharded lock map. The critical section is already scoped to one chain; the global lock is unnecessary.

**Impact**: High performance gain under multi-escrow concurrent load.
**Effort**: Low.

## 2. Incremental StartInference Request ID Set

**Current**: `validateAndAppend` iterates all stored blocks (1..latestBlockSequence) to build a set of known `StartInference` request IDs on every request. This is O(N) per request as the chain grows.

**Proposed**: Maintain a persistent `Set<string>` of known `StartInference` request IDs inside `v2DeveloperChainState`, updated incrementally when blocks are appended. Validation reads the set directly — O(1) per lookup.

**Impact**: High performance gain as chains grow beyond tens of blocks.
**Effort**: Low.

## 3. Deduper Entry Eviction

**Current**: `v2RequestDeduper.entries` grows forever. Every completed request ID stays in memory indefinitely with its cached response body.

**Proposed**: Add TTL-based or sequence-window-based eviction. For example, evict entries older than N minutes or when acknowledged `latestBlockSequence` advances past the entry's sequence. This caps memory and prevents unbounded growth.

**Impact**: Prevents memory leak under sustained traffic.
**Effort**: Medium.

## 4. Per-Entry Deduper Locking

**Current**: `v2RequestDeduper` uses a single global `d.mutex.Lock()` for all request IDs. High-concurrency workloads with many distinct escrows serialize on this lock.

**Proposed**: Shard by request ID or use `sync.Map` for entry lookup, with per-entry fine-grained locking for state transitions (leader election, wait, etc.).

**Impact**: Performance gain under high concurrency with many distinct request IDs.
**Effort**: Medium.

## 5. Relay Proxy Timeout

**Current**: `relayV2CompletionToIntended` uses the server's global 20-minute HTTP client timeout. If the intended executor is slow or hung, the relay caller blocks for up to 20 minutes.

**Proposed**: Use a shorter relay-specific timeout (e.g. 60s or configurable). Optionally add a circuit breaker per intended executor address so repeated failures fast-fail instead of saturating connections.

**Impact**: Prevents relay resource exhaustion under slow/unavailable executors.
**Effort**: Low.

## 6. Relay Proxy Retry on Transient Failure

**Current**: If the intended executor returns a transient 5xx or connection reset, the relay immediately returns `ErrV2IntendedExecutorUnavailable`. No retry attempt.

**Proposed**: Add 1–2 fast retries with backoff for transport-level errors (connection refused, reset, 502/503), but not for 4xx or business-logic errors.

**Impact**: Improved reliability under transient network issues.
**Effort**: Low.

## 7. Avoid Double-Buffering Request Body for Relay Path

**Current**: Both `parsedRequest.envelopeBody` (full envelope bytes) and `parsedRequest.openAIRequestBody` are kept in memory simultaneously. For relay, the full envelope body is forwarded; for execution, only the OpenAI body.

**Proposed**: For the relay path, forward the original raw request body bytes directly instead of re-serializing. This avoids subtle re-encoding differences and saves one allocation.

**Impact**: Minor memory optimization; correctness safety.
**Effort**: Low.

## 8. Stream History Buffer Cap

**Current**: `cachedV2StreamResponse.history` accumulates the entire stream in memory via `append(r.history, chunk...)`. For large streaming responses, this can consume significant RAM per cached request.

**Proposed**: Cap history buffer size (e.g. 10 MB). If exceeded, stop caching for late followers and return a truncated/error response. Alternatively, use a ring buffer or disk-backed spillover for very large streams.

**Impact**: Prevents memory exhaustion under large streaming responses.
**Effort**: Low.

## 9. Non-Blocking Subscriber Fan-Out

**Current**: `appendChunk` sends to each subscriber channel synchronously while holding the stream mutex. If a slow subscriber can't keep up, the send blocks, stalling all other subscribers and the upstream pump.

**Proposed**: Make subscriber send non-blocking (drop or close slow subscribers). Or use a separate write goroutine per subscriber so one slow reader doesn't stall others.

**Impact**: Prevents one slow client from degrading all concurrent readers of the same stream.
**Effort**: Medium.

## 10. Bounded Execution Context for Deduped Requests

**Current**: `context.WithoutCancel(ctx.Request().Context())` ensures the inference execution continues after the original HTTP client disconnects. However, it means abandoned requests consume resources indefinitely.

**Proposed**: Use a separate timeout context (e.g. 5 min) derived from `context.Background()` instead of `WithoutCancel`. This still survives client disconnect but won't run forever.

**Impact**: Prevents unbounded resource consumption from abandoned requests.
**Effort**: Low.

## Priority Summary

| # | Area | Impact | Effort |
|---|------|--------|--------|
| 1 | Per-chain lock | High perf under multi-escrow load | Low |
| 2 | Incremental StartInference ID set | High perf as chains grow | Low |
| 3 | Deduper entry eviction | Memory leak prevention | Medium |
| 4 | Per-entry deduper locking | Perf under high concurrency | Medium |
| 5 | Relay timeout | Reliability | Low |
| 6 | Relay retry | Reliability | Low |
| 7 | Avoid double-buffering | Minor memory/correctness | Low |
| 8 | Stream history cap | Memory safety | Low |
| 9 | Non-blocking fan-out | Reliability under slow clients | Medium |
| 10 | Bounded execution context | Resource safety | Low |
