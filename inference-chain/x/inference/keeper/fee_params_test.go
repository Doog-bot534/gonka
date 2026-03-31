package keeper_test

import (
	"testing"

	testkeeper "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestFeeParams_DefaultsFromParams(t *testing.T) {
	k, ctx := testkeeper.InferenceKeeper(t)

	// DefaultParams() includes FeeParams with production defaults.
	fp := k.GetFeeParams(ctx)
	require.NotNil(t, fp)
	require.Equal(t, types.DefaultFeeParams().MinGasPriceNgonka, fp.MinGasPriceNgonka)
	require.Equal(t, types.DefaultFeeParams().BaseValidationGas, fp.BaseValidationGas)
	require.Equal(t, types.DefaultFeeParams().GasPerPocCount, fp.GasPerPocCount)
}

func TestFeeParams_SetAndGet(t *testing.T) {
	k, ctx := testkeeper.InferenceKeeper(t)

	custom := &types.FeeParams{
		MinGasPriceNgonka: 42,
		BaseValidationGas: 1_000_000,
		GasPerPocCount:    200,
	}
	require.NoError(t, k.SetFeeParams(ctx, custom))

	fp := k.GetFeeParams(ctx)
	require.Equal(t, custom.MinGasPriceNgonka, fp.MinGasPriceNgonka)
	require.Equal(t, custom.BaseValidationGas, fp.BaseValidationGas)
	require.Equal(t, custom.GasPerPocCount, fp.GasPerPocCount)
}

func TestFeeParams_ZeroDisablesFees(t *testing.T) {
	k, ctx := testkeeper.InferenceKeeper(t)

	zero := &types.FeeParams{
		MinGasPriceNgonka: 0,
		BaseValidationGas: 0,
		GasPerPocCount:    0,
	}
	require.NoError(t, k.SetFeeParams(ctx, zero))

	fp := k.GetFeeParams(ctx)
	require.Equal(t, uint64(0), fp.MinGasPriceNgonka)
}
