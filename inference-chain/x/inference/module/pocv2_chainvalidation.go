package inference

import (
	"context"
	"sort"

	mathsdk "cosmossdk.io/math"
	"github.com/productscience/inference/x/inference/types"
)

// WeightCalculatorV2 encapsulates all the data needed to calculate new weights for participants using PoC v2.
// It follows the same structure as WeightCalculator (v1) but uses artifact batches and v2 validations.
type WeightCalculatorV2 struct {
	CurrentValidatorWeights map[string]int64
	ArtifactBatches         map[string][]types.PoCArtifactBatchV2
	Validations             map[string][]types.PoCValidationV2
	Participants            map[string]types.Participant
	Seeds                   map[string]types.RandomSeed
	EpochStartBlockHeight   int64
	Logger                  types.InferenceLogger
	WeightScaleFactor       mathsdk.LegacyDec
}

// NewWeightCalculatorV2 creates a new WeightCalculatorV2 instance.
func NewWeightCalculatorV2(
	currentValidatorWeights map[string]int64,
	artifactBatches map[string][]types.PoCArtifactBatchV2,
	validations map[string][]types.PoCValidationV2,
	participants map[string]types.Participant,
	seeds map[string]types.RandomSeed,
	epochStartBlockHeight int64,
	logger types.InferenceLogger,
	weightScaleFactor mathsdk.LegacyDec,
) *WeightCalculatorV2 {
	return &WeightCalculatorV2{
		CurrentValidatorWeights: currentValidatorWeights,
		ArtifactBatches:         artifactBatches,
		Validations:             validations,
		Participants:            participants,
		Seeds:                   seeds,
		EpochStartBlockHeight:   epochStartBlockHeight,
		Logger:                  logger,
		WeightScaleFactor:       weightScaleFactor,
	}
}

// Calculate computes the new weights for active participants using PoC v2 data.
func (wc *WeightCalculatorV2) Calculate() []*types.ActiveParticipant {
	sortedParticipants := wc.getSortedParticipantKeys()

	var activeParticipants []*types.ActiveParticipant
	for _, participantAddress := range sortedParticipants {
		activeParticipant := wc.validatedParticipantV2(participantAddress)
		if activeParticipant != nil {
			activeParticipants = append(activeParticipants, activeParticipant)
			wc.Logger.LogInfo("CalculateV2: Setting compute validator.", types.PoC, "activeParticipant", activeParticipant)
		}
	}

	return activeParticipants
}

func (wc *WeightCalculatorV2) getSortedParticipantKeys() []string {
	var sortedKeys []string
	for key := range wc.ArtifactBatches {
		sortedKeys = append(sortedKeys, key)
	}
	sort.Strings(sortedKeys)
	return sortedKeys
}

func (wc *WeightCalculatorV2) validatedParticipantV2(participantAddress string) *types.ActiveParticipant {
	participant, ok := wc.Participants[participantAddress]
	if !ok {
		wc.Logger.LogError("CalculateV2: Participant not found", types.PoC, "address", participantAddress)
		return nil
	}

	vals := wc.getParticipantValidationsV2(participantAddress)
	if len(vals) == 0 {
		wc.Logger.LogError("CalculateV2: No validations for participant found", types.PoC, "participant", participantAddress)
		return nil
	}

	nodeWeights, claimedWeight := wc.calculateParticipantWeightV2(wc.ArtifactBatches[participantAddress])
	if claimedWeight < 1 {
		wc.Logger.LogWarn("CalculateV2: Participant has non-positive claimedWeight.", types.PoC, "participant", participantAddress, "claimedWeight", claimedWeight)
		return nil
	}
	wc.Logger.LogInfo("CalculateV2: participant claims weight", types.PoC, "participant", participantAddress, "claimedWeight", claimedWeight)

	if participant.ValidatorKey == "" {
		wc.Logger.LogError("CalculateV2: Participant hasn't provided their validator key.", types.PoC, "participant", participantAddress)
		return nil
	}

	if !wc.pocValidatedV2(vals, participantAddress) {
		return nil
	}

	seed, found := wc.Seeds[participantAddress]
	if !found {
		wc.Logger.LogError("CalculateV2: Seed not found", types.PoC, "blockHeight", wc.EpochStartBlockHeight, "participant", participantAddress)
		return nil
	}

	mlNodes := make([]*types.MLNodeInfo, 0, len(nodeWeights))
	for _, n := range nodeWeights {
		mlNodes = append(mlNodes, &types.MLNodeInfo{
			NodeId:    n.nodeId,
			PocWeight: n.weight,
		})
	}

	wc.Logger.LogInfo("CalculateV2: mlNodes", types.PoC, "mlNodes", mlNodes)

	firstMLNodeArray := &types.ModelMLNodes{
		MlNodes: mlNodes,
	}
	modelMLNodesArray := []*types.ModelMLNodes{firstMLNodeArray}

	activeParticipant := &types.ActiveParticipant{
		Index:        participant.Address,
		ValidatorKey: participant.ValidatorKey,
		Weight:       claimedWeight,
		InferenceUrl: participant.InferenceUrl,
		Seed:         &seed,
		Models:       make([]string, 0),
		MlNodes:      modelMLNodesArray,
	}
	return activeParticipant
}

func (wc *WeightCalculatorV2) getParticipantValidationsV2(participantAddress string) []types.PoCValidationV2 {
	vals := wc.Validations[participantAddress]

	validators := make([]string, len(vals))
	for i, v := range vals {
		validators[i] = v.ValidatorParticipantAddress
	}
	wc.Logger.LogInfo("CalculateV2: Found ALL submitted validations for participant", types.PoC,
		"participant", participantAddress, "len(vals)", len(vals), "validators", validators)

	filteredVals := make([]types.PoCValidationV2, 0, len(vals))
	for _, v := range vals {
		if _, ok := wc.CurrentValidatorWeights[v.ValidatorParticipantAddress]; ok {
			filteredVals = append(filteredVals, v)
		}
	}

	filteredValidators := make([]string, len(filteredVals))
	for i, v := range filteredVals {
		filteredValidators[i] = v.ValidatorParticipantAddress
	}
	wc.Logger.LogInfo("CalculateV2: filtered validations to include only current validators", types.PoC,
		"participant", participantAddress, "len(vals)", len(filteredVals), "validators", filteredValidators)

	return filteredVals
}

// pocValidatedV2 checks if the participant passed validation by majority vote.
// Uses v2 semantics:
// - validated_weight == -1 -> invalid vote
// - validated_weight > 0 -> valid vote
// TODO: use voting for explicit weight once artifacts moved off-chain
func (wc *WeightCalculatorV2) pocValidatedV2(vals []types.PoCValidationV2, participantAddress string) bool {
	totalWeight := calculateTotalWeight(wc.CurrentValidatorWeights)
	halfWeight := int64(totalWeight / 2)
	shouldContinue := false

	if len(wc.CurrentValidatorWeights) > 0 {
		valOutcome := calculateValidationOutcomeV2(wc.CurrentValidatorWeights, vals)
		votedWeight := valOutcome.ValidWeight + valOutcome.InvalidWeight
		if valOutcome.ValidWeight > halfWeight {
			shouldContinue = true
			wc.Logger.LogInfo("CalculateV2: Participant received valid validations from more than half of participants by weight. Accepting",
				types.PoC, "participant", participantAddress,
				"validWeight", valOutcome.ValidWeight,
				"invalidWeight", valOutcome.InvalidWeight,
				"votedWeight", votedWeight,
				"totalWeight", totalWeight,
				"halfWeight", halfWeight,
			)
		} else if valOutcome.InvalidWeight > halfWeight {
			shouldContinue = false
			wc.Logger.LogWarn("CalculateV2: Participant received invalid validations from more than half of participants by weight. Rejecting",
				types.PoC, "participant", participantAddress,
				"validWeight", valOutcome.ValidWeight,
				"invalidWeight", valOutcome.InvalidWeight,
				"votedWeight", votedWeight,
				"totalWeight", totalWeight,
				"halfWeight", halfWeight,
			)
		} else {
			shouldContinue = false
			wc.Logger.LogWarn("CalculateV2: Participant did not receive a majority of either valid or invalid validations. Rejecting.",
				types.PoC, "participant", participantAddress,
				"validWeight", valOutcome.ValidWeight,
				"invalidWeight", valOutcome.InvalidWeight,
				"votedWeight", votedWeight,
				"totalWeight", totalWeight,
				"halfWeight", halfWeight,
			)
		}
	} else {
		shouldContinue = true
		if wc.EpochStartBlockHeight > 0 {
			wc.Logger.LogError("CalculateV2: No current validator weights found. Accepting the participant.", types.PoC, "participant", participantAddress)
		}
	}

	return shouldContinue
}

type nodeWeightV2 struct {
	nodeId string
	weight int64
}

// calculateParticipantWeightV2 computes the claimed weight from v2 artifact batches.
// Weight = total unique artifact nonces across all batches for this participant.
func (wc *WeightCalculatorV2) calculateParticipantWeightV2(batches []types.PoCArtifactBatchV2) ([]nodeWeightV2, int64) {
	nodeWeights := make(map[string]int64)
	totalWeight := int64(0)

	uniqueNonces := make(map[int64]struct{})
	for _, batch := range batches {
		weight := int64(0)
		for _, artifact := range batch.Artifacts {
			if _, exists := uniqueNonces[artifact.Nonce]; !exists {
				uniqueNonces[artifact.Nonce] = struct{}{}
				weight++
			}
		}

		weight = mathsdk.LegacyNewDec(weight).Mul(wc.WeightScaleFactor).TruncateInt64()
		nodeId := batch.NodeId
		nodeWeights[nodeId] += weight
		totalWeight += weight
	}

	nodeWeightsSlice := make([]nodeWeightV2, 0, len(nodeWeights))
	for nodeId, weight := range nodeWeights {
		nodeWeightsSlice = append(nodeWeightsSlice, nodeWeightV2{nodeId: nodeId, weight: weight})
	}
	sort.Slice(nodeWeightsSlice, func(i, j int) bool {
		return nodeWeightsSlice[i].nodeId < nodeWeightsSlice[j].nodeId
	})

	return nodeWeightsSlice, totalWeight
}

// calculateValidationOutcomeV2 computes valid/invalid weights from v2 validations.
// Uses v2 semantics:
// - validated_weight == -1 -> invalid vote
// - validated_weight > 0 -> valid vote
func calculateValidationOutcomeV2(currentValidatorsSet map[string]int64, validations []types.PoCValidationV2) validationOutcome {
	validWeight := int64(0)
	invalidWeight := int64(0)
	for _, v := range validations {
		if weight, ok := currentValidatorsSet[v.ValidatorParticipantAddress]; ok {
			if v.ValidatedWeight == -1 {
				invalidWeight += weight
			} else if v.ValidatedWeight > 0 {
				validWeight += weight
			}
			// validated_weight == 0 is treated as abstention (no weight added)
		}
	}
	return validationOutcome{
		ValidWeight:   validWeight,
		InvalidWeight: invalidWeight,
	}
}

// ComputeNewWeightsV2 computes new weights using PoC v2 data sources.
// This is the v2 equivalent of ComputeNewWeights.
func (am AppModule) ComputeNewWeightsV2(ctx context.Context, upcomingEpoch types.Epoch) []*types.ActiveParticipant {
	epochStartBlockHeight := upcomingEpoch.PocStartBlockHeight
	am.LogInfo("ComputeNewWeightsV2: computing new weights", types.PoC,
		"upcomingEpoch.Index", upcomingEpoch.Index,
		"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight)

	// Get preserved weights from inference-serving MLNodes (same as v1)
	preservedParticipants := am.GetPreviousEpochMLNodesWithInferenceAllocation(ctx, upcomingEpoch)
	am.LogInfo("ComputeNewWeightsV2: Retrieved preserved participants", types.PoC,
		"numPreservedParticipants", len(preservedParticipants))

	currentValidatorWeights, err := am.getCurrentValidatorWeights(ctx)
	am.LogInfo("ComputeNewWeightsV2: Retrieved current validator weights", types.PoC,
		"upcomingEpoch.Index", upcomingEpoch.Index,
		"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight,
		"weights", currentValidatorWeights)

	if err != nil {
		am.LogError("ComputeNewWeightsV2: Error getting current validator weights", types.PoC,
			"upcomingEpoch.Index", upcomingEpoch.Index,
			"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight,
			"error", err)
		return nil
	}

	// Get PoC v2 artifact batches
	allArtifactBatches, err := am.keeper.GetPoCArtifactBatchesV2ByStage(ctx, epochStartBlockHeight)
	if err != nil {
		am.LogError("ComputeNewWeightsV2: Error getting v2 artifact batches by PoC stage", types.PoC,
			"upcomingEpoch.Index", upcomingEpoch.Index,
			"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight,
			"error", err)
		return nil
	}

	// Build inference-serving node IDs for filtering (same as v1)
	inferenceServingNodeIds := am.getInferenceServingNodeIds(ctx, upcomingEpoch)
	am.LogInfo("ComputeNewWeightsV2: Found inference-serving nodes", types.PoC,
		"inferenceServingNodeIds", inferenceServingNodeIds)

	// Filter out artifact batches from inference-serving nodes
	artifactBatches := am.filterArtifactBatchesV2FromInferenceNodes(allArtifactBatches, inferenceServingNodeIds)

	am.LogInfo("ComputeNewWeightsV2: Filtered artifact batches", types.PoC,
		"upcomingEpoch.Index", upcomingEpoch.Index,
		"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight,
		"originalBatchesCount", len(allArtifactBatches),
		"filteredBatchesCount", len(artifactBatches))

	// Get PoC v2 validations
	validationsV2, err := am.keeper.GetPoCValidationsV2ByStage(ctx, epochStartBlockHeight)
	if err != nil {
		am.LogError("ComputeNewWeightsV2: Error getting PoC v2 validations by stage", types.PoC,
			"upcomingEpoch.Index", upcomingEpoch.Index,
			"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight,
			"error", err)
	}

	validators := make([]string, len(validationsV2))
	var i = 0
	for address := range validationsV2 {
		validators[i] = address
		i++
	}
	am.LogInfo("ComputeNewWeightsV2: Retrieved PoC v2 validations", types.PoC,
		"upcomingEpoch.Index", upcomingEpoch.Index,
		"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight,
		"len(validations)", len(validationsV2),
		"validators", validators)

	// Collect participants and seeds (same logic as v1)
	participants := make(map[string]types.Participant)
	seeds := make(map[string]types.RandomSeed)
	allowedBatches := make(map[string][]types.PoCArtifactBatchV2)

	var sortedBatchKeys []string
	for key := range artifactBatches {
		sortedBatchKeys = append(sortedBatchKeys, key)
	}
	sort.Strings(sortedBatchKeys)

	for _, participantAddress := range sortedBatchKeys {
		participant, ok := am.keeper.GetParticipant(ctx, participantAddress)
		if !ok {
			am.LogError("ComputeNewWeightsV2: Error getting participant", types.PoC,
				"address", participantAddress,
				"upcomingEpoch.Index", upcomingEpoch.Index,
				"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight)
			continue
		}
		participants[participantAddress] = participant

		seed, found := am.keeper.GetRandomSeed(ctx, upcomingEpoch.Index, participantAddress)
		if !found {
			am.LogError("ComputeNewWeightsV2: Participant didn't submit the seed for the upcoming epoch", types.PoC,
				"upcomingEpoch.Index", upcomingEpoch.Index,
				"upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight,
				"participant", participantAddress)
			continue
		}
		seeds[participantAddress] = seed
		allowedBatches[participantAddress] = artifactBatches[participantAddress]
	}

	// Add seeds for preserved participants
	for _, preservedParticipant := range preservedParticipants {
		participantAddress := preservedParticipant.Index
		if seed, found := am.keeper.GetRandomSeed(ctx, upcomingEpoch.Index, participantAddress); found {
			preservedParticipant.Seed = &seed
			seeds[participantAddress] = seed
			am.LogInfo("ComputeNewWeightsV2: Added seed for preserved participant", types.PoC,
				"participantAddress", participantAddress)
		} else {
			am.LogWarn("ComputeNewWeightsV2: No seed found for preserved participant", types.PoC,
				"participantAddress", participantAddress)
		}
	}

	// Create v2 weight calculator and calculate
	params := am.keeper.GetParams(ctx)
	weightScaleFactor := params.PocParams.GetWeightScaleFactorDec()
	calculator := NewWeightCalculatorV2(
		currentValidatorWeights,
		allowedBatches,
		validationsV2,
		participants,
		seeds,
		epochStartBlockHeight,
		am,
		weightScaleFactor,
	)
	pocMiningParticipants := calculator.Calculate()

	// Merge preserved participants with PoC mining participants (same logic as v1)
	var allActiveParticipants []*types.ActiveParticipant

	for _, preservedParticipant := range preservedParticipants {
		participantAddress := preservedParticipant.Index

		if pocParticipant := findParticipantByAddress(pocMiningParticipants, participantAddress); pocParticipant != nil {
			combinedMLNodes := mergeMLNodeArrays(preservedParticipant.MlNodes, pocParticipant.MlNodes)
			combinedWeight := int64(0)
			for _, mlNode := range combinedMLNodes[0].MlNodes {
				combinedWeight += mlNode.PocWeight
			}

			mergedParticipant := &types.ActiveParticipant{
				Index:        participantAddress,
				ValidatorKey: preservedParticipant.ValidatorKey,
				Weight:       combinedWeight,
				InferenceUrl: preservedParticipant.InferenceUrl,
				Seed:         pocParticipant.Seed,
				Models:       make([]string, 0),
				MlNodes:      combinedMLNodes,
			}

			allActiveParticipants = append(allActiveParticipants, mergedParticipant)

			am.LogInfo("ComputeNewWeightsV2: Merged preserved and PoC participant", types.PoC,
				"participantAddress", participantAddress,
				"preservedWeight", preservedParticipant.Weight,
				"pocWeight", pocParticipant.Weight,
				"combinedWeight", combinedWeight,
				"combinedMLNodes", combinedMLNodes)
		} else {
			allActiveParticipants = append(allActiveParticipants, preservedParticipant)

			am.LogInfo("ComputeNewWeightsV2: Added preserved-only participant", types.PoC,
				"participantAddress", participantAddress,
				"preservedWeight", preservedParticipant.Weight)
		}
	}

	preservedParticipantsSet := make(map[string]bool)
	for _, preservedParticipant := range preservedParticipants {
		preservedParticipantsSet[preservedParticipant.Index] = true
	}

	for _, pocParticipant := range pocMiningParticipants {
		if _, alreadyPreserved := preservedParticipantsSet[pocParticipant.Index]; alreadyPreserved {
			continue
		}
		allActiveParticipants = append(allActiveParticipants, pocParticipant)

		am.LogInfo("ComputeNewWeightsV2: Added PoC-only participant", types.PoC,
			"participantAddress", pocParticipant.Index,
			"pocWeight", pocParticipant.Weight)
	}

	am.LogInfo("ComputeNewWeightsV2: Final summary", types.PoC,
		"preservedParticipants", len(preservedParticipants),
		"pocMiningParticipants", len(pocMiningParticipants),
		"totalActiveParticipants", len(allActiveParticipants))

	return allActiveParticipants
}

// filterArtifactBatchesV2FromInferenceNodes removes PoC v2 artifact batches from nodes serving inference.
func (am AppModule) filterArtifactBatchesV2FromInferenceNodes(allBatches map[string][]types.PoCArtifactBatchV2, inferenceServingNodeIds map[string]bool) map[string][]types.PoCArtifactBatchV2 {
	filteredBatches := make(map[string][]types.PoCArtifactBatchV2)
	excludedBatchCount := 0

	for participantAddress, batches := range allBatches {
		var validBatches []types.PoCArtifactBatchV2

		for _, batch := range batches {
			if inferenceServingNodeIds[batch.NodeId] {
				excludedBatchCount++
				am.LogWarn("filterArtifactBatchesV2FromInferenceNodes: Excluding PoC v2 batch from inference-serving node", types.PoC,
					"participantAddress", participantAddress,
					"nodeId", batch.NodeId,
					"batchArtifactCount", len(batch.Artifacts))
			} else {
				validBatches = append(validBatches, batch)
			}
		}

		if len(validBatches) > 0 {
			filteredBatches[participantAddress] = validBatches
		}
	}

	am.LogInfo("filterArtifactBatchesV2FromInferenceNodes: Summary", types.PoC,
		"excludedBatchCount", excludedBatchCount,
		"originalParticipants", len(allBatches),
		"filteredParticipants", len(filteredBatches))

	return filteredBatches
}
