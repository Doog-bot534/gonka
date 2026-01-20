package keeper

import (
	"context"

	sdkerrors "cosmossdk.io/errors"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SubmitPocBatch(goCtx context.Context, msg *types.MsgSubmitPocBatch) (*types.MsgSubmitPocBatchResponse, error) {
	// V1 dispatch: route to V1 handler when poc_v2_enabled=false
	params := k.GetParams(goCtx)
	if !params.PocParams.PocV2Enabled {
		return k.submitPocBatchV1(goCtx, msg)
	}

	// V2 mode: this message type is deprecated
	return nil, sdkerrors.Wrap(types.ErrDeprecated, "MsgSubmitPocBatch is deprecated when poc_v2_enabled=true, use off-chain artifacts")
}
