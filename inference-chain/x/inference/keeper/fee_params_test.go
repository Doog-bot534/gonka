package keeper_test

import (
	"testing"

	testkeeper "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestFeeParams_NilAtGenesis(t *testing.T) {
	k, ctx := testkeeper.InferenceKeeper(t)

	// FeeParams are not set at genesis (enabled via upgrade handler).
	// GetFeeParams returns zero values, meaning no fee enforcement.
	fp := k.GetFeeParams(ctx)
	require.NotNil(t, fp)
	require.Equal(t, uint64(0), fp.MinGasPriceNgonka)
	require.Equal(t, uint64(0), fp.BaseValidationGas)
	require.Equal(t, uint64(0), fp.GasPerPocCount)
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

	// First enable fees
	require.NoError(t, k.SetFeeParams(ctx, types.DefaultFeeParams()))
	fp := k.GetFeeParams(ctx)
	require.Equal(t, uint64(10), fp.MinGasPriceNgonka)

	// Then disable by setting to zero
	zero := &types.FeeParams{
		MinGasPriceNgonka: 0,
		BaseValidationGas: 0,
		GasPerPocCount:    0,
	}
	require.NoError(t, k.SetFeeParams(ctx, zero))

	fp = k.GetFeeParams(ctx)
	require.Equal(t, uint64(0), fp.MinGasPriceNgonka)
}
