# Devshard Upgrade -- Implementation Notes

Working notes. Not intended for the repo long-term.


## Version binding

The devshard binary has its version hardcoded at build time. Devshard state stores the version on creation. Hosts compare the two -- mismatch is a consensus error.

Each version ships its own devshardctl.


## Settlement rejection

Chain must reject settlement tx for a version no longer in the approved set.


## Proxy

New `/devshard/` location in nginx pointing to versiond upstream.


## Self-sufficient devshard binary

The devshard binary must run independently from decentralized-api. The following interfaces are defined in `devshard/` but currently only implemented in `decentralized-api/internal/devshard/`:

### InferenceEngine (engine.go)
- `Execute(ctx, ExecuteRequest) (*ExecuteResult, error)`
- Dapi impl: `EngineAdapter` in `decentralized-api/internal/devshard/engine.go` -- wraps broker + completionapi
- Standalone impl: get mlnode URL via gRPC to dapi, then call `/chat/completions` directly on mlnode. Depends on https://github.com/gonka-ai/gonka/pull/945

### ValidationEngine (engine.go)
- `Validate(ctx, ValidateRequest) (*ValidateResult, error)`
- Dapi impl: `ValidationAdapter` in `decentralized-api/internal/devshard/validation.go` -- re-executes inference, compares logits

### MainnetBridge (bridge/interface.go)
- `OnEscrowCreated()`, `OnSettlementProposed()`, `OnSettlementFinalized()`, `GetEscrow()`, `GetHostInfo()`, `VerifyWarmKey()`, `SubmitDisputeState()`
- Dapi impl: `ChainBridge` in `decentralized-api/internal/devshard/bridge.go` -- uses CosmosMessageClient (gRPC)

### Signer (signing/interface.go)
- `Sign(message) ([]byte, error)`, `Address() string`
- Dapi impl: `NewSignerFromKeyring()` in `decentralized-api/internal/devshard/signer.go`

### Storage (storage/interface.go)
- `CreateSession()`, `MarkSettled()`, `ListActiveSessions()`, `AppendDiff()`, `GetDiffs()`, `AddSignature()`, `GetSignatures()`, `GetSessionMeta()`, `MarkFinalized()`, `LastFinalized()`, `Close()`
- Standalone impl: postgres as default. Need a fallback for environments without remote postgres (sqlite or local file)


## HTTP server

The standalone binary needs its own HTTP server replacing `HostManager.Register()` from `decentralized-api/internal/devshard/manager.go`. Same routes:

Authenticated (POST, require X-Devshard-Signature + X-Devshard-Timestamp):
- `/sessions/:id/chat/completions`
- `/sessions/:id/verify-timeout`
- `/sessions/:id/challenge-receipt`
- `/sessions/:id/gossip/nonce`
- `/sessions/:id/gossip/txs`

Unauthenticated (GET):
- `/sessions/:id/diffs`
- `/sessions/:id/mempool`
- `/sessions/:id/signatures`
- `/sessions/:id/payloads`

Auth is already in `devshard/transport/` (server.go, auth.go). The HostManager glue (session lifecycle, lazy creation, singleflight, recovery) must be reimplemented in the devshard binary.

Security: unauthenticated GET endpoints must have rate limiting. No public endpoint without auth should be exposed without limits.
