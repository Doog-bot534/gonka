package keeper_test

import (
	"testing"

	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestMsgServer_FinishFirst_SignatureFieldsPersistedForStartComparison(t *testing.T) {
	inferenceHelper, k, _ := NewMockInferenceHelper(t)
	requestTimestamp := inferenceHelper.context.BlockTime().UnixNano()

	originalPromptHash, promptHash, inferenceID, taSignature, executorSignature := buildInferenceSignatures(
		t,
		inferenceHelper.MockRequester,
		inferenceHelper.MockTransferAgent,
		inferenceHelper.MockExecutor,
		"promptPayload",
		requestTimestamp,
	)

	inferenceHelper.Mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), inferenceHelper.MockRequester.GetBechAddress()).Return(inferenceHelper.MockRequester).AnyTimes()
	inferenceHelper.Mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), inferenceHelper.MockTransferAgent.GetBechAddress()).Return(inferenceHelper.MockTransferAgent).AnyTimes()
	inferenceHelper.Mocks.AuthzKeeper.EXPECT().GranterGrants(gomock.Any(), gomock.Any()).Return(&authztypes.QueryGranterGrantsResponse{Grants: []*authztypes.GrantAuthorization{}}, nil).AnyTimes()
	inferenceHelper.Mocks.BankKeeper.ExpectAny(inferenceHelper.context)

	// 1) Process finish first — fields should now be persisted.
	finishResp, err := inferenceHelper.MessageServer.FinishInference(inferenceHelper.context, &types.MsgFinishInference{
		Creator:              inferenceHelper.MockExecutor.address,
		InferenceId:          inferenceID,
		ResponseHash:         "responseHash",
		ResponsePayload:      "responsePayload",
		PromptTokenCount:     10,
		CompletionTokenCount: 20,
		ExecutedBy:           inferenceHelper.MockExecutor.address,
		TransferredBy:        inferenceHelper.MockTransferAgent.address,
		RequestTimestamp:     requestTimestamp,
		TransferSignature:    taSignature,
		ExecutorSignature:    executorSignature,
		RequestedBy:          inferenceHelper.MockRequester.address,
		OriginalPrompt:       "promptPayload",
		PromptHash:           promptHash,
		OriginalPromptHash:   originalPromptHash,
		Model:                "model1",
	})
	require.NoError(t, err)
	require.Empty(t, finishResp.ErrorMessage)

	savedInference, found := k.GetInference(inferenceHelper.context, inferenceID)
	require.True(t, found)
	require.Equal(t, promptHash, savedInference.PromptHash)
	require.Equal(t, originalPromptHash, savedInference.OriginalPromptHash)
	require.Equal(t, inferenceHelper.MockRequester.address, savedInference.RequestedBy)
	require.Equal(t, inferenceHelper.MockExecutor.address, savedInference.ExecutedBy)

	// 2) Process start second — comparison should now pass.
	startResp, err := inferenceHelper.MessageServer.StartInference(inferenceHelper.context, &types.MsgStartInference{
		InferenceId:        inferenceID,
		PromptHash:         promptHash,
		PromptPayload:      "promptPayload",
		RequestedBy:        inferenceHelper.MockRequester.address,
		Creator:            inferenceHelper.MockTransferAgent.address,
		Model:              "model1",
		OriginalPrompt:     "promptPayload",
		OriginalPromptHash: originalPromptHash,
		RequestTimestamp:   requestTimestamp,
		TransferSignature:  taSignature,
		AssignedTo:         inferenceHelper.MockExecutor.address,
		MaxTokens:          calculations.DefaultMaxTokens,
	})
	require.NoError(t, err)
	require.Empty(t, startResp.ErrorMessage)
}
