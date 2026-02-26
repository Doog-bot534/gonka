package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/collections"
	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) PocWeightCommit(goCtx context.Context, msg *types.MsgPocWeightCommit) (*types.MsgPocWeightCommitResponse, error) {
	params, err := k.GetParams(goCtx)
	if err != nil {
		return nil, err
	}

	if !params.PocParams.PocV2Enabled {
		return nil, sdkerrors.Wrap(types.ErrNotSupported, "V2 disabled")
	}

	if len(msg.Weights) == 0 {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "weights must not be empty")
	}

	if len(msg.RootHash) != 32 {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("root_hash must be 32 bytes, got %d", len(msg.RootHash)))
	}

	if msg.Count == 0 {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "count must be > 0")
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

	if !epochContext.IsValidationExchangeWindow(currentBlockHeight) {
		return nil, sdkerrors.Wrap(types.ErrPocTooLate, "PoC validation exchange window closed")
	}

	addr, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return nil, sdkerrors.Wrap(types.ErrInvalidAddress, fmt.Sprintf("invalid creator address: %v", err))
	}
	_, err = k.Participants.Get(ctx, addr)
	if err != nil {
		return nil, sdkerrors.Wrap(types.ErrParticipantNotFound, fmt.Sprintf("creator %s is not a registered participant", msg.Creator))
	}

	agreedCountPK := collections.Join(startBlockHeight, msg.Creator)
	agreedEntry, agreedErr := k.AgreedCounts.Get(ctx, agreedCountPK)
	if agreedErr == nil {
		if msg.Count != agreedEntry.AgreedCount {
			return nil, sdkerrors.Wrap(types.ErrIllegalState,
				fmt.Sprintf("count %d does not match agreed count %d", msg.Count, agreedEntry.AgreedCount))
		}
	}

	var weightSum uint32
	for _, w := range msg.Weights {
		weightSum += w.Weight
	}
	if weightSum != msg.Count {
		return nil, sdkerrors.Wrap(types.ErrIllegalState,
			fmt.Sprintf("weight sum %d does not match count %d", weightSum, msg.Count))
	}

	pk := collections.Join(startBlockHeight, addr)

	_, err = k.PocWeightCommits.Get(ctx, pk)
	if err == nil {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("poc weight commit already submitted by %s at height=%d", msg.Creator, startBlockHeight))
	}

	commit := types.PocWeightCommit{
		Creator:                  msg.Creator,
		PocStageStartBlockHeight: startBlockHeight,
		Count:                    msg.Count,
		RootHash:                 msg.RootHash,
		Weights:                  msg.Weights,
		BlockHeight:              currentBlockHeight,
	}

	if err := k.PocWeightCommits.Set(ctx, pk, commit); err != nil {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("failed to store poc weight commit: %v", err))
	}

	k.LogInfo("[PocWeightCommit] Stored", types.PoC,
		"creator", msg.Creator,
		"startBlockHeight", startBlockHeight,
		"count", msg.Count,
		"weights", len(msg.Weights))

	return &types.MsgPocWeightCommitResponse{}, nil
}
