# Multi-Model PoC: Design 2 - Aggregation and Delegation

Working note for `README.md`. This file stays at the design level. It covers:
- aggregation of per-model PoC into total consensus weight
- delegation needed when direct validation becomes model-local
- the high-level epoch timeline needed for bootstrap models

`design-1.md` covers the model-aware PoC state and the existing PoC flow. This file focuses on the policy layer above it.

## Scope
- Assume `design-1.md` is implemented first.
- One MLNode serves one model at a time.
- A host serving two models uses two distinct MLNodes.
- This document describes the protocol shape, not final proto fields or exact keeper APIs.
- Inference validation weight semantics are out of scope here. This note is about PoC aggregation, delegation, and model-local selection rules, not about cleaning up the existing inference-validation paths.

## Problem
Once PoC becomes per-model, the chain must answer two separate questions:

1. How does per-model `pocWeight(group_i, p)` become total `consensusWeight(p)`?
2. How can the whole chain accept or reject a model-local PoC result if only members of that model group can validate it directly?

These are related, but they can be designed mostly independently:
- aggregation decides how much consensus value each model contributes
- delegation decides how non-members of a model group still participate in accepting or rejecting that group's PoC results

## Aggregation

Each eligible model group produces local PoC weight:
- `pocWeight(group_i, p)` is host `p`'s proven compute inside model group `group_i`

The chain converts local PoC weight into total consensus weight with governance-defined coefficients:

`consensusWeight(p) = sum over eligible groups of consensusKoeff_i * pocWeight(group_i, p)`

This keeps two numbers separate:
- local routing weight inside a model group
- total consensus weight across the chain

### Why coefficients are needed
Different models measure different kinds of hardware capacity:
- VRAM requirements differ
- tensor parallelism requirements differ
- memory bandwidth and interconnect matter differently
- throughput per nonce differs

Raw PoC numbers are not directly comparable across models. A per-model coefficient is the simplest conversion rule.

### Desired properties
- Inference routing stays model-local.
- Any model-aware selection flow derived from routing capacity, such as subnet escrow, should sample from the chosen model subgroup's local `weight`, not from root consensus weight or delegated voting power.
- Consensus power is aggregated across eligible groups.
- Governance can add or adjust coefficients without redesigning PoC.
- The chain can value scarce or strategically important models differently.

## Delegation

### Why it exists
In the current single-model system, every validator can directly validate the same PoC workload.

In the multi-model system:
- direct execution becomes model-local
- only hosts serving model `X` can directly validate PoC for model `X`

Without delegation, a model group with less than `2/3` of total network weight could never validate itself. Delegation restores chain-wide participation in acceptance of model-local PoC results.

### High-level idea
For each model group:
- members validate directly
- non-members can delegate validation power to a member of that group
- delegated weight changes who can approve or reject PoC, but does not change `pocWeight`

This creates two layers:
- direct execution is model-local
- acceptance power remains chain-wide

Delegation properties:
- per model group
- epoch-bound: changes take effect from the next snapshot, not mid-validation
- trust model: delegator trusts delegate to vote honestly for that group
- non-members who don't delegate effectively vote against acceptance

## Epoch Timeline

The high-level timeline is:

1. Before `start_poc - deploy_window`
   Regular epoch `N` is running. Delegation, refusal, and direct-intent preferences remain mutable.

2. At `start_poc - deploy_window`
   The chain evaluates bootstrap candidates for epoch `N+1`.
   It snapshots bootstrap-only state:
   - direct intent for new models
   - delegations relevant to those new models
   - `AP(N).weight` as the base consensus weight

   This snapshot is advisory. It answers:
   - which new models look pre-eligible
   - whether `> 2/3` acceptance is even reachable for them

3. At `start_poc`
   PoC generation starts for epoch `N+1`.

4. At start of PoC validation
   The chain freezes normal delegation state for validation:
   - delegation
   - explicit refusal
   - no intent in this snapshot

   Validation power for the `PoCValidationSnapshot` is then built in two different ways:
   - models already present in `AP(N).voting_powers` reuse those voting powers
   - bootstrap models derive voting power from `AP(N).weight` plus the earlier bootstrap delegation snapshot
   - DIRECT membership for bootstrap models comes from who actually submitted PoC store commits

5. At end of PoC validation
   The chain computes `AP(N+1)`:
   - evaluate accepted PoC results
   - compute new `consensusWeight`
   - compute `AP(N+1).voting_powers`
   - carry the new active set into the next epoch

This split is important. Existing eligible groups use the already-established `AP(N).voting_powers`. Bootstrap groups use `AP(N).weight` plus new PoC participation.

## Aggregation and Delegation Together

The combined flow is:
1. bootstrap pre-eligibility for new models
2. model-local PoC generation
3. model-local validation with delegated voting power
4. acceptance or rejection of each model group's PoC
5. conversion of accepted model-local PoC weight into total consensus weight
6. computation of next-epoch per-model voting powers

## Current Open Questions
- Should post-PoC eligibility require the same explicit reachability check as bootstrap pre-eligibility?
- Should delegation affect only acceptance, or also slot sampling in every path?
- Should delegation penalties and reward sharing modify consensus weight, rewards, or both?
- Should a new group's consensus contribution be capped until it has mature weight outside itself?
- Can a model serve inference before it becomes consensus-eligible?

## Operational Constraint: Model Changes

Mid-epoch model switching remains a real edge case.

If an operator changes a node from model A to model B during epoch `N`:
- staying on A too long may leave no time to load B before PoC in `N+1`
- switching to B too early means the node should stop being trusted for A during the rest of `N`

The intended rule remains:
- operator may redeploy during epoch `N`
- once redeployed, the node drops out of current-epoch scheduling for A
- at the start of `N+1`, it participates only if B is already loaded and healthy

There is no warm-up window. If the new model is not ready at epoch start, the node misses that cycle. That is operator risk.

## Relationship To `README.md`
`README.md` should keep the proposal-level story. This file exists to isolate the two core design topics that are easiest to reason about separately:
- coefficient-based aggregation
- delegation-based validation
