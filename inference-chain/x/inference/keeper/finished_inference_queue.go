package keeper

import (
	"context"

	"cosmossdk.io/collections"
)

// FinishedInferenceQueue stores completed inference IDs by block height.
// We intentionally process this queue in EndBlock to keep Start/Finish tx execution lightweight
// and defer expensive epoch/model reads used for InferenceValidationDetails construction.
func (k Keeper) EnqueueFinishedInference(ctx context.Context, blockHeight int64, inferenceID string) error {
	return k.FinishedInferenceQueue.Set(ctx, collections.Join(blockHeight, inferenceID), inferenceID)
}

// DequeueFinishedInference removes a queued finished-inference entry for a specific block.
func (k Keeper) DequeueFinishedInference(ctx context.Context, blockHeight int64, inferenceID string) {
	_ = k.FinishedInferenceQueue.Remove(ctx, collections.Join(blockHeight, inferenceID))
}

// ListFinishedInferenceIDsForHeight lists all queued finished inference IDs for a specific block height.
func (k Keeper) ListFinishedInferenceIDsForHeight(ctx context.Context, blockHeight int64) []string {
	it, err := k.FinishedInferenceQueue.Iterate(ctx, collections.NewPrefixedPairRange[int64, string](blockHeight))
	if err != nil {
		return nil
	}
	defer it.Close()
	vals, err := it.Values()
	if err != nil {
		return nil
	}
	return vals
}
