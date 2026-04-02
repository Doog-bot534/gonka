# Multi-Model PoC: Delegation Implementation

Covers delegation, group eligibility, voting power resolution, and consensus weight capping. Assumes `design-1.md` (model-aware PoC state) is implemented.

Out of scope: model registration and governance approval flow, unregistered models.

## Three Weight Terms

pocWeight(group_i, p) -- raw compute p proved inside model group i. Determines inference request routing within the group. Model-local. Lives in `EpochGroupData.ValidationWeights.Weight`.

consensusWeight(p) -- aggregated weight across all eligible groups: `sum(koeff_i * pocWeight(group_i, p))`. Stored as `ActiveParticipant.Weight`. This is the weight that gets delegated.

votingPower(group_i, p) -- validation acceptance power in group i. For a direct member: `consensusWeight(p) + sum of consensusWeight(d) for all d delegating to p in group_i`. For a non-member delegating to m: their consensusWeight flows into votingPower(group_i, m).

pocWeight drives inference distribution inside a group. consensusWeight drives block signing, governance, and reward distribution. votingPower drives PoC validation acceptance (2/3 threshold).

These are three distinct numbers. A participant with low pocWeight in a group can have high votingPower (many delegators). A participant with high pocWeight but in a non-eligible group contributes zero to consensusWeight. They must remain separate in the data model.

## Epoch Snapshot Semantics

We don't know who is DIRECT in a model group until we evaluate PoC results for that group. A host can switch models mid-epoch. DIRECT membership comes from actual PoC participation, not from prior declarations.

This means mode resolution and votingPower computation cannot happen before PoC. They happen per model group when evaluating PoC results.

### Delegation snapshot

At block `start_poc - deploy_window`, the chain freezes raw delegation state by copying current `PoCDelegation`, `PoCRefusal`, and `PoCDirectIntent` entries for active participants into a snapshot store. No epoch key -- just store the latest snapshot, overwrite next epoch.

Mutable state remains writable after the snapshot. Changes after `start_poc - deploy_window` affect the next snapshot, not the current one.

The snapshot stores raw entries, not resolved modes. Mode resolution requires knowing DIRECT membership which is only known at PoC evaluation time.

### Pre-eligibility (advisory)

At `start_poc - deploy_window`, the chain evaluates which new model groups are likely to qualify, using snapshot delegation + intent state + `ActiveParticipants(N-1)` consensus weights.

Additionally check if 2/3 acceptance is reachable: if `sum(votingPower of projected participants) / totalNetworkWeight <= 2/3`, the group cannot pass PoC validation even with perfect results. Emit an event so hosts can avoid wasting deployment effort.

### Deploy window

Between `start_poc - deploy_window` and `start_poc`, INTENT hosts deploy hardware for groups they expect to qualify. `deploy_window` is a governance parameter in `PoCParams`.

The deploy window does not protect from confirmation PoC. It is separate from the maintenance window. During the deploy window a host can request a maintenance window, redeploy at own risk (losing confirmation PoC if challenged), or use a separate MLNode.

### PoC evaluation per model

When evaluating PoC results for model group_i, the chain resolves delegation and computes votingPower for the first time:

1. DIRECT members = participants who submitted PoC results for this model
2. For each participant with `ActiveParticipants(N-1).weight > 0`:
   - If submitted PoC for this model -> DIRECT
   - Else check snapshot: refusal -> REFUSE, delegation -> DELEGATE, intent -> INTENT (didn't deploy, treated as NONE), else -> NONE
3. Compute votingPower for each DIRECT member m:
   `votingPower(m) = ActiveParticipants(N-1).weight[m] + sum(ActiveParticipants(N-1).weight[d]) for all d delegating to m (from snapshot)`
   Same formula for all DIRECT members regardless of whether they were in this group before.
4. Use votingPower for acceptance threshold: `sum(votingPower of approvers) / totalNetworkWeight > 2/3`

INTENT hosts that didn't participate in PoC for an eligible group resolve as NONE (penalty applies via delegation weight adjustment). If the group is not eligible, INTENT is consumed without penalty.

!! Note: That's a bit controversal, but currently we use NEW delegation for the voting in next POC N+1. The same will be used in all next Confirmation PoC in epoch N+1 (from .voting_power). We might want to use the same .voting_power for voting for POC N+1 but then it'll require special logic to handle new direct nodes for existing host and new eligible groups. 

### Post-PoC weight computation

After all PoC evaluations, `WeightCalculator` runs using delegation snapshot + PoC results:
- checks `IsGroupEligible` per group using actual PoC participants
- computes new consensusWeight (`.weight`) with caps and adjustments
- resolves participation modes (same modes as PoC evaluation -- DIRECT from PoC results, rest from snapshot)
- applies delegation weight adjustment (REFUSE/NONE/DELEGATE penalties)
- computes new per-group votingPower (`.voting_powers`) from final post-adjustment consensus weights
- writes result to `ActiveParticipants(N)`

### Confirmation PoC

All confirmation PoC during epoch N reads `ActiveParticipants(N).voting_powers`.

### Cleanup

After `SetActiveParticipants(N)`, delete consumed `PoCRefusal` and `PoCDirectIntent` entries. Snapshot store is overwritten by the next epoch's snapshot.

## Participation Modes

For a given model group and epoch, each participant with positive consensus weight resolves to exactly one mode:

| Mode | Meaning |
|------|---------|
| `DIRECT` | Member of the group (has MLNode deployed). Validates directly. |
| `INTENT` | Not yet a member. Declared intent to deploy MLNode for this model. |
| `DELEGATE` | Not a member. Delegates consensus weight to one group member. |
| `REFUSE` | Not a member. Explicitly refuses delegation for this epoch. |
| `NONE` | Not a member. No valid delegation, no refusal, no intent. |

Resolution priority: DIRECT > INTENT > REFUSE > DELEGATE > NONE.

DIRECT means the host already has hardware deployed. INTENT means the host commits to deploy before PoC starts. At PoC time, INTENT hosts that deployed become DIRECT. INTENT hosts that didn't deploy are treated as NONE (penalty).

INTENT only matters when a group is on the border of pre-eligibility. If the group is already pre-eligible from existing DIRECT members + delegations alone, INTENT adds nothing -- the host can just deploy and become DIRECT. INTENT exists specifically so hosts can signal commitment before deploying hardware, allowing the chain to count them toward pre-eligibility thresholds and hosts to avoid wasting deployment effort on groups that won't qualify.

A group that fails pre-eligibility can still become eligible if enough DIRECT participants independently participate in PoC for it. Pre-eligibility is an advisory gate -- it protects hosts from wasting resources on MLNode deployment for groups unlikely to qualify. It is not a hard block. `IsGroupEligible` (post-PoC) applies the same conditions (`W_threshold`, `V_min`, 2/3 reachability) but using actual PoC participants instead of projections from intents and delegations.

No separate transaction needed for DIRECT -- serving the model is the participation action.

## State: Mutable Delegation Preferences

### `PoCDelegation`

```proto
message PoCDelegation {
  string model_id    = 1;
  string delegator   = 2;  // participant address
  string delegate_to = 3;  // target participant address
}
```

Key: `(model_id, delegator)`. At most one entry per key.

Persistent. Carries over across epochs until overwritten or cleared. Set via `MsgSetPoCDelegation`. Empty `delegate_to` clears the entry.

Split delegation is not supported by construction -- single `delegate_to` field.

### `PoCRefusal`

```proto
message PoCRefusal {
  string model_id    = 1;
  string participant = 2;  // participant address
}
```

Key: `(model_id, participant)`. At most one entry per key.

Consumed on use. During `onEndOfPoCValidationStage` for epoch N, resolution reads active refusals and deletes them. To refuse again in N+1, the participant must resubmit.

No `target_epoch` field. Refusal means "refuse for the next unresolved epoch."

### `PoCDirectIntent`

```proto
message PoCDirectIntent {
  string model_id    = 1;
  string participant = 2;  // participant address
}
```

Key: `(model_id, participant)`. At most one entry per key.

Consumed on use, same as `PoCRefusal`. Declares "I commit to deploy an MLNode for this model before PoC starts." If the group becomes pre-eligible, the host must participate or face NONE penalty. If the group does not become pre-eligible, the intent is harmless and consumed without consequence.

### Last-write-wins

Submitting `MsgSetPoCDelegation` clears any active `PoCRefusal` or `PoCDirectIntent` for the same `(model_id, sender)`.
Submitting `MsgRefusePoCDelegation` clears any standing `PoCDelegation` or `PoCDirectIntent` for the same `(model_id, sender)`.
Submitting `MsgDeclarePoCIntent` clears any standing `PoCDelegation` or `PoCRefusal` for the same `(model_id, sender)`.

At any time, a participant has at most one of {delegation, refusal, intent} per model.

## State: Resolved Voting Power

### `ModelVotingPower`

```proto
message ModelVotingPower {
  string model_id     = 1;
  int64  voting_power = 2;
}
```

Added as a repeated field on `ActiveParticipant`:

```proto
message ActiveParticipant {
  string index                       = 1;
  string validator_key               = 2;
  int64  weight                      = 3;  // consensus weight (aggregated across groups)
  string inference_url               = 4;
  repeated string models             = 5;  // sorted by model_id
  RandomSeed seed                    = 6;
  repeated ModelMLNodes ml_nodes     = 7;  // per-model pocWeight and throughput, sorted by model_id
  repeated ModelVotingPower voting_powers = 8;  // per-model delegation-resolved votingPower, sorted by model_id
}
```

Only models where the participant is a DIRECT member get an entry. Delegators do not have their own `voting_powers` entry -- their consensusWeight is included in the delegate's votingPower.

### Validation Acceptance

See "PoC evaluation per model" in Epoch Snapshot Semantics for the full resolution flow. Summary:

`sum(votingPower(group_i, v) for v that approved p) / sum(consensusWeight(q) for all q) > 2/3`

In a slot-based validation model, the same votingPower values are used for slot sampling probability. Acceptance condition becomes: `slots_approved / total_slots > 2/3`, where slot assignment is weighted by votingPower.

Only DIRECT members vote. Their votingPower = own consensusWeight + delegated consensusWeight. DELEGATE, REFUSE, INTENT, and NONE participants do not vote -- a delegator's weight flows into the delegate's votingPower; a refuser's or non-participant's weight is absent from the numerator (effectively voting against acceptance).

The denominator is `sum(consensusWeight(p))` across all active participants. Root `EpochGroupData.total_weight` tracks this.

## Transactions

### `MsgSetPoCDelegation`

```proto
message MsgSetPoCDelegation {
  string sender      = 1;
  string model_id    = 2;
  string delegate_to = 3;  // empty to clear
}
```

Handler:
- `model_id` must be a governance-approved model
- `delegate_to` (when non-empty) must be a valid bech32 address (membership in the target group is checked at resolution time, not at tx time -- the target may join or leave before the next epoch)
- self-delegation rejected
- creates or overwrites `PoCDelegation(model_id, sender)`
- clears `PoCRefusal(model_id, sender)` and `PoCDirectIntent(model_id, sender)` if present

### `MsgRefusePoCDelegation`

```proto
message MsgRefusePoCDelegation {
  string sender   = 1;
  string model_id = 2;
}
```

Handler:
- `model_id` must be a governance-approved model
- records `PoCRefusal(model_id, sender)`
- clears `PoCDelegation(model_id, sender)` and `PoCDirectIntent(model_id, sender)` if present

### `MsgDeclarePoCIntent`

```proto
message MsgDeclarePoCIntent {
  string sender   = 1;
  string model_id = 2;
}
```

Handler:
- `model_id` must be a governance-approved model
- records `PoCDirectIntent(model_id, sender)`
- clears `PoCDelegation(model_id, sender)` and `PoCRefusal(model_id, sender)` if present
- if sender ends up submitting PoC for this model, they resolve as DIRECT (intent is superseded)

## WeightCalculator

Replaces the current `WeightCalculator` (renamed to `PoCWeightCalculator`). Takes pocWeight results + delegation state + governance params and produces all weights the system needs: group eligibility, per-group voting power, per-participant consensus weight, and group caps.

`PoCWeightCalculator` computes raw pocWeight from PoC results (model-local). `WeightCalculator` sits above it and handles everything cross-group.

### Inputs

```go
type WeightCalculator struct {
    // Per-group data (populated from PoCWeightCalculator output + model assignment)
    Groups             map[string]*GroupData  // model_id -> group data
    
    // Network-wide data (from delegation snapshot + ActiveParticipants(N-1))
    ConsensusWeights   map[string]int64       // participant -> ActiveParticipant.Weight from N-1
    TotalNetworkWeight int64                  // sum(ConsensusWeights)
    Delegations        map[string]map[string]string  // model_id -> (delegator -> delegate_to)
    Refusals           map[string]map[string]bool    // model_id -> (participant -> true)
    Intents            map[string]map[string]bool    // model_id -> (participant -> true)
    Params             WeightParams
}

type GroupData struct {
    Members          []string          // addresses of direct group members
    MemberPocWeights map[string]int64  // member -> pocWeight in this group
    ConsensusKoeff   math.LegacyDec   // coefficient for this model
    IsInitialGroup   bool              // exempt from cap
}

type WeightParams struct {
    WThreshold  math.LegacyDec  // minimum fraction of total weight from members for eligibility
    VMin        int64           // minimum number of hosts with non-zero consensus weight
    CapFactor   math.LegacyDec  // max group weight as multiple of members' weight in other groups
}
```

`ConsensusWeights` is `ActiveParticipant.Weight` from epoch N-1 for each participant. This is the aggregated consensus weight (`sum(koeff_i * pocWeight)` across eligible groups) that gets delegated.

`Members` is determined by actual PoC participation. A participant is a member if it submitted PoC results for this model.

### Group eligibility

Each condition is a separate function. All take `modelID` and operate on `Groups[modelID]`.

```go
// Condition 1: model is governance-approved with defined coefficient.
func (wc *WeightCalculator) IsGovernanceApproved(modelID string) bool

// Condition 2: members' consensus weight >= W_threshold * total network weight.
func (wc *WeightCalculator) MeetsWeightThreshold(modelID string) bool

// Condition 3: at least V_min members have non-zero consensus weight.
func (wc *WeightCalculator) MeetsMinHosts(modelID string) bool

// All pre-eligibility conditions combined.
func (wc *WeightCalculator) IsGroupPreEligible(modelID string) bool
```

Pre-eligibility is evaluated at `start_poc - deploy_window` using delegation snapshot + `ActiveParticipants(N-1)` data. Advisory only. Full eligibility is checked after PoC completes:

```go
// Checked after PoC completes. At least V_min members must have pocWeight > 0.
// Also serves as the independence check: prevents a group where only one
// member has real pocWeight and all acceptance power comes from delegations.
func (wc *WeightCalculator) IsGroupEligible(modelID string) bool
```

### Per-group voting power

```go
// ResolveGroupParticipation returns participation mode for each participant
// with positive consensus weight, for one model group.
func (wc *WeightCalculator) ResolveGroupParticipation(modelID string) map[string]ParticipationMode

// ComputeGroupVotingPowers resolves delegation for one model group and returns
// per-voter votingPower. Only DIRECT members get entries -- their votingPower
// includes delegated consensus weight.
//
// For each direct member m:
//   votingPower(m) = consensusWeight(m) + sum(consensusWeight(d)) for all d delegating to m
func (wc *WeightCalculator) ComputeGroupVotingPowers(modelID string) []ModelVotingPower
```

Mode resolution inside `ResolveGroupParticipation`:

```go
for each participant with ConsensusWeights[p] > 0:
    if p in group.Members  -> DIRECT
    else if Intents[p]     -> INTENT (didn't deploy; treated as NONE for penalty)
    else if Refusals[p]    -> REFUSE
    else if Delegations[p] targets a valid member -> DELEGATE
    else                   -> NONE
```

Invalid delegate target (not a member, or member with zero consensus weight) resolves to NONE and emits an event for operator visibility. Resolution never panics or aborts epoch creation.

### Per-group consensus weight cap

From README Appendix A. Limits how much consensus weight a group can contribute based on its members' proven weight in other eligible groups.

```go
// ComputeGroupCap returns the maximum consensus weight this group can
// contribute. Returns -1 (uncapped) for the initial group.
//
// cap = CapFactor * sum(member's consensus weight from other eligible groups)
//
// Uses epoch N-1 consensus weight (ConsensusWeights) minus each member's
// N-1 contribution from this group (koeff_i * previous pocWeight in this group).
// This breaks circular dependence -- we use proven weight from the previous
// epoch, not the one being computed.
//
// If koeff * sum(pocWeight) for this group exceeds the cap, scale all members
// proportionally to fit.
func (wc *WeightCalculator) ComputeGroupCap(modelID string) int64
```

The cap limits how much `consensusKoeff * pocWeight` from this group flows into `ActiveParticipant.Weight`. The cap is based on pocWeight and proven weight in other groups, not on votingPower or delegation.

### Total consensus weight

```go
// ComputeConsensusWeights produces final ActiveParticipant.Weight for each
// participant across all eligible groups, applying coefficients and caps.
//
// For each eligible group: if koeff_i * sum(pocWeight) exceeds the group cap,
// scale all members' contributions from this group proportionally to fit.
// Then: consensusWeight(p) = sum over eligible groups of (scaled koeff_i * pocWeight(group_i, p))
func (wc *WeightCalculator) ComputeConsensusWeights() map[string]int64
```

Replaces the current inline loop in `onEndOfPoCValidationStage` that calls `AggregateConsensusWeight`. Per-group caps applied before summing.

## Integration Into Epoch Creation

Current pipeline in `onEndOfPoCValidationStage` (module.go:539):

```
1. ComputeNewWeights        -> ActiveParticipants with per-model MlNodes/pocWeight
2. setModelsForParticipants  -> assigns models to participants
3. AggregateConsensusWeight  -> combines per-model weights with coefficients
4. AdjustWeightsByCollateral
5. ApplyPowerCapping         -> individual participant cap (existing, unchanged)
6. SetActiveParticipants
7. addEpochMembers
```

With delegation, `WeightCalculator` absorbs steps 3-5 and adds eligibility + delegation:

```
0. Delegation snapshot + IsGroupPreEligible (advisory)
                                  -> at start_poc - deploy_window. Uses N-1 data.
1. PoCWeightCalculator         -> ActiveParticipants with per-model MlNodes/pocWeight
                                  (renamed from ComputeNewWeights)
2. setModelsForParticipants    -> assigns models to participants
3. WeightCalculator phase 1    -> post-PoC eligibility, caps, consensus weight:
   a. checks group eligibility (IsGroupEligible -- V_min members with pocWeight > 0)
   b. computes group caps (ComputeGroupCap)
   c. computes consensus weight with caps applied (ComputeConsensusWeights)
   d. resolves participation modes (ResolveGroupParticipation)
4. ApplyDelegationWeightAdjustment -> weight penalties/transfers based on modes
5. AdjustWeightsByCollateral
6. ApplyPowerCapping           -> individual participant cap (existing, unchanged)
7. WeightCalculator phase 2    -> voting power per model group from final consensus weights:
   ComputeGroupVotingPowers(modelID) called for each eligible group,
   uses post-adjustment ActiveParticipant.Weight.
   Result written to ActiveParticipant.voting_powers.
8. SetActiveParticipants       -> persists ActiveParticipants(N) with both
                                  .weight (consensus) and .voting_powers (per-group)
9. addEpochMembers
10. Cleanup: clear consumed PoCRefusal and PoCDirectIntent entries
```

Participation modes are resolved in phase 1 (step 3d) because delegation weight adjustment (step 4) needs them. Voting power is computed in phase 2 (step 7) because it must reflect final consensus weights after all adjustments.

## Delegation Weight Adjustment

Happens in the same block as `WeightCalculator`, using its resolved participation modes directly. All adjustments are to consensus weight, not token transfers.

Applied per participant, per governance-approved model group where the participant is not a DIRECT member. Fractions are applied to the participant's consensus weight at the time of adjustment.

- `REFUSE` -> `consensusWeight(p) -= consensusWeight(p) * r_refusal`
- `NONE` (no choice made) -> `consensusWeight(p) -= consensusWeight(p) * r_penalty`
- `DELEGATE` -> `delta = consensusWeight(delegator) * r_delegation; consensusWeight(delegator) -= delta; consensusWeight(delegate) += delta`

If a participant is REFUSE or NONE for multiple groups, the reductions compound (each applied to the already-reduced weight). This makes the total penalty increase with the number of groups the participant ignores.

These adjustments apply after `ComputeConsensusWeights` and before `AdjustWeightsByCollateral` in the pipeline.

## File Layout

Inside `x/inference`:

| Purpose | Path |
|---------|------|
| Proto: delegation types, messages | `proto/inference/inference/poc_delegation.proto` |
| Proto: ModelVotingPower on ActiveParticipant | `proto/inference/inference/activeparticipants.proto` |
| Keeper: read/write PoCDelegation, PoCRefusal, PoCDirectIntent | `keeper/poc_delegation.go` |
| Keeper: msg server for delegation txs | `keeper/msg_server_poc_delegation.go` |
| Module: WeightCalculator | `module/weight_calculator.go` |
| Module: delegation weight adjustment | `module/delegation_weight_adjustment.go` |
| Tests: weight calculator tests | `module/weight_calculator_test.go` |
| Tests: keeper tests | `keeper/poc_delegation_test.go` |

## Queries

```proto
// Returns the current delegation state for a participant and model.
// This is the mutable state that will be used in the next delegation snapshot.
message QueryPoCDelegationRequest {
  string participant = 1;
  string model_id    = 2;  // empty to return all models
}

message QueryPoCDelegationResponse {
  repeated PoCDelegation   delegations = 1;
  repeated PoCRefusal      refusals    = 2;
  repeated PoCDirectIntent intents     = 3;
}
```

## Open Questions

- Concrete rule for `IsGroupEligible` independence: minimum distinct members with pocWeight > 0, or minimum fraction of group pocWeight from non-top-1 members?
- ComputeGroupCap needs each member's N-1 contribution from this specific group. Does `WeightCalculator` have access to previous epoch's per-group pocWeight, or do we need to store it?
- Should delegation weight adjustment fractions (`r_refusal`, `r_penalty`, `r_delegation`) be per-group (different for each model) or global governance parameters?
- Grace window (`T_grace`): during grace, participants must choose but no penalty for any choice. Where is grace tracked -- per model group creation epoch? Does the WeightCalculator need to know the group's age?
- Snapshot vs immediate delegation: should delegation changes take effect only at the next snapshot (`start_poc - deploy_window`), or should they be usable immediately? Snapshot is more consistent (all PoC evaluations within an epoch use the same delegation state) but adds complexity (snapshot store, freeze point). Immediate use is simpler but means delegation state can change between PoC evaluation of different model groups within the same epoch. Needs analysis of whether mid-epoch delegation changes create attack vectors.
