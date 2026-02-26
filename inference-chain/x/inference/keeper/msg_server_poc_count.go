package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/collections"
	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) PocCount(goCtx context.Context, msg *types.MsgPocCount) (*types.MsgPocCountResponse, error) {
	params, err := k.GetParams(goCtx)
	if err != nil {
		return nil, err
	}

	if !params.PocParams.PocV2Enabled {
		return nil, sdkerrors.Wrap(types.ErrNotSupported, "V2 disabled")
	}

	if len(msg.Entries) == 0 {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "entries must not be empty")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	currentBlockHeight := ctx.BlockHeight()
	startBlockHeight := msg.PocStageStartBlockHeight

	epochParams := params.EpochParams
	upcomingEpoch, found := k.Keeper.GetUpcomingEpoch(ctx)
	if !found {
		return nil, sdkerrors.Wrap(types.ErrUpcomingEpochNotFound, "failed to get upcoming epoch")
	}
	epochContext := types.NewEpochContext(*upcomingEpoch, *epochParams)

	if !epochContext.IsStartOfPocStage(startBlockHeight) {
		return nil, sdkerrors.Wrap(types.ErrPocWrongStartBlockHeight,
			fmt.Sprintf("start block height %d doesn't match PoC stage start %d", startBlockHeight, epochContext.PocStartBlockHeight))
	}

	if !epochContext.IsPoCCountWindow(currentBlockHeight) {
		return nil, sdkerrors.Wrap(types.ErrPocTooLate, "PoC count window closed")
	}

	pk := collections.Join(startBlockHeight, msg.Creator)

	_, err = k.PocCounts.Get(ctx, pk)
	if err == nil {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("poc count already submitted by %s at height=%d", msg.Creator, startBlockHeight))
	}

	pocCount := types.PocCount{
		Creator:                  msg.Creator,
		PocStageStartBlockHeight: startBlockHeight,
		Entries:                  msg.Entries,
		BlockHeight:              currentBlockHeight,
	}

	if err := k.PocCounts.Set(ctx, pk, pocCount); err != nil {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("failed to store poc count: %v", err))
	}

	k.LogInfo("[PocCount] Stored", types.PoC,
		"creator", msg.Creator,
		"startBlockHeight", startBlockHeight,
		"entries", len(msg.Entries))

	return &types.MsgPocCountResponse{}, nil
}
