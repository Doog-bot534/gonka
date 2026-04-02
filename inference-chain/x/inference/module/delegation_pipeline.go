package inference

import (
	"context"
	"sort"

	mathsdk "cosmossdk.io/math"
	"github.com/productscience/inference/x/inference/types"
)

// protoDecToLegacy converts a proto Decimal to LegacyDec, returning zero on nil or error.
// Follows the pattern from GetWeightScaleFactorDec in params.go.
func protoDecToLegacy(d *types.Decimal) mathsdk.LegacyDec {
	if d == nil || (d.Value == 0 && d.Exponent == 0) {
		return mathsdk.LegacyZeroDec()
	}
	dec, err := d.ToLegacyDec()
	if err != nil {
		return mathsdk.LegacyZeroDec()
	}
	return dec
}

// buildDelegationWeightCalculator constructs a DelegationWeightCalculator from
// the current epoch pipeline state.
func (am AppModule) buildDelegationWeightCalculator(
	ctx context.Context,
	activeParticipants []*types.ActiveParticipant,
	coefficients map[string]mathsdk.LegacyDec,
	params types.Params,
) *DelegationWeightCalculator {
	// Build group data from activeParticipants (post-setModelsForParticipants)
	groups := buildGroupData(activeParticipants, coefficients)

	// Load N-1 consensus weights
	consensusWeights, totalWeight := am.getPreviousConsensusWeights(ctx)

	// Load delegation state from keeper
	delegations, refusals, intents := am.loadDelegationState(ctx)

	// Build weight params from governance params
	wp := WeightParams{
		WThreshold: mathsdk.LegacyZeroDec(),
		VMin:       0,
		CapFactor:  mathsdk.LegacyZeroDec(),
	}
	if params.DelegationParams != nil {
		dp := params.DelegationParams
		wp.WThreshold = protoDecToLegacy(dp.WThreshold)
		wp.VMin = dp.VMin
		wp.CapFactor = protoDecToLegacy(dp.CapFactor)
	}

	return &DelegationWeightCalculator{
		Groups:             groups,
		ConsensusWeights:   consensusWeights,
		TotalNetworkWeight: totalWeight,
		Delegations:        delegations,
		Refusals:           refusals,
		Intents:            intents,
		Params:             wp,
	}
}

// buildGroupData constructs GroupData from activeParticipants after model assignment.
// Each participant's Models[] and MlNodes[] are parallel arrays.
func buildGroupData(
	activeParticipants []*types.ActiveParticipant,
	coefficients map[string]mathsdk.LegacyDec,
) map[string]*GroupData {
	groups := make(map[string]*GroupData)

	for _, p := range activeParticipants {
		for i, modelID := range p.Models {
			g, ok := groups[modelID]
			if !ok {
				coeff, hasCoeff := coefficients[modelID]
				if !hasCoeff {
					coeff = mathsdk.LegacyOneDec()
				}
				g = &GroupData{
					MemberPocWeights: make(map[string]int64),
					ConsensusKoeff:   coeff,
				}
				groups[modelID] = g
			}
			g.Members = append(g.Members, p.Index)
			if i < len(p.MlNodes) && p.MlNodes[i] != nil {
				g.MemberPocWeights[p.Index] = SumNodeWeights(p.MlNodes[i].MlNodes)
			}
		}
	}

	// Mark the first group (by sorted model ID) as initial (exempt from cap)
	if len(groups) > 0 {
		var modelIDs []string
		for id := range groups {
			modelIDs = append(modelIDs, id)
		}
		sort.Strings(modelIDs)
		groups[modelIDs[0]].IsInitialGroup = true
	}

	return groups
}

// getPreviousConsensusWeights reads ActiveParticipants from the effective epoch
// to get N-1 consensus weights.
func (am AppModule) getPreviousConsensusWeights(ctx context.Context) (map[string]int64, int64) {
	epochIndex, found := am.keeper.GetEffectiveEpochIndex(ctx)
	if !found {
		return make(map[string]int64), 0
	}
	effectiveAP, found := am.keeper.GetActiveParticipants(ctx, epochIndex)
	if !found {
		return make(map[string]int64), 0
	}
	weights := make(map[string]int64, len(effectiveAP.Participants))
	total := int64(0)
	for _, p := range effectiveAP.Participants {
		weights[p.Index] = p.Weight
		total += p.Weight
	}
	return weights, total
}

// loadDelegationState reads all delegation, refusal, and intent entries from keeper.
func (am AppModule) loadDelegationState(ctx context.Context) (
	delegations map[string]map[string]string,
	refusals map[string]map[string]bool,
	intents map[string]map[string]bool,
) {
	delegations = make(map[string]map[string]string)
	refusals = make(map[string]map[string]bool)
	intents = make(map[string]map[string]bool)

	allDelegations, err := am.keeper.GetAllPoCDelegations(ctx)
	if err == nil {
		for _, d := range allDelegations {
			if delegations[d.ModelId] == nil {
				delegations[d.ModelId] = make(map[string]string)
			}
			delegations[d.ModelId][d.Delegator] = d.DelegateTo
		}
	}

	// Iterate refusals
	refusalIter, err := am.keeper.PoCRefusals.Iterate(ctx, nil)
	if err == nil {
		keys, err := refusalIter.Keys()
		if err == nil {
			for _, key := range keys {
				modelID, participant := key.K1(), key.K2()
				if refusals[modelID] == nil {
					refusals[modelID] = make(map[string]bool)
				}
				refusals[modelID][participant] = true
			}
		}
	}

	// Iterate intents
	intentIter, err := am.keeper.PoCDirectIntents.Iterate(ctx, nil)
	if err == nil {
		keys, err := intentIter.Keys()
		if err == nil {
			for _, key := range keys {
				modelID, participant := key.K1(), key.K2()
				if intents[modelID] == nil {
					intents[modelID] = make(map[string]bool)
				}
				intents[modelID][participant] = true
			}
		}
	}

	return delegations, refusals, intents
}

// delegationAdjustmentParams extracts DelegationAdjustmentParams from governance params.
func (am AppModule) delegationAdjustmentParams(params types.Params) DelegationAdjustmentParams {
	if params.DelegationParams == nil {
		return DelegationAdjustmentParams{
			RRefusal:    mathsdk.LegacyZeroDec(),
			RPenalty:    mathsdk.LegacyZeroDec(),
			RDelegation: mathsdk.LegacyZeroDec(),
		}
	}
	dp := params.DelegationParams
	return DelegationAdjustmentParams{
		RRefusal:    protoDecToLegacy(dp.RRefusal),
		RPenalty:    protoDecToLegacy(dp.RPenalty),
		RDelegation: protoDecToLegacy(dp.RDelegation),
	}
}

// computeAndSetVotingPowers computes per-group voting powers from final weights
// and writes them to each participant's VotingPowers field.
func (am AppModule) computeAndSetVotingPowers(
	activeParticipants []*types.ActiveParticipant,
	dwc *DelegationWeightCalculator,
	eligibleGroups []string,
	allModes map[string]map[string]ParticipationMode,
) {
	// Build final weights map from participants
	finalWeights := make(map[string]int64, len(activeParticipants))
	for _, p := range activeParticipants {
		finalWeights[p.Index] = p.Weight
	}

	// For each eligible group, compute voting powers and distribute to participants
	// participantVP accumulates: participant -> []ModelVotingPower
	participantVP := make(map[string][]*types.ModelVotingPower)

	for _, modelID := range eligibleGroups {
		modes := allModes[modelID]
		if modes == nil {
			continue
		}
		vpMap := dwc.ComputeGroupVotingPowers(modelID, modes, finalWeights)
		for addr, vp := range vpMap {
			if vp > 0 {
				participantVP[addr] = append(participantVP[addr], &types.ModelVotingPower{
					ModelId:     modelID,
					VotingPower: vp,
				})
			}
		}
	}

	// Write voting powers to participants, sorted by model_id
	for _, p := range activeParticipants {
		vps := participantVP[p.Index]
		if len(vps) > 0 {
			sort.Slice(vps, func(i, j int) bool {
				return vps[i].ModelId < vps[j].ModelId
			})
			p.VotingPowers = vps
		}
	}
}
