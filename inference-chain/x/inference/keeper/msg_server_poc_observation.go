package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/collections"
	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SubmitPoCObservation(goCtx context.Context, msg *types.MsgSubmitPoCObservation) (*types.MsgSubmitPoCObservationResponse, error) {
	params, err := k.GetParams(goCtx)
	if err != nil {
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	currentBlockHeight := ctx.BlockHeight()
	startBlockHeight := msg.PocStageStartBlockHeight

	activeEvent, isActive, err := k.Keeper.GetActiveConfirmationPoCEvent(ctx)
	if err != nil {
		k.LogError(PocFailureTag+"[SubmitPoCObservation] Error checking confirmation PoC event", types.PoC, "error", err)
	}

	isMigrationTracking := params.PocParams.ConfirmationPocV2Enabled && isActive && activeEvent != nil && activeEvent.EventSequence == 0
	if !params.PocParams.PocV2Enabled && !isMigrationTracking {
		return nil, sdkerrors.Wrap(types.ErrNotSupported, "V2 disabled when poc_v2_enabled=false")
	}

	if len(msg.Arrivals) == 0 {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "arrivals must not be empty")
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

	addr, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return nil, sdkerrors.Wrap(types.ErrInvalidAddress, fmt.Sprintf("invalid creator address: %v", err))
	}
	pk := collections.Join(startBlockHeight, addr)

	_, err = k.PoCObservationsMap.Get(ctx, pk)
	if err == nil {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "observation already submitted for this height")
	}

	obs := types.PoCObservation{
		ValidatorAddress:         msg.Creator,
		PocStageStartBlockHeight: startBlockHeight,
		Arrivals:                 msg.Arrivals,
		BlockHeight:              currentBlockHeight,
	}

	if err := k.PoCObservationsMap.Set(ctx, pk, obs); err != nil {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("failed to store observation: %v", err))
	}

	k.LogInfo("[SubmitPoCObservation] Stored", types.PoC,
		"validator", msg.Creator,
		"startBlockHeight", startBlockHeight,
		"arrivals", len(msg.Arrivals))

	return &types.MsgSubmitPoCObservationResponse{}, nil
}
