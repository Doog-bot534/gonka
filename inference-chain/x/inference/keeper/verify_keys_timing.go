package keeper

import (
	"fmt"
	"time"

	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) verifySignatureWithTiming(
	ctx sdk.Context,
	components calculations.SignatureComponents,
	signatureType calculations.SignatureType,
	signature string,
	participant *types.Participant,
	useGrantees bool,
	inferenceField string,
	inferenceId string,
	source string,
	label string,
) error {
	if participant == nil || signature == "" {
		return nil
	}

	fetchStart := time.Now()
	if useGrantees {
		pubKeys, err := k.GetAccountPubKeysWithGrantees(ctx, participant.Address)
		if err != nil {
			k.LogError(fmt.Sprintf("%s: verifyKeys %s pubkey fetch failed", source, label), types.Inferences,
				inferenceField, inferenceId,
				"participant", participant.Address,
				"error", err,
			)
			return sdkerrors.Wrap(types.ErrParticipantNotFound, participant.Address)
		}
		k.LogInfo(fmt.Sprintf("%s: verifyKeys %s pubkey fetch complete", source, label), types.Inferences,
			inferenceField, inferenceId,
			"participant", participant.Address,
			"key_count", len(pubKeys),
			"duration_ms", durationMs(fetchStart),
		)

		validateStart := time.Now()
		err = calculations.ValidateSignatureWithGrantees(components, signatureType, pubKeys, signature)
		k.LogInfo(fmt.Sprintf("%s: verifyKeys %s signature validation complete", source, label), types.Inferences,
			inferenceField, inferenceId,
			"participant", participant.Address,
			"duration_ms", durationMs(validateStart),
			"success", err == nil,
		)
		if err != nil {
			return sdkerrors.Wrap(types.ErrInvalidSignature, fmt.Sprintf("%s signature validation failed", label))
		}
		return nil
	}

	pubKey, err := k.GetAccountPubKey(ctx, participant.Address)
	if err != nil {
		k.LogError(fmt.Sprintf("%s: verifyKeys %s pubkey fetch failed", source, label), types.Inferences,
			inferenceField, inferenceId,
			"participant", participant.Address,
			"error", err,
		)
		return sdkerrors.Wrap(types.ErrParticipantNotFound, participant.Address)
	}
	k.LogInfo(fmt.Sprintf("%s: verifyKeys %s pubkey fetch complete", source, label), types.Inferences,
		inferenceField, inferenceId,
		"participant", participant.Address,
		"duration_ms", durationMs(fetchStart),
	)

	validateStart := time.Now()
	err = calculations.ValidateSignature(components, signatureType, pubKey, signature)
	k.LogInfo(fmt.Sprintf("%s: verifyKeys %s signature validation complete", source, label), types.Inferences,
		inferenceField, inferenceId,
		"participant", participant.Address,
		"duration_ms", durationMs(validateStart),
		"success", err == nil,
	)
	if err != nil {
		return sdkerrors.Wrap(types.ErrInvalidSignature, fmt.Sprintf("%s signature validation failed", label))
	}
	return nil
}
