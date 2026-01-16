package keeper

import (
	"context"
	"fmt"

	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// SubmitPocBatchesV2 handles submission of PoC v2 batches from multiple nodes.
func (k msgServer) SubmitPocBatchesV2(goCtx context.Context, msg *types.MsgSubmitPocBatchesV2) (*types.MsgSubmitPocBatchesV2Response, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	currentBlockHeight := ctx.BlockHeight()
	startBlockHeight := msg.PocStageStartBlockHeight

	// Participant access gating: blocklisted accounts cannot participate in PoC.
	if k.IsPoCParticipantBlocked(ctx, msg.Creator) {
		k.LogError(PocFailureTag+"[SubmitPocBatchesV2] participant is blocked from PoC", types.PoC, "participant", msg.Creator)
		return nil, sdkerrors.Wrap(types.ErrParticipantBlocked, msg.Creator)
	}

	// Check for active confirmation PoC event first
	activeEvent, isActive, err := k.Keeper.GetActiveConfirmationPoCEvent(ctx)
	if err != nil {
		k.LogError(PocFailureTag+"[SubmitPocBatchesV2] Error checking confirmation PoC event", types.PoC, "error", err)
		// Continue with regular PoC check
	}

	// Validate PoC window once at message level (all batches share the same height)
	if isActive && activeEvent != nil && activeEvent.Phase == types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION {
		// Verify the message is for this confirmation PoC event
		if startBlockHeight != activeEvent.TriggerHeight {
			k.LogError(PocFailureTag+"[SubmitPocBatchesV2] Confirmation PoC: start block height mismatch", types.PoC,
				"participant", msg.Creator,
				"msg.PocStageStartBlockHeight", startBlockHeight,
				"event.TriggerHeight", activeEvent.TriggerHeight,
				"currentBlockHeight", currentBlockHeight)
			errMsg := fmt.Sprintf("[SubmitPocBatchesV2] Confirmation PoC active but start block height doesn't match. "+
				"participant = %s. msg.PocStageStartBlockHeight = %d. event.TriggerHeight = %d",
				msg.Creator, startBlockHeight, activeEvent.TriggerHeight)
			return nil, sdkerrors.Wrap(types.ErrPocWrongStartBlockHeight, errMsg)
		}

		// Verify we're in the batch submission window (generation + exchange period)
		epochParams := k.GetParams(ctx).EpochParams
		if !activeEvent.IsInBatchSubmissionWindow(currentBlockHeight, epochParams) {
			k.LogError(PocFailureTag+"[SubmitPocBatchesV2] Confirmation PoC: outside batch submission window", types.PoC,
				"participant", msg.Creator,
				"currentBlockHeight", currentBlockHeight,
				"generationStartHeight", activeEvent.GenerationStartHeight,
				"exchangeEndHeight", activeEvent.GetExchangeEnd(epochParams))
			return nil, sdkerrors.Wrap(types.ErrPocTooLate, "Confirmation PoC batch submission window closed")
		}
	} else {
		// Regular PoC logic
		epochParams := k.Keeper.GetParams(goCtx).EpochParams
		upcomingEpoch, found := k.Keeper.GetUpcomingEpoch(ctx)
		if !found {
			k.LogError(PocFailureTag+"[SubmitPocBatchesV2] Failed to get upcoming epoch", types.PoC,
				"participant", msg.Creator,
				"currentBlockHeight", currentBlockHeight)
			return nil, sdkerrors.Wrap(types.ErrUpcomingEpochNotFound, "Failed to get upcoming epoch")
		}
		epochContext := types.NewEpochContext(*upcomingEpoch, *epochParams)

		if !epochContext.IsStartOfPocStage(startBlockHeight) {
			k.LogError(PocFailureTag+"[SubmitPocBatchesV2] message start block height doesn't match the upcoming epoch group", types.PoC,
				"participant", msg.Creator,
				"msg.PocStageStartBlockHeight", startBlockHeight,
				"epochContext.PocStartBlockHeight", epochContext.PocStartBlockHeight,
				"currentBlockHeight", currentBlockHeight)
			errMsg := fmt.Sprintf("[SubmitPocBatchesV2] message start block height doesn't match the upcoming epoch group. "+
				"participant = %s. msg.PocStageStartBlockHeight = %d. epochContext.PocStartBlockHeight = %d. currentBlockHeight = %d",
				msg.Creator, startBlockHeight, epochContext.PocStartBlockHeight, currentBlockHeight)
			return nil, sdkerrors.Wrap(types.ErrPocWrongStartBlockHeight, errMsg)
		}

		if !epochContext.IsPoCExchangeWindow(currentBlockHeight) {
			k.LogError(PocFailureTag+"[SubmitPocBatchesV2] PoC exchange window is closed.", types.PoC,
				"participant", msg.Creator,
				"msg.PocStageStartBlockHeight", startBlockHeight,
				"currentBlockHeight", currentBlockHeight,
				"epochContext.PocStartBlockHeight", epochContext.PocStartBlockHeight)
			errMsg := fmt.Sprintf("PoC exchange window is closed. "+
				"participant = %s. msg.BlockHeight = %d, currentBlockHeight = %d, epochContext.PocStartBlockHeight = %d",
				msg.Creator, startBlockHeight, currentBlockHeight, epochContext.PocStartBlockHeight)
			return nil, sdkerrors.Wrap(types.ErrPocTooLate, errMsg)
		}
	}

	// Process each batch
	for i, batch := range msg.Batches {
		if batch.NodeId == "" {
			k.LogError(PocFailureTag+"[SubmitPocBatchesV2] NodeId is empty", types.PoC,
				"participant", msg.Creator,
				"batchIndex", i)
			return nil, sdkerrors.Wrap(types.ErrPocNodeIdEmpty, "NodeId is empty")
		}

		// Validate artifact vectors are non-empty
		for j, artifact := range batch.Artifacts {
			if len(artifact.Vector) == 0 {
				k.LogError(PocFailureTag+"[SubmitPocBatchesV2] Artifact vector is empty", types.PoC,
					"participant", msg.Creator,
					"batchIndex", i,
					"artifactIndex", j,
					"nonce", artifact.Nonce)
				return nil, sdkerrors.Wrap(types.ErrPocArtifactVectorEmpty, "artifact vector is empty")
			}
		}

		// Store the v2 batch with creator as participant (combine message-level height with payload)
		storedBatch := types.PoCBatchV2{
			ParticipantAddress:       msg.Creator,
			PocStageStartBlockHeight: startBlockHeight,
			NodeId:                   batch.NodeId,
			Artifacts:                batch.Artifacts,
		}

		k.SetPocBatchV2(ctx, storedBatch)

		k.LogInfo("[SubmitPocBatchesV2] Batch stored", types.PoC,
			"participant", msg.Creator,
			"startBlockHeight", startBlockHeight,
			"nodeId", batch.NodeId,
			"artifactsCount", len(batch.Artifacts))
	}

	return &types.MsgSubmitPocBatchesV2Response{}, nil
}

// SubmitPocValidationsV2 handles batch submission of PoC v2 validations.
func (k msgServer) SubmitPocValidationsV2(goCtx context.Context, msg *types.MsgSubmitPocValidationsV2) (*types.MsgSubmitPocValidationsV2Response, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	currentBlockHeight := ctx.BlockHeight()
	startBlockHeight := msg.PocStageStartBlockHeight

	// Participant access gating: blocklisted accounts cannot validate in PoC.
	if k.IsPoCParticipantBlocked(ctx, msg.Creator) {
		k.LogError(PocFailureTag+"[SubmitPocValidationsV2] validator is blocked from PoC", types.PoC, "validator", msg.Creator)
		return nil, sdkerrors.Wrap(types.ErrParticipantBlocked, msg.Creator)
	}

	// Check for active confirmation PoC event first
	activeEvent, isActive, err := k.Keeper.GetActiveConfirmationPoCEvent(ctx)
	if err != nil {
		k.LogError(PocFailureTag+"[SubmitPocValidationsV2] Error checking confirmation PoC event", types.PoC, "error", err)
		// Continue with regular PoC check
	}

	// Validate PoC window once at message level (all validations share the same height)
	if isActive && activeEvent != nil && activeEvent.Phase == types.ConfirmationPoCPhase_CONFIRMATION_POC_VALIDATION {
		// Verify the message is for this confirmation PoC event
		if startBlockHeight != activeEvent.TriggerHeight {
			k.LogError(PocFailureTag+"[SubmitPocValidationsV2] Confirmation PoC: start block height mismatch", types.PoC,
				"validatorParticipant", msg.Creator,
				"msg.PocStageStartBlockHeight", startBlockHeight,
				"event.TriggerHeight", activeEvent.TriggerHeight,
				"currentBlockHeight", currentBlockHeight)
			errMsg := fmt.Sprintf("[SubmitPocValidationsV2] Confirmation PoC active but start block height doesn't match. "+
				"validatorParticipant = %s. msg.PocStageStartBlockHeight = %d. event.TriggerHeight = %d",
				msg.Creator, startBlockHeight, activeEvent.TriggerHeight)
			return nil, sdkerrors.Wrap(types.ErrPocWrongStartBlockHeight, errMsg)
		}

		// Verify we're in the validation window
		epochParams := k.GetParams(ctx).EpochParams
		if !activeEvent.IsInValidationWindow(currentBlockHeight, epochParams) {
			k.LogError(PocFailureTag+"[SubmitPocValidationsV2] Confirmation PoC: outside validation window", types.PoC,
				"validatorParticipant", msg.Creator,
				"currentBlockHeight", currentBlockHeight,
				"validationStartHeight", activeEvent.GetValidationStart(epochParams),
				"validationEndHeight", activeEvent.GetValidationEnd(epochParams))
			return nil, sdkerrors.Wrap(types.ErrPocTooLate, "Confirmation PoC validation window closed")
		}
	} else {
		// Regular PoC logic
		epochParams := k.Keeper.GetParams(ctx).EpochParams
		upcomingEpoch, found := k.Keeper.GetUpcomingEpoch(ctx)
		if !found {
			k.LogError(PocFailureTag+"[SubmitPocValidationsV2] Failed to get upcoming epoch", types.PoC,
				"validatorParticipant", msg.Creator,
				"currentBlockHeight", currentBlockHeight)
			return nil, sdkerrors.Wrap(types.ErrUpcomingEpochNotFound, "[SubmitPocValidationsV2] Failed to get upcoming epoch")
		}
		epochContext := types.NewEpochContext(*upcomingEpoch, *epochParams)

		if !epochContext.IsStartOfPocStage(startBlockHeight) {
			k.LogError(PocFailureTag+"[SubmitPocValidationsV2] message start block height doesn't match the upcoming epoch", types.PoC,
				"validatorParticipant", msg.Creator,
				"msg.PocStageStartBlockHeight", startBlockHeight,
				"epochContext.PocStartBlockHeight", epochContext.PocStartBlockHeight,
				"currentBlockHeight", currentBlockHeight)
			errMsg := fmt.Sprintf("[SubmitPocValidationsV2] message start block height doesn't match the upcoming epoch. "+
				"validatorParticipant = %s. msg.PocStageStartBlockHeight = %d. epochContext.PocStartBlockHeight = %d. currentBlockHeight = %d",
				msg.Creator, startBlockHeight, epochContext.PocStartBlockHeight, currentBlockHeight)
			return nil, sdkerrors.Wrap(types.ErrPocWrongStartBlockHeight, errMsg)
		}

		if !epochContext.IsValidationExchangeWindow(currentBlockHeight) {
			k.LogError(PocFailureTag+"[SubmitPocValidationsV2] PoC validation exchange window is closed.", types.PoC,
				"validatorParticipant", msg.Creator,
				"msg.BlockHeight", startBlockHeight,
				"epochContext.PocStartBlockHeight", epochContext.PocStartBlockHeight,
				"currentBlockHeight", currentBlockHeight)
			errMsg := fmt.Sprintf("msg.BlockHeight = %d, currentBlockHeight = %d", startBlockHeight, currentBlockHeight)
			return nil, sdkerrors.Wrap(types.ErrPocTooLate, errMsg)
		}
	}

	// Process each validation
	for _, validation := range msg.Validations {
		// Store the v2 validation (combine message-level height with payload)
		storedValidation := types.PoCValidationV2{
			ParticipantAddress:          validation.ParticipantAddress,
			ValidatorParticipantAddress: msg.Creator,
			PocStageStartBlockHeight:    startBlockHeight,
			ValidatedWeight:             validation.ValidatedWeight,
		}

		k.SetPocValidationV2(ctx, storedValidation)

		k.LogInfo("[SubmitPocValidationsV2] Validation stored", types.PoC,
			"validator", msg.Creator,
			"participant", validation.ParticipantAddress,
			"validatedWeight", validation.ValidatedWeight)
	}

	return &types.MsgSubmitPocValidationsV2Response{}, nil
}
