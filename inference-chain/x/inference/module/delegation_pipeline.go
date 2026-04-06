package inference

import (
	"context"
	"sort"
	"strconv"

	mathsdk "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
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
	nextEpochDelegations, nextEpochRefusals, found := am.loadDelegationSnapshotState(ctx)
	if !found {
		am.LogError("delegation snapshot not found", types.PoC, "context", "onEndOfPoCValidationStage")
		nextEpochDelegations = map[string]map[string]string{}
		nextEpochRefusals = map[string]map[string]bool{}
	}
	consensusWeights, totalWeight := am.getPreviousConsensusWeights(ctx)
	groups := buildGroupData(activeParticipants, coefficients)

	return &DelegationWeightCalculator{
		Groups:             groups,
		ConsensusWeights:   consensusWeights,
		TotalNetworkWeight: totalWeight,
		Delegations:        nextEpochDelegations,
		Refusals:           nextEpochRefusals,
		Params:             buildWeightParams(params),
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
	if found {
		weights := make(map[string]int64, len(effectiveAP.Participants))
		total := int64(0)
		for _, p := range effectiveAP.Participants {
			weights[p.Index] = p.Weight
			total += p.Weight
		}
		return weights, total
	}

	// Bootstrap the epoch0 -> epoch1 transition from the genesis epoch-group
	// validator weights when ActiveParticipants(0) was never seeded.
	if epochIndex == 0 {
		return am.getEpochZeroValidationWeights(ctx)
	}

	return make(map[string]int64), 0
}

func (am AppModule) getEpochZeroValidationWeights(ctx context.Context) (map[string]int64, int64) {
	epochGroupData, found := am.keeper.GetEpochGroupData(ctx, 0, "")
	if !found {
		return make(map[string]int64), 0
	}

	weights := make(map[string]int64, len(epochGroupData.ValidationWeights))
	total := int64(0)
	for _, validationWeight := range epochGroupData.ValidationWeights {
		if validationWeight == nil {
			continue
		}
		weights[validationWeight.MemberAddress] = validationWeight.Weight
		total += validationWeight.Weight
	}
	return weights, total
}

func parseDelegationSnapshot(snapshot types.DelegationSnapshot) (
	delegations map[string]map[string]string,
	refusals map[string]map[string]bool,
) {
	delegations = make(map[string]map[string]string)
	refusals = make(map[string]map[string]bool)

	for _, d := range snapshot.Delegations {
		if delegations[d.ModelId] == nil {
			delegations[d.ModelId] = make(map[string]string)
		}
		delegations[d.ModelId][d.Delegator] = d.DelegateTo
	}
	for _, r := range snapshot.Refusals {
		if refusals[r.ModelId] == nil {
			refusals[r.ModelId] = make(map[string]bool)
		}
		refusals[r.ModelId][r.Participant] = true
	}
	return delegations, refusals
}

func (am AppModule) loadDelegationSnapshotState(ctx context.Context) (map[string]map[string]string, map[string]map[string]bool, bool) {
	snapshot, found := am.keeper.GetDelegationSnapshot(ctx)
	if !found {
		return nil, nil, false
	}
	delegations, refusals := parseDelegationSnapshot(snapshot)
	return delegations, refusals, true
}

// captureDelegationSnapshot stores the frozen delegation state used later at
// validation start. Intents are intentionally excluded from this snapshot.
func (am AppModule) captureDelegationSnapshot(ctx context.Context, blockHeight int64) {
	snapshot, err := am.buildDelegationSnapshot(ctx, blockHeight)
	if err != nil {
		am.LogError("captureDelegationSnapshot: failed to build", types.PoC, "error", err)
		return
	}

	if err := am.keeper.SetDelegationSnapshot(ctx, snapshot); err != nil {
		am.LogError("captureDelegationSnapshot: failed to store", types.PoC, "error", err)
		return
	}

	am.LogInfo("captureDelegationSnapshot: stored delegation snapshot", types.PoC,
		"height", blockHeight,
		"delegations", len(snapshot.Delegations),
		"refusals", len(snapshot.Refusals))
}

func (am AppModule) buildDelegationSnapshot(ctx context.Context, blockHeight int64) (types.DelegationSnapshot, error) {
	params, err := am.keeper.GetParams(ctx)
	if err != nil {
		return types.DelegationSnapshot{}, err
	}

	effectiveParticipants, _, _ := am.getEffectiveParticipantsWithConsensusWeights(ctx)
	modelIDs := approvedModelIDs(params.PocParams)
	delegationEntries, refusalEntries := am.loadFilteredDelegationSnapshotState(ctx, effectiveParticipants, modelIDs)

	return types.DelegationSnapshot{
		SnapshotHeight: blockHeight,
		Delegations:    delegationEntries,
		Refusals:       refusalEntries,
	}, nil
}

func (am AppModule) loadNextEpochDelegationState(ctx context.Context) (
	map[string]map[string]string,
	map[string]map[string]bool,
) {
	delegations, refusals, found := am.loadDelegationSnapshotState(ctx)
	if !found {
		am.LogError("validation delegation snapshot not found", types.PoC, "context", "onEndOfPoCValidationStage")
		return map[string]map[string]string{}, map[string]map[string]bool{}
	}
	return delegations, refusals
}

// captureBootstrapDelegationSnapshot stores the filtered bootstrap delegation and
// intent state needed to evaluate pre-eligibility for approved models that are
// not already active in the effective epoch.
func (am AppModule) captureBootstrapDelegationSnapshot(ctx context.Context, blockHeight int64) {
	snapshot, err := am.buildBootstrapDelegationSnapshot(ctx, blockHeight)
	if err != nil {
		am.LogError("captureBootstrapDelegationSnapshot: failed to build", types.PoC, "error", err)
		return
	}

	if err := am.keeper.SetBootstrapDelegationSnapshot(ctx, snapshot); err != nil {
		am.LogError("captureBootstrapDelegationSnapshot: failed to store", types.PoC, "error", err)
		return
	}

	results, totalNetworkWeight, err := am.buildBootstrapPreEligibilityReport(ctx, snapshot)
	if err != nil {
		am.LogError("captureBootstrapDelegationSnapshot: failed to compute report", types.PoC, "error", err)
		return
	}
	am.emitBootstrapPreEligibilityEvents(ctx, snapshot.SnapshotHeight, totalNetworkWeight, results)
	am.LogInfo("captureBootstrapDelegationSnapshot: stored bootstrap snapshot", types.PoC,
		"height", blockHeight,
		"delegations", len(snapshot.Delegations),
		"intents", len(snapshot.Intents),
		"bootstrapModels", len(results))
}

func (am AppModule) buildBootstrapDelegationSnapshot(ctx context.Context, blockHeight int64) (types.BootstrapDelegationSnapshot, error) {
	params, err := am.keeper.GetParams(ctx)
	if err != nil {
		return types.BootstrapDelegationSnapshot{}, err
	}

	effectiveParticipants, _, _ := am.getEffectiveParticipantsWithConsensusWeights(ctx)
	activeModels := activeModelSet(effectiveParticipants)
	bootstrapModelIDs := bootstrapCandidateModelIDs(params.PocParams, activeModels)

	bootstrapDelegationEntries, bootstrapIntentEntries, _, _ := am.loadFilteredBootstrapState(ctx, effectiveParticipants, bootstrapModelIDs)

	return types.BootstrapDelegationSnapshot{
		SnapshotHeight: blockHeight,
		Delegations:    bootstrapDelegationEntries,
		Intents:        bootstrapIntentEntries,
	}, nil
}

func (am AppModule) getEffectiveParticipantsWithConsensusWeights(ctx context.Context) ([]*types.ActiveParticipant, map[string]int64, int64) {
	epochIndex, found := am.keeper.GetEffectiveEpochIndex(ctx)
	if !found {
		return nil, map[string]int64{}, 0
	}

	ap, found := am.keeper.GetActiveParticipants(ctx, epochIndex)
	if !found {
		return nil, map[string]int64{}, 0
	}

	consensusWeights := make(map[string]int64, len(ap.Participants))
	totalNetworkWeight := int64(0)
	for _, participant := range ap.Participants {
		consensusWeights[participant.Index] = participant.Weight
		totalNetworkWeight += participant.Weight
	}

	return ap.Participants, consensusWeights, totalNetworkWeight
}

func activeModelSet(participants []*types.ActiveParticipant) map[string]bool {
	models := make(map[string]bool)
	for _, participant := range participants {
		for _, modelID := range participant.Models {
			if modelID != "" {
				models[modelID] = true
			}
		}
		for _, vp := range participant.VotingPowers {
			if vp != nil && vp.ModelId != "" {
				models[vp.ModelId] = true
			}
		}
	}
	return models
}

func bootstrapCandidateModelIDs(pocParams *types.PocParams, activeModels map[string]bool) []string {
	if pocParams == nil {
		return nil
	}

	candidates := make([]string, 0, len(pocParams.GetModelConfigs()))
	for _, modelConfig := range pocParams.GetModelConfigs() {
		if modelConfig == nil || modelConfig.ModelId == "" {
			continue
		}
		if activeModels[modelConfig.ModelId] {
			continue
		}
		candidates = append(candidates, modelConfig.ModelId)
	}

	sort.Strings(candidates)
	return candidates
}

func approvedModelIDs(pocParams *types.PocParams) []string {
	if pocParams == nil {
		return nil
	}

	models := make([]string, 0, len(pocParams.GetModelConfigs()))
	for _, modelConfig := range pocParams.GetModelConfigs() {
		if modelConfig == nil || modelConfig.ModelId == "" {
			continue
		}
		models = append(models, modelConfig.ModelId)
	}

	sort.Strings(models)
	return models
}

func (am AppModule) loadFilteredDelegationSnapshotState(
	ctx context.Context,
	effectiveParticipants []*types.ActiveParticipant,
	modelIDs []string,
) ([]*types.PoCDelegation, []*types.PoCRefusal) {
	delegationEntries := make([]*types.PoCDelegation, 0)
	refusalEntries := make([]*types.PoCRefusal, 0)

	for _, participant := range effectiveParticipants {
		for _, modelID := range modelIDs {
			delegation, found := am.keeper.GetPoCDelegation(ctx, modelID, participant.Index)
			if found {
				delegationCopy := delegation
				delegationEntries = append(delegationEntries, &delegationCopy)
			}

			if am.keeper.HasPoCRefusal(ctx, modelID, participant.Index) {
				refusalEntries = append(refusalEntries, &types.PoCRefusal{
					ModelId:     modelID,
					Participant: participant.Index,
				})
			}
		}
	}

	sort.Slice(delegationEntries, func(i, j int) bool {
		if delegationEntries[i].ModelId == delegationEntries[j].ModelId {
			return delegationEntries[i].Delegator < delegationEntries[j].Delegator
		}
		return delegationEntries[i].ModelId < delegationEntries[j].ModelId
	})
	sort.Slice(refusalEntries, func(i, j int) bool {
		if refusalEntries[i].ModelId == refusalEntries[j].ModelId {
			return refusalEntries[i].Participant < refusalEntries[j].Participant
		}
		return refusalEntries[i].ModelId < refusalEntries[j].ModelId
	})

	return delegationEntries, refusalEntries
}

func (am AppModule) loadFilteredBootstrapState(
	ctx context.Context,
	effectiveParticipants []*types.ActiveParticipant,
	bootstrapModelIDs []string,
) (
	[]*types.PoCDelegation,
	[]*types.PoCDirectIntent,
	map[string]map[string]string,
	map[string]map[string]bool,
) {
	delegationEntries := make([]*types.PoCDelegation, 0)
	intentEntries := make([]*types.PoCDirectIntent, 0)
	delegations := make(map[string]map[string]string)
	intents := make(map[string]map[string]bool)

	for _, participant := range effectiveParticipants {
		for _, modelID := range bootstrapModelIDs {
			delegation, found := am.keeper.GetPoCDelegation(ctx, modelID, participant.Index)
			if found {
				if delegations[modelID] == nil {
					delegations[modelID] = make(map[string]string)
				}
				delegations[modelID][participant.Index] = delegation.DelegateTo
				delegationCopy := delegation
				delegationEntries = append(delegationEntries, &delegationCopy)
			}

			if am.keeper.HasPoCDirectIntent(ctx, modelID, participant.Index) {
				if intents[modelID] == nil {
					intents[modelID] = make(map[string]bool)
				}
				intents[modelID][participant.Index] = true
				intentEntries = append(intentEntries, &types.PoCDirectIntent{
					ModelId:     modelID,
					Participant: participant.Index,
				})
			}
		}
	}

	sort.Slice(delegationEntries, func(i, j int) bool {
		if delegationEntries[i].ModelId == delegationEntries[j].ModelId {
			return delegationEntries[i].Delegator < delegationEntries[j].Delegator
		}
		return delegationEntries[i].ModelId < delegationEntries[j].ModelId
	})
	sort.Slice(intentEntries, func(i, j int) bool {
		if intentEntries[i].ModelId == intentEntries[j].ModelId {
			return intentEntries[i].Participant < intentEntries[j].Participant
		}
		return intentEntries[i].ModelId < intentEntries[j].ModelId
	})

	return delegationEntries, intentEntries, delegations, intents
}

func (am AppModule) loadBootstrapDelegationState(ctx context.Context) (map[string]map[string]string, bool) {
	snapshot, found := am.keeper.GetBootstrapDelegationSnapshot(ctx)
	if !found {
		return nil, false
	}

	bootstrapDelegations := make(map[string]map[string]string)
	for _, delegation := range snapshot.Delegations {
		if bootstrapDelegations[delegation.ModelId] == nil {
			bootstrapDelegations[delegation.ModelId] = make(map[string]string)
		}
		bootstrapDelegations[delegation.ModelId][delegation.Delegator] = delegation.DelegateTo
	}

	return bootstrapDelegations, true
}

func buildBootstrapPreEligibilityCalculator(
	consensusWeights map[string]int64,
	totalNetworkWeight int64,
	bootstrapModelIDs []string,
	bootstrapDelegations map[string]map[string]string,
	bootstrapIntents map[string]map[string]bool,
	params types.Params,
) *DelegationWeightCalculator {
	coefficients := ModelCoefficients(params.PocParams)
	groups := make(map[string]*GroupData, len(bootstrapModelIDs))
	for _, modelID := range bootstrapModelIDs {
		memberSet := bootstrapIntents[modelID]
		members := make([]string, 0, len(memberSet))
		for participant := range memberSet {
			members = append(members, participant)
		}
		sort.Strings(members)

		coeff, ok := coefficients[modelID]
		if !ok {
			coeff = mathsdk.LegacyOneDec()
		}
		groups[modelID] = &GroupData{
			Members:          members,
			MemberPocWeights: make(map[string]int64),
			ConsensusKoeff:   coeff,
		}
	}

	return &DelegationWeightCalculator{
		Groups:             groups,
		ConsensusWeights:   consensusWeights,
		TotalNetworkWeight: totalNetworkWeight,
		Delegations:        bootstrapDelegations,
		Refusals:           map[string]map[string]bool{},
		Params:             buildWeightParams(params),
	}
}

func (am AppModule) buildBootstrapPreEligibilityReport(
	ctx context.Context,
	snapshot types.BootstrapDelegationSnapshot,
) ([]*types.BootstrapModelPreEligibility, int64, error) {
	params, err := am.keeper.GetParams(ctx)
	if err != nil {
		return nil, 0, err
	}

	effectiveParticipants, consensusWeights, totalNetworkWeight := am.getEffectiveParticipantsWithConsensusWeights(ctx)
	activeModels := activeModelSet(effectiveParticipants)
	bootstrapModelIDs := bootstrapCandidateModelIDs(params.PocParams, activeModels)

	bootstrapDelegations := make(map[string]map[string]string)
	bootstrapIntents := make(map[string]map[string]bool)
	for _, delegation := range snapshot.Delegations {
		if bootstrapDelegations[delegation.ModelId] == nil {
			bootstrapDelegations[delegation.ModelId] = make(map[string]string)
		}
		bootstrapDelegations[delegation.ModelId][delegation.Delegator] = delegation.DelegateTo
	}
	for _, intent := range snapshot.Intents {
		if bootstrapIntents[intent.ModelId] == nil {
			bootstrapIntents[intent.ModelId] = make(map[string]bool)
		}
		bootstrapIntents[intent.ModelId][intent.Participant] = true
	}

	calculator := buildBootstrapPreEligibilityCalculator(
		consensusWeights,
		totalNetworkWeight,
		bootstrapModelIDs,
		bootstrapDelegations,
		bootstrapIntents,
		params,
	)

	return buildBootstrapPreEligibilityResults(calculator, bootstrapModelIDs), totalNetworkWeight, nil
}

func buildBootstrapPreEligibilityResults(
	calculator *DelegationWeightCalculator,
	bootstrapModelIDs []string,
) []*types.BootstrapModelPreEligibility {
	results := make([]*types.BootstrapModelPreEligibility, 0, len(bootstrapModelIDs))
	for _, modelID := range bootstrapModelIDs {
		intentHostCount := int64(0)
		intentWeight := int64(0)
		group := calculator.Groups[modelID]
		if group != nil {
			for _, participant := range group.Members {
				if calculator.ConsensusWeights[participant] > 0 {
					intentHostCount++
				}
				intentWeight += calculator.ConsensusWeights[participant]
			}
		}

		meetsWeightThreshold := calculator.MeetsWeightThreshold(modelID)
		meetsVMin := calculator.MeetsMinHosts(modelID)
		meetsReachability := calculator.MeetsReachabilityThreshold(modelID)
		results = append(results, &types.BootstrapModelPreEligibility{
			ModelId:              modelID,
			PreEligible:          calculator.IsGroupPreEligible(modelID) && meetsReachability,
			MeetsWeightThreshold: meetsWeightThreshold,
			MeetsVMin:            meetsVMin,
			MeetsReachability:    meetsReachability,
			IntentHostCount:      intentHostCount,
			IntentWeight:         intentWeight,
			ReachableVotingPower: calculator.ProjectedReachableVotingPower(modelID),
		})
	}
	return results
}

func (am AppModule) emitBootstrapPreEligibilityEvents(
	ctx context.Context,
	snapshotHeight int64,
	totalNetworkWeight int64,
	results []*types.BootstrapModelPreEligibility,
) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	for _, result := range results {
		sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
			"bootstrap_model_preeligibility",
			sdk.NewAttribute("snapshot_height", strconv.FormatInt(snapshotHeight, 10)),
			sdk.NewAttribute("model_id", result.ModelId),
			sdk.NewAttribute("pre_eligible", strconv.FormatBool(result.PreEligible)),
			sdk.NewAttribute("meets_weight_threshold", strconv.FormatBool(result.MeetsWeightThreshold)),
			sdk.NewAttribute("meets_v_min", strconv.FormatBool(result.MeetsVMin)),
			sdk.NewAttribute("meets_reachability", strconv.FormatBool(result.MeetsReachability)),
			sdk.NewAttribute("intent_host_count", strconv.FormatInt(result.IntentHostCount, 10)),
			sdk.NewAttribute("intent_weight", strconv.FormatInt(result.IntentWeight, 10)),
			sdk.NewAttribute("reachable_voting_power", strconv.FormatInt(result.ReachableVotingPower, 10)),
			sdk.NewAttribute("total_network_weight", strconv.FormatInt(totalNetworkWeight, 10)),
		))
	}
}

// ComputeModelVotingPowers computes per-model voting powers for PoC validation acceptance.
// DIRECT membership comes from store commit keys (participants who submitted PoC).
// Delegation-resolved: each DIRECT member's votingPower includes delegated consensus weight.
// Uses AP(N) consensus weights as the base.
func ComputeModelVotingPowers(
	storeCommitKeys []types.PoCParticipantModelKey,
	consensusWeights map[string]int64,
	delegations map[string]map[string]string,
) map[string]map[string]int64 {
	directMembers := make(map[string]map[string]bool)
	for _, key := range storeCommitKeys {
		if directMembers[key.ModelID] == nil {
			directMembers[key.ModelID] = make(map[string]bool)
		}
		directMembers[key.ModelID][key.ParticipantAddress] = true
	}

	modelVotingPowers := make(map[string]map[string]int64, len(directMembers))

	for modelID, members := range directMembers {
		vp := make(map[string]int64, len(members))

		for addr := range members {
			vp[addr] = consensusWeights[addr]
		}

		// Add delegated weight
		modelDelegations := delegations[modelID]
		for delegator, target := range modelDelegations {
			if !members[target] {
				continue
			}
			if members[delegator] {
				continue
			}
			vp[target] += consensusWeights[delegator]
		}

		modelVotingPowers[modelID] = vp
	}

	return modelVotingPowers
}

// computeAndSetVotingPowers computes per-group voting powers from final weights
// and writes them to each participant's VotingPowers field for visibility.
func (am AppModule) computeAndSetVotingPowers(
	activeParticipants []*types.ActiveParticipant,
	dwc *DelegationWeightCalculator,
	eligibleGroups []string,
	allModes map[string]map[string]ParticipationMode,
) {
	finalWeights := make(map[string]int64, len(activeParticipants))
	for _, p := range activeParticipants {
		finalWeights[p.Index] = p.Weight
	}

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
