package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

const PocFailureTag = "[PoC Failure]"

func (k msgServer) SubmitPocValidation(goCtx context.Context, msg *types.MsgSubmitPocValidation) (*types.MsgSubmitPocValidationResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	// Defense-in-depth: Validate PoC period even though AnteHandler should catch this
	// This ensures validation occurs even if the message was nested and bypassed the AnteHandler
	if err := k.ValidatePocPeriod(ctx, msg.PocStageStartBlockHeight, PocWindowValidation); err != nil {
		k.LogError(PocFailureTag+"[SubmitPocValidation] PoC period validation failed", types.PoC,
			"participant", msg.ParticipantAddress,
			"validatorParticipant", msg.Creator,
			"pocStageStartBlockHeight", msg.PocStageStartBlockHeight,
			"error", err)
		return nil, err
	}

	currentBlockHeight := ctx.BlockHeight()

	validation := toPoCValidation(msg, currentBlockHeight)
	k.SetPoCValidation(ctx, *validation)

	return &types.MsgSubmitPocValidationResponse{}, nil
}

func toPoCValidation(msg *types.MsgSubmitPocValidation, currentBlockHeight int64) *types.PoCValidation {
	return &types.PoCValidation{
		ParticipantAddress:          msg.ParticipantAddress,
		ValidatorParticipantAddress: msg.Creator,
		PocStageStartBlockHeight:    msg.PocStageStartBlockHeight,
		ValidatedAtBlockHeight:      currentBlockHeight,
		Nonces:                      msg.Nonces,
		Dist:                        msg.Dist,
		ReceivedDist:                msg.ReceivedDist,
		RTarget:                     msg.RTarget,
		FraudThreshold:              msg.FraudThreshold,
		NInvalid:                    msg.NInvalid,
		ProbabilityHonest:           msg.ProbabilityHonest,
		FraudDetected:               msg.FraudDetected,
	}
}
