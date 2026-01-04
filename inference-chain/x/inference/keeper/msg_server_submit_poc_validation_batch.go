package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SubmitPocValidationBatch(goCtx context.Context, msg *types.MsgSubmitPocValidationBatch) (*types.MsgSubmitPocValidationBatchResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	for _, pocValidation := range msg.Data {
		_, err := k.ProcessValidation(ctx, pocValidation, msg.Creator)
		if err != nil {
			k.LogError("Failed to process PoC validation", types.PoC, "error", err, "participant", pocValidation.ParticipantAddress, "creator", msg.Creator, "fraudDetected", pocValidation.FraudDetected)
			continue
		}
	}

	return &types.MsgSubmitPocValidationBatchResponse{}, nil
}
