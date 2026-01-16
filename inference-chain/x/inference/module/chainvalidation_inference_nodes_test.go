package inference_test

import (
	inference "github.com/productscience/inference/x/inference/module"
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestGetInferenceServingNodeIds_ScopedByParticipant(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	am := inference.NewAppModule(nil, k, nil, nil, nil, nil)

	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId: 1,
		Participants: []*types.ActiveParticipant{
			{
				Index: "participant-a",
				MlNodes: []*types.ModelMLNodes{
					{
						MlNodes: []*types.MLNodeInfo{
							mlNode("node-1", 10, true),  // inference
							mlNode("node-2", 10, false), // PoC
						},
					},
				},
			},
			{
				Index: "participant-b",
				MlNodes: []*types.ModelMLNodes{
					{
						MlNodes: []*types.MLNodeInfo{
							mlNode("node-1", 10, false), // same id as participant-a, PoC
							mlNode("node-3", 10, false),
						},
					},
				},
			},
		},
	}))

	inferenceServingNodeIds := am.GetInferenceServingNodeIds(ctx, types.Epoch{Index: 2})

	require.Len(t, inferenceServingNodeIds, 1)
	require.Contains(t, inferenceServingNodeIds, "participant-a")
	require.True(t, inferenceServingNodeIds["participant-a"]["node-1"])
	require.False(t, inferenceServingNodeIds["participant-a"]["node-2"])
	require.NotContains(t, inferenceServingNodeIds, "participant-b")
}

func TestFilterPoCBatchesFromInferenceNodes_NoCrossParticipantCollision(t *testing.T) {
	k, _, _ := keepertest.InferenceKeeperReturningMocks(t)
	am := inference.NewAppModule(nil, k, nil, nil, nil, nil)

	allBatches := map[string][]types.PoCBatch{
		"participant-a": {
			{ParticipantAddress: "participant-a", NodeId: "node-1", Nonces: []int64{1}},
			{ParticipantAddress: "participant-a", NodeId: "node-2", Nonces: []int64{2}},
		},
		"participant-b": {
			{ParticipantAddress: "participant-b", NodeId: "node-1", Nonces: []int64{3}},
		},
	}
	inferenceServingNodeIds := map[string]map[string]bool{
		"participant-a": {"node-1": true},
	}

	filtered := am.FilterPoCBatchesFromInferenceNodes(allBatches, inferenceServingNodeIds)

	require.Len(t, filtered, 2)
	require.Len(t, filtered["participant-a"], 1)
	require.Equal(t, "node-2", filtered["participant-a"][0].NodeId)
	require.Len(t, filtered["participant-b"], 1)
	require.Equal(t, "node-1", filtered["participant-b"][0].NodeId)
}

func TestFilterPoCBatchesFromInferenceNodes_FiltersOnlyInferenceNodes(t *testing.T) {
	k, _, _ := keepertest.InferenceKeeperReturningMocks(t)
	am := inference.NewAppModule(nil, k, nil, nil, nil, nil)

	allBatches := map[string][]types.PoCBatch{
		"participant-a": {
			{ParticipantAddress: "participant-a", NodeId: "node-1", Nonces: []int64{1}},
			{ParticipantAddress: "participant-a", NodeId: "node-2", Nonces: []int64{2}},
			{ParticipantAddress: "participant-a", NodeId: "node-3", Nonces: []int64{3}},
		},
		"participant-b": {
			{ParticipantAddress: "participant-b", NodeId: "node-4", Nonces: []int64{4}},
			{ParticipantAddress: "participant-b", NodeId: "node-5", Nonces: []int64{5}},
		},
		"participant-c": {
			{ParticipantAddress: "participant-c", NodeId: "node-6", Nonces: []int64{6}},
		},
	}
	inferenceServingNodeIds := map[string]map[string]bool{
		"participant-a": {"node-1": true, "node-3": true},
		"participant-c": {"node-6": true},
	}

	filtered := am.FilterPoCBatchesFromInferenceNodes(allBatches, inferenceServingNodeIds)

	require.Len(t, filtered, 2)
	require.Len(t, filtered["participant-a"], 1)
	require.Equal(t, "node-2", filtered["participant-a"][0].NodeId)
	require.Len(t, filtered["participant-b"], 2)
	require.Equal(t, "node-4", filtered["participant-b"][0].NodeId)
	require.Equal(t, "node-5", filtered["participant-b"][1].NodeId)
	require.NotContains(t, filtered, "participant-c")
}

func mlNode(id string, weight int64, pocSlot bool) *types.MLNodeInfo {
	return &types.MLNodeInfo{
		NodeId:             id,
		PocWeight:          weight,
		TimeslotAllocation: []bool{true, pocSlot},
	}
}
