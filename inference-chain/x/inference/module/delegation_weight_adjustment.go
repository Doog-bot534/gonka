package inference

import (
	mathsdk "cosmossdk.io/math"
	"github.com/productscience/inference/x/inference/types"
)

// DelegationAdjustmentParams holds the penalty/transfer fractions for delegation weight adjustment.
type DelegationAdjustmentParams struct {
	RRefusal    mathsdk.LegacyDec // REFUSE penalty fraction
	RPenalty    mathsdk.LegacyDec // NONE penalty fraction
	RDelegation mathsdk.LegacyDec // DELEGATE transfer fraction
}

// IsNoOp returns true if all adjustment fractions are zero.
func (p DelegationAdjustmentParams) IsNoOp() bool {
	return p.RRefusal.IsZero() && p.RPenalty.IsZero() && p.RDelegation.IsZero()
}

// ApplyDelegationWeightAdjustment modifies consensus weights in-place based on
// resolved participation modes per model group.
//
// DIRECT participants are never penalized. For non-DIRECT participants in each
// eligible group:
//   - REFUSE:   weight -= weight * r_refusal
//   - NONE:     weight -= weight * r_penalty
//   - DELEGATE: delta = weight * r_delegation; weight -= delta; delegate.weight += delta
//
// Reductions compound across groups (applied to already-reduced weight).
// When all r_* values are 0, this is a complete no-op.
func ApplyDelegationWeightAdjustment(
	participants []*types.ActiveParticipant,
	dwc *DelegationWeightCalculator,
	eligibleGroups []string,
	modes map[string]map[string]ParticipationMode,
	params DelegationAdjustmentParams,
) {
	if params.IsNoOp() {
		return
	}

	// Build weight index for fast lookup
	weightIndex := make(map[string]*types.ActiveParticipant, len(participants))
	for _, p := range participants {
		weightIndex[p.Index] = p
	}

	// Process groups in deterministic order (eligibleGroups is sorted)
	for _, modelID := range eligibleGroups {
		groupModes := modes[modelID]
		if groupModes == nil {
			continue
		}

		for addr, mode := range groupModes {
			p, ok := weightIndex[addr]
			if !ok || p.Weight <= 0 {
				continue
			}

			switch mode {
			case ModeDirect:
				// No adjustment
				continue
			case ModeRefuse:
				if !params.RRefusal.IsZero() {
					penalty := params.RRefusal.MulInt64(p.Weight).TruncateInt64()
					p.Weight -= penalty
				}
			case ModeNone:
				if !params.RPenalty.IsZero() {
					penalty := params.RPenalty.MulInt64(p.Weight).TruncateInt64()
					p.Weight -= penalty
				}
			case ModeDelegate:
				if !params.RDelegation.IsZero() {
					if modelDelegations, ok := dwc.Delegations[modelID]; ok {
						if delegateTo, ok := modelDelegations[addr]; ok {
							delta := params.RDelegation.MulInt64(p.Weight).TruncateInt64()
							p.Weight -= delta
							if target, ok := weightIndex[delegateTo]; ok {
								target.Weight += delta
							}
						}
					}
				}
			}

			if p.Weight < 0 {
				p.Weight = 0
			}
		}
	}
}
