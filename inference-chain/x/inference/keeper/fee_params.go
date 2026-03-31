package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
)

// GetFeeParams returns the fee parameters from the Params store.
// Returns a zero-value FeeParams (no fee enforcement) if not yet set.
func (k Keeper) GetFeeParams(ctx context.Context) *types.FeeParams {
	params, err := k.GetParams(ctx)
	if err != nil {
		k.LogError("Unable to get Params in GetFeeParams", types.System, "error", err)
		return &types.FeeParams{}
	}
	if params.FeeParams == nil {
		return &types.FeeParams{}
	}
	return params.FeeParams
}

// SetFeeParams updates just the FeeParams within the full Params store.
func (k Keeper) SetFeeParams(ctx context.Context, fp *types.FeeParams) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	params.FeeParams = fp
	return k.SetParams(ctx, params)
}
