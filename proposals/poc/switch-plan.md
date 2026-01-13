# PoC v2 Switch Design

> **Scope**: Switch chain from v1 PoC logic to v2 after on-chain upgrade.
> Builds on top of [messages-plan.md](./messages-plan.md) which defined proto messages and storage.

## Overview

Enable v2 weight calculation and orchestration via a governance parameter flip.
When `poc_v2_params.enabled = true`, the chain and decentralized-api use v2 logic.

## Design Goals

1. **Minimal changes** - Preserve all v1 code, add v2 alongside
2. **Governance-controlled** - Switch via parameter update after upgrade
3. **Code separation** - v2 logic in dedicated files, not mixed with v1
4. **testermint compatibility** - Continue working with both v1 and v2 flows
5. **No MLNode stop calls** - v2 flow does not call `.Stop()` on MLNode

## Chain Parameters

### New `PoCv2Params` in `params.proto`

```protobuf
message PoCv2Params {
  bool enabled = 1;      // false by default, enabled via governance
  string model_id = 2;   // Required when enabled
  int64 seq_len = 3;     // Required when enabled
}
```

Embedded in main `Params` message:
```protobuf
message Params {
  // ... existing fields ...
  PoCv2Params poc_v2_params = 13;
}
```

### Validation

- `enabled = false`: No validation required
- `enabled = true`: `model_id` must be non-empty, `seq_len` must be positive

## Weight Calculation Switch

### Entry Point: `ComputeNewWeights`

```
if poc_v2_params.enabled:
    → ComputeNewWeightsV2()
else:
    → ComputeNewWeightsV1()
```

### V2 Weight Calculation (`pocv2_chainvalidation.go`)

- Query `PoCArtifactBatchesV2` and `PoCValidationsV2` by stage
- For each participant: `validated_weight > 0` → valid vote
- TODO: Derive explicit weight from voting once artifacts are off-chain
- Same `validation_sample_size` semantics as v1

## Confirmation PoC Switch

Same pattern in `confirmation_poc.go`:

```
if poc_v2_params.enabled:
    → calculateConfirmationWeightsV2()
else:
    → existing v1 calculator
```

## decentralized-api Switch

### MLNode Client (`mlnodeclient/poc_v2_requests.go`)

New methods for v2 endpoints:
- `InitGenerateV2()` → `POST /api/v1/inference/pow/init/generate`
- `GenerateV2()` → `POST /api/v1/inference/pow/generate`
- `GetPowStatusV2()` → `GET /api/v1/inference/pow/status`

Request differences from v1:
- Includes `model_id`, `seq_len` from chain params
- Does NOT include `k_dim` (removed for v2)
- No `.Stop()` calls during generation
- `StopPowV2()` called once at validation stage transition (not per-batch)

### Node Worker Commands (`broker/node_worker_commands.go`)

- `StartPoCNodeCommand`: If `poc_v2_params.enabled`, use `StartPoCNodeCommandV2` (no Stop() before init)
- For validation: If `poc_v2_params.enabled`, use `TransitionPoCToValidatingV2Command` (no network call - just state transition)
- Actual v2 validation handled by orchestrator via `StopPowV2` + `GenerateV2` with validation artifacts

### Orchestrator (`internal/pocv2/node_orchestrator_v2.go`)

- `ValidateReceivedArtifacts()`: Query v2 artifact batches, sample, call `GenerateV2()` on MLNodes
- Calls `StopPowV2()` once on all nodes before starting validation requests
- Reuses chain bridge pattern from v1

## testermint Updates

### Mock Routes (`PowV2Routes.kt`)

Relaxed state preconditions:
- `init/generate`: Allowed if not already `GENERATING`
- `generate`: Allowed in any state (transitions to `POW_VALIDATING`)

This aligns with the "no Stop()" requirement - nodes can receive v2 commands without requiring a stop first.

## Activation Sequence

1. Deploy upgrade binary with v2 code
2. Chain upgrade executes
3. Governance proposal to set `poc_v2_params.enabled = true`
4. Next PoC stage uses v2 logic

## Not Covered

- Off-chain artifact storage migration
- Multi-validator consensus on `validated_weight`
- v1 deprecation/removal

---

## Implementation Status

See [switch-impl.md](./switch-impl.md) for detailed implementation.

| Area | Status |
|------|--------|
| Chain parameters | ✅ Done |
| Chain weight calculation | ✅ Done |
| Chain confirmation weights | ✅ Done |
| Chain gRPC query | ✅ Done |
| DAPI v2 orchestrator | ✅ Done |
| DAPI switch routing | ✅ Done |
| DAPI v2 commands | ✅ Done |
| DAPI StopPowV2 client | ✅ Done |
| v2 command unit tests | ✅ Done |
| testermint compatibility | ✅ Done |

**Ready for testnet activation** - Set `poc_v2_params.enabled = true` via governance.
