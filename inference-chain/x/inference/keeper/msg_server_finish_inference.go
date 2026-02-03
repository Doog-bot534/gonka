package keeper

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"time"

	sdkerrors "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) FinishInference(goCtx context.Context, msg *types.MsgFinishInference) (*types.MsgFinishInferenceResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	startTime := time.Now()

	k.LogInfo("FinishInference", types.Inferences, "inference_id", msg.InferenceId, "executed_by", msg.ExecutedBy, "created_by", msg.Creator)

	if msg.PromptTokenCount > types.MaxAllowedTokens {
		return failedFinish(ctx, sdkerrors.Wrapf(types.ErrTokenCountOutOfRange, "prompt_token_count exceeds limit (%d > %d)", msg.PromptTokenCount, types.MaxAllowedTokens), msg), nil
	}
	if msg.CompletionTokenCount > types.MaxAllowedTokens {
		return failedFinish(ctx, sdkerrors.Wrapf(types.ErrTokenCountOutOfRange, "completion_token_count exceeds limit (%d > %d)", msg.CompletionTokenCount, types.MaxAllowedTokens), msg), nil
	}

	// Developer access gating: until cutoff height only allowlisted developers may run inference flows.
	// We gate by the original requester (developer), not the executor/TA.
	devAccessStart := time.Now()
	if k.IsDeveloperAccessRestricted(ctx, ctx.BlockHeight()) && !k.IsAllowedDeveloper(ctx, msg.RequestedBy) {
		k.LogError("FinishInference: developer is not allowlisted at this height", types.Inferences, "developer", msg.RequestedBy, "blockHeight", ctx.BlockHeight())
		return failedFinish(ctx, sdkerrors.Wrap(types.ErrDeveloperNotAllowlisted, msg.RequestedBy), msg), nil
	}
	k.LogInfo("FinishInference: developer access check complete", types.Inferences, "inference_id", msg.InferenceId, "duration_ms", durationMs(devAccessStart))

	// Transfer Agent access gating: only allowlisted TAs may be involved in inferences.
	taAccessStart := time.Now()
	if k.IsTransferAgentRestricted(ctx) && !k.IsAllowedTransferAgent(ctx, msg.TransferredBy) {
		k.LogError("FinishInference: transfer agent is not allowlisted", types.Inferences,
			"transferAgent", msg.TransferredBy, "blockHeight", ctx.BlockHeight())
		return failedFinish(ctx, sdkerrors.Wrap(types.ErrTransferAgentNotAllowlisted, msg.TransferredBy), msg), nil
	}
	k.LogInfo("FinishInference: transfer agent access check complete", types.Inferences, "inference_id", msg.InferenceId, "duration_ms", durationMs(taAccessStart))

	participantsStart := time.Now()
	executor, found := k.GetParticipant(ctx, msg.ExecutedBy)
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
	k.LogInfo("FinishInference: participants fetched", types.Inferences, "inference_id", msg.InferenceId, "duration_ms", durationMs(participantsStart))

	verifyStart := time.Now()
	err := k.verifyFinishKeys(ctx, msg, &transferAgent, &requestor, &executor)
	if err != nil {
		k.LogError("FinishInference: verifyKeys failed", types.Inferences, "error", err)
		return failedFinish(ctx, sdkerrors.Wrap(types.ErrInvalidSignature, err.Error()), msg), nil
	}
	k.LogInfo("FinishInference: verifyKeys complete", types.Inferences, "inference_id", msg.InferenceId, "duration_ms", durationMs(verifyStart))

	getInferenceStart := time.Now()
	existingInference, found := k.GetInference(ctx, msg.InferenceId)
	k.LogInfo("FinishInference: GetInference complete", types.Inferences, "inference_id", msg.InferenceId, "found", found, "duration_ms", durationMs(getInferenceStart))

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

	// Record the current price only if this is the first message (StartInference not processed yet)
	// This ensures consistent pricing regardless of message arrival order
	if !existingInference.StartProcessed() {
		priceStart := time.Now()
		existingInference.Model = msg.Model
		k.RecordInferencePrice(goCtx, &existingInference, msg.InferenceId)
		k.LogInfo("FinishInference: RecordInferencePrice complete", types.Inferences, "inference_id", msg.InferenceId, "duration_ms", durationMs(priceStart))
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

	processStart := time.Now()
	inference, payments, err := calculations.ProcessFinishInference(&existingInference, msg, blockContext, k)
	if err != nil {
		return failedFinish(ctx, err, msg), nil
	}
	k.LogInfo("FinishInference: ProcessFinishInference complete", types.Inferences, "inference_id", msg.InferenceId, "duration_ms", durationMs(processStart))

	paymentsStart := time.Now()
	finalInference, err := k.processInferencePayments(ctx, inference, payments, true)
	if err != nil {
		return failedFinish(ctx, err, msg), nil
	}
	k.LogInfo("FinishInference: processInferencePayments complete", types.Inferences, "inference_id", msg.InferenceId, "duration_ms", durationMs(paymentsStart))
	setInferenceStart := time.Now()
	err = k.SetInference(ctx, *finalInference)
	if err != nil {
		return failedFinish(ctx, err, msg), nil
	}
	k.LogInfo("FinishInference: SetInference complete", types.Inferences, "inference_id", msg.InferenceId, "duration_ms", durationMs(setInferenceStart))
	if existingInference.IsCompleted() {
		completedStart := time.Now()
		err := k.handleInferenceCompleted(ctx, finalInference)
		if err != nil {
			return failedFinish(ctx, err, msg), nil
		}
		k.LogInfo("FinishInference: handleInferenceCompleted complete", types.Inferences, "inference_id", msg.InferenceId, "duration_ms", durationMs(completedStart))
	}
	emitInferenceFinishedRandomEvent(ctx, msg, startTime)
	k.LogInfo("FinishInference: completed", types.Inferences, "inference_id", msg.InferenceId, "duration_ms", durationMs(startTime))

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

func (k msgServer) verifyFinishKeys(ctx sdk.Context, msg *types.MsgFinishInference, transferAgent *types.Participant, requestor *types.Participant, executor *types.Participant) error {
	// Hash-based signature verification (post-upgrade flow)
	// Dev signs: original_prompt_hash + timestamp + ta_address
	// TA signs: prompt_hash + timestamp + ta_address + executor_address
	// Executor signs: prompt_hash + timestamp + ta_address + executor_address
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

	// Verify Executor signature (prompt_hash)
	if err := calculations.VerifyKeys(ctx, taComponents, calculations.SignatureData{
		ExecutorSignature: msg.ExecutorSignature, Executor: executor,
	}, k); err != nil {
		k.LogError("FinishInference: Executor signature failed", types.Inferences, "error", err)
		return err
	}

	return nil
}

func emitInferenceFinishedRandomEvent(ctx sdk.Context, msg *types.MsgFinishInference, start time.Time) {
	baseSeed := os.Getenv("INFERENCE_FINISHED_RND_SEED")
	if baseSeed == "" {
		if host, err := os.Hostname(); err == nil {
			baseSeed = host
		}
	}

	seedInput := fmt.Sprintf("%s|%s|%d", baseSeed, msg.InferenceId, ctx.BlockHeight())
	hash := sha256.Sum256([]byte(seedInput))
	seed := int64(binary.BigEndian.Uint64(hash[:8]))
	rng := rand.New(rand.NewSource(seed))
	value := rng.Int63()

	emitStart := time.Now()
	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			"inference_finished_rnd",
			sdk.NewAttribute("inference_id", msg.InferenceId),
			sdk.NewAttribute("value", fmt.Sprintf("%d", value)),
			sdk.NewAttribute("duration_since_start_ms", fmt.Sprintf("%.3f", durationMsBetween(start, time.Now()))),
		),
	)
	ctx.Logger().Info("FinishInference: inference_finished_rnd event emitted",
		"inference_id", msg.InferenceId,
		"duration_ms", durationMs(emitStart),
	)
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
