# Multi-Model PoC Design

Part 1: system audit and phase 1 design. Delegation, economics, and consensus aggregation are in design-2.md.

## Scope
- Only PoC v2. PoC v1 is dead code, out of scope.
- Part 1 describes the current single-model PoC precisely. Part 2 defines minimal changes for correct multi-model PoC.
- Every statement checked against code or the live mainnet params endpoint.

## Current Mainnet Baseline
Current mainnet params return:
- `poc_params.model_id = Qwen/Qwen3-235B-A22B-Instruct-2507-FP8`
- `poc_params.seq_len = 1024`
- `poc_params.validation_sample_size = 200`
- `poc_params.validation_slots = 0`
- `poc_params.poc_v2_enabled = true`
- `poc_params.confirmation_poc_v2_enabled = true`
- `poc_params.poc_normalization_enabled = true`

This means:
- PoC generation and validation use exactly one model chain-wide.
- Mainnet currently uses O(N^2) validation, not slot sampling.
- The active path is PoC v2, with off-chain artifact storage plus on-chain commit and validation messages.

Mainnet was also pushed into a single-model shape by code and upgrades:
- `v0.2.8` pinned `PocParams.model_id` to `Qwen/Qwen3-235B-A22B-Instruct-2507-FP8` and removed three obsolete governance models.
- `v0.2.9` removed every remaining governance model except `Qwen/Qwen3-235B-A22B-Instruct-2507-FP8`.
- `decentralized-api/broker/enforced_model.go` still defaults nodes to that same model unless `ENFORCED_MODEL_ID` is explicitly disabled.

## What Already Supports Multiple Models
The repository still contains real multi-model infrastructure:
- `Model` registry on-chain
- `HardwareNode.models` for per-node model support declarations
- `ActiveParticipant.models` and `ActiveParticipant.ml_nodes` for per-model node arrays
- `EpochGroupData.model_id` and `sub_group_models` for model sub-groups
- `GetRandomExecutor(model)` routing through model-specific sub-groups

The system is "multi-model around the edges, single-model in the PoC pipeline".

## Data Structures

### On-chain
`Model` in `model.proto`
- Stores governance model metadata.
- Fields include `proposed_by`, `id`, `units_of_compute_per_token`, `context_window`, `quantization`, `coins_per_input_token`, `coins_per_output_token`, `hf_repo`, `hf_commit`, `model_args`, `v_ram`, `throughput_per_nonce`, `validation_threshold`.
- Important nuance: the current `MsgRegisterModel` path populates only `proposed_by`, `id`, `units_of_compute_per_token`, `hf_repo`, `hf_commit`, `model_args`, `v_ram`, `throughput_per_nonce`, and `validation_threshold`. The extra metadata fields exist in the proto, but the current msg server does not set them.

`HardwareNode` in `hardware_node.proto`
- Declares one physical node owned by a participant.
- Key fields: `local_id`, `status`, `models`, `hardware`, `host`, `port`.
- Submitted through `MsgSubmitHardwareDiff`.

`ActiveParticipant` in `activeparticipants.proto`
- Stores one participant's state for an epoch.
- Key fields: `index`, `validator_key`, `weight`, `inference_url`, `models`, `seed`, `ml_nodes`.
- `models` and `ml_nodes` are index-aligned.
- `weight` is one participant-global number, not an explicit map of model -> weight.

`ModelMLNodes` and `MLNodeInfo` in `epoch_group_data.proto`
- `ModelMLNodes` is a wrapper around `[]MLNodeInfo`.
- `MLNodeInfo` contains `node_id`, `throughput`, `poc_weight`, `timeslot_allocation`.
- `timeslot_allocation[0]` is `PRE_POC_SLOT`.
- `timeslot_allocation[1]` is `POC_SLOT`.

`EpochGroupData` in `epoch_group_data.proto`
- Keyed by `(epoch_index, model_id)`.
- `model_id == ""` means the parent epoch group.
- `model_id != ""` means a model-specific sub-group.
- Parent groups carry `sub_group_models`.
- `validation_weights` store per-member `weight`, `reputation`, `ml_nodes`, and `confirmation_weight`.
- Important nuance for sub-groups: `validation_weights[*].ml_nodes` is filtered correctly to the specific model, but `validation_weights[*].weight` is still copied from the participant-global weight. This is one of the current multi-model bugs.
- Important nuance: the parent group does not store ML nodes. `epoch_group.getMLNodeInfo()` returns `nil` for `model_id == ""`, so ML node snapshots live only in sub-groups.

`PocParams` in `params.proto`
- Defines one PoC model for the whole chain through `model_id` and `seq_len`.
- Also contains `weight_scale_factor`, `validation_sample_size`, `stat_test`, `validation_slots`, and `poc_normalization_enabled`.
- There is no repeated or per-model PoC config structure.

`PoCV2StoreCommit` in `poc_v2.proto`
- Keyed by `(poc_stage_start_block_height, participant_address)`.
- Stores `count`, `root_hash`, and `commit_block_height`.

`MLNodeWeightDistribution` in `poc_v2.proto`
- Keyed by `(poc_stage_start_block_height, participant_address)`.
- Stores `weights []MLNodeWeight`, where each item is `{node_id, weight}`.

`PoCValidationV2` in `poc_v2.proto`
- Keyed by `(poc_stage_start_block_height, participant_address, validator_participant_address)`.
- `validated_weight > 0` means valid.
- `validated_weight <= 0` means invalid for validation outcome purposes.

### Off-chain
`ArtifactStore` in `decentralized-api/poc/artifacts/store.go`
- Stores artifacts in `artifacts.data` as `[LE32 len][LE32 nonce][vector_bytes]`.
- Maintains an incremental MMR where each leaf is `LE32(nonce) || vector`.
- Tracks per-node artifact counts in `nodes.json`.
- Keyed by PoC stage height only. One store per stage, not per `(stage, model)`.

`PoCParamsV2` in `decentralized-api/mlnodeclient/poc_v2_requests.go`
- Sent to ML nodes for generation and validation.
- Contains only `Model` and `SeqLen`.

`POST /v1/poc/proofs` in `decentralized-api/internal/server/public/poc_handler.go`
- Takes `poc_stage_start_block_height`, `root_hash`, `count`, `leaf_indices`, validator auth fields, timestamp, and signature.
- Does not take `model_id`.

## Current Data Flow

### 1. PoC generation
1. Broker builds `pocParams` in `queryCurrentPoCParams()`.
2. `enrichWithPocParams()` reads `PocParams.model_id` and `PocParams.seq_len` from chain params.
3. `StartPoCNodeCommandV2` sends `PoCInitGenerateRequestV2` with `PoCParamsV2{Model, SeqLen}` to every PoC node.
4. ML nodes stream generated artifacts back to the API node callback.
5. The API stores artifacts in the local MMR-backed `ArtifactStore`.
6. The API submits:
   - `MsgPoCV2StoreCommit(poc_stage_start_block_height, count, root_hash)`
   - `MsgMLNodeWeightDistribution(poc_stage_start_block_height, weights[])`

Result: one PoC commit and one per-node distribution per participant per stage.

### 2. PoC validation
1. `OffChainValidator.ValidateAll()` queries all commits for the stage.
2. It reads `validation_sample_size` and `validation_slots` from `PocParams`.
3. With `validation_slots = 0` on mainnet, every validator validates every participant.
4. For each participant:
   - Sample leaf indices deterministically with `sha256(validatorPubKey + ":" + blockHash + ":" + blockHeight)`.
   - Request proofs from `POST /v1/poc/proofs`.
   - Verify proof coverage, MMR proofs, vector encoding, duplicate nonces, and porosity.
   - Re-run the sampled nonces on the validator's own ML node with the same `PoCParamsV2{Model, SeqLen}` and the chain stat-test parameters.
5. Submit `MsgSubmitPocValidationsV2(poc_stage_start_block_height, validations[])`.

Important: proof requests and stored validations carry no model_id. Re-validation on ML nodes uses `PoCParamsV2{Model, SeqLen}` for statistical test execution, but model identity does not appear in artifact storage or proof request identity.

### 3. Weight formation at epoch boundary
The main pipeline runs in `onEndOfPoCValidationStage()`.

#### Step 1: `ComputeNewWeights()`
This function builds the next epoch's `[]*ActiveParticipant`.

PoC-derived participants:
- Read all `PoCV2StoreCommit`, `MLNodeWeightDistribution`, and `PoCValidationV2` entries for the stage.
- `calculateParticipantWeight()` computes:
  - `totalWeight = commit.count * combinedFactor`
  - `nodeWeight = distribution.weight * combinedFactor`
- `combinedFactor = weight_scale_factor * timeNormalizationFactor` when normalization is enabled.
- `pocValidated()` decides whether the participant passes:
  - With `validation_slots == 0`, votes are weighted by current validator weight.
  - Threshold is `validWeight > totalWeight * 2 / 3`.
  - If neither valid nor invalid reaches `> 2/3`, guardians can break the tie only by unanimous agreement among guardians who voted.
- Successful PoC participants are emitted with one flat ML-node array in `MlNodes[0]`.

Preserved participants:
- `GetPreviousEpochMLNodesWithInferenceAllocation()` loads previous-epoch `ActiveParticipants`.
- It preserves nodes where `TimeslotAllocation[1] == true`.
- Those nodes keep their current `PocWeight` into the next epoch.

Merge:
- If a participant has both preserved nodes and fresh PoC nodes, `mergeMLNodeArrays()` unions them by `NodeId`.
- The merged participant weight is recomputed as the sum of unique node `PocWeight`.

Important nuance:
- Comments in this area still say "inference-serving node" for `POC_SLOT == true`.
- The code actually keys off `TimeslotAllocation[1]`, not a separate "inference" flag.
- There is also a helper named `getInferenceServingNodeIds()`, but it reads the parent epoch group. Parent epoch groups do not store ML nodes, so this helper cannot recover node IDs from normal parent-group state.

#### Step 2: `setModelsForParticipants()`
This turns the flat ML-node array into per-model arrays.

For each participant:
1. Read the flat array from `p.MlNodes[0]`.
2. Deduplicate by `NodeId`.
3. Initialize every node with `TimeslotAllocation = [true, false]`.
4. Build `supportedModelsByNode` from `HardwareNode.models` and the sorted governance-model list.
5. Iterate governance models in sorted order.
6. Assign each unassigned ML node to the first governance model it supports.
7. Drop nodes that support no governance model.
8. Set `p.Models`, `p.MlNodes`, and recompute `p.Weight` with `RecalculateWeight()`.

Important consequences:
- One ML node can support multiple governance models in `HardwareNode.models`.
- But after this step it is assigned to at most one model for the epoch.
- Per-model weight exists only implicitly as `sum(p.MlNodes[i].MlNodes[*].PocWeight)`.
- The system never stores that sum as an explicit field.

#### Step 3: weight adjustments
Two participant-global transforms run next:
- `AdjustWeightsByCollateral()`
- `applyEpochPowerCapping()`

These modify `p.Weight` in place. They do not compute new per-model weights.

#### Step 4: `AllocateMLNodesForPoC()`
This updates `timeslot_allocation` for the next epoch using previous-epoch history, reward filtering, and node-selection heuristics.

It does not change the participant-global `weight`.

Important nuance:
- The final `POC_SLOT` allocation loop is already per model. It builds per-model node sets, iterates models one by one, and writes `TimeslotAllocation[1] = true` only within that model's node bucket.
- But some inputs to that allocation are still global rather than per-model. In particular, thresholding and the `<34% non-voting` constraint use participant-global weight aggregated across models.

#### Step 5: epoch group creation
`addEpochMembers()` adds participants to the parent epoch group, then `addToModelGroups()` creates one sub-group entry per model.

This is where the current multi-model design breaks:
- `subMember.Models` is narrowed to one model.
- `subMember.MlNodes` is narrowed to that model's ML nodes.
- But `subMember.Weight` is not recomputed.
- `updateEpochGroupWithNewMember()` writes `ValidationWeight.Weight = member.Weight`.

So if one participant has:
- Model A nodes with weight 400
- Model B nodes with weight 600
- Total participant weight 1000

Then:
- parent group stores weight 1000
- Model A sub-group stores weight 1000
- Model B sub-group also stores weight 1000

The subgroup ML-node snapshots are model-specific, but the subgroup weight is still participant-global.

## Why This Works Today
With only one governance model:
- every assigned ML node ends up in the same model bucket
- the participant-global weight equals that model's weight
- copying parent weight into the only sub-group is accidentally correct

The current system works on mainnet because mainnet is forced into a single-model shape.

-------------------------

## Design

One MLNode hosts one model at a time. If a host supports two models, it runs two distinct MLNodes.

Phase 1 goal: correct per-model PoC generation, proof serving, validation, and epoch-group weights. Delegation, economics, and consensus aggregation belong to design-2.md.

Upgrade approach: synchronized network update. No backward compatibility with old single-model PoC-v2 shapes. Old records pruned in upgrade handler.

All existing testermint tests must keep passing. Check `MultiModelTests.kt` coverage first.

### 1. Per-model PoC parameters

PocParams has one global `model_id` and `seq_len`. Different models need different sequence lengths and stat test parameters.

PocParams gains a repeated per-model config:

```
message PoCModelConfig {
  string model_id = 1;
  int64 seq_len = 2;
  PoCStatTestParams stat_test = 3;
  string weight_scale_factor = 4; // Decimal
}
```


`PocParams.models` replaces the singular `model_id`, `seq_len`, `stat_test`, and `weight_scale_factor` fields. The list defines which models participate in PoC. Global fields (`validation_sample_size`, `validation_slots`, `poc_normalization_enabled`) stay on PocParams.

With one model, behavior is equivalent to current system.

### 2. Per-model storage identity

Every PoC-v2 record binds to `(stage, model_id)` scope. Adding `model_id` to keeper storage is not enough on its own. The local MMR, proof API, artifact callbacks, commit pipeline, and validation pipeline must all follow the same `(stage, model_id)` scoping.

Storage keys gain `model_id`:

| Record | Current key | New key |
|---|---|---|
| Commit | (stage, participant) | (stage, participant, model_id) |
| Distribution | (stage, participant) | (stage, participant, model_id) |
| Validation | (stage, participant, validator) | (stage, participant, validator, model_id) |

Local artifact stores become stage-first: one store path per stage, with one model-scoped store under it. Conceptually the identity is still `(stage, model_id)`, but pruning stays stage-based with no new pruning logic. Proof requests include `model_id`.

Query APIs and internal readers must follow the same identity change. The current V2 flow depends on participant-level and stage-level PoC queries. In multi-model mode those queries must become model-aware too. Stage-wide readers should return atomic `(participant, model)` records, not participant records with repeated model entries that are split client-side.

### 3. TX messages

TX batching and persistence identity are separate concerns. A TX batches multiple model-scoped entries for transport. Each entry persists as one model-scoped record.

Keep the current incremental commit cadence. During generation, the participant still submits periodic commits as artifacts accumulate. Each commit/distribution TX batches the current per-model entries for transport. Persistence identity is still per `(stage, participant, model_id)`. This keeps the existing commit-worker shape and only makes payloads model-aware.

`MsgPoCV2StoreCommit` replaces singular `count`/`root_hash` with repeated per-model entries:

```
message PoCV2CommitEntry {
  string model_id = 1;
  uint32 count = 2;
  bytes root_hash = 3;
}
```

`MsgMLNodeWeightDistribution` replaces singular `weights` with repeated per-model entries:

```
message MLNodeDistributionEntry {
  string model_id = 1;
  repeated MLNodeWeight weights = 2;
}
```

`PoCValidationEntryV2` replaces `PoCValidationPayloadV2`, adding `model_id`:

```
message PoCValidationEntryV2 {
  string participant_address = 1;
  string model_id = 2;
  int64 validated_weight = 3;
}
```

Each handler unpacks entries and persists one record per model-scoped key.

### 4. PoC generation

Broker reads `PocParams.models`. For each `PoCModelConfig`, dispatches generation only to MLNodes assigned to that model in the current epoch. Each model's artifacts go to a separate local store keyed by `(stage, model_id)` under the stage directory.

Callback identity must include `model_id` so artifact callbacks route to the correct store.

Proof and callback signatures must also bind `model_id`, not just routing and storage keys.

### 5. PoC validation

Direct validation for model X only by MLNodes serving model X. If a validator has no MLNode for model X, it skips model X in phase 1. No cross-model delegation yet.

Proof requests include `model_id` to route to the correct artifact store.

Validation work items keyed by `(participant, model)`. One validation result per `(participant, model, validator)`.

Slot sampling seed: `SHA256(validatorPubKey:blockHash:blockHeight:modelId)`. Different models get independent sampling.

`PoCValidationSnapshot` stays stage-global in phase 1. It stores chain-global validator weights and timing data, not model-local validator-set state.

Both O(N^2) and slot-based modes use model-scoped validator sets. Vote eligibility is model-local, but vote power stays participant-global aggregated weight. The acceptance rule stays unchanged from current PoC: `>2/3` valid accepts, `>2/3` invalid rejects, otherwise guardians can break the tie with the existing unanimous-voters rule.

Confirmation PoC follows the same model-aware paths: per-model stores, per-model proofs, per-model validation records.

### 6. Preserved nodes stay in model bucket

Current: `GetPreviousEpochMLNodesWithInferenceAllocation()` flattens preserved nodes into `MlNodes[0]`. `setModelsForParticipants()` later re-derives model membership from `HardwareNode.models`.

Problem: preserved node loses its proven model bucket and may be reassigned differently.

Change: preserve the previous per-model bucket structure instead of flattening nodes into `MlNodes[0]`. Each preserved node keeps its previous `model_id`. Merge preserved and fresh PoC nodes per model, not as a flat list.

### 7. Per-model weight and subgroup state

Current bug: `addToModelGroups()` copies unchanged participant-global weight into every subgroup.

Per-model weight is computed first, then aggregated. Not the other way around -- do not compute participant-global weight first and project back into models.

Fix:

1. Subgroup weight per `(participant, model)` = sum of that model's node PocWeights.
2. `addToModelGroups()` writes this per-model weight as subgroup weight.
3. Participant-global weight = sum of all per-model weights, then adjusted by collateral and power capping.

Collateral and power capping apply to the aggregated participant weight only. They are participant-level concepts (collateral is staked per participant, not per model). Subgroup weights reflect proven compute, participant weight reflects consensus power.

With one model, subgroup weight equals raw PoC weight and participant weight equals the collateral-adjusted value.

### 8. Remove single-model enforcement

`enforced_model.go` defaults all nodes to one model. Remove or make test-only.

### 9. Boundary with design-2.md

This document defines:
- Model-aware PoC generation, proof serving, validation, preserved-node handling, weight computation
- Per-model subgroup weights from proven compute
- Participant weight as collateral-adjusted sum of subgroup weights

design-2.md defines:
- Cross-model aggregation with consensus coefficients
- Delegation for cross-model validation
- Model-level economics

## Implementation Expectations

Each phase must preserve correct single-model behavior end to end. After implementation of any individual phase:
- the chain must still work in the normal 100% single-model case
- all existing unit tests must pass
- all existing testermint tests must pass
- the expected place to run the full suites is CI/CD, not local development

The implementation is complete only after the last phase. At that point:
- everything described in this document must be implemented and working
- there must be no intentional leftovers from `design-1.md`
- multi-model PoC behavior must have dedicated testermint coverage

The final multi-model testermint coverage will be added only after the last phase lands. At that point decide whether to extend `testermint/src/test/kotlin/MultiModelTests.kt` or to add a separate PoC-focused multi-model test. Do not treat the current `MultiModelTests.kt` inference coverage as sufficient coverage for this document.

## Phases

### Phase 1. Storage and protocol primitives
- `PoCModelConfig` message and `repeated models` in PocParams
- `model_id` in storage keys for commits, distributions, validations
- model-aware query/API changes for commit, distribution, validation, and validation-snapshot readers
- Per-model entries in TX messages
- `model_id` in proof requests
- regenerate protobuf Go code after proto changes:

```bash
cd inference-chain && ignite generate proto-go
```

- Prune old PoC-v2 records in upgrade handler

Exit:
- every PoC-v2 record has model-aware identity
- the chain still works correctly for the single-model case
- all existing unit tests and existing testermint tests pass in CI/CD

### Phase 2. Model-aware artifact stores and proof plumbing
- Local artifact store keyed by `(stage, model_id)`
- Artifact callbacks bound to model-aware stores
- Proof serving bound to `(stage, participant, model_id)`
- Confirmation PoC reads from model-aware paths

Exit:
- one store per `(stage, model_id)`
- one proof request per `(stage, participant, model_id)`
- the chain still works correctly for the single-model case
- all existing unit tests and existing testermint tests pass in CI/CD

### Phase 3. Model-aware execution
- Broker dispatches per-model generation to correct MLNodes
- Validation work items keyed by `(participant, model)`
- Validation executors filtered by model membership
- Slot sampling seed includes `model_id`
- O(N^2) and slot-based validation both model-aware

Exit:
- generation and validation for model X use only model-X nodes
- the chain still works correctly for the single-model case
- all existing unit tests and existing testermint tests pass in CI/CD

### Phase 4. Preserved nodes and weight computation
- Preserved nodes keep previous per-model bucket structure and previous `model_id`
- Merge preserved and fresh PoC nodes per model
- Compute per-model subgroup weights from node PocWeights
- Compute participant weight as collateral-adjusted sum

Exit:
- preserved nodes keep `model_id`
- subgroup weights reflect per-model compute
- the chain still works correctly for the single-model case
- all existing unit tests and existing testermint tests pass in CI/CD

### Phase 5. Subgroup state and cleanup
- `addToModelGroups()` writes per-model weight
- Remove single-model enforcement
- Regression coverage: multi-model PoC, subgroup weights, slot mode, confirmation PoC

Exit:
- subgroups store model-local weight
- everything in this document is implemented and working, with no intentional leftovers
- dedicated multi-model PoC testermint coverage exists
- the chain still works correctly for the single-model case
- all existing unit tests and existing testermint tests pass in CI/CD
