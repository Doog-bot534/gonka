package keeper

import (
	"context"

	sdkerrors "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) FinishInference(goCtx context.Context, msg *types.MsgFinishInference) (*types.MsgFinishInferenceResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	k.LogInfo("FinishInference", types.Inferences, "inference_id", msg.InferenceId, "executed_by", msg.ExecutedBy, "created_by", msg.Creator)
	if msg.Creator != msg.ExecutedBy {
		err := sdkerrors.Wrapf(types.ErrInferenceRoleMismatch, "creator (%s) must equal executed_by (%s)", msg.Creator, msg.ExecutedBy)
		k.LogError("FinishInference: creator-role invariant failed", types.Inferences, "error", err)
		return failedFinish(ctx, err, msg), nil
	}

	if msg.PromptTokenCount > types.MaxAllowedTokens {
		return failedFinish(ctx, sdkerrors.Wrapf(types.ErrTokenCountOutOfRange, "prompt_token_count exceeds limit (%d > %d)", msg.PromptTokenCount, types.MaxAllowedTokens), msg), nil
	}
	if msg.CompletionTokenCount > types.MaxAllowedTokens {
		return failedFinish(ctx, sdkerrors.Wrapf(types.ErrTokenCountOutOfRange, "completion_token_count exceeds limit (%d > %d)", msg.CompletionTokenCount, types.MaxAllowedTokens), msg), nil
	}

	// Developer access gating: until cutoff height only allowlisted developers may run inference flows.
	// We gate by the original requester (developer), not the executor/TA.
	if k.IsDeveloperAccessRestricted(ctx, ctx.BlockHeight()) && !k.IsAllowedDeveloper(ctx, msg.RequestedBy) {
		k.LogError("FinishInference: developer is not allowlisted at this height", types.Inferences, "developer", msg.RequestedBy, "blockHeight", ctx.BlockHeight())
		return failedFinish(ctx, sdkerrors.Wrap(types.ErrDeveloperNotAllowlisted, msg.RequestedBy), msg), nil
	}

	// Transfer Agent access gating: only allowlisted TAs may be involved in inferences.
	if k.IsTransferAgentRestricted(ctx) && !k.IsAllowedTransferAgent(ctx, msg.TransferredBy) {
		k.LogError("FinishInference: transfer agent is not allowlisted", types.Inferences,
			"transferAgent", msg.TransferredBy, "blockHeight", ctx.BlockHeight())
		return failedFinish(ctx, sdkerrors.Wrap(types.ErrTransferAgentNotAllowlisted, msg.TransferredBy), msg), nil
	}

	_, found := k.GetParticipant(ctx, msg.ExecutedBy)
	if !found {
		k.LogError("FinishInference: executor not found", types.Inferences, "executed_by", msg.ExecutedBy)
		return failedFinish(ctx, sdkerrors.Wrap(types.ErrParticipantNotFound, msg.ExecutedBy), msg), nil
	}

	requestor, found := k.GetParticipant(ctx, msg.RequestedBy)
	if !found {
		k.LogError("FinishInference: requestor not found", types.Inferences, "requested_by", msg.RequestedBy)
		return failedFinish(ctx, sdkerrors.Wrap(types.ErrParticipantNotFound, msg.RequestedBy), msg), nil
	}

	transferAgent, found := k.GetParticipant(ctx, msg.TransferredBy)
	if !found {
		k.LogError("FinishInference: transfer agent not found", types.Inferences, "transferred_by", msg.TransferredBy)
		return failedFinish(ctx, sdkerrors.Wrap(types.ErrParticipantNotFound, msg.TransferredBy), msg), nil
	}

	existingInference, found := k.GetInference(ctx, msg.InferenceId)

	if found && existingInference.FinishedProcessed() {
		k.LogError("FinishInference: inference already finished", types.Inferences, "inferenceId", msg.InferenceId)
		return failedFinish(ctx, sdkerrors.Wrap(types.ErrInferenceFinishProcessed, "inference has already finished processed"), msg), nil
	}

	if found && existingInference.Status == types.InferenceStatus_EXPIRED {
		k.LogWarn("FinishInference: cannot finish expired inference", types.Inferences,
			"inferenceId", msg.InferenceId,
			"currentStatus", existingInference.Status,
			"executedBy", msg.ExecutedBy)
		return failedFinish(ctx, sdkerrors.Wrap(types.ErrInferenceExpired, "inference has already expired"), msg), nil
	}

	// Signature verification policy:
	// - Start first: finish performs equality checks only (no TA/dev re-verification).
	// - Finish first: verify dev + TA signatures.
	// - Executor signature verification is disabled by policy in both paths.
	if existingInference.StartProcessed() {
		// TODO: punish executor if Dev fails
		if err := compareFinishDevComponents(msg, &existingInference); err != nil {
			k.LogError("FinishInference: dev component mismatch", types.Inferences, "error", err, "inferenceId", msg.InferenceId)
			return failedFinish(ctx, err, msg), nil
		}
		// TODO: re-check TA signature if any of the checks fail and punish the TA if the signature is invalid
		if err := compareFinishTAComponents(msg, &existingInference); err != nil {
			k.LogError("FinishInference: TA component mismatch", types.Inferences, "error", err, "inferenceId", msg.InferenceId)
			return failedFinish(ctx, err, msg), nil
		}
		if err := compareFinishRoleFields(msg, &existingInference); err != nil {
			k.LogError("FinishInference: role/address field mismatch", types.Inferences, "error", err, "inferenceId", msg.InferenceId)
			return failedFinish(ctx, err, msg), nil
		}
		k.LogInfo("FinishInference: start-first policy; TA signature skipped and components compared", types.Inferences, "inferenceId", msg.InferenceId)
	} else {
		err := k.verifyFinishKeys(ctx, msg, &transferAgent, &requestor)
		if err != nil {
			k.LogError("FinishInference: verifyFinishKeys failed", types.Inferences, "error", err)
			return failedFinish(ctx, sdkerrors.Wrap(types.ErrInvalidSignature, err.Error()), msg), nil
		}
		k.LogInfo("FinishInference: dev signature verified on first message", types.Inferences, "inferenceId", msg.InferenceId)
		k.LogInfo("FinishInference: TA signature verified (finish-first policy)", types.Inferences, "inferenceId", msg.InferenceId)
	}
	k.LogInfo("FinishInference: executor signature verification disabled by policy", types.Inferences, "inferenceId", msg.InferenceId)

	// Record the current price only if this is the first message (StartInference not processed yet)
	// This ensures consistent pricing regardless of message arrival order
	if !existingInference.StartProcessed() {
		existingInference.Model = msg.Model
		k.RecordInferencePrice(goCtx, &existingInference, msg.InferenceId)
	} else if existingInference.Model == "" {
		k.LogError("FinishInference: model not set by the processed start message", types.Inferences,
			"inferenceId", msg.InferenceId,
			"executedBy", msg.ExecutedBy)
	} else if existingInference.Model != msg.Model {
		k.LogError("FinishInference: model mismatch", types.Inferences,
			"inferenceId", msg.InferenceId,
			"existingInference.Model", existingInference.Model,
			"msg.Model", msg.Model)
	}

	blockContext := calculations.BlockContext{
		BlockHeight:    ctx.BlockHeight(),
		BlockTimestamp: ctx.BlockTime().UnixMilli(),
	}

	inference, payments, err := calculations.ProcessFinishInference(&existingInference, msg, blockContext, k)
	if err != nil {
		return failedFinish(ctx, err, msg), nil
	}

	finalInference, err := k.processInferencePayments(ctx, inference, payments, true)
	if err != nil {
		return failedFinish(ctx, err, msg), nil
	}
	err = k.SetInference(ctx, *finalInference)
	if err != nil {
		return failedFinish(ctx, err, msg), nil
	}
	if existingInference.IsCompleted() {
		err := k.handleInferenceCompleted(ctx, finalInference)
		if err != nil {
			return failedFinish(ctx, err, msg), nil
		}
	}

	return &types.MsgFinishInferenceResponse{InferenceIndex: msg.InferenceId}, nil
}

func failedFinish(ctx sdk.Context, err error, msg *types.MsgFinishInference) *types.MsgFinishInferenceResponse {
	ctx.EventManager().EmitEvent(
		sdk.NewEvent("finish_inference",
			sdk.NewAttribute("result", "failed")))
	return &types.MsgFinishInferenceResponse{
		InferenceIndex: msg.InferenceId,
		ErrorMessage:   err.Error(),
	}
}

func (k msgServer) verifyFinishKeys(ctx sdk.Context, msg *types.MsgFinishInference, transferAgent *types.Participant, requestor *types.Participant) error {
	// Hash-based signature verification (post-upgrade flow)
	// Dev signs: original_prompt_hash + timestamp + ta_address
	// TA signs: prompt_hash + timestamp + ta_address + executor_address
	devComponents := getFinishDevSignatureComponents(msg)
	taComponents := getFinishTASignatureComponents(msg)

	// Extra seconds for long-running inferences; deduping via inferenceId is primary replay defense
	if err := k.validateTimestamp(ctx, devComponents, msg.InferenceId, 60*60); err != nil {
		return err
	}

	// Verify dev signature (original_prompt_hash)
	if err := calculations.VerifyKeys(ctx, devComponents, calculations.SignatureData{
		DevSignature: msg.InferenceId, Dev: requestor,
	}, k); err != nil {
		k.LogError("FinishInference: dev signature failed", types.Inferences, "error", err)
		return err
	}

	// Verify TA signature (prompt_hash)
	if err := k.verifyTASignature(ctx, msg, taComponents, transferAgent); err != nil {
		return err
	}

	return nil
}

// verifyTASignature verifies TA signature using prompt_hash.
// Includes upgrade-epoch fallback for inferences started before hash-based signing.
func (k msgServer) verifyTASignature(ctx sdk.Context, msg *types.MsgFinishInference, taComponents calculations.SignatureComponents, transferAgent *types.Participant) error {
	err := calculations.VerifyKeys(ctx, taComponents, calculations.SignatureData{
		TransferSignature: msg.TransferSignature, TransferAgent: transferAgent,
	}, k)
	if err == nil {
		return nil
	}

	// Upgrade-epoch fallback: inferences started before hash-based signing use original_prompt_hash
	// This path will be removed after upgrade epoch completes
	directComponents := calculations.SignatureComponents{
		Payload:         msg.OriginalPromptHash,
		Timestamp:       msg.RequestTimestamp,
		TransferAddress: msg.TransferredBy,
		ExecutorAddress: msg.ExecutedBy,
	}
	if fallbackErr := calculations.VerifyKeys(ctx, directComponents, calculations.SignatureData{
		TransferSignature: msg.TransferSignature, TransferAgent: transferAgent,
	}, k); fallbackErr != nil {
		k.LogError("FinishInference: TA signature failed", types.Inferences, "promptHashErr", err, "fallbackErr", fallbackErr)
		return err
	}

	k.LogDebug("FinishInference: Using upgrade-epoch fallback for TA signature", types.Inferences, "inferenceId", msg.InferenceId)
	return nil
}

// getFinishDevSignatureComponents returns components for dev signature verification
// Dev signs: original_prompt_hash + timestamp + ta_address (no executor)
func getFinishDevSignatureComponents(msg *types.MsgFinishInference) calculations.SignatureComponents {
	return calculations.SignatureComponents{
		Payload:         msg.OriginalPromptHash,
		Timestamp:       msg.RequestTimestamp,
		TransferAddress: msg.TransferredBy,
		ExecutorAddress: "", // Dev doesn't include executor address
	}
}

// getFinishTASignatureComponents returns components for TA/Executor signature verification
// TA/Executor sign: prompt_hash + timestamp + ta_address + executor_address
func getFinishTASignatureComponents(msg *types.MsgFinishInference) calculations.SignatureComponents {
	return calculations.SignatureComponents{
		Payload:         msg.PromptHash,
		Timestamp:       msg.RequestTimestamp,
		TransferAddress: msg.TransferredBy,
		ExecutorAddress: msg.ExecutedBy,
	}
}

func compareFinishDevComponents(msg *types.MsgFinishInference, inference *types.Inference) error {
	if inference.OriginalPromptHash != msg.OriginalPromptHash {
		return sdkerrors.Wrapf(
			types.ErrDevComponentMismatch,
			"original_prompt_hash mismatch: finish=%s start=%s",
			msg.OriginalPromptHash,
			inference.OriginalPromptHash,
		)
	}
	if inference.RequestTimestamp != msg.RequestTimestamp {
		return sdkerrors.Wrapf(
			types.ErrDevComponentMismatch,
			"request_timestamp mismatch: finish=%d start=%d",
			msg.RequestTimestamp,
			inference.RequestTimestamp,
		)
	}
	if inference.TransferredBy != msg.TransferredBy {
		return sdkerrors.Wrapf(
			types.ErrDevComponentMismatch,
			"transfer agent mismatch: finish=%s start=%s",
			msg.TransferredBy,
			inference.TransferredBy,
		)
	}
	return nil
}

func compareFinishTAComponents(msg *types.MsgFinishInference, inference *types.Inference) error {
	if inference.PromptHash != msg.PromptHash {
		return sdkerrors.Wrapf(
			types.ErrTAComponentMismatch,
			"prompt_hash mismatch: finish=%s start=%s",
			msg.PromptHash,
			inference.PromptHash,
		)
	}
	if inference.RequestTimestamp != msg.RequestTimestamp {
		return sdkerrors.Wrapf(
			types.ErrTAComponentMismatch,
			"request_timestamp mismatch: finish=%d start=%d",
			msg.RequestTimestamp,
			inference.RequestTimestamp,
		)
	}
	if inference.TransferredBy != msg.TransferredBy {
		return sdkerrors.Wrapf(
			types.ErrTAComponentMismatch,
			"transfer agent mismatch: finish=%s start=%s",
			msg.TransferredBy,
			inference.TransferredBy,
		)
	}
	if inference.AssignedTo != "" && inference.AssignedTo != msg.ExecutedBy {
		return sdkerrors.Wrapf(
			types.ErrTAComponentMismatch,
			"executor mismatch: finish.executed_by=%s start.assigned_to=%s",
			msg.ExecutedBy,
			inference.AssignedTo,
		)
	}
	return nil
}

func compareFinishRoleFields(msg *types.MsgFinishInference, inference *types.Inference) error {
	if inference.RequestedBy != msg.RequestedBy {
		return sdkerrors.Wrapf(
			types.ErrInferenceRoleMismatch,
			"requested_by mismatch: finish=%s start=%s",
			msg.RequestedBy,
			inference.RequestedBy,
		)
	}
	if inference.Model != "" && inference.Model != msg.Model {
		return sdkerrors.Wrapf(
			types.ErrInferenceRoleMismatch,
			"model mismatch: finish=%s start=%s",
			msg.Model,
			inference.Model,
		)
	}
	return nil
}

func (k msgServer) handleInferenceCompleted(ctx sdk.Context, existingInference *types.Inference) error {
	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			"inference_finished",
			sdk.NewAttribute("inference_id", existingInference.InferenceId),
		),
	)

	executedBy := existingInference.ExecutedBy
	executor, found := k.GetParticipant(ctx, executedBy)
	if !found {
		k.LogError("handleInferenceCompleted: executor not found", types.Inferences, "executed_by", executedBy)
	} else {
		executor.CurrentEpochStats.InferenceCount++
		executor.LastInferenceTime = existingInference.EndBlockTimestamp
		if err := k.SetParticipant(ctx, executor); err != nil {
			return err
		}

	}

	effectiveEpoch, found := k.GetEffectiveEpoch(ctx)
	if !found {
		k.LogError("Effective Epoch Index not found", types.EpochGroup)
		return types.ErrEffectiveEpochNotFound.Wrapf("handleInferenceCompleted: Effective Epoch Index not found")
	}
	currentEpochGroup, err := k.GetEpochGroupForEpoch(ctx, *effectiveEpoch)
	if err != nil {
		k.LogError("Unable to get current Epoch Group", types.EpochGroup, "err", err)
		return err
	}

	existingInference.EpochPocStartBlockHeight = uint64(effectiveEpoch.PocStartBlockHeight)
	existingInference.EpochId = effectiveEpoch.Index
	currentEpochGroup.GroupData.NumberOfRequests++

	executorPower := uint64(0)
	executorReputation := int32(0)
	for _, weight := range currentEpochGroup.GroupData.ValidationWeights {
		if weight.MemberAddress == existingInference.ExecutedBy {
			executorPower = uint64(weight.Weight)
			executorReputation = weight.Reputation
			break
		}
	}

	modelEpochGroup, err := currentEpochGroup.GetSubGroup(ctx, existingInference.Model)
	if err != nil {
		k.LogError("Unable to get model Epoch Group", types.EpochGroup, "err", err)
		return err
	}

	inferenceDetails := types.InferenceValidationDetails{
		InferenceId:          existingInference.InferenceId,
		ExecutorId:           existingInference.ExecutedBy,
		ExecutorReputation:   executorReputation,
		TrafficBasis:         uint64(math.Max(currentEpochGroup.GroupData.NumberOfRequests, currentEpochGroup.GroupData.PreviousEpochRequests)),
		ExecutorPower:        executorPower,
		EpochId:              effectiveEpoch.Index,
		Model:                existingInference.Model,
		TotalPower:           uint64(modelEpochGroup.GroupData.TotalWeight),
		CreatedAtBlockHeight: ctx.BlockHeight(),
	}
	if inferenceDetails.TotalPower == inferenceDetails.ExecutorPower {
		k.LogWarn("Executor Power equals Total Power", types.Validation,
			"model", existingInference.Model,
			"epoch_id", currentEpochGroup.GroupData.EpochGroupId,
			"epoch_start_block_height", currentEpochGroup.GroupData.PocStartBlockHeight,
			"group_id", modelEpochGroup.GroupData.EpochGroupId,
			"inference_id", existingInference.InferenceId,
			"executor_id", inferenceDetails.ExecutorId,
			"executor_power", inferenceDetails.ExecutorPower,
		)
	}
	k.LogDebug(
		"Adding Inference Validation Details",
		types.Validation,
		"inference_id", inferenceDetails.InferenceId,
		"epoch_id", inferenceDetails.EpochId,
		"executor_id", inferenceDetails.ExecutorId,
		"executor_power", inferenceDetails.ExecutorPower,
		"executor_reputation", inferenceDetails.ExecutorReputation,
		"traffic_basis", inferenceDetails.TrafficBasis,
	)
	k.SetInferenceValidationDetails(ctx, inferenceDetails)
	err = k.SetInference(ctx, *existingInference)
	if err != nil {
		return err
	}
	k.SetEpochGroupData(ctx, *currentEpochGroup.GroupData)
	return nil
}
