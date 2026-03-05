package inference

import (
	"fmt"
	"time"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/tx"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	blstypes "github.com/productscience/inference/x/bls/types"
	"github.com/productscience/inference/x/inference/types"
)

var InferenceOperationKeyPerms = []sdk.Msg{
	&types.MsgStartInference{},
	&types.MsgFinishInference{},
	&types.MsgClaimRewards{},
	&types.MsgValidation{},
	&types.MsgSubmitPocBatch{},
	&types.MsgSubmitPocValidation{},
	&types.MsgSubmitPocValidationsV2{},   // PoC v2 validations
	&types.MsgPoCV2StoreCommit{},         // PoC v2 off-chain store commits
	&types.MsgMLNodeWeightDistribution{}, // PoC v2 ML node weight distribution
	&types.MsgSubmitSeed{},
	&types.MsgBridgeExchange{},
	&types.MsgSubmitTrainingKvRecord{},
	&types.MsgJoinTraining{},
	&types.MsgJoinTrainingStatus{},
	&types.MsgTrainingHeartbeat{},
	&types.MsgSetBarrier{},
	&types.MsgClaimTrainingTaskForAssignment{},
	&types.MsgAssignTrainingTask{},
	&types.MsgSubmitNewUnfundedParticipant{},
	&types.MsgSubmitHardwareDiff{},
	&types.MsgInvalidateInference{},
	&types.MsgRevalidateInference{},
	&blstypes.MsgSubmitDealerPart{},
	&blstypes.MsgSubmitVerificationVector{},
	&blstypes.MsgRequestThresholdSignature{},
	&blstypes.MsgSubmitPartialSignature{},
	&blstypes.MsgSubmitGroupKeyValidationSignature{},
}

func GrantMLOperationalKeyPermissions(
	clientCtx client.Context,
	txFactory tx.Factory,
	operatorAddress sdk.AccAddress,
	mlOperationalAddress sdk.AccAddress,
	expiration *time.Time,
) error {
	var expirationTime time.Time
	if expiration != nil {
		expirationTime = *expiration
	} else {
		expirationTime = time.Now().Add(365 * 24 * time.Hour)
	}

	var grantMsgs []sdk.Msg
	for _, msgType := range InferenceOperationKeyPerms {
		authorization := authztypes.NewGenericAuthorization(sdk.MsgTypeURL(msgType))
		grantMsg, err := authztypes.NewMsgGrant(
			operatorAddress,
			mlOperationalAddress,
			authorization,
			&expirationTime,
		)
		if err != nil {
			return fmt.Errorf("failed to create MsgGrant for %s: %w", sdk.MsgTypeURL(msgType), err)
		}
		grantMsgs = append(grantMsgs, grantMsg)
	}

	return tx.GenerateOrBroadcastTxWithFactory(clientCtx, txFactory, grantMsgs...)
}
