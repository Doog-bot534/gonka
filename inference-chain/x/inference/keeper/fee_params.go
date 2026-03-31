package keeper

import (
	"context"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

// GetFeeParams returns the fee parameters from the KV store.
// Returns zero values if not yet set (no fee enforcement until upgrade migration
// or governance sets them).
func (k Keeper) GetFeeParams(ctx context.Context) types.FeeParams {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	bz := store.Get(types.FeeParamsKey)
	if bz == nil {
		return types.FeeParams{}
	}
	fp, err := types.UnmarshalFeeParams(bz)
	if err != nil {
		k.LogError("Unable to unmarshal FeeParams, using defaults", types.System, "error", err)
		return types.DefaultFeeParams()
	}
	return fp
}

// SetFeeParams stores the fee parameters in the KV store.
func (k Keeper) SetFeeParams(ctx context.Context, fp types.FeeParams) error {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	bz, err := fp.Marshal()
	if err != nil {
		return err
	}
	store.Set(types.FeeParamsKey, bz)
	return nil
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
