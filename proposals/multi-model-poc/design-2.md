# Multi-Model PoC: Design 2 - Aggregation and Delegation

Working note for `README.md`. This file covers the high-level design for:
- aggregation of per-model PoC weight into consensus weight
- delegation needed when PoC validation becomes model-local

It is intentionally separated from `design-1.md`, which documents the current implementation and the low-level changes required to make PoC state model-aware.

## Scope
- Assume `design-1.md` is implemented first.
- One MLNode hosts one model at a time.
- If a host wants to support two models, it must have two distinct MLNodes.
- This document stays at the design level. It does not define final proto fields or exact keeper APIs yet.

## Problem
Once PoC becomes per-model, the chain needs answers to two independent questions:

1. How does per-model PoC weight become total consensus weight?
2. How is per-model PoC validated when only hosts serving that model can execute direct validation?

These are related, but they can be designed mostly independently:
- aggregation decides how much consensus value each model contributes
- delegation decides how non-members of a model group still participate in accepting or rejecting that group's PoC results

## Aggregation

### High-level idea
Each eligible model group produces its own local PoC weight:
- `pocWeight(group_i, p)` = host `p` weight inside model group `group_i`

The chain then converts those local weights into total consensus weight with governance-defined coefficients:

`consensusWeight(p) = sum over eligible groups of consensusKoeff_i * pocWeight(group_i, p)`

This keeps two things separate:
- local model capacity and routing weight inside a model group
- total consensus power across the whole chain

### Why coefficients are needed
Different models measure different kinds of hardware capacity:
- VRAM requirements differ
- tensor parallelism requirements differ
- memory bandwidth and interconnect matter differently
- throughput per nonce differs

So PoC results from different models are not directly comparable as raw numbers. A coefficient per model is the simplest way to convert model-local PoC weight into shared consensus weight.

### Desired properties
The aggregation rule should satisfy:
- model-local routing uses model-local weight only
- consensus power uses aggregated weight across eligible groups
- governance can add or adjust coefficients without changing the PoC mechanism itself
- higher-value models can contribute more consensus power if the chain wants to incentivize scarce hardware

Implementation note: the current intended shape is one `WeightCalculator` over all model-scoped validations for the stage. Keep this simple unless profiling shows multi-model validation volume makes the calculator path too heavy.

### Open design questions
- Should coefficients be static governance params or derived partly from observed demand?
- Should consensus weight from a new group be capped until the group is mature?
- Should coefficients apply before or after collateral adjustment and power capping?
- Should there be a special bootstrap rule for the first eligible model group?

## Delegation

### Why delegation appears
In the current single-model system, everyone can directly validate the same PoC model.

In the multi-model system, direct execution of PoC validation becomes model-local:
- only hosts serving model X can directly run validation for model X

That breaks the old assumption that every host directly validates every host. Delegation is the simplest way to restore chain-wide participation in acceptance of model-local PoC results.

### High-level idea
For each model group:
- a host inside the group can validate directly
- a host outside the group can delegate its validation power to some host inside the group

This creates two layers:
- direct execution is done only by model members
- acceptance power can still reflect broader consensus participation through delegation

### Desired properties
Delegation should provide:
- a way for non-members of a model group to participate indirectly
- a way to preserve chain-wide security assumptions even when validation execution is model-local
- a clear trust model: delegator trusts delegate to vote honestly for that group
- clear epoch-bound semantics so delegation does not change unpredictably mid-validation

### Minimum design requirements
The proposal will need to define:
- whether delegation is per group
- when delegation updates take effect
- whether delegation can be split across multiple delegates
- whether explicit refusal is allowed instead of delegation
- whether reward sharing between delegator and delegate exists
- what happens if a delegate is inactive or malicious

## Aggregation and Delegation Interaction
These mechanisms are mostly independent, but they meet at one place:
- delegated or direct validation decides whether a model group's PoC result is accepted
- accepted model-local PoC weight is then converted into consensus weight through coefficients

So the intended pipeline is:
1. model-local PoC generation
2. model-local direct validation
3. group acceptance using direct votes plus any delegated power defined for that group
4. conversion of accepted model-local weight into total consensus weight with coefficients

## What Still Needs To Be Decided
- eligibility conditions for a model group before its weight can count toward consensus
- whether delegation affects only PoC acceptance or also slot sampling
- how slot-based validation works when validator sets are group-local
- whether bootstrap groups need special protection or caps
- whether a model group can serve inference before it is eligible for consensus weight

## Problems

### Mid-epoch model change

Operator changes a node from model A to B during epoch N. PoC for epoch N+1 starts at the first block of N+1. There is no warm-up window.

That creates a direct conflict:
- if the node stays on A until the epoch boundary, it cannot load B in time for PoC in N+1
- if the node switches to B early, it can no longer be trusted as an A node for the rest of epoch N

Skipping all of N+1 is too expensive. It can mean about 24 hours of wasted server rent.

So the intended rule is:
- operator may redeploy the node to B during epoch N
- once that happens, the node drops out of current-epoch scheduling under A
- at the start of N+1, the node participates only if B is already loaded and healthy

This is an operator-risk action. In practice it should happen near the end of the epoch to minimize lost participation time.

### No warm-up window

No warm-up between epochs is critical for confirmation PoC security. Giving nodes extra time to prepare a model before a challenge is a direct attack vector.

A warm-up window is less problematic for regular PoC, but keeping one rule for both paths is simpler and safer:
- no designated warm-up window
- no trust in model B until the node is actually serving B and can be challenged

If a node is not ready at epoch start, it misses that cycle. That is operator risk, not a protocol failure.

## Relationship To `README.md`
`README.md` already contains the core proposal idea:
- per-model PoC
- coefficient-based aggregation
- delegation for model-local validation

This file exists to isolate those two topics from the implementation audit in `design-1.md`, so they can evolve independently.
