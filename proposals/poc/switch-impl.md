# PoC v2 Switch Implementation

> **Scope**: Implementation of governance-controlled switch from v1 to v2 PoC logic.
> See [switch-plan.md](./switch-plan.md) for design.

## Files Created/Modified

### inference-chain (Parameters + Weight Calculation)

#### Modified: `proto/inference/inference/params.proto`

Added `PoCv2Params` message:
```protobuf
message PoCv2Params {
  option (gogoproto.equal) = true;
  bool enabled = 1;
  string model_id = 2;
  int64 seq_len = 3;
}
```

Added to `Params`:
```protobuf
PoCv2Params poc_v2_params = 13;
```

#### Modified: `x/inference/types/params.go`

Added defaults:
```go
func DefaultPocV2Params() *PoCv2Params {
    return &PoCv2Params{
        Enabled: false,
        ModelId: "",
        SeqLen:  256,
    }
}
```

Added validation:
```go
func (p *PoCv2Params) Validate() error {
    if p.Enabled {
        if p.ModelId == "" {
            return fmt.Errorf("poc_v2_params.model_id must be set when enabled")
        }
        if p.SeqLen <= 0 {
            return fmt.Errorf("poc_v2_params.seq_len must be positive when enabled")
        }
    }
    return nil
}
```

#### Regenerated: `x/inference/types/params.pb.go`

Via `ignite generate proto-go`. Contains generated `PoCv2Params` struct.

Note: Proto generates field name `PocV2Params` (lowercase 'o') but type name `PoCv2Params`.

#### New File: `x/inference/module/pocv2_chainvalidation.go`

V2 weight calculation separated from v1:

```go
func (am AppModule) ComputeNewWeightsV2(ctx context.Context, upcomingEpoch types.Epoch) []*types.ActiveParticipant

type OrchestratorChainBridgeV2 interface {
    PoCv2ArtifactBatchesForStage(height int64) ([]*types.PoCArtifactBatchV2, error)
    PoCv2ValidationsForStage(height int64) ([]*types.PoCValidationV2, error)
}

func calculateParticipantWeightV2(validations []*types.PoCValidationV2) int64
func pocValidatedV2(validation *types.PoCValidationV2) bool // validated_weight > 0
```

Key semantics:
- `validated_weight > 0` → valid vote
- TODO comment for explicit weight derivation once artifacts are off-chain

#### Modified: `x/inference/module/module.go`

Added v2 routing:
```go
func (am AppModule) ComputeNewWeights(...) []*types.ActiveParticipant {
    params := am.keeper.GetParams(ctx)
    if params.PocV2Params != nil && params.PocV2Params.Enabled {
        return am.ComputeNewWeightsV2(ctx, upcomingEpoch)
    }
    return am.ComputeNewWeightsV1(ctx, upcomingEpoch)
}
```

#### Modified: `x/inference/module/confirmation_poc.go`

Added v2 routing in `updateConfirmationWeights`:
```go
if params.PocV2Params != nil && params.PocV2Params.Enabled {
    confirmationParticipants = am.calculateConfirmationWeightsV2(ctx, event, currentValidatorWeights)
} else {
    // existing v1 calculator
}
```

Added helper functions:
- `calculateConfirmationWeightsV2`
- `getPoCArtifactBatchesV2ForConfirmation`
- `getPoCValidationsV2ForConfirmation`

---

### decentralized-api (Full v2 Switch Routing)

#### Modified: `internal/event_listener/new_block_dispatcher.go`

Added v2 orchestrator and switch routing:
```go
type OnNewBlockDispatcher struct {
    // ... existing fields ...
    nodePocOrchestratorV2  pocv2.NodePoCOrchestratorV2
    cachedPocV2ParamsBlock int64  // Block height at which v2 params were last cached
    cachedPocV2Enabled     bool   // Cached value of poc_v2_params.enabled
}

// isPocV2Enabled checks chain params and caches result per block
func (d *OnNewBlockDispatcher) isPocV2Enabled(ctx context.Context, blockHeight int64) bool

// Routing in handlePhaseTransitions:
if epochContext.IsStartOfPoCValidationStage(blockHeight) {
    if d.isPocV2Enabled(ctx, blockHeight) && d.nodePocOrchestratorV2 != nil {
        d.nodePocOrchestratorV2.ValidateReceivedArtifacts(epochContext.PocStartBlockHeight)
    } else {
        d.nodePocOrchestrator.ValidateReceivedBatches(epochContext.PocStartBlockHeight)
    }
}
```

#### Modified: `broker/broker.go`

Extended `pocParams` to include v2 params:
```go
type pocParams struct {
    startPoCBlockHeight int64
    startPoCBlockHash   string
    modelParams         *types.PoCModelParams
    // v2 params - set when poc_v2_params.enabled is true
    v2Enabled bool
    v2ModelId string
    v2SeqLen  int64
}
```

Added v2 callback URL helpers:
```go
const PoCv2ArtifactsBasePath = "/v2/poc-artifacts"

func GetPocArtifactsV2GeneratedCallbackUrl(callbackUrl string) string
func GetPocArtifactsV2ValidatedCallbackUrl(callbackUrl string) string
```

Modified `getCommandForState` to use v2 commands when enabled:
```go
case PocStatusGenerating:
    if pocGenParams.v2Enabled {
        return StartPoCNodeCommandV2{...}
    }
    // ... v1 fallback

case PocStatusValidating:
    if pocGenParams.v2Enabled {
        // No-network command - actual v2 validation is handled by orchestrator
        return TransitionPoCToValidatingV2Command{}
    }
    // ... v1 fallback (InitValidateNodeCommand)
```

Key change: When v2 is enabled, the broker returns `TransitionPoCToValidatingV2Command` which makes **no network calls**. This ensures no v1 `/api/v1/pow/init/validate` calls are made in v2 mode. The v2 orchestrator handles `StopPowV2` and `GenerateV2` validation requests directly.

#### Modified: `broker/node_worker_commands.go`

V2 command structs (no Stop() calls during generation):
```go
type StartPoCNodeCommandV2 struct {
    BlockHeight int64
    BlockHash   string
    PubKey      string
    CallbackUrl string
    TotalNodes  int
    Model       string // model_id from chain params
    SeqLen      int64  // seq_len from chain params
}

type ValidatePoCNodeCommandV2 struct {
    BlockHeight int64
    BlockHash   string
    PubKey      string
    CallbackUrl string
    TotalNodes  int
    Model       string
    SeqLen      int64
    Nonces      []int64
    Artifacts   []mlnodeclient.ArtifactV2
}

// No-network command for v2 validation state transition
type TransitionPoCToValidatingV2Command struct{}
```

The `TransitionPoCToValidatingV2Command` is used when v2 is enabled to transition broker state to POC/Validating **without making any network calls**. The actual v2 validation (StopPowV2 + GenerateV2) is handled by the v2 orchestrator.

#### New File: `broker/node_worker_commands_v2_test.go`

Unit tests for v2 commands:
- `TestTransitionPoCToValidatingV2Command_Success` - Verifies no network calls are made
- `TestTransitionPoCToValidatingV2Command_CancelledContext` - Context cancellation handling
- `TestStartPoCNodeCommandV2_Success` - Verifies v2 generation works without Stop()
- `TestStartPoCNodeCommandV2_AlreadyGenerating` - Idempotency check
- `TestStopPowV2_MockBehavior` - Mock client behavior
```

#### Modified: `internal/pocv2/node_orchestrator_v2.go`

Implemented real chain query for v2 artifact batches:
```go
func (b *OrchestratorChainBridgeV2Impl) PoCv2ArtifactBatchesForStage(startPoCBlockHeight int64) (*PoCArtifactBatchesV2Response, error) {
    queryClient := b.cosmosClient.NewInferenceQueryClient()
    resp, err := queryClient.PocV2BatchesForStage(ctx, &types.QueryPocV2BatchesForStageRequest{
        BlockHeight: startPoCBlockHeight,
    })
    // Transform chain response to orchestrator format
    // ...
}
```

Relaxed node selection for v2 validation:
```go
func filterNodesForV2Validation(nodes []broker.NodeResponse) []broker.NodeResponse {
    // Accept nodes in POC status (any sub-status) or INFERENCE status
    // Excludes only FAILED, UNKNOWN, and administratively disabled nodes
}
```

Added `StopPowV2` call at validation stage transition:
```go
func (o *NodePoCOrchestratorV2Impl) ValidateReceivedArtifacts(pocStageStartBlockHeight int64) {
    // ...
    nodes = filterNodesForV2Validation(nodes)
    
    // Stop PoC v2 generation on all nodes before starting validation.
    // This is called once per validation stage transition (not per batch).
    o.stopGenerationOnAllNodes(nodes)
    
    // Then proceed with validation requests...
}

func (o *NodePoCOrchestratorV2Impl) stopGenerationOnAllNodes(nodes []broker.NodeResponse) {
    // Calls StopPowV2 on each node (best-effort, logs errors but continues)
}
```

#### Modified: `main.go`

Added v2 orchestrator initialization:
```go
nodePocOrchestratorV2 := pocv2.NewNodePoCOrchestratorV2ForCosmosChain(
    participantInfo.GetPubKey(),
    nodeBroker,
    config.GetApiConfig().PoCCallbackUrl,
    config.GetChainNodeConfig().Url,
    recorder,
    chainPhaseTracker,
)
listener := event_listener.NewEventListener(config, nodePocOrchestrator, nodePocOrchestratorV2, ...)
```

#### New File: `mlnodeclient/poc_v2_requests.go`

Request/response DTOs:
```go
type PoCInitGenerateRequestV2 struct {
    BlockHash   string      `json:"block_hash"`
    BlockHeight int64       `json:"block_height"`
    PublicKey   string      `json:"public_key"`
    NodeId      int         `json:"node_id"`
    NodeCount   int         `json:"node_count"`
    Params      PoCParamsV2 `json:"params"`
    URL         string      `json:"url"`
}

type PoCGenerateRequestV2 struct {
    BlockHash   string         `json:"block_hash"`
    BlockHeight int64          `json:"block_height"`
    PublicKey   string         `json:"public_key"`
    NodeId      int            `json:"node_id"`
    NodeCount   int            `json:"node_count"`
    Nonces      []int64        `json:"nonces"`
    Params      PoCParamsV2    `json:"params"`
    URL         string         `json:"url"`
    Validation  *ValidationV2  `json:"validation,omitempty"`
}

type PoCParamsV2 struct {
    Model  string `json:"model"`
    SeqLen int64  `json:"seq_len"`
}
```

Client methods:
- `InitGenerateV2(req PoCInitGenerateRequestV2) (*PoCInitGenerateResponseV2, error)`
- `GenerateV2(req PoCGenerateRequestV2) (*PoCGenerateResponseV2, error)`
- `GetPowStatusV2() (*PoCStatusResponseV2, error)`
- `StopPowV2() (*PoCStopResponseV2, error)` - stops generation on all backends

Note: No `k_dim` parameter (removed for v2)

---

### testermint (v2 Route Compatibility)

#### Modified: `mock_server/src/main/kotlin/.../routes/PowV2Routes.kt`

Relaxed state preconditions to support "no Stop()" flow:

`handleInitGenerateV2`:
- Was: Required `ModelState.STOPPED`
- Now: Allowed if not `ModelState.GENERATING`

`handleGenerateV2`:
- Was: Required specific states
- Now: Allowed in any state, transitions to `POW_VALIDATING`

#### Modified: `src/main/kotlin/MockServerInferenceMock.kt`

Implemented v2 mock methods:
```kotlin
override fun setPocV2Response(weight: Long, hostName: String?, scenarioName: String) {
    // Log for v2 PoC generation mock setup
}

override fun setPocV2ValidationResponse(weight: Long, scenarioName: String) {
    // Log for v2 PoC validation mock setup
}
```

#### Modified: `src/main/kotlin/data/AppExport.kt`

Added `PocV2Params` data class:
```kotlin
data class PocV2Params(
    val enabled: Boolean,
    @SerializedName("model_id")
    val modelId: String,
    @SerializedName("seq_len")
    val seqLen: Long,
)
```

Included in `InferenceParams`:
```kotlin
data class InferenceParams(
    // ... existing params
    @SerializedName("poc_v2_params")
    val pocV2Params: PocV2Params? = null,
)
```

#### Modified: `internal/event_listener/integration_test.go` (decentralized-api)

Updated test mocks to include `PocV2Params`:
```go
pocV2Params := &types.PoCv2Params{
    Enabled: true,
    ModelId: "test-model-v2",
    SeqLen:  128,
}
mockQueryClient.On("Params", ...).Return(&types.QueryParamsResponse{
    Params: types.Params{
        ValidationParams: validationParams,
        PocV2Params:      pocV2Params,
    },
}, nil)
```

---

## Key Implementation Notes

### Naming Convention

Proto generates:
- Type: `PoCv2Params` (preserves message name)
- Field: `PocV2Params` (from snake_case `poc_v2_params`)

Use `params.PocV2Params` for field access, `*types.PoCv2Params` for type.

### No k_dim

V2 requests intentionally omit `k_dim`. The MLNode determines dimensions from the model.

### Stop() Semantics in v2

**During generation**: No `Stop()` call before `InitGenerateV2`. Nodes handle concurrent generation requests.

**At validation transition**: The v2 orchestrator calls `StopPowV2()` once on all nodes **before** sending validation requests. This is a single coordinated stop, not per-batch stops like v1.

**Key difference from v1**: The broker does not call any v1 PoW endpoints when v2 is enabled. All v2 operations go through the dedicated v2 orchestrator and MLNode v2 API (`/api/v1/inference/pow/*`).

### validated_weight Semantics

- `validated_weight > 0` → valid vote
- `validated_weight <= 0` → invalid/reject
- Future: explicit weight derivation from validator voting

---

## Activation

1. Binary upgrade deploys v2 code
2. Governance proposal sets:
   ```json
   {
     "poc_v2_params": {
       "enabled": true,
       "model_id": "production-model-v2",
       "seq_len": 256
     }
   }
   ```
3. Next PoC stage uses v2 logic

---

## Implementation Status

| Component | Status | Notes |
|-----------|--------|-------|
| Chain params (`poc_v2_params`) | ✅ Done | Proto + Go validation |
| Chain weight calc switch | ✅ Done | `ComputeNewWeights` routes to v1/v2 |
| Chain confirmation switch | ✅ Done | `confirmation_poc.go` routes to v1/v2 |
| Chain gRPC query v2 batches | ✅ Done | `PocV2BatchesForStage` implemented |
| DAPI v2 orchestrator | ✅ Done | `node_orchestrator_v2.go` |
| DAPI switch routing | ✅ Done | Dispatcher + broker route by `enabled` |
| DAPI v2 callback URLs | ✅ Done | `/v2/poc-artifacts` base path |
| DAPI v2 commands | ✅ Done | `StartPoCNodeCommandV2`, `ValidatePoCNodeCommandV2`, `TransitionPoCToValidatingV2Command` |
| DAPI StopPowV2 client | ✅ Done | `StopPowV2()` method in mlnodeclient |
| DAPI v2 orchestrator stop | ✅ Done | `stopGenerationOnAllNodes()` called once at validation stage |
| v2 command unit tests | ✅ Done | `node_worker_commands_v2_test.go` |
| testermint v2 mocks | ✅ Done | `PowV2Routes`, `MockServerInferenceMock` |
| testermint data classes | ✅ Done | `PocV2Params` in `AppExport.kt` |
| Integration tests | ✅ Done | Fixed mocks to include `PocV2Params` |

All components are implemented and tests pass.
