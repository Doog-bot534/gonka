package keeper_test

import (
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/stretchr/testify/require"
)

func TestPendingInferenceValidationQueue_ListFinishedInferenceIDs_FIFO(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)

	require.NoError(t, keeper.EnqueueFinishedInference(ctx, "c"))
	require.NoError(t, keeper.EnqueueFinishedInference(ctx, "a"))
	require.NoError(t, keeper.EnqueueFinishedInference(ctx, "b"))
	require.NoError(t, keeper.EnqueueFinishedInference(ctx, "other"))

	got := keeper.ListFinishedInferenceIDs(ctx)
	require.Equal(t, []string{"c", "a", "b", "other"}, got)
}
