# Phase 4: Dual Migration Mode

## Motivation

Enable a migration period where Confirmation PoC runs V2 while regular PoC remains V1. This allows testing V2 validation in production via confirmation PoC events before fully switching. Once V2 validation coverage is proven sufficient, the system auto-switches to full V2 mode.

## Parameter Design

Added `confirmation_poc_v2_enabled` to `PocParams` alongside existing `poc_v2_enabled`:

```protobuf
message PocParams {
  // ... existing fields ...
  bool poc_v2_enabled = 8;
  bool confirmation_poc_v2_enabled = 9;  // NEW
}
```

### Mode Matrix

| `poc_v2_enabled` | `confirmation_poc_v2_enabled` | Mode | Regular PoC | Confirmation PoC |
|------------------|------------------------------|------|-------------|------------------|
| false | false | Full V1 | V1 (on-chain batches) | V1 (on-chain batches) |
| false | true | Migration | V1 (on-chain batches) | V2 (off-chain commits) |
| true | true | Full V2 | V2 (off-chain commits) | V2 (off-chain commits) |
| true | false | Invalid | Treated as Full V2 | Treated as Full V2 |

Default for new deployments: `poc_v2_enabled=true, confirmation_poc_v2_enabled=true` (full V2).

## Auto-Switch Logic

At the end of Confirmation PoC validation (in `updateConfirmationWeights`), the system evaluates:

```
if migration_mode (poc_v2_enabled=false, confirmation_poc_v2_enabled=true):
    count participants with >= 75% validation vote coverage
    if >= 75% of active participants have sufficient coverage:
        set poc_v2_enabled = true  // direct param modification
```

### Thresholds

- `DefaultAutoSwitchParticipantThreshold = 0.75` - 75% of participants must pass
- `DefaultAutoSwitchCoverageThreshold = 0.75` - Each participant needs 75% vote coverage

## Implementation

### Chain Changes

#### 1. Parameter (`params.proto`)

```protobuf
message PocParams {
  bool poc_v2_enabled = 8;
  bool confirmation_poc_v2_enabled = 9;  // Enables V2 for Confirmation PoC only
}
```

#### 2. Migration Logic (`module/poc_migration.go`)

Pure functions for migration state and auto-switch evaluation:

- `MigrationState` - enum: `ModeFullV1`, `ModeMigration`, `ModeFullV2`
- `GetMigrationState(pocV2Enabled, confirmationPocV2Enabled)` - determines current mode
- `ValidationCoverage` - struct with participant address, total/voted weight, coverage ratio
- `CalculateCoverages(validations, validatorWeights)` - computes coverage for all participants
- `ShouldAutoSwitch(coverages, participantThreshold, coverageThreshold)` - returns bool
- `EvaluateAutoSwitch(coverages, ...)` - returns detailed `AutoSwitchResult`

#### 3. Confirmation PoC Updates (`module/confirmation_poc.go`)

- Changed dispatch logic to use `confirmation_poc_v2_enabled || poc_v2_enabled`
- Added `checkAutoSwitchToV2()` function called when in migration mode
- Auto-switch directly modifies params via `keeper.SetParams()`

### DAPI Changes

#### 1. Phase Tracker (`chainphase/phase_tracker.go`)

- Added `confirmationPocV2Enabled` field
- Added `UpdateConfirmationPocV2Enabled(enabled bool)` method
- Added `IsConfirmationPoCv2Enabled() bool` method
- Updated `EpochState` struct with `ConfirmationPocV2Enabled` field

#### 2. Event Listener (`event_listener/new_block_dispatcher.go`)

- Updated to call `UpdateConfirmationPocV2Enabled()` on each block

### Testermint Changes

#### 1. Types (`data/AppExport.kt`)

- Added `confirmationPocV2Enabled` to `PocParams` data class

#### 2. Tests (`PoCMigrationTests.kt`)

Added two new tests:

- `migration mode - v1 regular poc with v2 confirmation poc`: Verifies migration mode behavior
- `auto-switch - migration to full v2 on sufficient validation coverage`: Verifies auto-switch

## File Changes Summary

| File | Change |
|------|--------|
| `proto/inference/inference/params.proto` | Added `confirmation_poc_v2_enabled` field |
| `module/poc_migration.go` | NEW: Pure migration logic functions |
| `module/confirmation_poc.go` | Updated dispatch + auto-switch logic |
| `chainphase/phase_tracker.go` | Added `confirmationPocV2Enabled` field + methods |
| `event_listener/new_block_dispatcher.go` | Update tracker on block |
| `data/AppExport.kt` | Added `confirmationPocV2Enabled` to `PocParams` |
| `PoCMigrationTests.kt` | Added migration mode + auto-switch tests |

## Migration Sequence

1. **Deploy** with `poc_v2_enabled=false, confirmation_poc_v2_enabled=false` (Full V1)
2. **Enable migration** via governance: set `confirmation_poc_v2_enabled=true`
3. **Monitor** confirmation PoC events using V2
4. **Auto-switch** happens when validation coverage is sufficient
5. **Full V2** mode active (`poc_v2_enabled=true`)

## Rollback

If issues arise after auto-switch:
- Submit governance proposal to set `poc_v2_enabled=false`
- Can keep `confirmation_poc_v2_enabled=true` to stay in migration mode
- Or set both to `false` for full V1 rollback
