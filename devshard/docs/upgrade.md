# Devshard Upgrades

Devshard binaries version independently of mainnet. No cosmovisor, no coordinated upgrades.

## Flow

```
governance proposal -> params.approved_versions -> dapi GET /versions -> versiond polls, downloads, runs
```

`DevshardEscrowParams.approved_versions` is a governance-controlled list. Each entry has a name, a binary URL, and a sha256. Updates go through `MsgUpdateParams`.

sha256 is the sole identity for a binary -- the URL is a download hint only. Two governance proposals that point at different mirrors but carry the same hash cause zero restarts. A proposal that reuses a name but changes the hash replaces the binary with a zero-downtime swap: versiond downloads the new binary first, then stops the old process.

Cached binaries are re-hashed on every versiond startup, so a tampered file on disk is detected before routing traffic to it.

## Multiple versions per host

Every host runs every approved version concurrently. If `approved_versions = [v1, v2, v3]`, each host has three devshard child processes running side by side under versiond, reachable via three different URL prefixes. Hosts do not pick and choose -- it is all approved versions or nothing.

## Version binding

Escrow creation is version-agnostic. `MsgCreateDevshardEscrow` takes no version.

The user picks a version by choosing the URL path at session start:

```
/v1/devshard/*          -> dapi, in-process (pre-versiond, unchanged)
/devshard/<version>/*   -> versiond -> devshard binary for <version>
```

The first request routes to one version's binary. That host writes its build-time version into off-chain state. Every subsequent diff asserts the same version -- a host whose binary version does not match state refuses to sign. A session that tries to drift across versions cannot collect 2/3+ signatures and cannot settle.

`/v1/devshard/*` is the legacy path served directly by dapi and is not managed by versiond. From v2 onward, all versioned traffic goes through `/devshard/<version>/*`.

## Deprecation

Governance removes a version from `approved_versions`.

Settlement is user-driven: `MsgSettleDevshardEscrow` is submitted by the user, who has the active stake in recovering unused escrow. Hosts can settle only if the user has disappeared past a timeout. So during the voting period, users are the ones expected to close out in-flight sessions on the version being removed.

Once the proposal passes, the chain rejects `MsgSettleDevshardEscrow` for that version, and versiond stops the binary on its next poll. Any session still open at that point loses the escrow. The chain cannot block new sessions on a deprecated version at create time because creation carries no version -- enforcement is at settle, and nowhere earlier.

## Operator overrides

Host operators can override individual versions without waiting on governance:

- `VERSIOND_OVERRIDE_<name>=/path/to/binary` -- replace the downloaded binary for `<name>` with a local file. versiond still checks sha256 and still restarts on changes; it just uses the local file instead of the URL. Useful for hotfixes and local debugging.
- `VERSIOND_FORCE=v4-rc1` -- run a version not in `approved_versions`. Requires a matching `VERSIOND_OVERRIDE_...`. Useful for testing a release candidate before governance votes it in.

Neither mechanism affects consensus. A forced or overridden version that is not in `approved_versions` cannot settle to mainnet, so any session that uses it is off-chain only.

## What versiond manages

Only the devshard binary. dapi is untouched by versiond. `devshardctl` is a user-side CLI, shipped per release for client compatibility, not managed by versiond.

## WARN: temporary build arrangement

Target end-state: a devshard binary in the `devshard/` Go module with zero dapi dependency. The first versioned release does not ship that.

Instead, the first release builds the devshard binary out of `decentralized-api/`, reusing dapi's ML engine, validation, bridge, signer, and host manager directly. Rewriting these into `devshard/` is follow-up work.

Operator impact:
- Large binary (full dapi dependency closure).
- Runs as its own process under versiond, independent from dapi at runtime.
- Governance flow, versiond, routing, settlement protocol: unchanged.

The later migration to a self-contained `devshard/` binary is transparent to clients, governance, and versiond.
