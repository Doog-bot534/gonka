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
	nextEpochDelegations, nextEpochRefusals, found := am.loadRegularDelegationSnapshotState(ctx)
	if !found {
		am.LogError("regular delegation snapshot not found", types.PoC, "context", "onEndOfPoCValidationStage")
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

type epochParticipationState struct {
	calculator              *DelegationWeightCalculator
	eligibleModels          []string
	participationByModel    map[string]map[string]ParticipationMode
	bootstrapPenaltyByModel map[string]map[string]BootstrapPenaltyMode
}

func buildParticipationByModel(
	calculator *DelegationWeightCalculator,
	eligibleModels []string,
) map[string]map[string]ParticipationMode {
	participationByModel := make(map[string]map[string]ParticipationMode, len(eligibleModels))
	for _, modelID := range eligibleModels {
		participationByModel[modelID] = calculator.ResolveGroupParticipation(modelID)
	}
	return participationByModel
}

func (state *epochParticipationState) regularAdjustmentModels() []string {
	if len(state.bootstrapPenaltyByModel) == 0 {
		return state.eligibleModels
	}

	regularModels := make([]string, 0, len(state.eligibleModels))
	for _, modelID := range state.eligibleModels {
		if _, isBootstrapModel := state.bootstrapPenaltyByModel[modelID]; isBootstrapModel {
			continue
		}
		regularModels = append(regularModels, modelID)
	}
	return regularModels
}

func (am AppModule) prepareEpochParticipationState(
	ctx context.Context,
	activeParticipants []*types.ActiveParticipant,
	params types.Params,
	pocStageStartHeight int64,
) (*epochParticipationState, error) {
	coefficients := ModelCoefficients(params.PocParams)
	calculator := am.buildDelegationWeightCalculator(ctx, activeParticipants, coefficients, params)
	eligibleModels := calculator.EligibleGroups()
	participationByModel := buildParticipationByModel(calculator, eligibleModels)

	state := &epochParticipationState{
		calculator:              calculator,
		eligibleModels:          eligibleModels,
		participationByModel:    participationByModel,
		bootstrapPenaltyByModel: map[string]map[string]BootstrapPenaltyMode{},
	}

	bootstrapInputs, found := am.loadBootstrapPenaltyInputs(ctx)
	if !found {
		return state, nil
	}

	bootstrapPenaltyByModel, err := am.resolveBootstrapPenaltyModes(
		ctx,
		activeParticipants,
		pocStageStartHeight,
		bootstrapInputs,
	)
	if err != nil {
		return nil, err
	}
	state.bootstrapPenaltyByModel = bootstrapPenaltyByModel

	return state, nil
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

func parseRegularDelegationSnapshot(snapshot types.DelegationSnapshot) (
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

func parseBootstrapDelegationSnapshot(snapshot types.BootstrapDelegationSnapshot) (
	delegations map[string]map[string]string,
	intents map[string]map[string]bool,
) {
	delegations = make(map[string]map[string]string)
	intents = make(map[string]map[string]bool)

	for _, d := range snapshot.Delegations {
		if delegations[d.ModelId] == nil {
			delegations[d.ModelId] = make(map[string]string)
		}
		delegations[d.ModelId][d.Delegator] = d.DelegateTo
	}
	for _, i := range snapshot.Intents {
		if intents[i.ModelId] == nil {
			intents[i.ModelId] = make(map[string]bool)
		}
		intents[i.ModelId][i.Participant] = true
	}

	return delegations, intents
}

func (am AppModule) loadRegularDelegationSnapshotState(
	ctx context.Context,
) (
	map[string]map[string]string,
	map[string]map[string]bool,
	bool,
) {
	snapshot, found := am.keeper.GetDelegationSnapshot(ctx)
	if !found {
		return nil, nil, false
	}
	delegations, refusals := parseRegularDelegationSnapshot(snapshot)
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

	am.emitBootstrapPreEligibilityEvents(ctx, snapshot)
	am.LogInfo("captureBootstrapDelegationSnapshot: stored bootstrap snapshot", types.PoC,
		"height", blockHeight,
		"delegations", len(snapshot.Delegations),
		"intents", len(snapshot.Intents),
		"bootstrapModels", len(snapshot.Preeligibility))
}

func (am AppModule) buildBootstrapDelegationSnapshot(
	ctx context.Context,
	blockHeight int64,
) (
	types.BootstrapDelegationSnapshot,
	error,
) {
	params, err := am.keeper.GetParams(ctx)
	if err != nil {
		return types.BootstrapDelegationSnapshot{}, err
	}

	effectiveParticipants, consensusWeights, totalNetworkWeight := am.getEffectiveParticipantsWithConsensusWeights(ctx)
	activeModels := activeModelSet(effectiveParticipants)
	bootstrapModelIDs := bootstrapCandidateModelIDs(params.PocParams, activeModels)

	bootstrapDelegationEntries,
	bootstrapIntentEntries,
	bootstrapDelegations,
	bootstrapIntents := am.loadFilteredBootstrapState(
		ctx,
		effectiveParticipants,
		bootstrapModelIDs,
	)
	calculator := buildBootstrapPreEligibilityCalculator(
		consensusWeights,
		totalNetworkWeight,
		bootstrapModelIDs,
		bootstrapDelegations,
		bootstrapIntents,
		params,
	)
	results := buildBootstrapPreEligibilityResults(calculator, bootstrapModelIDs)

	return types.BootstrapDelegationSnapshot{
		SnapshotHeight:     blockHeight,
		Delegations:        bootstrapDelegationEntries,
		Intents:            bootstrapIntentEntries,
		TotalNetworkWeight: totalNetworkWeight,
		Preeligibility:     results,
	}, nil
}

func (am AppModule) getEffectiveParticipantsWithConsensusWeights(
	ctx context.Context,
) ([]*types.ActiveParticipant, map[string]int64, int64) {
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

func (am AppModule) loadBootstrapSnapshotState(ctx context.Context) (
	types.BootstrapDelegationSnapshot,
	map[string]map[string]string,
	map[string]map[string]bool,
	bool,
) {
	snapshot, found := am.keeper.GetBootstrapDelegationSnapshot(ctx)
	if !found {
		return types.BootstrapDelegationSnapshot{}, nil, nil, false
	}

	delegations, intents := parseBootstrapDelegationSnapshot(snapshot)
	return snapshot, delegations, intents, true
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

func indexBootstrapPreEligibility(
	results []*types.BootstrapModelPreEligibility,
) map[string]*types.BootstrapModelPreEligibility {
	resultByModel := make(map[string]*types.BootstrapModelPreEligibility, len(results))
	for _, result := range results {
		if result == nil || result.ModelId == "" {
			continue
		}
		resultByModel[result.ModelId] = result
	}
	return resultByModel
}

func (am AppModule) emitBootstrapPreEligibilityEvents(
	ctx context.Context,
	snapshot types.BootstrapDelegationSnapshot,
) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	for _, result := range snapshot.Preeligibility {
		sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
			"bootstrap_model_preeligibility",
			sdk.NewAttribute("snapshot_height", strconv.FormatInt(snapshot.SnapshotHeight, 10)),
			sdk.NewAttribute("model_id", result.ModelId),
			sdk.NewAttribute("pre_eligible", strconv.FormatBool(result.PreEligible)),
			sdk.NewAttribute("meets_weight_threshold", strconv.FormatBool(result.MeetsWeightThreshold)),
			sdk.NewAttribute("meets_v_min", strconv.FormatBool(result.MeetsVMin)),
			sdk.NewAttribute("meets_reachability", strconv.FormatBool(result.MeetsReachability)),
			sdk.NewAttribute("intent_host_count", strconv.FormatInt(result.IntentHostCount, 10)),
			sdk.NewAttribute("intent_weight", strconv.FormatInt(result.IntentWeight, 10)),
			sdk.NewAttribute("reachable_voting_power", strconv.FormatInt(result.ReachableVotingPower, 10)),
			sdk.NewAttribute("total_network_weight", strconv.FormatInt(snapshot.TotalNetworkWeight, 10)),
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
	eligibleModels []string,
	participationByModel map[string]map[string]ParticipationMode,
) {
	finalWeights := make(map[string]int64, len(activeParticipants))
	for _, p := range activeParticipants {
		finalWeights[p.Index] = p.Weight
	}

	participantVP := make(map[string][]*types.ModelVotingPower)

	for _, modelID := range eligibleModels {
		modes := participationByModel[modelID]
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
