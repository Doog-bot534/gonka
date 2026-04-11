# Devshard Upgrade -- Implementation Notes

Scope: the first versioned release, where the devshard binary is built out of `decentralized-api/` as a temporary shortcut. The long-term rewrite into the `devshard/` module is not covered here.

## Version binding

`MsgCreateDevshardEscrow` and `DevshardEscrow` unchanged -- no version field.

The binary embeds its version via ldflags `-X`. On first interaction with an escrow, the host writes that version to `EscrowState.boundVersion`. Every `ApplyLocal` asserts the binary's version matches state; mismatch means the host refuses to sign. `boundVersion` contributes to the state hash, so 2/3+ signatures implicitly attest to it.

Each version ships its own `devshardctl`.

## Settlement rejection

`MsgSettleDevshardEscrow` gets a `version` field. At settle, the chain rejects if `version` is empty or not in the current `approved_versions`. Because `boundVersion` is part of `state_root` and state_root is signed by 2/3+ hosts, any attempt to mis-declare the version at settle time fails signature verification.

Rollout: land the field as optional with a warning first, then flip to hard-reject in the upgrade handler of the same release so old clients get one window to upgrade.

## Proxy

Add a `/devshard/` location in `proxy/nginx.unified.conf.template` pointing at a `versiond_backend` upstream. Clone streaming config from `/api/`. Location ordering must keep `/v1/devshard/` matching dapi directly, not versiond.

## Devshardd binary

`devshard/` is a separate Go module and cannot import `decentralized-api/internal/...`. To reuse dapi's adapters without a rewrite, the binary lives at `decentralized-api/cmd/devshardd/`.

Wiring:
- `cosmosclient` -- chain reads and writes
- `internaldevshard.NewChainBridge(recorder)` for the bridge (or `devshard/bridge.NewRESTBridge`, equivalent; both stub the action methods)
- `internaldevshard.NewSignerFromKeyring(keyring, uid)`
- `devshard/storage.NewSQLite(path)` -- tentative, see Storage below
- `internaldevshard.NewEngineAdapter(...)` and `NewValidationAdapter(...)`
- `internaldevshard.NewHostManager(...)` with `RecoverSessions()` on startup and `Register(echoGroup)` for routes
- echo server on `--port`

Dropped from dapi's `main()`: admin server, model manager, PoC commit worker, event dispatcher, block queue, config sync, node manager gRPC server.

Build:
```
go build -ldflags "-X decentralized-api/cmd/devshardd.Version=$VERSION" \
  -o build/devshardd ./cmd/devshardd
```

Validate before shipping that `devshardd`'s `main()` does not transitively pull in dapi's init-time side effects (dispatcher, modelmanager, adminserver). Expected binary size 60-100 MB.

## Storage

Decide whether sqlite stays available at all. Multiple devshardd instances cannot share a sqlite file, and sqlite does not scale horizontally, so postgres is the only viable backend for real host deployments.

A postgres adapter is required. Implements the existing `storage.Storage` interface at `devshard/storage/interface.go` -- no new interface surface.

Pruning is required and does not exist today. Settled sessions accumulate diffs, signatures, and meta rows forever. Add a retention mechanism that drops rows for `status=settled` sessions past a window, run from a background goroutine. Retention policy (window size, guarantees) is a separate decision.

## Other required changes

Not skipped by the shortcut:

- `MsgSettleDevshardEscrow.version` plus approved-versions check at settle (see Settlement rejection).
- `EscrowState.boundVersion` plus first-interaction record, per-apply assertion, and state-hash inclusion (see Version binding).
- nginx `/devshard/` location (see Proxy).
- Rate limiting on transport GET endpoints. `HostManager.Register` at `decentralized-api/internal/devshard/manager.go:243` currently does not pass `transport.WithRateLimit(...)`, so the unauthenticated GETs (`/diffs`, `/mempool`, `/signatures`, `/payloads`) are unthrottled. This is a live bug in the dapi path today, not only a gap for the new binary.
