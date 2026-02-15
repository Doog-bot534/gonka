package keeper_test

import (
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestPoCObservationsQuery_Empty(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	resp, err := k.PoCObservations(sdkCtx, &types.QueryPoCObservationsRequest{
		PocStageStartBlockHeight: 100,
	})
	require.NoError(t, err)
	require.Empty(t, resp.Observations)
}

func TestPoCObservationsQuery_ReturnsStored(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	addr1 := sdk.MustAccAddressFromBech32("gonka1hgt9lxxxwpsnc3yn2nheqqy9a8vlcjwvgzpve2")
	addr2 := sdk.MustAccAddressFromBech32("gonka1pda35dczayfhy2udffky7wzset9tpkpatzaksd")

	obs1 := types.PoCObservation{
		ValidatorAddress:         addr1.String(),
		PocStageStartBlockHeight: 100,
		Arrivals: []*types.PoCObservationArrival{
			{Participant: addr2.String(), Count: 5},
		},
		BlockHeight: 101,
	}
	obs2 := types.PoCObservation{
		ValidatorAddress:         addr2.String(),
		PocStageStartBlockHeight: 100,
		Arrivals: []*types.PoCObservationArrival{
			{Participant: addr1.String(), Count: 3},
		},
		BlockHeight: 102,
	}

	require.NoError(t, k.PoCObservationsMap.Set(sdkCtx, collections.Join(int64(100), addr1), obs1))
	require.NoError(t, k.PoCObservationsMap.Set(sdkCtx, collections.Join(int64(100), addr2), obs2))

	resp, err := k.PoCObservations(sdkCtx, &types.QueryPoCObservationsRequest{
		PocStageStartBlockHeight: 100,
	})
	require.NoError(t, err)
	require.Len(t, resp.Observations, 2)
}

func TestPoCObservationsQuery_FiltersByHeight(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	addr1 := sdk.MustAccAddressFromBech32("gonka1hgt9lxxxwpsnc3yn2nheqqy9a8vlcjwvgzpve2")

	obs := types.PoCObservation{
		ValidatorAddress:         addr1.String(),
		PocStageStartBlockHeight: 100,
		Arrivals: []*types.PoCObservationArrival{
			{Participant: "gonka1pda35dczayfhy2udffky7wzset9tpkpatzaksd", Count: 5},
		},
		BlockHeight: 101,
	}
	require.NoError(t, k.PoCObservationsMap.Set(sdkCtx, collections.Join(int64(100), addr1), obs))

	resp, err := k.PoCObservations(sdkCtx, &types.QueryPoCObservationsRequest{
		PocStageStartBlockHeight: 200,
	})
	require.NoError(t, err)
	require.Empty(t, resp.Observations)
}

func TestPoCConsensusQuery_Empty(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	resp, err := k.PoCConsensus(sdkCtx, &types.QueryPoCConsensusRequest{
		PocStageStartBlockHeight: 100,
	})
	require.NoError(t, err)
	require.Empty(t, resp.Entries)
}

func TestPoCConsensusQuery_MajorityAgreement(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	addr1 := sdk.MustAccAddressFromBech32("gonka1hgt9lxxxwpsnc3yn2nheqqy9a8vlcjwvgzpve2")
	addr2 := sdk.MustAccAddressFromBech32("gonka1pda35dczayfhy2udffky7wzset9tpkpatzaksd")
	addr3 := sdk.MustAccAddressFromBech32("gonka13779rkgy6ke7cdj8f097pdvx34uvrlcqq8nq2w")

	participantA := "gonka1xxczezuqw0pe67xag5s3zgyrzh4w3zyermjgs9"

	obs1 := types.PoCObservation{
		ValidatorAddress:         addr1.String(),
		PocStageStartBlockHeight: 100,
		Arrivals: []*types.PoCObservationArrival{
			{Participant: participantA, Count: 10},
		},
	}
	obs2 := types.PoCObservation{
		ValidatorAddress:         addr2.String(),
		PocStageStartBlockHeight: 100,
		Arrivals: []*types.PoCObservationArrival{
			{Participant: participantA, Count: 10},
		},
	}
	obs3 := types.PoCObservation{
		ValidatorAddress:         addr3.String(),
		PocStageStartBlockHeight: 100,
		Arrivals: []*types.PoCObservationArrival{
			{Participant: participantA, Count: 5},
		},
	}

	require.NoError(t, k.PoCObservationsMap.Set(sdkCtx, collections.Join(int64(100), addr1), obs1))
	require.NoError(t, k.PoCObservationsMap.Set(sdkCtx, collections.Join(int64(100), addr2), obs2))
	require.NoError(t, k.PoCObservationsMap.Set(sdkCtx, collections.Join(int64(100), addr3), obs3))

	resp, err := k.PoCConsensus(sdkCtx, &types.QueryPoCConsensusRequest{
		PocStageStartBlockHeight: 100,
	})
	require.NoError(t, err)
	require.Len(t, resp.Entries, 1)

	entry := resp.Entries[0]
	require.Equal(t, participantA, entry.Participant)
	require.Equal(t, uint32(10), entry.AgreedCount)
	require.Equal(t, int32(3), entry.TotalValidators)
	require.Equal(t, int32(2), entry.AgreeingCount)
}

func TestPoCConsensusQuery_SplitVote(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	addr1 := sdk.MustAccAddressFromBech32("gonka1hgt9lxxxwpsnc3yn2nheqqy9a8vlcjwvgzpve2")
	addr2 := sdk.MustAccAddressFromBech32("gonka1pda35dczayfhy2udffky7wzset9tpkpatzaksd")
	addr3 := sdk.MustAccAddressFromBech32("gonka13779rkgy6ke7cdj8f097pdvx34uvrlcqq8nq2w")

	participantA := "gonka1xxczezuqw0pe67xag5s3zgyrzh4w3zyermjgs9"

	obs1 := types.PoCObservation{
		ValidatorAddress:         addr1.String(),
		PocStageStartBlockHeight: 100,
		Arrivals: []*types.PoCObservationArrival{
			{Participant: participantA, Count: 20},
		},
	}
	obs2 := types.PoCObservation{
		ValidatorAddress:         addr2.String(),
		PocStageStartBlockHeight: 100,
		Arrivals: []*types.PoCObservationArrival{
			{Participant: participantA, Count: 10},
		},
	}
	obs3 := types.PoCObservation{
		ValidatorAddress:         addr3.String(),
		PocStageStartBlockHeight: 100,
		Arrivals: []*types.PoCObservationArrival{
			{Participant: participantA, Count: 5},
		},
	}

	require.NoError(t, k.PoCObservationsMap.Set(sdkCtx, collections.Join(int64(100), addr1), obs1))
	require.NoError(t, k.PoCObservationsMap.Set(sdkCtx, collections.Join(int64(100), addr2), obs2))
	require.NoError(t, k.PoCObservationsMap.Set(sdkCtx, collections.Join(int64(100), addr3), obs3))

	resp, err := k.PoCConsensus(sdkCtx, &types.QueryPoCConsensusRequest{
		PocStageStartBlockHeight: 100,
	})
	require.NoError(t, err)
	require.Len(t, resp.Entries, 1)

	entry := resp.Entries[0]
	require.Equal(t, participantA, entry.Participant)
	require.Equal(t, uint32(10), entry.AgreedCount)
	require.Equal(t, int32(2), entry.AgreeingCount)
}

func TestPoCConsensusQuery_MultipleParticipants(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	addr1 := sdk.MustAccAddressFromBech32("gonka1hgt9lxxxwpsnc3yn2nheqqy9a8vlcjwvgzpve2")
	addr2 := sdk.MustAccAddressFromBech32("gonka1pda35dczayfhy2udffky7wzset9tpkpatzaksd")

	participantA := "gonka13779rkgy6ke7cdj8f097pdvx34uvrlcqq8nq2w"
	participantB := "gonka1xxczezuqw0pe67xag5s3zgyrzh4w3zyermjgs9"

	obs1 := types.PoCObservation{
		ValidatorAddress:         addr1.String(),
		PocStageStartBlockHeight: 100,
		Arrivals: []*types.PoCObservationArrival{
			{Participant: participantA, Count: 10},
			{Participant: participantB, Count: 7},
		},
	}
	obs2 := types.PoCObservation{
		ValidatorAddress:         addr2.String(),
		PocStageStartBlockHeight: 100,
		Arrivals: []*types.PoCObservationArrival{
			{Participant: participantA, Count: 10},
			{Participant: participantB, Count: 7},
		},
	}

	require.NoError(t, k.PoCObservationsMap.Set(sdkCtx, collections.Join(int64(100), addr1), obs1))
	require.NoError(t, k.PoCObservationsMap.Set(sdkCtx, collections.Join(int64(100), addr2), obs2))

	resp, err := k.PoCConsensus(sdkCtx, &types.QueryPoCConsensusRequest{
		PocStageStartBlockHeight: 100,
	})
	require.NoError(t, err)
	require.Len(t, resp.Entries, 2)

	entryMap := make(map[string]*types.PoCConsensusEntry)
	for _, e := range resp.Entries {
		entryMap[e.Participant] = e
	}

	require.Equal(t, uint32(10), entryMap[participantA].AgreedCount)
	require.Equal(t, uint32(7), entryMap[participantB].AgreedCount)
}

func TestPoCConsensusQuery_SelfOnlyObservations(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	addr1 := sdk.MustAccAddressFromBech32("gonka1hgt9lxxxwpsnc3yn2nheqqy9a8vlcjwvgzpve2")
	addr2 := sdk.MustAccAddressFromBech32("gonka1pda35dczayfhy2udffky7wzset9tpkpatzaksd")
	addr3 := sdk.MustAccAddressFromBech32("gonka13779rkgy6ke7cdj8f097pdvx34uvrlcqq8nq2w")

	obs1 := types.PoCObservation{
		ValidatorAddress:         addr1.String(),
		PocStageStartBlockHeight: 100,
		Arrivals: []*types.PoCObservationArrival{
			{Participant: addr1.String(), Count: 50},
		},
	}
	obs2 := types.PoCObservation{
		ValidatorAddress:         addr2.String(),
		PocStageStartBlockHeight: 100,
		Arrivals: []*types.PoCObservationArrival{
			{Participant: addr2.String(), Count: 80},
		},
	}
	obs3 := types.PoCObservation{
		ValidatorAddress:         addr3.String(),
		PocStageStartBlockHeight: 100,
		Arrivals: []*types.PoCObservationArrival{
			{Participant: addr3.String(), Count: 120},
		},
	}

	require.NoError(t, k.PoCObservationsMap.Set(sdkCtx, collections.Join(int64(100), addr1), obs1))
	require.NoError(t, k.PoCObservationsMap.Set(sdkCtx, collections.Join(int64(100), addr2), obs2))
	require.NoError(t, k.PoCObservationsMap.Set(sdkCtx, collections.Join(int64(100), addr3), obs3))

	resp, err := k.PoCConsensus(sdkCtx, &types.QueryPoCConsensusRequest{
		PocStageStartBlockHeight: 100,
	})
	require.NoError(t, err)
	require.Len(t, resp.Entries, 3)

	entryMap := make(map[string]*types.PoCConsensusEntry)
	for _, e := range resp.Entries {
		entryMap[e.Participant] = e
	}

	require.Equal(t, uint32(50), entryMap[addr1.String()].AgreedCount)
	require.Equal(t, int32(1), entryMap[addr1.String()].TotalValidators)
	require.Equal(t, int32(1), entryMap[addr1.String()].AgreeingCount)

	require.Equal(t, uint32(80), entryMap[addr2.String()].AgreedCount)
	require.Equal(t, int32(1), entryMap[addr2.String()].TotalValidators)

	require.Equal(t, uint32(120), entryMap[addr3.String()].AgreedCount)
	require.Equal(t, int32(1), entryMap[addr3.String()].TotalValidators)
}

func TestPoCConsensusQuery_NilRequest(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	_, err := k.PoCConsensus(sdkCtx, nil)
	require.Error(t, err)

	_, err = k.PoCObservations(sdkCtx, nil)
	require.Error(t, err)
}
