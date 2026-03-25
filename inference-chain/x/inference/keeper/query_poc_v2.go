package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"github.com/productscience/inference/x/inference/utils"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// PocV2ValidationsForStage returns all PoC v2 validations for a given stage.
func (k Keeper) PocV2ValidationsForStage(goCtx context.Context, req *types.QueryPocV2ValidationsForStageRequest) (*types.QueryPocV2ValidationsForStageResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	pocValidations, err := k.GetPoCValidationsV2ByStage(ctx, req.BlockHeight)
	if err != nil {
		k.LogError("failed to get PoC v2 validations", types.PoC, "err", err)
		return nil, status.Error(codes.Internal, "failed to get PoC v2 validations")
	}

	grouped := make(map[string]map[string][]types.PoCValidationV2)
	for _, validations := range pocValidations {
		for _, validation := range validations {
			participantGroups, found := grouped[validation.ParticipantAddress]
			if !found {
				participantGroups = make(map[string][]types.PoCValidationV2)
				grouped[validation.ParticipantAddress] = participantGroups
			}
			participantGroups[validation.ModelId] = append(participantGroups[validation.ModelId], validation)
		}
	}

	pocValidationsWithParticipants := make([]types.PoCValidationsWithParticipantsV2, 0, len(grouped))
	for participantIndex, byModel := range grouped {
		addr, err := sdk.AccAddressFromBech32(participantIndex)
		if err != nil {
			k.LogError("PocV2ValidationsForStage. Invalid address", types.PoC, "address", participantIndex, "err", err)
			continue
		}

		acc := k.AccountKeeper.GetAccount(ctx, addr)
		if acc == nil {
			k.LogError("PocV2ValidationsForStage. Account not found", types.PoC, "address", participantIndex)
			continue
		}

		pubKey := acc.GetPubKey()
		if pubKey == nil {
			k.LogError("PocV2ValidationsForStage. PubKey not found", types.PoC, "address", participantIndex)
			continue
		}

		for modelID, validations := range byModel {
			pocValidationsWithParticipants = append(pocValidationsWithParticipants, types.PoCValidationsWithParticipantsV2{
				Participant:   participantIndex,
				PocValidation: validations,
				PubKey:        utils.PubKeyToString(pubKey),
				HexPubKey:     utils.PubKeyToHexString(pubKey),
				ModelId:       modelID,
			})
		}
	}

	return &types.QueryPocV2ValidationsForStageResponse{
		PocValidation: pocValidationsWithParticipants,
	}, nil
}
