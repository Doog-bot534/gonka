package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

const PocFailureTag = "[PoC Failure]"

func (k msgServer) SubmitPocValidation(goCtx context.Context, msg *types.MsgSubmitPocValidation) (*types.MsgSubmitPocValidationResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	// PoC period validation is performed in the ante handler (PocPeriodValidationDecorator)
	// No need to duplicate validation here
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
