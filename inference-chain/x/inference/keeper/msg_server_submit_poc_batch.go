package keeper

import (
	"context"

	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SubmitPocBatch(goCtx context.Context, msg *types.MsgSubmitPocBatch) (*types.MsgSubmitPocBatchResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if msg.NodeId == "" {
		k.LogError(PocFailureTag+"[SubmitPocBatch] NodeId is empty", types.PoC,
			"participant", msg.Creator,
			"msg.NodeId", msg.NodeId)
		return nil, sdkerrors.Wrap(types.ErrPocNodeIdEmpty, "NodeId is empty")
	}

	// PoC period validation is performed in the ante handler (PocPeriodValidationDecorator)
	// No need to duplicate validation here
	currentBlockHeight := ctx.BlockHeight()
	startBlockHeight := msg.PocStageStartBlockHeight

	storedBatch := types.PoCBatch{
		ParticipantAddress:       msg.Creator,
		PocStageStartBlockHeight: startBlockHeight,
		ReceivedAtBlockHeight:    currentBlockHeight,
		Nonces:                   msg.Nonces,
		Dist:                     msg.Dist,
		BatchId:                  msg.BatchId,
		NodeId:                   msg.NodeId,
	}

	k.SetPocBatch(ctx, storedBatch)

	return &types.MsgSubmitPocBatchResponse{}, nil
}
