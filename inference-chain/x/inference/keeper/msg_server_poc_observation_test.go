package keeper_test

import (
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func setupObservationTest(t *testing.T) (keeper.Keeper, types.MsgServer, sdk.Context) {
	k, ctx := keepertest.InferenceKeeper(t)
	ms := keeper.NewMsgServerImpl(k)
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	params, err := k.GetParams(sdkCtx)
	require.NoError(t, err)
	params.PocParams.PocV2Enabled = true
	require.NoError(t, k.SetParams(sdkCtx, params))

	epochParams := params.EpochParams
	pocStart := int64(100)
	epoch := &types.Epoch{Index: 2, PocStartBlockHeight: pocStart}
	k.SetEpoch(sdkCtx, epoch)
	_ = k.SetEffectiveEpochIndex(sdkCtx, 1)

	_ = epochParams
	return k, ms, ctx
}

func TestSubmitPoCObservation_Success(t *testing.T) {
	k, ms, ctx := setupObservationTest(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	params, _ := k.GetParams(sdkCtx)
	epochCtx := types.NewEpochContext(
		types.Epoch{Index: 2, PocStartBlockHeight: 100},
		*params.EpochParams,
	)
	exchangeWindow := epochCtx.PoCExchangeWindow()
	sdkCtx = sdkCtx.WithBlockHeight(exchangeWindow.Start)

	msg := &types.MsgSubmitPoCObservation{
		Creator:                  "gonka1hgt9lxxxwpsnc3yn2nheqqy9a8vlcjwvgzpve2",
		PocStageStartBlockHeight: 100,
		Arrivals: []*types.PoCObservationArrival{
			{Participant: "gonka1pda35dczayfhy2udffky7wzset9tpkpatzaksd", Count: 5},
			{Participant: "gonka13779rkgy6ke7cdj8f097pdvx34uvrlcqq8nq2w", Count: 3},
		},
	}

	resp, err := ms.SubmitPoCObservation(sdkCtx, msg)
	require.NoError(t, err)
	require.NotNil(t, resp)

	addr, _ := sdk.AccAddressFromBech32(msg.Creator)
	obs, err := k.PoCObservationsMap.Get(sdkCtx, collections.Join(int64(100), addr))
	require.NoError(t, err)
	require.Equal(t, msg.Creator, obs.ValidatorAddress)
	require.Len(t, obs.Arrivals, 2)
	require.Equal(t, uint32(5), obs.Arrivals[0].Count)
}

func TestSubmitPoCObservation_V2Disabled(t *testing.T) {
	k, ms, ctx := setupObservationTest(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	params, _ := k.GetParams(sdkCtx)
	params.PocParams.PocV2Enabled = false
	k.SetParams(sdkCtx, params)

	sdkCtx = sdkCtx.WithBlockHeight(101)

	msg := &types.MsgSubmitPoCObservation{
		Creator:                  "gonka1hgt9lxxxwpsnc3yn2nheqqy9a8vlcjwvgzpve2",
		PocStageStartBlockHeight: 100,
		Arrivals: []*types.PoCObservationArrival{
			{Participant: "gonka1pda35dczayfhy2udffky7wzset9tpkpatzaksd", Count: 5},
		},
	}

	_, err := ms.SubmitPoCObservation(sdkCtx, msg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "V2 disabled")
}

func TestSubmitPoCObservation_EmptyArrivals(t *testing.T) {
	_, ms, ctx := setupObservationTest(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(101)

	msg := &types.MsgSubmitPoCObservation{
		Creator:                  "gonka1hgt9lxxxwpsnc3yn2nheqqy9a8vlcjwvgzpve2",
		PocStageStartBlockHeight: 100,
		Arrivals:                 []*types.PoCObservationArrival{},
	}

	_, err := ms.SubmitPoCObservation(sdkCtx, msg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "arrivals must not be empty")
}

func TestSubmitPoCObservation_WrongStartBlockHeight(t *testing.T) {
	k, ms, ctx := setupObservationTest(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	params, _ := k.GetParams(sdkCtx)
	epochCtx := types.NewEpochContext(
		types.Epoch{Index: 2, PocStartBlockHeight: 100},
		*params.EpochParams,
	)
	exchangeWindow := epochCtx.PoCExchangeWindow()
	sdkCtx = sdkCtx.WithBlockHeight(exchangeWindow.Start)
	_ = k

	msg := &types.MsgSubmitPoCObservation{
		Creator:                  "gonka1hgt9lxxxwpsnc3yn2nheqqy9a8vlcjwvgzpve2",
		PocStageStartBlockHeight: 999,
		Arrivals: []*types.PoCObservationArrival{
			{Participant: "gonka1pda35dczayfhy2udffky7wzset9tpkpatzaksd", Count: 5},
		},
	}

	_, err := ms.SubmitPoCObservation(sdkCtx, msg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "start block height")
}

func TestSubmitPoCObservation_OutsideExchangeWindow(t *testing.T) {
	k, ms, ctx := setupObservationTest(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	params, _ := k.GetParams(sdkCtx)
	epochCtx := types.NewEpochContext(
		types.Epoch{Index: 2, PocStartBlockHeight: 100},
		*params.EpochParams,
	)
	exchangeWindow := epochCtx.PoCExchangeWindow()
	sdkCtx = sdkCtx.WithBlockHeight(exchangeWindow.End + 10)
	_ = k

	msg := &types.MsgSubmitPoCObservation{
		Creator:                  "gonka1hgt9lxxxwpsnc3yn2nheqqy9a8vlcjwvgzpve2",
		PocStageStartBlockHeight: 100,
		Arrivals: []*types.PoCObservationArrival{
			{Participant: "gonka1pda35dczayfhy2udffky7wzset9tpkpatzaksd", Count: 5},
		},
	}

	_, err := ms.SubmitPoCObservation(sdkCtx, msg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exchange window closed")
}

func TestSubmitPoCObservation_DuplicateSubmission(t *testing.T) {
	k, ms, ctx := setupObservationTest(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	params, _ := k.GetParams(sdkCtx)
	epochCtx := types.NewEpochContext(
		types.Epoch{Index: 2, PocStartBlockHeight: 100},
		*params.EpochParams,
	)
	exchangeWindow := epochCtx.PoCExchangeWindow()
	sdkCtx = sdkCtx.WithBlockHeight(exchangeWindow.Start)
	_ = k

	msg := &types.MsgSubmitPoCObservation{
		Creator:                  "gonka1hgt9lxxxwpsnc3yn2nheqqy9a8vlcjwvgzpve2",
		PocStageStartBlockHeight: 100,
		Arrivals: []*types.PoCObservationArrival{
			{Participant: "gonka1pda35dczayfhy2udffky7wzset9tpkpatzaksd", Count: 5},
		},
	}

	_, err := ms.SubmitPoCObservation(sdkCtx, msg)
	require.NoError(t, err)

	_, err = ms.SubmitPoCObservation(sdkCtx, msg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already submitted")
}
