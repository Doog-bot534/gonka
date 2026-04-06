# Multi-Model PoC: Delegation Implementation

Covers delegation, bootstrap pre-eligibility, voting-power resolution, and consensus-weight capping. Assumes `design-1.md` is implemented.

Out of scope:
- model registration and governance approval flow
- unregistered models
- the detailed economics of reward sharing

## Three Weight Terms

`pocWeight(group_i, p)`
- raw compute proved by host `p` inside model group `group_i`
- model-local
- drives inference routing inside the group

`consensusWeight(p)`
- aggregated weight across eligible groups
- `sum(koeff_i * pocWeight(group_i, p))`
- stored as `ActiveParticipant.Weight`
- drives consensus power and is the weight that gets delegated

`votingPower(group_i, p)`
- PoC validation acceptance power in model group `group_i`
- for a direct member:
  `consensusWeight(p) + sum(consensusWeight(d) for delegators d -> p in group_i)`

These numbers must stay separate. A participant can have:
- low `pocWeight` in a group but high `votingPower` from delegators
- high `pocWeight` in a non-eligible group but zero contribution to `consensusWeight`

## Core Principle

DIRECT membership cannot be resolved at `start_poc - deploy_window`.

A host becomes DIRECT for a model only if it actually participates in PoC for that model. Because of that, the design now uses two different snapshots:

1. `BootstrapDelegationSnapshot`
   - captured at `start_poc - deploy_window`
   - used only for bootstrap models not already active in `AP(N)`
   - stores delegation + direct intent
   - used for advisory pre-eligibility and for bootstrap-model validation-time voting power, with delegators drawn from the existing active set and base weights from `AP(N).weight`
   - late delegation submitted after this snapshot does not affect the current bootstrap-model validation path

2. `DelegationSnapshot`
   - captured at `poc_validation_start`
   - used for next-epoch participation resolution and voting-power computation
   - stores delegation + refusal
   - intentionally excludes intent

The timeline below is the intended procedure.

## Epoch Procedure

Let `AP(N)` be the effective active set in epoch `N`. Let `AP(N+1)` be the active set produced at the end of PoC validation.

### 1. Before `start_poc - deploy_window`

Mutable preference state is live:
- `PoCDelegation`
- `PoCRefusal`
- `PoCDirectIntent`

Changes still affect epoch `N+1`.

### 2. At `start_poc - deploy_window`

Capture `BootstrapDelegationSnapshot` for bootstrap candidates:
- only governance-approved models not already active in `AP(N)`
- only active participants from `AP(N)`
- delegation + direct intent
- base weights from `AP(N).weight`

Use it to evaluate advisory pre-eligibility for bootstrap models:
- governance approval
- weight threshold
- `V_min`
- `2/3` reachability

Emit an event so operators can see whether a bootstrap model looks viable before deploying hardware.

### 3. Between `start_poc - deploy_window` and `start_poc`

Hosts with direct intent deploy hardware for bootstrap models they expect to qualify.

This deploy window is operational only. It does not create trust. If the host is not ready at PoC time, it is not DIRECT.

If a host overwrites direct intent with delegation after the bootstrap snapshot, that change is too late for the current bootstrap-model validation path. The frozen bootstrap snapshot still controls current-cycle bootstrap validation.

### 4. At `poc_validation_start`

Capture `DelegationSnapshot`:
- delegation
- refusal
- no intent
- filtered to active participants and approved models

Then build `PoCValidationSnapshot.ModelVotingPowers` with two branches:

1. Existing active models
   Reuse `AP(N).voting_powers`.

2. Bootstrap models
   Build fresh voting power from:
   - `AP(N).weight`
   - `BootstrapDelegationSnapshot`
   - current stage store commits

This is the key split in the procedure:
- for groups already in `AP(N).voting_powers`, reuse them
- for groups not yet active, derive validation power from `AP(N).weight` plus bootstrap delegation

### 5. During regular PoC validation

For a participant-model pair `(p, group_i)`:

1. DIRECT members are participants who submitted PoC for `group_i`.
2. Only DIRECT members get validation voting-power entries.
3. For existing active models, voting powers come from `AP(N).voting_powers` (already delegation-resolved at previous epoch formation).
4. For bootstrap models, each DIRECT member starts with its own `AP(N).weight`, and delegated `AP(N).weight` from non-members flows to the delegate.
5. Acceptance uses:

   `sum(votingPower of approvers) / totalNetworkWeight > 2/3`

For slot-based validation, the same `votingPower` values define slot sampling probability.

### 6. At end of PoC validation

Compute `AP(N+1)`:
- `PoCWeightCalculator` computes raw per-model `pocWeight`
- model assignment sets model membership for next epoch
- `DelegationWeightCalculator` computes:
  - eligible groups
  - group caps
  - new `consensusWeight`
  - participation modes using `DelegationSnapshot`
- collateral adjustment and power capping apply
- `computeAndSetVotingPowers` writes `AP(N+1).voting_powers`

These next-epoch voting powers use:
- final post-adjustment weights
- `DelegationSnapshot`
- DIRECT membership from assigned models in `AP(N+1)`

### 7. During confirmation PoC in epoch `N+1`

Confirmation PoC reuses `AP(N+1).voting_powers`.

At confirmation `GENERATION -> VALIDATION`, `captureConfirmationValidationSnapshot` writes a `PoCValidationSnapshot` from those stored voting powers. DAPI uses the same snapshot for slot sampling.

### 8. Cleanup

After `SetActiveParticipants(N+1)`:
- clear `PoCRefusal`
- clear `PoCDirectIntent`
- overwrite both snapshot stores on the next cycle

## Participation Modes

For a model group and epoch, a participant with positive consensus weight resolves to one mode:

| Mode | Meaning |
|------|---------|
| `DIRECT` | Member of the group for that epoch. Validates directly. |
| `DELEGATE` | Delegates to one direct member in that group. |
| `REFUSE` | Explicitly refuses delegation for that group. |
| `NONE` | No valid delegation, no refusal, no direct membership. |

Resolution logic (pseudocode):

```
for each participant with ConsensusWeights[p] > 0:
    if p in group.Members  -> DIRECT
    else if Refusals[p]    -> REFUSE
    else if Delegations[p] targets a valid DIRECT member with positive weight -> DELEGATE
    else                   -> NONE
```

Invalid delegate target (not a member, or member with zero weight) resolves to NONE. An event is emitted for operator visibility. Resolution never panics or aborts epoch creation.

Direct intent only matters in bootstrap pre-eligibility. If the group is already pre-eligible from existing members plus delegations, intent adds nothing -- the host can just deploy and become DIRECT. Intent exists so hosts can signal commitment before deploying hardware, letting the chain count them toward pre-eligibility thresholds.

A group that fails pre-eligibility can still become eligible if enough hosts independently participate in PoC for it. Pre-eligibility is advisory, not a hard block.

Important nuance on current implementation:
- direct intent matters only in bootstrap pre-eligibility evaluation
- direct intent is not part of `DelegationSnapshot`
- next-epoch participation resolution has no separate `INTENT` mode
- a missed bootstrap intent is handled implicitly: no commit means no DIRECT role, and a late delegation after the bootstrap snapshot does not change current bootstrap validation

## State

### Mutable preference state

`PoCDelegation`
```proto
message PoCDelegation {
  string model_id    = 1;
  string delegator   = 2;
  string delegate_to = 3;
}
```

- key: `(model_id, delegator)`
- persistent until overwritten or cleared
- split delegation is not supported

`PoCRefusal`
```proto
message PoCRefusal {
  string model_id    = 1;
  string participant = 2;
}
```

- key: `(model_id, participant)`
- consumed after epoch formation: to refuse again in the next epoch, the participant must resubmit

`PoCDirectIntent`
```proto
message PoCDirectIntent {
  string model_id    = 1;
  string participant = 2;
}
```

- key: `(model_id, participant)`
- bootstrap-only intent to become DIRECT in the next epoch
- consumed after epoch formation

### Transaction handler rules

`MsgSetPoCDelegation`:
- `model_id` must be governance-approved
- `delegate_to` (when non-empty) must be valid bech32 (membership checked at resolution time, not tx time)
- self-delegation rejected
- empty `delegate_to` clears the entry
- clears refusal and intent for the same `(model_id, sender)`

`MsgRefusePoCDelegation`:
- `model_id` must be governance-approved
- clears delegation and intent for the same `(model_id, sender)`

`MsgDeclarePoCIntent`:
- `model_id` must be governance-approved
- clears delegation and refusal for the same `(model_id, sender)`
- if sender submits PoC for this model, they resolve as DIRECT (intent superseded)

At most one of `{delegation, refusal, intent}` may exist per `(model_id, participant)` at any time.

### Snapshot state

`BootstrapDelegationSnapshot`
- snapshot height
- filtered delegations for bootstrap models
- filtered intents for bootstrap models

Bootstrap pre-eligibility results are emitted as advisory events at snapshot time. They are not persisted inside the snapshot state.

`DelegationSnapshot`
- snapshot height
- filtered delegations for approved models
- filtered refusals for approved models
- no intents

`PoCValidationSnapshot`
- stage start height
- snapshot height
- app hash
- timestamps
- `ModelVotingPowers`
- `TotalNetworkWeight`

## Validation Voting Powers

Only DIRECT members get voting-power entries for a model.

For a DIRECT member `m`:

`votingPower(group_i, m) = baseWeight(m) + sum(baseWeight(d) for valid delegators d -> m)`

Where `baseWeight` depends on the path:
- regular PoC, existing model: `AP(N).voting_powers` already resolved
- regular PoC, bootstrap model: `AP(N).weight`
- confirmation PoC: final `AP(N+1).weight`

The acceptance rule is:

`sum(votingPower of approvers) / totalNetworkWeight > 2/3`

For slot sampling:
- slot assignment is weighted by the same `votingPower`
- acceptance becomes `approved_slots / total_slots > 2/3`

## DelegationWeightCalculator

The cross-group calculator operates on:
- `Groups`
- `ConsensusWeights` from `AP(N).weight`
- `Delegations`
- `Refusals`
- governance params `WThreshold`, `VMin`, `CapFactor`

It provides:
- `IsGroupPreEligible`
- `ProjectedReachableVotingPower`
- `MeetsReachabilityThreshold`
- `IsGroupEligible`
- `ResolveGroupParticipation`
- `ComputeGroupCap`
- `ComputeConsensusWeights`
- `ComputeGroupVotingPowers`

### Eligibility

Bootstrap pre-eligibility currently checks:
- governance approval
- weight threshold
- `V_min`
- explicit reachability `> 2/3`

Post-PoC eligibility currently checks:
- governance approval
- weight threshold
- at least `V_min` members with positive `pocWeight`

Current repo status:
- bootstrap reachability is implemented
- post-PoC reachability is not yet enforced in `IsGroupEligible`

### Group cap

The cap limits how much `consensusKoeff * pocWeight` from one model may flow into `ActiveParticipant.Weight`.

Current formula:

`cap(group_i) = CapFactor * sum(member's N-1 consensus weight from other groups)`

The initial group is exempt.

This protects against a group fabricating large local PoC weight without having real consensus weight elsewhere.

## Integration Into Epoch Creation

Current next-epoch pipeline is:

```text
1. ComputeNewWeights
2. setModelsForParticipants
3. DelegationWeightCalculator:
   - EligibleGroups
   - ComputeConsensusWeights
   - ResolveGroupParticipation
4. AdjustWeightsByCollateral
5. ApplyPowerCapping
6. computeAndSetVotingPowers
7. SetActiveParticipants
8. addEpochMembers
9. cleanup refusal + intent
```

Regular PoC validation-time voting powers are built separately at `poc_validation_start`, not in this pipeline.

## Delegation Weight Adjustment

Design intent:
- `REFUSE` reduces reward or weight by `r_refusal`
- `NONE` reduces reward or weight by `r_penalty`
- `DELEGATE` transfers `r_delegation` value from delegator to delegate

Current repo status:
- the adjustment code exists
- the call site in `onEndOfPoCValidationStage` is active
- the current TODO is to decide whether this should affect reward, consensus weight, or both

So this part of the design is active in code, but the economics are still not final.

## File Layout

| Purpose | Path |
|---------|------|
| Proto: delegation types, messages, snapshot | `proto/inference/inference/poc_delegation.proto` |
| Proto: validation snapshot | `proto/inference/inference/poc_validation_snapshot.proto` |
| Keeper: delegation CRUD + snapshots | `keeper/poc_delegation.go` |
| Keeper: delegation tx handlers | `keeper/msg_server_poc_delegation.go` |
| Keeper: validation snapshot CRUD | `keeper/poc_validation_snapshot.go` |
| Module: DelegationWeightCalculator | `module/delegation_weight_calculator.go` |
| Module: delegation pipeline + snapshots | `module/delegation_pipeline.go` |
| Module: delegation weight adjustment | `module/delegation_weight_adjustment.go` |
| Tests: calculator + pipeline | `module/delegation_pipeline_test.go`, `module/weight_calculator_test.go` |

## Queries

`QueryPoCDelegation` returns the current mutable preference state for a participant and model:
- delegations
- refusals
- intents

This is the state that will feed the next snapshot cycle.

## Open Questions

- Should `IsGroupEligible` add the same explicit reachability check used by bootstrap pre-eligibility?
- What is the final independence rule for post-PoC eligibility beyond "`V_min` members with positive `pocWeight`"?
- Should delegation adjustment apply to rewards, weights, or both?
- Should adjustment fractions be global or per-model?
- Does the cap need exact previous-epoch per-group contribution, instead of the current approximation?
- How should a grace window be represented if bootstrap groups later need one?
