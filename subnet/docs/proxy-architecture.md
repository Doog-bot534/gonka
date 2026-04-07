# Subnet Proxy Architecture

This note explains how the main `subnetctl` runtime pieces fit together:

- `Gateway`
- `Proxy`
- `SpeculativeEngine`
- `ParticipantRequestLimiter`
- metrics / connection observation

It focuses on responsibilities, dependencies, and what is intentionally independent.

## High-Level Picture

```mermaid
flowchart TD
    Client[OpenAI-compatible client]
    Gateway[Gateway]
    GLimiter[GatewayLimiter]
    PLimiter[ParticipantRequestLimiter]
    Metrics[SubnetMetrics]
    ConnTrack[HostConnectionTracker]

    subgraph Runtime["Per-escrow subnetRuntime"]
        Proxy[Proxy]
        Engine[SpeculativeEngine]
        Registry[streamRegistry]
        Perf[PerfTracker]
        Session[user.Session]
        SM[state.StateMachine]
        Clients[transport.HTTPClient(s)]
    end

    Client --> Gateway
    Gateway --> GLimiter
    Gateway --> PLimiter
    Gateway --> Metrics
    Gateway --> Proxy

    Proxy --> Engine
    Proxy --> Session
    Proxy --> SM
    Proxy --> Registry
    Proxy --> Perf

    Engine --> Session
    Engine --> Registry
    Engine --> Perf
    Engine --> Metrics

    Session --> Clients
    Session --> SM

    Clients --> PLimiter
    Clients --> ConnTrack
    ConnTrack --> Metrics
    PLimiter --> Metrics
```

## Mental Model

There are two layers:

1. A **gateway layer** that accepts public HTTP requests and chooses an escrow runtime.
2. A **per-escrow execution layer** that actually runs subnet protocol logic for one escrow.

The easiest way to think about it is:

- `Gateway` decides **which escrow** gets the request.
- `Proxy` decides **how to run that request** inside one escrow.
- `SpeculativeEngine` decides **how many host attempts** to race for that one request.
- `user.Session` owns **protocol state and nonce progression**.
- `transport.HTTPClient` performs **real network calls to hosts**.
- `ParticipantRequestLimiter` is a **cross-runtime shared guard** on those host calls.
- `SubnetMetrics` and `HostConnectionTracker` are **observers**, not core business logic.

## Component Responsibilities

### `Gateway`

`Gateway` is the top-level HTTP multiplexer in front of all runtimes.

It is responsible for:

- serving pooled OpenAI-compatible endpoints like `/v1/chat/completions`
- exposing admin and metrics endpoints
- choosing a `subnetRuntime` when multiple escrows are active
- enforcing gateway-wide admission control:
  - `GatewayLimiter` for request concurrency and input-token reservation
  - `ParticipantRequestLimiter` for participant/nginx safety
- tracking per-runtime load (`activeRequests`, `reservedTokens`)

It is **not** responsible for subnet protocol execution details. Once it forwards a request to a runtime, the runtime-specific logic takes over.

### `subnetRuntime`

`subnetRuntime` is the per-escrow container object.

It groups:

- `Proxy`
- `user.Session`
- `PerfTracker`
- per-runtime HTTP handler
- runtime metadata like escrow id, model, and participant keys

This is the boundary between:

- shared process-wide routing state
- escrow-specific protocol state

### `Proxy`

`Proxy` is the OpenAI-facing handler for one escrow.

It is responsible for:

- parsing chat completion requests
- normalizing content
- building `user.InferenceParams`
- switching between streaming and non-streaming response handling
- mapping runner errors to HTTP responses

It is intentionally thin. It does not do host selection itself. It delegates execution to `SpeculativeEngine`.

### `SpeculativeEngine`

`SpeculativeEngine` is the request runner for one escrow.

It is responsible for:

- preparing one or more subnet inference attempts
- deciding when to start additional attempts
- racing attempts across hosts
- picking the streaming winner
- recording host performance samples
- cleaning up failed attempts with timeout vote logic

This is where the **speculative behavior** lives:

- start primary host
- maybe start secondary immediately
- maybe escalate later on receipt timeout / first-token timeout / response timeout / immediate failure

It depends on:

- `user.Session` for nonce progression and protocol requests
- `streamRegistry` for receipt and SSE routing
- `PerfTracker` for host-health estimates

It does **not** know anything about multi-escrow routing. That is a `Gateway` concern.

### `user.Session`

`user.Session` is the subnet protocol state owner.

It is responsible for:

- maintaining the local `StateMachine`
- composing diffs
- assigning the next host via nonce progression
- preparing requests for hosts
- processing host responses back into local protocol state
- collecting timeout votes and sending pending diffs

This is the most protocol-coupled piece.

Important relationship:

- `SpeculativeEngine` may start multiple attempts
- but every attempt still goes through `Session.PrepareInference()`
- so session state remains the source of truth for nonce and host ordering

### `ParticipantRequestLimiter`

`ParticipantRequestLimiter` is a shared process-wide limiter keyed by participant identity, usually derived from host IP / URL hostname.

It is responsible for:

- maintaining a token-bucket budget per participant
- preventing new transport requests when a participant is near the nginx limit
- marking a participant exhausted when upstream returns `429` or `503`
- letting `Gateway` reject escrows whose participant set is unsafe

It is intentionally **outside** any one runtime, because the same participant can appear:

- in multiple slots
- in the same escrow multiple times
- across different escrows

So this limiter must be shared globally, not attached per runtime.

### `SubnetMetrics`

`SubnetMetrics` is the observability façade.

It is responsible for:

- HTTP request metrics
- gateway rejection counters
- speculative decision counters
- timeout counters
- host latency histograms
- exposing gateway collector gauges

It should stay observational:

- it reads state
- it increments counters
- it should not decide routing or protocol behavior

The only slight coupling is that some components call metrics helpers directly when events happen.

### `HostConnectionTracker`

`HostConnectionTracker` observes transport connection lifecycle.

It tracks:

- active connections
- idle keepalive connections
- recently closed connections

It is used to understand the network footprint of host traffic. It is not part of request routing, subnet logic, or participant budget enforcement.

This means it is strongly related to **transport observability**, but mostly independent from:

- `Proxy`
- `SpeculativeEngine`
- `user.Session`

### `transport.HTTPClient`

`transport.HTTPClient` is the real network boundary to remote hosts.

It is responsible for:

- signing outbound requests
- sending host protocol RPCs
- parsing SSE receipts and metadata
- reporting upstream status codes back to the participant limiter
- participating in connection tracking

This is where several concerns meet:

- protocol transport
- participant-limiter admission hook
- connection tracking

But it still does not decide multi-host speculation or multi-escrow routing.

## Request Lifecycle

For a pooled request:

1. Client calls `Gateway` on `/v1/chat/completions`.
2. `Gateway` parses the request and applies `GatewayLimiter`.
3. `Gateway` skips escrows blocked by `ParticipantRequestLimiter`.
4. `Gateway` chooses the least-loaded runtime and forwards the request to that runtime’s `Proxy`.
5. `Proxy` normalizes the request and builds `InferenceParams`.
6. `Proxy` calls `SpeculativeEngine.RunInference(...)`.
7. `SpeculativeEngine` prepares one or more attempts through `user.Session`.
8. `user.Session` sends host requests via `transport.HTTPClient`.
9. `transport.HTTPClient`:
   - checks participant admission
   - sends the HTTP request
   - reports upstream status like `429` / `503`
   - feeds SSE receipt/data callbacks
10. `streamRegistry` routes receipts and winning stream chunks.
11. `SpeculativeEngine` finalizes the race and updates performance history.
12. `Proxy` returns the final client response.

## Dependency Map

### Shared / global pieces

These are effectively process-wide:

- `Gateway`
- `GatewayLimiter`
- `ParticipantRequestLimiter`
- `SubnetMetrics`
- `HostConnectionTracker`

### Per-runtime pieces

These belong to one escrow runtime:

- `subnetRuntime`
- `Proxy`
- `SpeculativeEngine`
- `PerfTracker`
- `streamRegistry`
- `user.Session`
- `state.StateMachine`

### Boundary pieces

These connect the runtime to the outside world:

- `transport.HTTPClient`
- bridge / chain REST access during runtime construction

## What Is Independent

The cleanest independence boundaries are:

### `Gateway` vs `Proxy`

- `Gateway` is about routing and admission across escrows.
- `Proxy` is about serving one escrow.

You can reason about speculative logic without understanding multi-escrow routing.

### `SpeculativeEngine` vs `ParticipantRequestLimiter`

- `SpeculativeEngine` decides when to add more attempts.
- `ParticipantRequestLimiter` decides whether transport calls are allowed at all.

They influence the same request outcome, but they solve different problems:

- speculation = latency / resiliency
- participant limiter = upstream capacity safety

### `HostConnectionTracker` vs business logic

`HostConnectionTracker` is almost entirely orthogonal to subnet behavior.

It tells you:

- how many sockets exist
- whether connections are reused
- whether idle / close behavior looks healthy

It does not change request selection or protocol state.

### `Metrics` vs control flow

Metrics should remain downstream of events.

If you deleted metrics collection, the proxy should still behave the same.

## What Is Not Fully Independent

Some parts are intentionally coupled:

### `Proxy` and `SpeculativeEngine`

`Proxy` is a thin wrapper around `SpeculativeEngine`, so they are conceptually separate but operationally very close.

### `SpeculativeEngine` and `user.Session`

These are tightly coupled.

The engine relies on session semantics for:

- nonce assignment
- host rotation
- response processing
- timeout vote submission

You usually cannot modify one deeply without understanding the other.

### `transport.HTTPClient` and `ParticipantRequestLimiter`

After the new participant logic, transport is now the enforcement point for participant-bound host calls.

That is intentional, because this is the one place that sees:

- every outbound host request
- every upstream status code

## Practical Rule Of Thumb

If you are changing:

- **escrow selection**: start in `Gateway`
- **client HTTP API behavior**: start in `Proxy`
- **multi-host racing / fallbacks**: start in `SpeculativeEngine`
- **protocol state / diffs / nonces / timeout votes**: start in `user.Session`
- **participant capacity protection**: start in `ParticipantRequestLimiter`
- **network socket visibility**: start in `HostConnectionTracker`
- **Prometheus exposure**: start in `SubnetMetrics`

## Short Summary

The system is layered like this:

- `Gateway` chooses an escrow.
- `Proxy` converts client requests into subnet inference requests.
- `SpeculativeEngine` runs one request across one escrow’s hosts.
- `user.Session` owns protocol state and host sequencing.
- `transport.HTTPClient` performs the real network calls.
- `ParticipantRequestLimiter` is a shared safety rail across all runtimes.
- `SubnetMetrics` and `HostConnectionTracker` observe the system rather than drive it.

That split is the main architectural idea.
