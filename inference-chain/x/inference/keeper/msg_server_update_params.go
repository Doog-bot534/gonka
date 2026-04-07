package keeper

import (
	"context"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) UpdateParams(goCtx context.Context, req *types.MsgUpdateParams) (*types.MsgUpdateParamsResponse, error) {
	if err := k.CheckPermission(goCtx, req, GovernancePermission); err != nil {
		return nil, err
	}

	if err := req.Params.Validate(); err != nil {
		return nil, errorsmod.Wrap(err, "invalid params")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	if err := k.SetParams(ctx, req.Params); err != nil {
		return nil, err
	}

	// Sync FeeParams to its dedicated KV store so GonkaFeeChecker reads the
	// governance-updated values. FeeParams lives in a separate key because it
	// was added after the initial Params proto and needs to be readable by the
	// ante handler without deserializing the full Params object.
	if req.Params.FeeParams != nil {
		if err := k.SetFeeParams(ctx, req.Params.FeeParams); err != nil {
			return nil, errorsmod.Wrap(err, "failed to sync fee params")
		}
	}

	err := k.PrecomputeSPRTValues(ctx)
	if err != nil {
		k.LogError("Failed to precompute SPRT values", types.Validation, "error", err)
		return nil, err
	}

	return &types.MsgUpdateParamsResponse{}, nil
}
