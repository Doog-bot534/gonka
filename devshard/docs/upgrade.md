# Devshard Upgrades

Devshard versioning is fully decoupled from mainnet. Devshard binaries upgrade independently and much more frequently.

## Version Lifecycle

```
governance proposal -> approved_versions in chain params -> dapi serves GET /versions -> versiond polls and deploys
```

Mainnet maintains the set of approved devshard versions in `DevshardEscrowParams.approved_versions`. Each version has a name, download URL, and sha256 hash. Changes require a governance proposal via `MsgUpdateParams`.

Every host must run ALL approved versions simultaneously. versiond handles download, verification, process management, and routing.

## Version Binding

A devshard binds to exactly one version. The version is determined by the path the user hits on first interaction. Once bound, it cannot change. Hosts treat version mismatch as a consensus error.

There is no migration between versions. Devshards are short-lived; operators settle the old devshard and create a new one on the newer version.

## Request Routing

Two paths coexist:

```
/v1/devshard/*        -> decentralized-api (current, unchanged)
/devshard/*             -> versiond -> standalone devshard binary (v2+)
```

v1 continues to run through dapi as it does today. New versions (v2+) route through versiond to the standalone devshard binary. Both paths are live simultaneously.

The user's client library controls which version to use by choosing the URL path.

## Deprecation

Removing a version from the approved set is a governance vote. The process:

1. Governance proposal changes approved set from [v1, v2, v3] to [v2, v3]
2. During the voting period, hosts are incentivized to settle all v1 devshards
3. After the proposal passes, settlement on v1 is no longer possible
4. versiond automatically stops the v1 binary

Hosts bear the risk of unsettled devshards on deprecated versions. No grace period exists beyond the governance voting window.

In the future, the chain may block new devshard creation on deprecated versions before finalization, giving hosts more time.

## What Gets Versioned

Only the devshard binary. The decentralized-api is never touched by versiond. The devshard binary is a separate artifact from dapi for all versions going forward.
