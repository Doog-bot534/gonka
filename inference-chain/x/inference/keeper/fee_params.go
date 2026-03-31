package keeper

import (
	"context"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// GetFeeParams returns the fee parameters from the collections store.
// Returns zero values if not yet set (no fee enforcement until upgrade migration
// or governance sets them).
func (k Keeper) GetFeeParams(ctx context.Context) types.FeeParams {
	fp, err := k.FeeParamsItem.Get(ctx)
	if err != nil {
		// Not found or decode error: return zero values (no enforcement).
		return types.FeeParams{}
	}
	return fp
}

// SetFeeParams stores the fee parameters in the collections store.
func (k Keeper) SetFeeParams(ctx context.Context, fp types.FeeParams) error {
	return k.FeeParamsItem.Set(ctx, fp)
}

// UpdateFeeParams is a governance-gated operation that updates fee parameters.
// The caller must have governance authority (checked via msg.Authority).
func (k msgServer) UpdateFeeParams(goCtx context.Context, authority string, fp types.FeeParams) error {
	if err := k.CheckPermission(goCtx, &types.MsgUpdateParams{Authority: authority}, GovernancePermission); err != nil {
		return err
	}
	if err := fp.Validate(); err != nil {
		return errorsmod.Wrap(err, "invalid fee params")
	}
	ctx := sdk.UnwrapSDKContext(goCtx)
	if err := k.SetFeeParams(ctx, fp); err != nil {
		return err
	}
	k.LogInfo("fee params updated via governance", types.System,
		"min_gas_price_ngonka", fp.MinGasPriceNgonka,
		"base_validation_gas", fp.BaseValidationGas,
		"gas_per_poc_count", fp.GasPerPoCCount)
	return nil
}
