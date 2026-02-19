package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/collections"
	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) TreeRootCommit(goCtx context.Context, msg *types.MsgTreeRootCommit) (*types.MsgTreeRootCommitResponse, error) {
	params, err := k.GetParams(goCtx)
	if err != nil {
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	currentBlockHeight := ctx.BlockHeight()
	startBlockHeight := msg.PocStageStartBlockHeight

	activeEvent, isActive, err := k.Keeper.GetActiveConfirmationPoCEvent(ctx)
	if err != nil {
		k.LogError(PocFailureTag+"[TreeRootCommit] Error checking confirmation PoC event", types.PoC, "error", err)
	}

	isMigrationTracking := params.PocParams.ConfirmationPocV2Enabled && isActive && activeEvent != nil && activeEvent.EventSequence == 0
	if !params.PocParams.PocV2Enabled && !isMigrationTracking {
		return nil, sdkerrors.Wrap(types.ErrNotSupported, "V2 disabled when poc_v2_enabled=false")
	}

	if len(msg.Entries) == 0 {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "entries must not be empty")
	}

	if msg.TreeIndex < 0 || msg.TreeIndex > 15 {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("tree_index must be 0..15, got %d", msg.TreeIndex))
	}

	if isActive && activeEvent != nil && startBlockHeight == activeEvent.TriggerHeight {
		epochParams := params.EpochParams
		if !activeEvent.IsInBatchSubmissionWindow(currentBlockHeight, epochParams) {
			return nil, sdkerrors.Wrap(types.ErrPocTooLate, "confirmation PoC batch submission window closed")
		}
	} else {
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
		if !epochContext.IsPoCExchangeWindow(currentBlockHeight) {
			return nil, sdkerrors.Wrap(types.ErrPocTooLate, "PoC exchange window closed")
		}
	}

	pk := collections.Join(startBlockHeight, msg.TreeIndex)

	_, err = k.TreeRootCommits.Get(ctx, pk)
	if err == nil {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("tree root commit already exists for tree_index=%d at height=%d", msg.TreeIndex, startBlockHeight))
	}

	commit := types.TreeRootCommit{
		Creator:                  msg.Creator,
		PocStageStartBlockHeight: startBlockHeight,
		TreeIndex:                msg.TreeIndex,
		Entries:                  msg.Entries,
		BlockHeight:              currentBlockHeight,
	}

	if err := k.TreeRootCommits.Set(ctx, pk, commit); err != nil {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("failed to store tree root commit: %v", err))
	}

	k.LogInfo("[TreeRootCommit] Stored", types.PoC,
		"creator", msg.Creator,
		"treeIndex", msg.TreeIndex,
		"startBlockHeight", startBlockHeight,
		"entries", len(msg.Entries))

	return &types.MsgTreeRootCommitResponse{}, nil
}
