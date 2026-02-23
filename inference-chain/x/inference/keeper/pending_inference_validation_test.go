package keeper_test

import (
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/stretchr/testify/require"
)

func TestPendingInferenceValidationQueue_GetFinishedInferenceIDsForHeight_Deterministic(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	blockHeight := int64(100)

	require.NoError(t, keeper.SetPendingInferenceValidation(ctx, blockHeight, "c"))
	require.NoError(t, keeper.SetPendingInferenceValidation(ctx, blockHeight, "a"))
	require.NoError(t, keeper.SetPendingInferenceValidation(ctx, blockHeight, "b"))
	require.NoError(t, keeper.SetPendingInferenceValidation(ctx, blockHeight+1, "other-height"))

	got := keeper.GetFinishedInferenceIDsForHeight(ctx, blockHeight)
	require.Equal(t, []string{"a", "b", "c"}, got)
}

func TestPendingInferenceValidationQueue_Remove(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	blockHeight := int64(200)

	require.NoError(t, keeper.SetPendingInferenceValidation(ctx, blockHeight, "a"))
	require.NoError(t, keeper.SetPendingInferenceValidation(ctx, blockHeight, "b"))

	keeper.RemovePendingInferenceValidation(ctx, blockHeight, "a")
	require.Equal(t, []string{"b"}, keeper.GetFinishedInferenceIDsForHeight(ctx, blockHeight))

	keeper.RemovePendingInferenceValidation(ctx, blockHeight, "missing")
	require.Equal(t, []string{"b"}, keeper.GetFinishedInferenceIDsForHeight(ctx, blockHeight))
}
