package keeper

import (
	"context"

	"cosmossdk.io/collections"
)

// SetPendingInferenceValidation enqueues an inference ID to be processed in EndBlock for the given block height.
func (k Keeper) SetPendingInferenceValidation(ctx context.Context, blockHeight int64, inferenceID string) error {
	return k.PendingInferenceValidationQueue.Set(ctx, collections.Join(blockHeight, inferenceID), inferenceID)
}

// RemovePendingInferenceValidation removes a queued inference validation entry for a specific block.
func (k Keeper) RemovePendingInferenceValidation(ctx context.Context, blockHeight int64, inferenceID string) {
	_ = k.PendingInferenceValidationQueue.Remove(ctx, collections.Join(blockHeight, inferenceID))
}

// GetAllPendingInferenceValidationForHeight lists all queued inference IDs for a specific block height.
func (k Keeper) GetAllPendingInferenceValidationForHeight(ctx context.Context, blockHeight int64) []string {
	it, err := k.PendingInferenceValidationQueue.Iterate(ctx, collections.NewPrefixedPairRange[int64, string](blockHeight))
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
