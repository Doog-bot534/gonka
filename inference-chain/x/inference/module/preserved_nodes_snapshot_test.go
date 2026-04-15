package inference

import (
	"testing"

	mathsdk "cosmossdk.io/math"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/types"
)

func TestCaptureGenerationStartTimestampStoresPreservedNodesSnapshot(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)

	am.captureGenerationStartTimestamp(ctx, 1234, 100, nil)

	validationSnapshot, found, err := k.GetPoCValidationSnapshot(ctx, 100)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(1234), validationSnapshot.GenerationStartTimestamp)

	preservedSnapshot, found, err := k.GetPreservedNodesSnapshot(ctx, 100)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(100), preservedSnapshot.EpisodeAnchorHeight)
	require.Empty(t, preservedSnapshot.ModelPreservedNodes)
}

func TestCaptureGenerationStartTimestampStoresProvidedPreservedNodesSnapshot(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)

	expectedSnapshot := &types.PreservedNodesSnapshot{
		EpisodeAnchorHeight: 300,
		ModelPreservedNodes: []*types.ModelPreservedNodes{
			{
				ModelId:          "model-a",
				PreservedNodeIds: []string{"node-1"},
			},
		},
	}

	am.captureGenerationStartTimestamp(ctx, 1234, 300, expectedSnapshot)

	preservedSnapshot, found, err := k.GetPreservedNodesSnapshot(ctx, 300)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, *expectedSnapshot, preservedSnapshot)
}

func TestDeleteGenerationSnapshotsDeletesPreservedNodesSnapshot(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)

	require.NoError(t, k.SetPoCValidationSnapshot(ctx, types.PoCValidationSnapshot{
		PocStageStartHeight:      200,
		GenerationStartTimestamp: 4567,
	}))
	require.NoError(t, k.SetPreservedNodesSnapshot(ctx, types.PreservedNodesSnapshot{
		EpisodeAnchorHeight: 200,
	}))

	am.deleteGenerationSnapshots(ctx, 200)

	_, found, err := k.GetPoCValidationSnapshot(ctx, 200)
	require.NoError(t, err)
	require.False(t, found)

	_, found, err = k.GetPreservedNodesSnapshot(ctx, 200)
	require.NoError(t, err)
	require.False(t, found)
}

func TestGetNotPreservedTotalWeightByParticipantUsesPreservedSnapshot(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)

	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId: 5,
		Participants: []*types.ActiveParticipant{
			{
				Index:  testutil.Executor,
				Models: []string{"model-a"},
				MlNodes: []*types.ModelMLNodes{
					{
						MlNodes: []*types.MLNodeInfo{
							{NodeId: "node-1", PocWeight: 10, TimeslotAllocation: []bool{true, false}},
							{NodeId: "node-2", PocWeight: 20, TimeslotAllocation: []bool{true, false}},
						},
					},
				},
			},
		},
	}))

	weights, err := am.GetNotPreservedTotalWeightByParticipant(
		ctx,
		5,
		map[string]mathsdk.LegacyDec{"model-a": mathsdk.LegacyOneDec()},
		&types.PreservedNodesSnapshot{
			EpisodeAnchorHeight: 321,
			ModelPreservedNodes: []*types.ModelPreservedNodes{
				{
					ModelId:          "model-a",
					PreservedNodeIds: []string{"node-1"},
				},
			},
		},
	)
	require.NoError(t, err)
	require.Equal(t, int64(20), weights[testutil.Executor])
}

func TestGetInferenceServingNodeIdsUsesUpcomingEpochAnchor(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)

	require.NoError(t, k.SetPreservedNodesSnapshot(ctx, types.PreservedNodesSnapshot{
		EpisodeAnchorHeight: 100,
		ModelPreservedNodes: []*types.ModelPreservedNodes{
			{
				ModelId:          "model-a",
				PreservedNodeIds: []string{"node-1"},
			},
		},
	}))

	inferenceServingNodeIds := am.getInferenceServingNodeIds(ctx, types.Epoch{Index: 2, PocStartBlockHeight: 100})
	require.Contains(t, inferenceServingNodeIds, "node-1")
}
