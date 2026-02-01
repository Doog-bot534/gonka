package calculations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetSlots_Determinism(t *testing.T) {
	weights := map[string]int64{
		"node1": 100,
		"node2": 200,
		"node3": 300,
	}
	appHash := "1234567890"
	participant := "gonka100"
	nSlots := 64

	slots1 := GetSlots(appHash, participant, weights, nSlots)
	slots2 := GetSlots(appHash, participant, weights, nSlots)

	require.Equal(t, slots1, slots2, "same inputs should produce same outputs")
}

func TestGetSlots_WeightDistribution(t *testing.T) {
	weights := map[string]int64{
		"node1": 100,
		"node2": 200,
		"node3": 300,
	}
	appHash := "testhash"
	participant := "participant1"
	nSlots := 1000

	slots := GetSlots(appHash, participant, weights, nSlots)

	counts := make(map[string]int)
	for _, slot := range slots {
		counts[slot]++
	}

	// With weights 100:200:300, expected ratios are ~1:2:3
	// Allow 15% tolerance for statistical variance
	total := float64(nSlots)
	require.InDelta(t, 100.0/600.0, float64(counts["node1"])/total, 0.15)
	require.InDelta(t, 200.0/600.0, float64(counts["node2"])/total, 0.15)
	require.InDelta(t, 300.0/600.0, float64(counts["node3"])/total, 0.15)
}

func TestGetSlots_EmptyWeights(t *testing.T) {
	slots := GetSlots("hash", "participant", nil, 10)
	require.Nil(t, slots)

	slots = GetSlots("hash", "participant", map[string]int64{}, 10)
	require.Nil(t, slots)
}

func TestGetSlots_ZeroSlots(t *testing.T) {
	weights := map[string]int64{"node1": 100}
	slots := GetSlots("hash", "participant", weights, 0)
	require.Nil(t, slots)
}

func TestGetSlots_ZeroTotalWeight(t *testing.T) {
	weights := map[string]int64{
		"node1": 0,
		"node2": 0,
	}
	slots := GetSlots("hash", "participant", weights, 10)
	require.Nil(t, slots)
}

func TestGetSlot_SingleSlot(t *testing.T) {
	weights := map[string]int64{
		"node1": 100,
		"node2": 200,
		"node3": 300,
	}
	appHash := "1234567890"
	participant := "gonka100"

	slot := GetSlot(appHash, participant, weights, 0)
	require.NotEmpty(t, slot)
	require.Contains(t, []string{"node1", "node2", "node3"}, slot)
}

func TestGetSlot_MatchesGetSlots(t *testing.T) {
	weights := map[string]int64{
		"node1": 100,
		"node2": 200,
		"node3": 300,
	}
	appHash := "testhash"
	participant := "participant1"
	nSlots := 10

	slots := GetSlots(appHash, participant, weights, nSlots)

	for i := 0; i < nSlots; i++ {
		singleSlot := GetSlot(appHash, participant, weights, i)
		require.Equal(t, slots[i], singleSlot, "GetSlot should match GetSlots at index %d", i)
	}
}

func TestGetSlots_DifferentParticipants(t *testing.T) {
	weights := map[string]int64{
		"node1": 100,
		"node2": 200,
		"node3": 300,
	}
	appHash := "hash"
	nSlots := 64

	slots1 := GetSlots(appHash, "participant1", weights, nSlots)
	slots2 := GetSlots(appHash, "participant2", weights, nSlots)

	require.NotEqual(t, slots1, slots2, "different participants should have different slots")
}

func TestGetSlots_SingleValidator(t *testing.T) {
	weights := map[string]int64{
		"only_node": 1000,
	}
	appHash := "hash"
	participant := "participant"
	nSlots := 10

	slots := GetSlots(appHash, participant, weights, nSlots)

	for _, slot := range slots {
		require.Equal(t, "only_node", slot)
	}
}
