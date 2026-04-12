package keeper

import (
	"context"
	"fmt"

	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

const PocFailureTag = "[PoC Failure]"

// PoCV2StoreCommit handles submission of off-chain artifact store commits.
func (k msgServer) PoCV2StoreCommit(goCtx context.Context, msg *types.MsgPoCV2StoreCommit) (*types.MsgPoCV2StoreCommitResponse, error) {
	if err := k.CheckPermission(goCtx, msg, NoPermission); err != nil {
		return nil, err
	}

	params, err := k.GetParams(goCtx)
	if err != nil {
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	currentBlockHeight := ctx.BlockHeight()
	startBlockHeight := msg.PocStageStartBlockHeight

	// Check for active confirmation PoC event
	activeEvent, isActive, err := k.Keeper.GetActiveConfirmationPoCEvent(ctx)
	if err != nil {
		k.LogError(PocFailureTag+"[PoCV2StoreCommit] Error checking confirmation PoC event", types.PoC, "error", err)
	}

	if !params.PocParams.PocV2Enabled {
		return nil, sdkerrors.Wrap(types.ErrNotSupported, "V2 disabled when poc_v2_enabled=false")
	}

	// Participant access gating: blocklisted accounts cannot submit PoC artifacts.
	if k.IsPoCParticipantBlocked(ctx, msg.Creator) {
		k.LogError(PocFailureTag+"[PoCV2StoreCommit] participant is blocked from PoC", types.PoC, "participant", msg.Creator)
		return nil, sdkerrors.Wrap(types.ErrParticipantBlocked, msg.Creator)
	}

	if len(msg.Entries) == 0 {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "entries must not be empty")
	}

	// Validate PoC window
	// For confirmation PoC: accept during batch submission window (generation + exchange)
	// For regular PoC: accept during exchange window
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

	for _, entry := range msg.Entries {
		if entry.Count == 0 {
			return nil, sdkerrors.Wrap(types.ErrIllegalState, "entry count must be greater than 0")
		}
		if len(entry.RootHash) != 32 {
			return nil, sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("root_hash must be 32 bytes, got %d", len(entry.RootHash)))
		}

		modelID := entry.ModelId
		if modelID == "" {
			return nil, sdkerrors.Wrap(types.ErrIllegalState, "model_id must not be empty")
		}
		if _, found := k.GetGovernanceModel(ctx, modelID); !found {
			return nil, sdkerrors.Wrap(types.ErrInvalidModel, fmt.Sprintf("model_id %q is not a governance model", modelID))
		}

		pk := pocV2StoreCommitKey(startBlockHeight, addr, modelID)
		existing, err := k.PoCV2StoreCommits.Get(ctx, pk)
		if err == nil {
			if existing.CommitBlockHeight == currentBlockHeight {
				return nil, sdkerrors.Wrap(types.ErrIllegalState, "only one commit per block allowed")
			}
			if entry.Count <= existing.Count {
				return nil, sdkerrors.Wrap(types.ErrIllegalState,
					fmt.Sprintf("count must increase: got %d, last recorded %d", entry.Count, existing.Count))
			}
		}

		commit := types.PoCV2StoreCommit{
			ParticipantAddress:       msg.Creator,
			PocStageStartBlockHeight: startBlockHeight,
			Count:                    entry.Count,
			RootHash:                 entry.RootHash,
			CommitBlockHeight:        currentBlockHeight,
			ModelId:                  modelID,
		}

		if err := k.PoCV2StoreCommits.Set(ctx, pk, commit); err != nil {
			return nil, sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("failed to store commit: %v", err))
		}

		k.LogInfo("[PoCV2StoreCommit] Stored", types.PoC,
			"participant", msg.Creator,
			"model_id", modelID,
			"startBlockHeight", startBlockHeight,
			"count", entry.Count)
	}

	return &types.MsgPoCV2StoreCommitResponse{}, nil
}

// MLNodeWeightDistribution handles submission of per-node weight distribution.
func (k msgServer) MLNodeWeightDistribution(goCtx context.Context, msg *types.MsgMLNodeWeightDistribution) (*types.MsgMLNodeWeightDistributionResponse, error) {
	if err := k.CheckPermission(goCtx, msg, NoPermission); err != nil {
		return nil, err
	}

	params, err := k.GetParams(goCtx)
	if err != nil {
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	currentBlockHeight := ctx.BlockHeight()
	startBlockHeight := msg.PocStageStartBlockHeight

	// Check for active confirmation PoC event
	activeEvent, isActive, err := k.Keeper.GetActiveConfirmationPoCEvent(ctx)
	if err != nil {
		k.LogError(PocFailureTag+"[MLNodeWeightDistribution] Error checking confirmation PoC event", types.PoC, "error", err)
	}

	if !params.PocParams.PocV2Enabled {
		return nil, sdkerrors.Wrap(types.ErrNotSupported, "V2 disabled when poc_v2_enabled=false")
	}

	// Participant access gating: blocklisted accounts cannot submit PoC artifacts.
	if k.IsPoCParticipantBlocked(ctx, msg.Creator) {
		k.LogError(PocFailureTag+"[MLNodeWeightDistribution] participant is blocked from PoC", types.PoC, "participant", msg.Creator)
		return nil, sdkerrors.Wrap(types.ErrParticipantBlocked, msg.Creator)
	}

	if len(msg.Entries) == 0 {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "entries must not be empty")
	}

	// Validate window: accept from exchange window through end of validation
	if isActive && activeEvent != nil {
		if startBlockHeight != activeEvent.TriggerHeight {
			return nil, sdkerrors.Wrap(types.ErrPocWrongStartBlockHeight,
				fmt.Sprintf("confirmation PoC: start block height %d doesn't match event trigger %d", startBlockHeight, activeEvent.TriggerHeight))
		}
		confirmParams, err := k.GetParams(ctx)
		if err != nil {
			return nil, err
		}
		epochParams := confirmParams.EpochParams
		validationEnd := activeEvent.GetValidationEnd(epochParams)
		if currentBlockHeight > validationEnd {
			return nil, sdkerrors.Wrap(types.ErrPocTooLate, "confirmation PoC validation window closed")
		}
	} else {
		regularParams, err := k.Keeper.GetParams(goCtx)
		if err != nil {
			return nil, err
		}
		epochParams := regularParams.EpochParams
		upcomingEpoch, found := k.Keeper.GetUpcomingEpoch(ctx)
		if !found {
			return nil, sdkerrors.Wrap(types.ErrUpcomingEpochNotFound, "failed to get upcoming epoch")
		}
		epochContext := types.NewEpochContext(*upcomingEpoch, *epochParams)

		if !epochContext.IsStartOfPocStage(startBlockHeight) {
			return nil, sdkerrors.Wrap(types.ErrPocWrongStartBlockHeight,
				fmt.Sprintf("start block height %d doesn't match PoC stage start %d", startBlockHeight, epochContext.PocStartBlockHeight))
		}
		// Accept through end of validation phase
		validationEnd := epochContext.EndOfPoCValidation()
		if currentBlockHeight > validationEnd {
			return nil, sdkerrors.Wrap(types.ErrPocTooLate, "PoC validation window closed")
		}
	}

	addr, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return nil, sdkerrors.Wrap(types.ErrInvalidAddress, fmt.Sprintf("invalid creator address: %v", err))
	}

	for _, entry := range msg.Entries {
		if len(entry.Weights) == 0 {
			return nil, sdkerrors.Wrap(types.ErrIllegalState, "entry weights must not be empty")
		}

		modelID := entry.ModelId
		if modelID == "" {
			return nil, sdkerrors.Wrap(types.ErrIllegalState, "model_id must not be empty")
		}
		if _, found := k.GetGovernanceModel(ctx, modelID); !found {
			return nil, sdkerrors.Wrap(types.ErrInvalidModel, fmt.Sprintf("model_id %q is not a governance model", modelID))
		}

		pk := pocV2StoreCommitKey(startBlockHeight, addr, modelID)
		commit, err := k.PoCV2StoreCommits.Get(ctx, pk)
		if err != nil {
			return nil, sdkerrors.Wrap(types.ErrIllegalState, "no store commit found for this stage and model")
		}

		var sum uint64
		for _, w := range entry.Weights {
			sum += uint64(w.Weight)
		}
		if sum != uint64(commit.Count) {
			return nil, sdkerrors.Wrap(types.ErrIllegalState,
				fmt.Sprintf("weight sum %d does not match committed count %d", sum, commit.Count))
		}

		distribution := types.MLNodeWeightDistribution{
			ParticipantAddress:       msg.Creator,
			PocStageStartBlockHeight: startBlockHeight,
			Weights:                  entry.Weights,
			ModelId:                  modelID,
		}

		if err := k.MLNodeWeightDistributions.Set(ctx, pk, distribution); err != nil {
			return nil, sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("failed to store distribution: %v", err))
		}

		k.LogInfo("[MLNodeWeightDistribution] Stored", types.PoC,
			"participant", msg.Creator,
			"model_id", modelID,
			"startBlockHeight", startBlockHeight,
			"nodeCount", len(entry.Weights))
	}

	return &types.MsgMLNodeWeightDistributionResponse{}, nil
}
