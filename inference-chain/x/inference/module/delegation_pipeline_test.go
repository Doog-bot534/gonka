package inference

import (
	"testing"

	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/types"
)

func TestBuildBootstrapDelegationSnapshot_FiltersActiveParticipantsOnly(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams = &types.PocParams{
		Models: []*types.PoCModelConfig{
			{ModelId: "active-model", WeightScaleFactor: types.DecimalFromFloat(1)},
			{ModelId: "new-model", WeightScaleFactor: types.DecimalFromFloat(1)},
		},
	}
	params.DelegationParams = &types.DelegationParams{
		DeployWindow: 1,
		WThreshold:   types.DecimalFromFloat(0.5),
		VMin:         2,
	}
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: 1, PocStartBlockHeight: 100}))
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:             1,
		EpochGroupId:        1,
		PocStartBlockHeight: 100,
		Participants: []*types.ActiveParticipant{
			{Index: testutil.Executor, Weight: 100, Models: []string{"active-model"}},
			{Index: testutil.Executor2, Weight: 60, Models: []string{"active-model"}},
			{Index: testutil.Validator, Weight: 40, Models: []string{"active-model"}},
		},
	}))

	require.NoError(t, k.SetPoCDirectIntent(ctx, "new-model", testutil.Executor))
	require.NoError(t, k.SetPoCDirectIntent(ctx, "new-model", testutil.Executor2))
	require.NoError(t, k.SetPoCDelegation(ctx, types.PoCDelegation{
		ModelId:    "new-model",
		Delegator:  testutil.Validator,
		DelegateTo: testutil.Executor,
	}))

	outsider := testutil.Bech32Addr(99)
	require.NoError(t, k.SetPoCDirectIntent(ctx, "new-model", outsider))
	require.NoError(t, k.SetPoCDelegation(ctx, types.PoCDelegation{
		ModelId:    "new-model",
		Delegator:  outsider,
		DelegateTo: testutil.Executor,
	}))

	am := NewAppModule(nil, k, nil, nil, nil, nil)
	snapshot, err := am.buildBootstrapDelegationSnapshot(ctx, 197)
	require.NoError(t, err)

	require.Len(t, snapshot.Delegations, 1)
	require.Equal(t, testutil.Validator, snapshot.Delegations[0].Delegator)
	require.Len(t, snapshot.Intents, 2)

	results, totalNetworkWeight, err := am.buildBootstrapPreEligibilityReport(ctx, snapshot)
	require.NoError(t, err)
	require.Equal(t, int64(200), totalNetworkWeight)
	require.Len(t, results, 1)
	require.Equal(t, "new-model", results[0].ModelId)
	require.True(t, results[0].PreEligible)
	require.Equal(t, int64(2), results[0].IntentHostCount)
	require.Equal(t, int64(160), results[0].IntentWeight)
	require.Equal(t, int64(200), results[0].ReachableVotingPower)
}

func TestGetPreviousConsensusWeights_FallsBackToEpochZeroGroupWeights(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)

	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 0))
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: 0,
		ModelId:    "",
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: testutil.Validator, Weight: 100},
			{MemberAddress: testutil.Validator2, Weight: 201},
		},
	})

	am := NewAppModule(nil, k, nil, nil, nil, nil)
	weights, total := am.getPreviousConsensusWeights(ctx)

	require.Equal(t, int64(301), total)
	require.Equal(t, map[string]int64{
		testutil.Validator:  100,
		testutil.Validator2: 201,
	}, weights)
}

func TestBuildDelegationSnapshot_FiltersActiveParticipantsAndExcludesIntents(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams = &types.PocParams{
		Models: []*types.PoCModelConfig{
			{ModelId: "active-model", WeightScaleFactor: types.DecimalFromFloat(1)},
			{ModelId: "new-model", WeightScaleFactor: types.DecimalFromFloat(1)},
		},
	}
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:             1,
		EpochGroupId:        1,
		PocStartBlockHeight: 100,
		Participants: []*types.ActiveParticipant{
			{Index: testutil.Executor, Weight: 100, Models: []string{"active-model"}},
			{Index: testutil.Executor2, Weight: 60, Models: []string{"active-model"}},
		},
	}))

	require.NoError(t, k.SetPoCDelegation(ctx, types.PoCDelegation{
		ModelId:    "new-model",
		Delegator:  testutil.Executor,
		DelegateTo: testutil.Executor2,
	}))
	require.NoError(t, k.SetPoCRefusal(ctx, "new-model", testutil.Executor2))
	require.NoError(t, k.SetPoCDirectIntent(ctx, "new-model", testutil.Executor))

	outsider := testutil.Bech32Addr(100)
	require.NoError(t, k.SetPoCDelegation(ctx, types.PoCDelegation{
		ModelId:    "new-model",
		Delegator:  outsider,
		DelegateTo: testutil.Executor,
	}))
	require.NoError(t, k.SetPoCRefusal(ctx, "new-model", outsider))

	am := NewAppModule(nil, k, nil, nil, nil, nil)
	snapshot, err := am.buildDelegationSnapshot(ctx, 197)
	require.NoError(t, err)

	require.Len(t, snapshot.Delegations, 1)
	require.Equal(t, testutil.Executor, snapshot.Delegations[0].Delegator)
	require.Len(t, snapshot.Refusals, 1)
	require.Equal(t, testutil.Executor2, snapshot.Refusals[0].Participant)
}

func TestBuildBootstrapModelPreEligibilityResults_Conditions(t *testing.T) {
	makeParams := func(threshold float64, vmin int64) types.Params {
		return types.Params{
			PocParams: &types.PocParams{
				Models: []*types.PoCModelConfig{
					{ModelId: "candidate", WeightScaleFactor: types.DecimalFromFloat(1)},
				},
			},
			DelegationParams: &types.DelegationParams{
				WThreshold: types.DecimalFromFloat(threshold),
				VMin:       vmin,
			},
		}
	}

	t.Run("fails_weight_threshold_only", func(t *testing.T) {
		calc := buildBootstrapPreEligibilityCalculator(
			map[string]int64{"a": 100, "b": 60, "c": 40},
			200,
			[]string{"candidate"},
			map[string]map[string]string{"candidate": {"b": "a", "c": "a"}},
			map[string]map[string]bool{"candidate": {"a": true}},
			makeParams(0.6, 1),
		)

		result := buildBootstrapPreEligibilityResults(calc, []string{"candidate"})
		require.Len(t, result, 1)
		require.False(t, result[0].PreEligible)
		require.False(t, result[0].MeetsWeightThreshold)
		require.True(t, result[0].MeetsVMin)
		require.True(t, result[0].MeetsReachability)
	})

	t.Run("fails_vmin_only", func(t *testing.T) {
		calc := buildBootstrapPreEligibilityCalculator(
			map[string]int64{"a": 100, "b": 60, "c": 40},
			200,
			[]string{"candidate"},
			map[string]map[string]string{"candidate": {"b": "a", "c": "a"}},
			map[string]map[string]bool{"candidate": {"a": true}},
			makeParams(0.4, 2),
		)

		result := buildBootstrapPreEligibilityResults(calc, []string{"candidate"})
		require.Len(t, result, 1)
		require.False(t, result[0].PreEligible)
		require.True(t, result[0].MeetsWeightThreshold)
		require.False(t, result[0].MeetsVMin)
		require.True(t, result[0].MeetsReachability)
	})

	t.Run("fails_reachability_only", func(t *testing.T) {
		calc := buildBootstrapPreEligibilityCalculator(
			map[string]int64{"a": 50, "b": 10, "c": 40},
			100,
			[]string{"candidate"},
			map[string]map[string]string{},
			map[string]map[string]bool{"candidate": {"a": true, "b": true}},
			makeParams(0.5, 2),
		)

		result := buildBootstrapPreEligibilityResults(calc, []string{"candidate"})
		require.Len(t, result, 1)
		require.False(t, result[0].PreEligible)
		require.True(t, result[0].MeetsWeightThreshold)
		require.True(t, result[0].MeetsVMin)
		require.False(t, result[0].MeetsReachability)
	})
}

func TestCaptureBootstrapDelegationSnapshot_StoresSnapshotAndEmitsEvents(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams = &types.PocParams{
		Models: []*types.PoCModelConfig{
			{ModelId: "active-model", WeightScaleFactor: types.DecimalFromFloat(1)},
			{ModelId: "eligible-model", WeightScaleFactor: types.DecimalFromFloat(1)},
			{ModelId: "ineligible-model", WeightScaleFactor: types.DecimalFromFloat(1)},
		},
	}
	params.DelegationParams = &types.DelegationParams{
		DeployWindow: 1,
		WThreshold:   types.DecimalFromFloat(0.5),
		VMin:         2,
	}
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:             1,
		EpochGroupId:        1,
		PocStartBlockHeight: 100,
		Participants: []*types.ActiveParticipant{
			{Index: testutil.Executor, Weight: 100, Models: []string{"active-model"}},
			{Index: testutil.Executor2, Weight: 60, Models: []string{"active-model"}},
			{Index: testutil.Validator, Weight: 40, Models: []string{"active-model"}},
		},
	}))

	require.NoError(t, k.SetPoCDirectIntent(ctx, "eligible-model", testutil.Executor))
	require.NoError(t, k.SetPoCDirectIntent(ctx, "eligible-model", testutil.Executor2))
	require.NoError(t, k.SetPoCDelegation(ctx, types.PoCDelegation{
		ModelId:    "eligible-model",
		Delegator:  testutil.Validator,
		DelegateTo: testutil.Executor,
	}))

	require.NoError(t, k.SetPoCDirectIntent(ctx, "ineligible-model", testutil.Executor))

	am := NewAppModule(nil, k, nil, nil, nil, nil)
	ctx = ctx.WithEventManager(sdk.NewEventManager())
	am.captureBootstrapDelegationSnapshot(ctx, 197)

	snapshot, found := k.GetBootstrapDelegationSnapshot(ctx)
	require.True(t, found)
	require.Equal(t, int64(197), snapshot.SnapshotHeight)

	resultsSlice, totalNetworkWeight, err := am.buildBootstrapPreEligibilityReport(ctx, snapshot)
	require.NoError(t, err)
	require.Equal(t, int64(200), totalNetworkWeight)
	require.Len(t, resultsSlice, 2)

	results := map[string]*types.BootstrapModelPreEligibility{}
	for _, result := range resultsSlice {
		results[result.ModelId] = result
	}
	require.True(t, results["eligible-model"].PreEligible)
	require.False(t, results["ineligible-model"].PreEligible)

	events := ctx.EventManager().Events()
	require.Len(t, events, 2)
	require.Equal(t, "bootstrap_model_preeligibility", events[0].Type)
	require.Equal(t, "bootstrap_model_preeligibility", events[1].Type)
}

func TestCaptureDelegationSnapshot_StoresFrozenState(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams = &types.PocParams{
		Models: []*types.PoCModelConfig{
			{ModelId: "active-model", WeightScaleFactor: types.DecimalFromFloat(1)},
			{ModelId: "candidate", WeightScaleFactor: types.DecimalFromFloat(1)},
		},
	}
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:             1,
		EpochGroupId:        1,
		PocStartBlockHeight: 100,
		Participants: []*types.ActiveParticipant{
			{Index: testutil.Executor, Weight: 100, Models: []string{"active-model"}},
			{Index: testutil.Validator, Weight: 100, Models: []string{"active-model"}},
		},
	}))

	require.NoError(t, k.SetPoCDelegation(ctx, types.PoCDelegation{
		ModelId:    "candidate",
		Delegator:  testutil.Validator,
		DelegateTo: testutil.Executor,
	}))
	require.NoError(t, k.SetPoCRefusal(ctx, "candidate", testutil.Executor))
	require.NoError(t, k.SetPoCDirectIntent(ctx, "candidate", testutil.Executor))

	am := NewAppModule(nil, k, nil, nil, nil, nil)
	am.captureDelegationSnapshot(ctx, 197)

	snapshot, found := k.GetDelegationSnapshot(ctx)
	require.True(t, found)
	require.Equal(t, int64(197), snapshot.SnapshotHeight)
	require.Len(t, snapshot.Delegations, 1)
	require.Len(t, snapshot.Refusals, 1)
}

func TestComputeStoreCommitVotingPowers_UsesExistingVotingPowersAndBootstrapDelegationForNewModels(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams = &types.PocParams{
		Models: []*types.PoCModelConfig{
			{ModelId: "existing-model", WeightScaleFactor: types.DecimalFromFloat(1)},
			{ModelId: "new-model", WeightScaleFactor: types.DecimalFromFloat(1)},
		},
	}
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:             1,
		EpochGroupId:        1,
		PocStartBlockHeight: 100,
		Participants: []*types.ActiveParticipant{
			{
				Index:        testutil.Executor,
				Weight:       100,
				Models:       []string{"existing-model"},
				VotingPowers: []*types.ModelVotingPower{{ModelId: "existing-model", VotingPower: 70}},
			},
			{
				Index:        testutil.Executor2,
				Weight:       60,
				Models:       []string{"existing-model"},
				VotingPowers: []*types.ModelVotingPower{{ModelId: "existing-model", VotingPower: 30}},
			},
			{
				Index:  testutil.Validator,
				Weight: 40,
			},
		},
	}))

	require.NoError(t, k.SetBootstrapDelegationSnapshot(ctx, types.BootstrapDelegationSnapshot{
		SnapshotHeight: 111,
		Delegations: []*types.PoCDelegation{
			{
				ModelId:    "new-model",
				Delegator:  testutil.Validator,
				DelegateTo: testutil.Executor,
			},
		},
	}))

	require.NoError(t, k.SetPoCV2StoreCommit(ctx, types.PoCV2StoreCommit{
		ParticipantAddress:       testutil.Executor,
		PocStageStartBlockHeight: 180,
		ModelId:                  "new-model",
		Count:                    1,
	}))
	require.NoError(t, k.SetPoCV2StoreCommit(ctx, types.PoCV2StoreCommit{
		ParticipantAddress:       testutil.Executor2,
		PocStageStartBlockHeight: 180,
		ModelId:                  "existing-model",
		Count:                    1,
	}))

	am := NewAppModule(nil, k, nil, nil, nil, nil)
	modelWeights, totalWeight := am.computeStoreCommitVotingPowers(ctx, 180, "test")
	require.Equal(t, int64(200), totalWeight)

	got := map[string]map[string]int64{}
	for _, modelWeight := range modelWeights {
		got[modelWeight.ModelId] = types.VotingPowerSliceToMap(modelWeight.VotingPowers)
	}

	require.Equal(t, map[string]int64{
		testutil.Executor:  70,
		testutil.Executor2: 30,
	}, got["existing-model"])
	require.Equal(t, map[string]int64{
		testutil.Executor: 140,
	}, got["new-model"])
}

func TestComputeStoreCommitVotingPowers_DoesNotFallbackToLiveDelegations(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams = &types.PocParams{
		Models: []*types.PoCModelConfig{
			{ModelId: "new-model", WeightScaleFactor: types.DecimalFromFloat(1)},
		},
	}
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:             1,
		EpochGroupId:        1,
		PocStartBlockHeight: 100,
		Participants: []*types.ActiveParticipant{
			{Index: testutil.Executor, Weight: 100},
			{Index: testutil.Validator, Weight: 40},
		},
	}))

	require.NoError(t, k.SetPoCDelegation(ctx, types.PoCDelegation{
		ModelId:    "new-model",
		Delegator:  testutil.Validator,
		DelegateTo: testutil.Executor,
	}))
	require.NoError(t, k.SetPoCV2StoreCommit(ctx, types.PoCV2StoreCommit{
		ParticipantAddress:       testutil.Executor,
		PocStageStartBlockHeight: 180,
		ModelId:                  "new-model",
		Count:                    1,
	}))

	am := NewAppModule(nil, k, nil, nil, nil, nil)
	modelWeights, _ := am.computeStoreCommitVotingPowers(ctx, 180, "test")
	require.Len(t, modelWeights, 1)

	got := types.VotingPowerSliceToMap(modelWeights[0].VotingPowers)
	require.Equal(t, map[string]int64{
		testutil.Executor: 100,
	}, got)
}

func TestBuildDelegationWeightCalculator_UsesValidationSnapshotForNextEpochVotingPowers(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams = &types.PocParams{
		Models: []*types.PoCModelConfig{
			{ModelId: "model-a", WeightScaleFactor: types.DecimalFromFloat(1)},
		},
	}
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:             1,
		EpochGroupId:        1,
		PocStartBlockHeight: 100,
		Participants: []*types.ActiveParticipant{
			{Index: testutil.Executor, Weight: 100},
			{Index: testutil.Validator, Weight: 40},
			{Index: testutil.Executor2, Weight: 20},
		},
	}))

	require.NoError(t, k.SetDelegationSnapshot(ctx, types.DelegationSnapshot{
		SnapshotHeight: 111,
		Delegations: []*types.PoCDelegation{
			{
				ModelId:    "model-a",
				Delegator:  testutil.Validator,
				DelegateTo: testutil.Executor,
			},
		},
	}))
	require.NoError(t, k.SetPoCDelegation(ctx, types.PoCDelegation{
		ModelId:    "model-a",
		Delegator:  testutil.Validator,
		DelegateTo: testutil.Executor2,
	}))

	activeParticipants := []*types.ActiveParticipant{
		{
			Index:  testutil.Executor,
			Models: []string{"model-a"},
			MlNodes: []*types.ModelMLNodes{{
				MlNodes: []*types.MLNodeInfo{{NodeId: "node-1", PocWeight: 10}},
			}},
			Weight: 100,
		},
	}

	am := NewAppModule(nil, k, nil, nil, nil, nil)
	dwc := am.buildDelegationWeightCalculator(ctx, activeParticipants, map[string]sdkmath.LegacyDec{"model-a": sdkmath.LegacyOneDec()}, params)
	modes := dwc.ResolveGroupParticipation("model-a")
	require.Equal(t, ModeDelegate, modes[testutil.Validator])
	require.Equal(t, ModeNone, modes[testutil.Executor2])

	vp := dwc.ComputeGroupVotingPowers("model-a", modes, map[string]int64{
		testutil.Executor:  100,
		testutil.Validator: 40,
		testutil.Executor2: 20,
	})
	require.Equal(t, int64(140), vp[testutil.Executor])
}

func TestProjectedReachableVotingPower(t *testing.T) {
	calc := &DelegationWeightCalculator{
		Groups: map[string]*GroupData{
			"candidate": {
				Members:          []string{"a", "b"},
				MemberPocWeights: map[string]int64{},
				ConsensusKoeff:   sdkmath.LegacyOneDec(),
			},
		},
		ConsensusWeights: map[string]int64{
			"a": 50,
			"b": 20,
			"c": 30,
		},
		TotalNetworkWeight: 100,
		Delegations: map[string]map[string]string{
			"candidate": {
				"c": "a",
			},
		},
	}

	require.Equal(t, int64(100), calc.ProjectedReachableVotingPower("candidate"))
	require.True(t, calc.MeetsReachabilityThreshold("candidate"))
}
