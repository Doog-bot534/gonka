# Multi-Model PoC Issues

Outstanding issues in the current `design-1.md` implementation.

Some items are older behaviors that became more visible once PoC became model-aware. Others are missing pieces that `design-1.md` still expects to be finished.

## 1. [fixed] Allocation voting constraint mixes raw and adjusted weight

Files:
- `inference-chain/x/inference/module/model_assignment.go`

What happens:
- `GetParticipantWeight()` sums raw `PocWeight` across all models.
- That raw cross-model sum is compared against `maxAllowedNonVotingWeight`.
- `maxAllowedNonVotingWeight` is derived from capped participant weight, which is already in consensus-power space.

Why this is wrong:
- The comparison mixes different units.
- It is only accidentally safe when all model coefficients are `1.0` and no other adjustment changes the scale.

Needed fix:
- Run the voting constraint in one consistent unit.
- Either keep the whole calculation in raw per-model space, or convert both sides into the same aggregated consensus-weight space.

## 2. [deferred, not critical when 1 MLNode = 1 model] `setModelsForParticipants()` can overwrite PoC-proven model buckets

Files:
- `inference-chain/x/inference/module/model_assignment.go`

What happens:
- `Calculator.Calculate()` builds per-model node buckets from actual PoC results.
- When hardware nodes are found, `setModelsForParticipants()` flattens those buckets and assigns nodes again from hardware declarations and governance-model ordering.

Why this is wrong:
- It can undo the model assignment that PoC already proved.
- Preserved nodes and fresh PoC nodes can end up in a different model bucket than the one they actually came from.

Needed fix:
- Keep Calculator-produced model buckets as the source of truth for proven nodes.
- Only use hardware metadata as a fallback when no reliable model assignment exists in PoC-derived state.

## 3. [fixed] Broker still has first-model fallback

Files:
- `decentralized-api/broker/broker.go`

What happens:
- `ResolveNodeModelID()` falls back to the first alphabetically sorted configured model when epoch assignment is ambiguous.
- `resolvePoCModelForNode()` rejects one ambiguous case, but still falls back to the first sorted configured model when no epoch assignment exists.

Why this is wrong:
- The runtime can silently schedule PoC for the wrong model.
- This keeps a single-model shortcut alive in a path that is supposed to be model-aware.

Needed fix:
- Remove first-model fallback from PoC scheduling.
- If model assignment is ambiguous, skip scheduling and surface the ambiguity explicitly.

## 4. [fixed, upgrade handler deferred] Upgrade cleanup and remaining single-model leftovers

What is still missing:
- Upgrade handler to prune legacy PoC-v2 records.
- Migration from singular PoC params into `PocParams.models`.
- Final verification that no single-model enforcement bridge remains.
- Removal of silent fallback for unknown `model_id`.

Why this matters:
- The runtime is moving toward fully model-aware PoC, but upgrade and cleanup work is still incomplete.
- Leaving compatibility shortcuts in place makes behavior harder to reason about and easier to break later.

Needed fix:
- Finish the Phase 5 cleanup path from `design-1.md`.

## 5. [deferred, pre-existing, not specific to multi-model] `ConfirmationWeight` uses live coefficients instead of epoch-pinned coefficients

Files:
- `inference-chain/x/inference/module/module.go`
- `inference-chain/x/inference/module/confirmation_poc.go`
- `inference-chain/x/inference/keeper/accountsettle.go`
- `inference-chain/x/inference/keeper/bitcoin_rewards.go`

What happens:
- `ConfirmationWeight` is now treated as aggregated consensus weight, which is correct.
- But the aggregation is recomputed from the current `PocParams.models[*].weight_scale_factor` at several later stages:
- epoch-member initialization
- confirmation update and ratio/slashing paths
- settlement recomputation

Why this is wrong:
- This is only safe if model coefficients cannot change during the lifetime of the epoch.
- If governance changes `weight_scale_factor` after epoch formation, the same epoch can be evaluated with different coefficients in different phases.

Needed fix:
- Use epoch-pinned coefficients for all confirmation-weight baseline, update, ratio, slashing, and settlement paths.
- Once an epoch is formed, do not recompute those values from live params.
