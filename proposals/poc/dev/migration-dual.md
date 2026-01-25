# Phase 4: Dual Migration Mode (V2 Tracking)

## Motivation

Enable a migration period to measure mlnode V2 adoption before fully switching. In migration mode, one confirmation PoC per epoch uses V2 (tracking only), while the rest use V1.

Key properties:
- **No new trigger mechanism**: reuse existing probabilistic confirmation PoC triggers.
- **V2 tracking**: first confirmation event per epoch (`event_sequence == 0`) uses V2 for tracking only, no weight impact.
- **V1**: all other confirmation events use V1 with weight/slashing enforcement.
- **Manual switch**: governance sets `poc_v2_enabled=true` once adoption is sufficient (no auto-switch).

## Parameter Design

```protobuf
message PocParams {
  bool poc_v2_enabled = 8;
  bool confirmation_poc_v2_enabled = 9;  // enables migration mode
}
```

### Mode Matrix

| poc_v2 | confirm_v2 | Regular PoC | Confirmation PoC |
|--------|------------|-------------|------------------|
| false | false | V1 | V1 (all events) |
| false | true | V1 | event 0: V2 tracking, event 1+: V1 |
| true | true | V2 | V2 (all events) |
| true | false | Invalid | Treated as Full V2 |

## Event Dispatch Rules

In migration mode (`poc_v2_enabled=false, confirmation_poc_v2_enabled=true`):

- `event_sequence == 0`: V2 tracking only (no weight impact)
- `event_sequence >= 1`: V1 (affects weights/slashing)

Notes:
- At most 1 V2 tracking event per epoch (best-effort, probabilistic).
- V2 tracking logs coverage metrics but does NOT modify weights.

## Implementation

### DAPI Changes

#### broker/broker.go

```go
func (b *Broker) IsV2EndpointsEnabled() bool {
    return b.phaseTracker.IsPoCv2Enabled() || b.phaseTracker.IsConfirmationPoCv2Enabled()
}

func (b *Broker) IsMigrationMode() bool {
    return !b.phaseTracker.IsPoCv2Enabled() && b.phaseTracker.IsConfirmationPoCv2Enabled()
}

func (b *Broker) shouldUseV2ForPoC(confirmationEvent *types.ConfirmationPoCEvent) bool {
    if b.IsPoCv2Enabled() {
        return true
    }
    if b.IsMigrationMode() && confirmationEvent != nil && confirmationEvent.EventSequence == 0 {
        return true
    }
    return false
}
```

#### mlnode/post_generated_artifacts_v2_handler.go

```go
if s.broker != nil && !s.broker.IsV2EndpointsEnabled() {
    return echo.NewHTTPError(http.StatusServiceUnavailable, "V2 endpoints disabled")
}
```

### Chain Changes

#### module/confirmation_poc.go

```go
func (am AppModule) updateConfirmationWeights(...) error {
    migrationState := GetMigrationStateFromParams(params.PocParams)

    switch migrationState {
    case ModeFullV2:
        am.evaluateConfirmation(ctx, event, ..., useV2: true)
    case ModeMigration:
        // event_sequence == 0: V2 tracking (event stored, skip weight evaluation)
        // event_sequence >= 1: V1 (affects weights)
        if event.EventSequence > 0 {
            am.evaluateConfirmation(ctx, event, ..., useV2: false)
        }
    default:
        am.evaluateConfirmation(ctx, event, ..., useV2: false)
    }
    return nil
}
```

## Files Summary

| File | Change |
|------|--------|
| `broker/broker.go` | Add `IsV2EndpointsEnabled()`, `IsMigrationMode()`, `shouldUseV2ForPoC()` |
| `mlnode/post_generated_artifacts_v2_handler.go` | Use `IsV2EndpointsEnabled()` |
| `chainphase/phase_tracker.go` | `IsConfirmationPoCv2Enabled()` (existing) |
| `module/confirmation_poc.go` | Switch on event_sequence in migration mode |
| `keeper/query_confirmation_poc_events.go` | Query to list confirmation PoC events by epoch |

## Migration Sequence

1. **Deploy** with `poc_v2_enabled=false, confirmation_poc_v2_enabled=false` (Full V1).
2. **Enable migration** via governance: set `confirmation_poc_v2_enabled=true`.
3. **Monitor** V2 tracking results (first confirmation event per epoch):
   - Query `ListConfirmationPoCEvents` to get trigger heights.
   - Query V2 data (StoreCommits) at trigger heights off-chain.
   - V1 continues for other events.
4. **Manual switch**: submit governance proposal to set `poc_v2_enabled=true` once adoption is sufficient.
5. **Full V2 active**: all PoC (regular + confirmation) uses V2.

## Rollback

If issues arise after enabling full V2:
- Submit governance proposal to set `poc_v2_enabled=false`.
- Keep `confirmation_poc_v2_enabled=true` to stay in migration mode (optional).
- Or set both to `false` for full V1 rollback.
