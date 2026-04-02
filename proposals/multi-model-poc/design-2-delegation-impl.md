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

votingPower for epoch N depends on consensusWeight, which depends on N's PoC results. This creates two phases within each epoch:

1. Regular PoC validation at start of epoch N reads `ActiveParticipants(N-1).voting_powers` and uses it when counting PoC validations (acceptance threshold). New PoC results don't exist yet.

2. After PoC validation completes, `PoCWeightCalculator` produces raw pocWeight, then `WeightCalculator` runs (phase 1 + adjustments + phase 2):
   - resolves delegations from mutable state
   - computes new consensusWeight (`.weight`) and per-group votingPower (`.voting_powers`)
   - writes result to `ActiveParticipants(N)`

3. All confirmation PoC during epoch N reads `ActiveParticipants(N).voting_powers`.

Delegation transactions submitted during epoch N affect only epoch N+1. Mutable delegation state is read once during step 2 and the result is immutable for the rest of the epoch.

## Participation Modes

For a given model group and epoch, each participant with positive consensus weight resolves to exactly one mode:

| Mode | Meaning |
|------|---------|
| `DIRECT` | Member of the group. Validates directly. |
| `DELEGATE` | Not a member. Delegates consensus weight to one group member. |
| `REFUSE` | Not a member. Explicitly refuses delegation for this epoch. |
| `NONE` | Not a member. No valid delegation, no refusal. |

Resolution priority: DIRECT > REFUSE > DELEGATE > NONE.

Direct membership always wins regardless of submitted delegation or refusal. Serving the model is the participation action -- no separate transaction needed.

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

No `target_epoch` field. Refusal always means "refuse for the next unresolved epoch." Avoids stale records and does not require participants to know future epoch numbers.

### Last-write-wins

Submitting `MsgSetPoCDelegation` clears any active `PoCRefusal` for the same `(model_id, sender)`.
Submitting `MsgRefusePoCDelegation` clears any standing `PoCDelegation` for the same `(model_id, sender)`.

At any time, a participant has at most one of {delegation, refusal} per model.

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

`ActiveParticipant` already carries per-model data (`models`, `ml_nodes`). `voting_powers` is the same pattern.

Only models where the participant is a DIRECT member get an entry. Delegators do not have their own voting_powers entry -- their consensusWeight is included in the delegate's votingPower. `ActiveParticipants` is immutable per epoch -- written once at epoch creation, never modified. The right place for data that must not change mid-epoch.

### Validation Acceptance

Host p's PoC result in group_i is accepted if:

`sum(votingPower(group_i, v) for v that approved p) / sum(consensusWeight(q) for all q) > 2/3`

In a slot-based validation model, the same votingPower values are used for slot sampling probability. Acceptance condition becomes: `slots_approved / total_slots > 2/3`, where slot assignment is weighted by votingPower.

A voter is a DIRECT member of the group who submitted a validation result. Only DIRECT members execute validation -- they run the PoC and submit approve/reject. Their votingPower includes their own consensusWeight plus any weight delegated to them.

DELEGATE, REFUSE, and NONE participants do not vote independently. A delegator's consensusWeight flows into the delegate's votingPower. A refuser's or non-participant's consensusWeight is absent from the numerator entirely -- effectively voting against acceptance.

The denominator is `sum(consensusWeight(p))` across all active participants. Root `EpochGroupData` (where `model_id` is empty) already tracks this as `total_weight` -- incremented by `AddMember` for each participant's `ActiveParticipant.Weight`.

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
- clears `PoCRefusal(model_id, sender)` if present

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
- clears `PoCDelegation(model_id, sender)` if present

## WeightCalculator

Replaces the current `WeightCalculator` (renamed to `PoCWeightCalculator`). Takes pocWeight results + delegation state + governance params and produces all weights the system needs: group eligibility, per-group voting power, per-participant consensus weight, and group caps.

`PoCWeightCalculator` computes raw pocWeight from PoC results (model-local). `WeightCalculator` sits above it and handles everything cross-group.

### Inputs

```go
type WeightCalculator struct {
    // Per-group data (populated from PoCWeightCalculator output + model assignment)
    Groups             map[string]*GroupData  // model_id -> group data
    
    // Network-wide data
    ConsensusWeights   map[string]int64       // participant -> ActiveParticipant.Weight from N-1
    TotalNetworkWeight int64                  // sum(ConsensusWeights)
    Delegations        map[string]map[string]string  // model_id -> (delegator -> delegate_to)
    Refusals           map[string]map[string]bool    // model_id -> (participant -> true)
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

`Members` is determined by hardware assignment (`setModelsForParticipants`). A participant is a member if it has an MLNode deployed for this model.

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

Pre-eligibility is evaluated at epoch start (during `initializeUpcomingEpochModelGroups`) using epoch N-1 data. Only pre-eligible groups get subgroups created and run PoC. Full eligibility is checked after PoC completes:

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

The cap limits how much `consensusKoeff * pocWeight` from this group flows into `ActiveParticipant.Weight`. It does not affect pocWeight or votingPower.

Delegation affects votingPower but not the cap. The cap is based on pocWeight and proven weight in other groups.

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

This replaces the current inline loop in `onEndOfPoCValidationStage` that calls `AggregateConsensusWeight`. The cap logic integrates naturally here -- apply per-group caps before summing.

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
0. IsGroupPreEligible          -> at epoch start, before PoC. Determines which
                                  groups get subgroups and run PoC. Uses N-1 data.
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
   Caller also: clears consumed refusals
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
| Keeper: read/write PoCDelegation, PoCRefusal | `keeper/poc_delegation.go` |
| Keeper: msg server for delegation txs | `keeper/msg_server_poc_delegation.go` |
| Module: WeightCalculator | `module/weight_calculator.go` |
| Module: delegation weight adjustment | `module/delegation_weight_adjustment.go` |
| Tests: weight calculator tests | `module/weight_calculator_test.go` |
| Tests: keeper tests | `keeper/poc_delegation_test.go` |

## Queries

```proto
// Returns the current PoCDelegation or PoCRefusal for a participant and model.
// This is the mutable state that will be used in the next epoch resolution.
message QueryPoCDelegationRequest {
  string participant = 1;
  string model_id    = 2;  // empty to return all models
}

message QueryPoCDelegationResponse {
  repeated PoCDelegation delegations = 1;
  repeated PoCRefusal    refusals    = 2;
}
```

## Open Questions

- Concrete rule for `IsGroupEligible` independence: minimum distinct members with pocWeight > 0, or minimum fraction of group pocWeight from non-top-1 members?
- ComputeGroupCap needs each member's N-1 contribution from this specific group. Does `WeightCalculator` have access to previous epoch's per-group pocWeight, or do we need to store it?
- Should delegation weight adjustment fractions (`r_refusal`, `r_penalty`, `r_delegation`) be per-group (different for each model) or global governance parameters?
- Grace window (`T_grace`): during grace, participants must choose but no penalty for any choice. Where is grace tracked -- per model group creation epoch? Does the WeightCalculator need to know the group's age?
