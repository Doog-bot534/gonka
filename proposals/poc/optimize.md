# Proposal: PoC Validation Sampling Optimization

## Implementation Status

**Status**: Implemented

**Key Files**:
- Algorithm: `inference-chain/x/inference/calculations/slots.go`
- Chain validation: `inference-chain/x/inference/module/chainvalidation.go`
- DAPI filtering: `decentralized-api/poc/validator.go`
- Snapshot storage: `inference-chain/x/inference/keeper/poc_validation_snapshot.go`
- Proto definitions: `inference-chain/proto/inference/inference/poc_validation_snapshot.proto`
- Parameter: `PocParams.ValidationSlots` in `inference-chain/proto/inference/inference/params.proto`

## Goal / Problem

Current PoC validation has O(N*N) complexity where N is the number of active participants:

- Each validator validates ALL participants with commits (`validator.go:ValidateAll` iterates `AllPoCV2StoreCommitsForStage`)
- Chain checks votes from ALL validators for each participant (`chainvalidation.go:pocValidated` iterates `CurrentValidatorWeights`)
- Total validations per epoch: N validators * N participants = N^2

This is not scalable. With 100 participants, 10,000 validations occur per epoch. With 1,000 participants, 1,000,000 validations.

## Proposal

Reduce complexity to O(N * N_SLOTS) by assigning each participant a fixed set of N_SLOTS validators through weighted random sampling. Only these assigned validators validate the participant, and only their votes count for consensus.

### Core Mechanism

1. **Deterministic Assignment**: After PoC generation completes, use app_hash (or block_hash at validation phase start) to deterministically assign N_SLOTS validators to each participant
2. **Weighted Sampling**: Validators are sampled proportionally to their weight in `CurrentValidatorWeights` using the `get_slots()` algorithm
3. **Fixed Validator Set**: Each participant P has exactly N_SLOTS assigned validators, computed as:
   ```
   assigned_validators = get_slots(app_hash, P.address, weights, N_SLOTS)
   ```
4. **Consensus**: Pass if >50% of assigned validators' weight votes valid

### Algorithm Reference

The sampling algorithm is prototyped in `proposals/poc/optimize.py`:

```python
def get_slots(app_hash, host_address, all_weights, n_slots, start_idx=0):
    """
    Sample n_slots validators based on weight distribution.
    
    Weight ranges:
        [0, 99]     => validator1 (weight 100)
        [100, 299]  => validator2 (weight 200)
        [300, 599]  => validator3 (weight 300)
    
    Complexity: O(n_slots log n_slots + n_weights)
    """
```

Key properties:
- Deterministic: same inputs always produce same validator set
- Weighted: higher-weight validators appear in more slots (proportionally)
- Efficient: O(n_slots log n_slots + n_weights) per participant

### Weight Synchronization

`CurrentValidatorWeights` must be identical in DAPI and chain at validation time.

**Implemented Solution**: On-chain snapshot at validation phase start.

When validation phase begins (`poc_validation_start` or confirmation PoC `GENERATION->VALIDATION`), the chain captures a `PoCValidationSnapshot` containing:
- `app_hash`: The deterministic randomness source from the block header
- `validator_weights`: Current validator weights as `repeated ValidatorWeight` (sorted by address)
- `poc_stage_start_height`: Key for lookup (regular PoC) or `trigger_height` (confirmation PoC)

**Proto Definition** (`poc_validation_snapshot.proto`):
```protobuf
message PoCValidationSnapshot {
  int64 poc_stage_start_height = 1;
  int64 snapshot_height = 2;
  string app_hash = 3;
  repeated ValidatorWeight validator_weights = 4;
}

message ValidatorWeight {
  string address = 1;
  int64 weight = 2;
}
```

**Query Flow**:
- DAPI queries `PoCValidationSnapshot` RPC to get weights and app_hash
- Chain retrieves snapshot from keeper when computing weights
- Both use identical `GetSlots()` algorithm with same inputs

## Implementation

### DAPI Changes (`decentralized-api/poc/validator.go`)

Current `ValidateAll`:
```go
// Current: validates ALL commits
commitsResp, _ := queryClient.AllPoCV2StoreCommitsForStage(...)
for _, commit := range commitsResp.Commits {
    workItems = append(workItems, participantWork{...})
}
```

Modified: filter to only assigned participants:
```go
// Get weights for sampling
weights := getValidatorWeights(epochState)
appHash := getAppHashAtValidationStart()

// Filter to participants where we're assigned
for _, commit := range commitsResp.Commits {
    assignedValidators := getSlots(appHash, commit.ParticipantAddress, weights, N_SLOTS)
    if !contains(assignedValidators, v.pubKey) {
        continue // Skip - not our assignment
    }
    workItems = append(workItems, participantWork{...})
}
```

### Chain Changes (`inference-chain/x/inference/module/chainvalidation.go`)

Current `pocValidated`:
```go
// Current: counts votes from ALL validators
func (wc *WeightCalculator) pocValidated(vals []types.PoCValidationV2, participantAddress string) bool {
    totalWeight := calculateTotalWeight(wc.CurrentValidatorWeights)
    halfWeight := int64(totalWeight / 2)
    // ... iterates all validations
}
```

Modified: count only assigned validators:
```go
func (wc *WeightCalculator) pocValidated(vals []types.PoCValidationV2, participantAddress string) bool {
    // Compute assigned validators for this participant
    assignedValidators := getSlots(wc.AppHash, participantAddress, wc.CurrentValidatorWeights, N_SLOTS)
    
    // Calculate total weight from assigned validators only
    assignedWeight := int64(0)
    for _, v := range assignedValidators {
        if w, ok := wc.CurrentValidatorWeights[v]; ok {
            assignedWeight += w
        }
    }
    halfWeight := assignedWeight / 2
    
    // Count votes from assigned validators only
    valOutcome := calculateValidationOutcomeFiltered(assignedValidators, wc.CurrentValidatorWeights, vals)
    // ... consensus logic unchanged
}
```

### Go Implementation of `get_slots`

**Location**: `inference-chain/x/inference/calculations/slots.go`

The implementation provides two functions:
- `GetSlots(appHash, participantAddress string, weights map[string]int64, nSlots int) []string` - Returns all assigned validators
- `GetSlot(appHash, participantAddress string, weights map[string]int64, slotIdx int) string` - Returns single slot (for future fallback expansion)

Key implementation details:
- Weights sorted alphabetically by address for determinism
- Uses `uint64` modulo to avoid negative values from signed integer conversion
- Single-pass mapping of sorted random values to cumulative weight ranges

```go
func slotRandomVal(appHash, participantAddress string, slotIdx int, totalWeight int64) int64 {
    seedData := fmt.Sprintf("%s%s%d", appHash, participantAddress, slotIdx)
    hash := sha256.Sum256([]byte(seedData))
    // Use uint64 for modulo to avoid negative values
    return int64(binary.BigEndian.Uint64(hash[:8]) % uint64(totalWeight))
}
```

**Tests**: `inference-chain/x/inference/calculations/slots_test.go`

### Parameters

| Parameter | Location | Default | Notes |
|-----------|----------|---------|-------|
| `ValidationSlots` | `PocParams` in params.proto | 0 (disabled) | Set to 64-128 to enable sampling |
| Consensus threshold | hardcoded | >50% weight | Matches current behavior |
| Hash source | `PoCValidationSnapshot.AppHash` | - | Captured at validation phase start |

**Configuration**: Set `PocParams.ValidationSlots` via governance. Value of 0 disables sampling (O(N²) fallback).

N_SLOTS selection rationale:
- Too low: higher variance in attack probability (see security analysis)
- Too high: diminishing returns, approaches O(N*N) again
- 64-128 slots provides strong statistical guarantees while maintaining scalability gains

## Security Assessment

### Current vs Proposed Security Model

**Current model**: Requires >50% of TOTAL validator weight to vote "valid". An attacker needs >50% of network weight to corrupt any participant's validation.

**Proposed model**: Requires >50% of ASSIGNED validators' weight to vote "valid". Same global requirement (>50% to attack anyone), but introduces sampling variance per participant.

### Attack Probability Analysis

With sampling, an attacker controlling fraction `f` of total weight could be over-represented in a specific participant's assigned validators by chance. This follows a binomial distribution.

| Attacker Weight (f) | Expected Malicious Slots (N=64) | P(>50% slots) | P(>50% slots) N=128 |
|---------------------|--------------------------------|---------------|---------------------|
| 30% | 19.2 | < 10⁻⁶ | < 10⁻¹⁰ |
| 35% | 22.4 | ~10⁻⁴ | ~10⁻⁷ |
| 40% | 25.6 | ~2-3% | ~0.1% |
| 45% | 28.8 | ~13% | ~5% |
| 49% | 31.4 | ~38% | ~35% |

**Key insight**: Attack probability is per-participant. An attacker cannot simultaneously attack all N participants with favorable odds—they can only target specific participants, and each target has independent sampling.

### N_SLOTS Recommendations

| N_SLOTS | Security Margin | Performance (N=10k) | Use Case |
|---------|-----------------|---------------------|----------|
| 64 | Good | 99.4% reduction | Initial deployment, smaller networks |
| 128 | Very good | 98.7% reduction | Production recommended |
| 256 | Excellent | 97.4% reduction | High-security requirements |

**Recommendation**: Use N_SLOTS=128 for production to provide stronger security margins while retaining substantial scalability benefits.

### Determinism Requirements

The `GetSlots()` algorithm produces identical results in DAPI and chain by:

1. **Shared code**: Both import `calculations.GetSlots` from `inference-chain/x/inference/calculations`
2. **Sort order**: Alphabetical by validator address
3. **Hash function**: SHA-256 with `fmt.Sprintf("%s%s%d", appHash, participantAddress, slotIdx)`
4. **Integer handling**: Uses `uint64` modulo to ensure positive values
5. **Snapshot sync**: Both query the same `PoCValidationSnapshot` for weights and app_hash

## Optional Fallback

**Status**: Not yet implemented. `GetSlot()` function exists in `calculations/slots.go` for future use.

The `validate_host()` function in `optimize.py` demonstrates incremental slot expansion when initial N_SLOTS doesn't reach consensus:

```python
# Fallback: expand slots one at a time until consensus
slot_idx = N_SLOTS
while slot_idx < total_weight:
    next_slot = get_slot(app_hash, host, prev_weights, slot_idx)
    if next_slot not in validator_votes:
        validator_votes[next_slot] = get_vote_from(next_slot)
    slot_idx += 1
    if voted_yes > slot_idx / 2:
        return True
    elif voted_no > slot_idx / 2:
        return False
```

**Security note**: The expanded slots use the same deterministic weighted random sampling as initial slots—they are equally random/secure. An attacker controlling X% of weight cannot force expansion since they can only control their own validators' votes, not honest validators'.

### Fallback Considerations

**Reasons to include fallback**:
- Handles edge cases where honest validators genuinely disagree
- Provides path to resolution instead of automatic failure

**Reasons to defer fallback**:
- Added code complexity (more paths to test/audit)
- Dynamic threshold (`slot_idx / 2`) has different statistical properties
- Edge cases may indicate real problems that shouldn't auto-resolve

**Current implementation**: Fixed N_SLOTS only. No-consensus triggers guardian protection (if enabled) or rejection. Revisit fallback if consensus failures become common in practice.
