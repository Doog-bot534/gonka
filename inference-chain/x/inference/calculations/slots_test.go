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

func TestGetSlots_NegativeWeightsSkipped(t *testing.T) {
	weights := map[string]int64{
		"node1": 100,
		"node2": -50, // Should be skipped
		"node3": 200,
	}
	appHash := "hash"
	participant := "participant"
	nSlots := 100

	slots := GetSlots(appHash, participant, weights, nSlots)
	require.NotNil(t, slots)
	require.Len(t, slots, nSlots)

	// Verify node2 never appears
	for _, slot := range slots {
		require.NotEqual(t, "node2", slot, "negative weight validator should not appear")
		require.Contains(t, []string{"node1", "node3"}, slot)
	}
}

func TestGetSlots_MixedWeights(t *testing.T) {
	weights := map[string]int64{
		"valid1":   100,
		"negative": -100,
		"zero":     0,
		"valid2":   200,
	}
	appHash := "hash"
	participant := "participant"
	nSlots := 100

	slots := GetSlots(appHash, participant, weights, nSlots)
	require.NotNil(t, slots)

	counts := make(map[string]int)
	for _, slot := range slots {
		counts[slot]++
	}

	// Only valid1 and valid2 should appear
	require.Equal(t, 0, counts["negative"])
	require.Equal(t, 0, counts["zero"])
	require.Greater(t, counts["valid1"], 0)
	require.Greater(t, counts["valid2"], 0)
}

func TestGetSlots_AllNegativeOrZeroWeights(t *testing.T) {
	weights := map[string]int64{
		"node1": -100,
		"node2": 0,
		"node3": -50,
	}
	slots := GetSlots("hash", "participant", weights, 10)
	require.Nil(t, slots)
}

func TestGetSlots_MoreSlotsThanValidators(t *testing.T) {
	weights := map[string]int64{
		"node1": 100,
		"node2": 200,
	}
	appHash := "hash"
	participant := "participant"
	nSlots := 1000 // Many more slots than validators

	slots := GetSlots(appHash, participant, weights, nSlots)
	require.Len(t, slots, nSlots)

	// Both validators should appear multiple times
	counts := make(map[string]int)
	for _, slot := range slots {
		counts[slot]++
	}
	require.Greater(t, counts["node1"], 100, "node1 should appear many times")
	require.Greater(t, counts["node2"], 100, "node2 should appear many times")
}

func TestGetSlots_LargeWeightDisparity(t *testing.T) {
	weights := map[string]int64{
		"whale": 9900,
		"small": 100,
	}
	appHash := "hash"
	participant := "participant"
	nSlots := 1000

	slots := GetSlots(appHash, participant, weights, nSlots)

	counts := make(map[string]int)
	for _, slot := range slots {
		counts[slot]++
	}

	// whale should have ~99% of slots, small ~1%
	total := float64(nSlots)
	require.InDelta(t, 0.99, float64(counts["whale"])/total, 0.05)
	require.InDelta(t, 0.01, float64(counts["small"])/total, 0.05)
}

func TestGetSlots_DifferentAppHash(t *testing.T) {
	weights := map[string]int64{
		"node1": 100,
		"node2": 200,
		"node3": 300,
	}
	participant := "participant"
	nSlots := 64

	slots1 := GetSlots("appHash1", participant, weights, nSlots)
	slots2 := GetSlots("appHash2", participant, weights, nSlots)

	require.NotEqual(t, slots1, slots2, "different appHash should produce different slots")
}

func TestGetSlot_NegativeWeightsSkipped(t *testing.T) {
	weights := map[string]int64{
		"node1": 100,
		"node2": -50,
		"node3": 200,
	}

	// Run multiple slot indices to verify negative weight is never selected
	for i := 0; i < 100; i++ {
		slot := GetSlot("hash", "participant", weights, i)
		require.NotEqual(t, "node2", slot, "negative weight validator should not be returned")
	}
}

func TestGetSlot_AllNegativeOrZeroWeights(t *testing.T) {
	weights := map[string]int64{
		"node1": -100,
		"node2": 0,
	}
	slot := GetSlot("hash", "participant", weights, 0)
	require.Empty(t, slot)
}

func TestGetSlots_LargeNSlots(t *testing.T) {
	weights := map[string]int64{
		"node1": 100,
		"node2": 200,
		"node3": 300,
	}
	appHash := "hash"
	participant := "participant"
	nSlots := 10000

	slots := GetSlots(appHash, participant, weights, nSlots)
	require.Len(t, slots, nSlots)

	// Verify distribution is still proportional
	counts := make(map[string]int)
	for _, slot := range slots {
		counts[slot]++
	}
	total := float64(nSlots)
	require.InDelta(t, 100.0/600.0, float64(counts["node1"])/total, 0.05)
	require.InDelta(t, 200.0/600.0, float64(counts["node2"])/total, 0.05)
	require.InDelta(t, 300.0/600.0, float64(counts["node3"])/total, 0.05)
}

func TestGetSlots_OrderIndependentOfMapIteration(t *testing.T) {
	// Test that results are deterministic regardless of Go's map iteration order
	// by creating the same weights map multiple times and verifying same results
	appHash := "hash"
	participant := "participant"
	nSlots := 64

	var firstResult []string
	for iteration := 0; iteration < 10; iteration++ {
		// Create fresh map each time (Go may use different iteration order)
		weights := map[string]int64{
			"zebra":  100,
			"alpha":  200,
			"middle": 300,
			"beta":   150,
			"zulu":   250,
		}
		slots := GetSlots(appHash, participant, weights, nSlots)

		if firstResult == nil {
			firstResult = slots
		} else {
			require.Equal(t, firstResult, slots, "results should be deterministic across map iterations")
		}
	}
}
