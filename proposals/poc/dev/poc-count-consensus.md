# Replace TreeRootCommit with validator-based PocCount consensus

## Summary

- Replace `MsgTreeRootCommit` (tree roots report counts) with `MsgPocCount` (any validator reports observed counts) and `MsgPocWeightCommit` (participants submit weights + count in one message)
- Compute `AgreedCounts` eagerly in EndBlock at `PoCCountDeadline` instead of lazily per query
- Trees are now pure P2P transport for header propagation -- no longer have on-chain consensus authority
- Fallback path (trees disabled) uses existing `MsgSubmitBatchV2` + `MsgPoCV2StoreCommit` + `MsgMLNodeWeightDistribution` unchanged
- Rename `PoCCommitPhase` to `PoCCountPhase` throughout

## Why the old design was wrong

The `MsgTreeRootCommit` approach had tree roots (a small subset of nodes) as the sole reporters of per-participant counts on-chain. This created two problems:

1. **Skewed consensus** -- with only 1-3 tree roots per tree reporting, a single dishonest root could shift the agreed count for every participant in that tree.
2. **Outsized influence** -- tree structure determined who got to report, coupling P2P topology to consensus authority. The transport layer should not dictate weight outcomes.

The fix separates concerns: trees handle gossip, all validators independently report what they observed, and the chain computes majority agreement across reporters.

## New flow

1. **PoCGenerate**: participants publish artifact headers via propagation trees, receivers record first arrivals
2. **PoCGenerateWindDown**: propagation continues, no new on-chain messages
3. **PoCCount** (renamed from PoCCommit): validators submit `MsgPocCount` with per-participant observed counts. At `PoCCountDeadline`, EndBlock runs `ComputeAgreedCounts` (majority of validators agreeing on >= count).
4. **PoCValidate**: participants query their `AgreedCount`, submit `MsgPocWeightCommit` (count + root hash + per-node weights in a single message)
5. **ComputeNewWeights** reads from `PocWeightCommits` when agreed counts exist, otherwise falls back to the original `PoCV2StoreCommit` + `MLNodeWeightDistribution` path

## Test plan

- [x] All chain node tests pass (`go test ./...`)
- [x] All API node tests pass (`go test ./...`)
- [ ] Integration tests via testermint
- [ ] Manual test on local testnet with propagation enabled
