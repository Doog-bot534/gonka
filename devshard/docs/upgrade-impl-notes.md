# Devshard Upgrade -- Implementation Notes

Scope: the first versioned release only.

This document is about the temporary implementation we actually ship now. The
long-term architecture stays in `devshard/docs/upgrade.md`.

## Current implementation contract

The first versioned release is intentionally small:

- `/v1/devshard/*` remains the legacy path served directly by dapi
- `/devshard/<version>/*` is the new path served through
  `proxy -> versiond -> devshardd`
- `devshardd` is a temporary standalone host binary built out of the
  `decentralized-api/` module
- `devshardctl` defaults to its release version path and can still override the
  route prefix for tests or local debugging

The main goal of this release is to prove that the standalone host process can
run behind versiond while the legacy dapi path continues to work unchanged.

Version isolation is strict:

- `/devshard/<version>/*` hosts must talk to other `/devshard/<version>/*`
  hosts
- `/v1/devshard/*` hosts must talk only to `/v1/devshard/*` hosts
- the temporary release should not add cross-prefix fallback between those two
  families

## What is implemented now

### Proxy and routing

The proxy exposes two parallel routes:

```text
/v1/devshard/*        -> dapi legacy HostManager
/devshard/<version>/* -> versiond-managed devshardd
```

Location ordering matters. `/v1/devshard/*` must continue to hit dapi
directly. `/devshard/*` is reserved for versiond-routed standalone binaries.

### Temporary standalone binary

The standalone host binary lives at `decentralized-api/cmd/devshardd/`.

It is a thin bootstrap around shared devshard runtime code:

- query-only chain access
- devshard signer loaded from the existing keyring
- mainnet bridge backed by chain queries
- NodeManager gRPC client for ML node acquisition
- shared devshard host/session HTTP runtime
- sqlite session storage under versiond's data dir

Dropped from dapi's `main()`:

- admin server
- model manager
- PoC worker
- event dispatcher
- block queue
- config sync
- NodeManager gRPC server
- NATS / tx pipeline

Build:

```bash
go build -ldflags "-X decentralized-api/cmd/devshardd.Version=$VERSION" \
  -o build/devshardd ./cmd/devshardd
```

### Test shape

Both flows are covered on purpose:

- `DevshardTests.kt` verifies the legacy `/v1/devshard` path
- `DevshardStandaloneTests.kt` verifies the standalone
  `/devshard/<version>` path through proxy and versiond

The standalone test setup uses `VERSIOND_FORCE=dev` together with
`VERSIOND_OVERRIDE_dev` to run the locally built binary.

## Explicit non-goals for this release

The following items are not part of the temporary implementation:

- chain-side `approved_versions` enforcement
- `MsgSettleDevshardEscrow.version`
- off-chain `boundVersion` tracking in session state
- settlement rejection based on the binary version
- operator workflow for governance-driven version deprecation
- moving the standalone binary fully into the `devshard/` module
- replacing sqlite with postgres
- session pruning / retention background jobs

Those may still make sense later, but they should not shape the temporary code
path now.

## Code ownership

The temporary release should still move reusable code toward `devshard/`.

Current direction:

- keep dapi-only bootstrap and deployment wiring inside `decentralized-api/`
- move reusable devshard HTTP/session runtime into `devshard/`
- keep both legacy dapi and standalone devshardd using the same shared
  runtime underneath

## Known follow-up items

- Rate limiting on transport GET endpoints is still worth fixing for both
  paths.
- sqlite is acceptable for the temporary release but not the final host
  deployment story.
- once the standalone runtime settles, the remaining bootstrap code can move
  from `decentralized-api/cmd/devshardd/` into the `devshard/` module.
