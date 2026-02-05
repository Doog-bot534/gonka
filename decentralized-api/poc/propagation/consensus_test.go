package propagation

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCalculateConsensus_EmptyObservations(t *testing.T) {
	results := CalculateConsensus(nil, nil, 1000)
	require.Empty(t, results)

	results = CalculateConsensus([]FirstArrivalObservation{}, []BundleHeader{}, 1000)
	require.Empty(t, results)
}

func TestCalculateConsensus_BasicMajority(t *testing.T) {
	participant := "participant1"
	pocHeight := int64(100)

	bundles := []BundleHeader{
		{Participant: participant, PocHeight: pocHeight, Count: 50},
	}

	observations := []FirstArrivalObservation{
		{ValidatorAddress: "val1", PocHeight: pocHeight, Arrivals: map[string]ArrivalInfo{
			participant: {Time: 100, Count: 50},
		}},
		{ValidatorAddress: "val2", PocHeight: pocHeight, Arrivals: map[string]ArrivalInfo{
			participant: {Time: 100, Count: 50},
		}},
		{ValidatorAddress: "val3", PocHeight: pocHeight, Arrivals: map[string]ArrivalInfo{
			participant: {Time: 100, Count: 50},
		}},
	}

	results := CalculateConsensus(observations, bundles, 1000)

	require.Len(t, results, 1)
	require.Equal(t, int64(50), results[participant].AgreedCount)
	require.Equal(t, 3, results[participant].TotalValidators)
	require.Equal(t, 3, results[participant].AgreeingCount)
}

func TestCalculateConsensus_NoMajority(t *testing.T) {
	participant := "participant1"
	pocHeight := int64(100)

	bundles := []BundleHeader{
		{Participant: participant, PocHeight: pocHeight, Count: 50},
	}

	observations := []FirstArrivalObservation{
		{ValidatorAddress: "val1", PocHeight: pocHeight, Arrivals: map[string]ArrivalInfo{
			participant: {Time: 100, Count: 50},
		}},
		{ValidatorAddress: "val2", PocHeight: pocHeight, Arrivals: map[string]ArrivalInfo{}},
		{ValidatorAddress: "val3", PocHeight: pocHeight, Arrivals: map[string]ArrivalInfo{}},
	}

	results := CalculateConsensus(observations, bundles, 1000)

	require.Len(t, results, 1)
	require.Equal(t, int64(0), results[participant].AgreedCount)
	require.Equal(t, 0, results[participant].AgreeingCount)
}

func TestCalculateConsensus_DeadlineFiltering(t *testing.T) {
	participant := "participant1"
	pocHeight := int64(100)
	deadline := int64(500)

	bundles := []BundleHeader{
		{Participant: participant, PocHeight: pocHeight, Count: 50},
	}

	observations := []FirstArrivalObservation{
		{ValidatorAddress: "val1", PocHeight: pocHeight, Arrivals: map[string]ArrivalInfo{
			participant: {Time: 400, Count: 50},
		}},
		{ValidatorAddress: "val2", PocHeight: pocHeight, Arrivals: map[string]ArrivalInfo{
			participant: {Time: 600, Count: 50},
		}},
		{ValidatorAddress: "val3", PocHeight: pocHeight, Arrivals: map[string]ArrivalInfo{
			participant: {Time: 700, Count: 50},
		}},
	}

	results := CalculateConsensus(observations, bundles, deadline)

	require.Len(t, results, 1)
	require.Equal(t, int64(0), results[participant].AgreedCount)
	require.Equal(t, 0, results[participant].AgreeingCount)
}

func TestCalculateConsensus_HigherCountNeedsMajority(t *testing.T) {
	participant := "participant1"
	pocHeight := int64(100)

	bundles := []BundleHeader{
		{Participant: participant, PocHeight: pocHeight, Count: 50},
		{Participant: participant, PocHeight: pocHeight, Count: 100},
	}

	observations := []FirstArrivalObservation{
		{ValidatorAddress: "val1", PocHeight: pocHeight, Arrivals: map[string]ArrivalInfo{
			participant: {Time: 100, Count: 100},
		}},
		{ValidatorAddress: "val2", PocHeight: pocHeight, Arrivals: map[string]ArrivalInfo{
			participant: {Time: 100, Count: 100},
		}},
		{ValidatorAddress: "val3", PocHeight: pocHeight, Arrivals: map[string]ArrivalInfo{
			participant: {Time: 100, Count: 50},
		}},
		{ValidatorAddress: "val4", PocHeight: pocHeight, Arrivals: map[string]ArrivalInfo{
			participant: {Time: 100, Count: 50},
		}},
		{ValidatorAddress: "val5", PocHeight: pocHeight, Arrivals: map[string]ArrivalInfo{
			participant: {Time: 100, Count: 50},
		}},
	}

	results := CalculateConsensus(observations, bundles, 1000)

	require.Len(t, results, 1)
	require.Equal(t, int64(50), results[participant].AgreedCount)
	require.Equal(t, 5, results[participant].AgreeingCount)
}

func TestCalculateConsensus_HigherCountWithMajority(t *testing.T) {
	participant := "participant1"
	pocHeight := int64(100)

	bundles := []BundleHeader{
		{Participant: participant, PocHeight: pocHeight, Count: 50},
		{Participant: participant, PocHeight: pocHeight, Count: 100},
	}

	observations := []FirstArrivalObservation{
		{ValidatorAddress: "val1", PocHeight: pocHeight, Arrivals: map[string]ArrivalInfo{
			participant: {Time: 100, Count: 100},
		}},
		{ValidatorAddress: "val2", PocHeight: pocHeight, Arrivals: map[string]ArrivalInfo{
			participant: {Time: 100, Count: 100},
		}},
		{ValidatorAddress: "val3", PocHeight: pocHeight, Arrivals: map[string]ArrivalInfo{
			participant: {Time: 100, Count: 100},
		}},
	}

	results := CalculateConsensus(observations, bundles, 1000)

	require.Len(t, results, 1)
	require.Equal(t, int64(100), results[participant].AgreedCount)
	require.Equal(t, 3, results[participant].AgreeingCount)
}

func TestCalculateConsensus_MultipleParticipants(t *testing.T) {
	pocHeight := int64(100)

	bundles := []BundleHeader{
		{Participant: "p1", PocHeight: pocHeight, Count: 50},
		{Participant: "p2", PocHeight: pocHeight, Count: 75},
		{Participant: "p3", PocHeight: pocHeight, Count: 25},
	}

	observations := []FirstArrivalObservation{
		{ValidatorAddress: "val1", PocHeight: pocHeight, Arrivals: map[string]ArrivalInfo{
			"p1": {Time: 100, Count: 50},
			"p2": {Time: 100, Count: 75},
			"p3": {Time: 100, Count: 25},
		}},
		{ValidatorAddress: "val2", PocHeight: pocHeight, Arrivals: map[string]ArrivalInfo{
			"p1": {Time: 100, Count: 50},
			"p2": {Time: 100, Count: 75},
		}},
		{ValidatorAddress: "val3", PocHeight: pocHeight, Arrivals: map[string]ArrivalInfo{
			"p1": {Time: 100, Count: 50},
			"p3": {Time: 100, Count: 25},
		}},
	}

	results := CalculateConsensus(observations, bundles, 1000)

	require.Len(t, results, 3)
	require.Equal(t, int64(50), results["p1"].AgreedCount)
	require.Equal(t, int64(75), results["p2"].AgreedCount)
	require.Equal(t, int64(25), results["p3"].AgreedCount)
}

func TestCalculateConsensus_CountThreshold(t *testing.T) {
	participant := "participant1"
	pocHeight := int64(100)

	bundles := []BundleHeader{
		{Participant: participant, PocHeight: pocHeight, Count: 100},
	}

	observations := []FirstArrivalObservation{
		{ValidatorAddress: "val1", PocHeight: pocHeight, Arrivals: map[string]ArrivalInfo{
			participant: {Time: 100, Count: 100},
		}},
		{ValidatorAddress: "val2", PocHeight: pocHeight, Arrivals: map[string]ArrivalInfo{
			participant: {Time: 100, Count: 50},
		}},
		{ValidatorAddress: "val3", PocHeight: pocHeight, Arrivals: map[string]ArrivalInfo{
			participant: {Time: 100, Count: 75},
		}},
	}

	results := CalculateConsensus(observations, bundles, 1000)

	require.Equal(t, int64(0), results[participant].AgreedCount)
}

func TestCalculateConsensus_ExactDeadline(t *testing.T) {
	participant := "participant1"
	pocHeight := int64(100)
	deadline := int64(500)

	bundles := []BundleHeader{
		{Participant: participant, PocHeight: pocHeight, Count: 50},
	}

	observations := []FirstArrivalObservation{
		{ValidatorAddress: "val1", PocHeight: pocHeight, Arrivals: map[string]ArrivalInfo{
			participant: {Time: 500, Count: 50},
		}},
		{ValidatorAddress: "val2", PocHeight: pocHeight, Arrivals: map[string]ArrivalInfo{
			participant: {Time: 500, Count: 50},
		}},
		{ValidatorAddress: "val3", PocHeight: pocHeight, Arrivals: map[string]ArrivalInfo{
			participant: {Time: 501, Count: 50},
		}},
	}

	results := CalculateConsensus(observations, bundles, deadline)

	require.Equal(t, int64(50), results[participant].AgreedCount)
	require.Equal(t, 2, results[participant].AgreeingCount)
}

func TestCalculateConsensus_DeadlineFromObservations(t *testing.T) {
	participant := "participant1"
	pocHeight := int64(100)

	bundles := []BundleHeader{
		{Participant: participant, PocHeight: pocHeight, Count: 50},
	}

	observations := []FirstArrivalObservation{
		{ValidatorAddress: "val1", PocHeight: pocHeight, Timestamp: 1000, Arrivals: map[string]ArrivalInfo{
			participant: {Time: 900, Count: 50},
		}},
		{ValidatorAddress: "val2", PocHeight: pocHeight, Timestamp: 1100, Arrivals: map[string]ArrivalInfo{
			participant: {Time: 950, Count: 50},
		}},
		{ValidatorAddress: "val3", PocHeight: pocHeight, Timestamp: 1050, Arrivals: map[string]ArrivalInfo{
			participant: {Time: 1001, Count: 50},
		}},
	}

	deadline := observations[0].Timestamp
	for _, obs := range observations[1:] {
		if obs.Timestamp < deadline {
			deadline = obs.Timestamp
		}
	}
	require.Equal(t, int64(1000), deadline)

	results := CalculateConsensus(observations, bundles, deadline)

	require.Equal(t, int64(50), results[participant].AgreedCount)
	require.Equal(t, 2, results[participant].AgreeingCount)
}
