package keeper_test

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"context"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/ethereum/go-ethereum/crypto/secp256k1"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestMsgServer_InferenceTiming(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test skipped with -short")
	}

	const (
		epochId         = 1
		iterations      = 100
		numParticipants = 1000
		numModels       = 5
		nodesPerPart    = 50
	)

	inferenceHelper, k, ctx := NewMockInferenceHelper(t)
	requestTimestamp := inferenceHelper.context.BlockTime().UnixNano()
	initialBlockHeight := int64(10)
	ctx, err := advanceEpoch(ctx, &k, inferenceHelper.Mocks, initialBlockHeight, epochId)
	require.NoError(t, err)

	// Create real accounts for signing
	devAcc := NewMockAccount(testutil.Requester)
	taAcc := NewMockAccount(testutil.Creator)
	execAcc := NewMockAccount(testutil.Executor)

	// Create grantee accounts by reusing the same valid addresses but in different roles
	devGrantee := execAcc
	taGrantee := devAcc
	execGrantee := taAcc

	// 1. Create a massive EpochGroupData and populate Participant store
	fmt.Printf("Simulating Large Epoch Group: %d participants, %d total nodes\n", numParticipants, numParticipants*nodesPerPart)

	valWeights := make([]*types.ValidationWeight, numParticipants)
	for i := 0; i < numParticipants; i++ {
		addr := testutil.Requester
		if i > 0 {
			addr = testutil.Executor
		}
		if i > 1 {
			addr = testutil.Creator
		}
		if i > 2 {
			addr = testutil.Requester
		}

		mlNodes := make([]*types.MLNodeInfo, nodesPerPart)
		for j := 0; j < nodesPerPart; j++ {
			mlNodes[j] = &types.MLNodeInfo{
				NodeId:     fmt.Sprintf("node-%d-%d", i, j),
				Throughput: 100,
				PocWeight:  10,
			}
		}

		valWeights[i] = &types.ValidationWeight{
			MemberAddress: addr,
			Weight:        1000,
			Reputation:    100,
			MlNodes:       mlNodes,
		}
	}

	// Register valid ones
	for _, addr := range []string{testutil.Requester, testutil.Executor, testutil.Creator} {
		k.SetParticipant(ctx, types.Participant{
			Address: addr,
			Status:  types.ParticipantStatus_ACTIVE,
		})
	}

	// Setup Authz Mock to return one grantee for each account
	inferenceHelper.Mocks.AuthzKeeper.EXPECT().GranterGrants(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, req *authztypes.QueryGranterGrantsRequest) (*authztypes.QueryGranterGrantsResponse, error) {
			grantee := ""
			switch req.Granter {
			case testutil.Requester:
				grantee = devGrantee.address
			case testutil.Creator:
				grantee = taGrantee.address
			case testutil.Executor:
				grantee = execGrantee.address
			}

			if grantee == "" {
				return &authztypes.QueryGranterGrantsResponse{Grants: []*authztypes.GrantAuthorization{}}, nil
			}

			genericAuth := authztypes.NewGenericAuthorization("/inference.inference.MsgStartInference")
			anyAuth, _ := codectypes.NewAnyWithValue(genericAuth)

			return &authztypes.QueryGranterGrantsResponse{
				Grants: []*authztypes.GrantAuthorization{
					{
						Granter:       req.Granter,
						Grantee:       grantee,
						Authorization: anyAuth,
					},
				},
			}, nil
		}).AnyTimes()

	modelIds := make([]string, numModels)
	for i := 0; i < numModels; i++ {
		modelIds[i] = fmt.Sprintf("model-%d", i)
		k.SetModel(ctx, &types.Model{Id: modelIds[i]})
	}

	largeGroupData := types.EpochGroupData{
		EpochIndex:          epochId,
		PocStartBlockHeight: uint64(initialBlockHeight),
		ModelId:             "",
		SubGroupModels:      modelIds,
		ValidationWeights:   valWeights,
		TotalWeight:         int64(numParticipants * 1000),
	}
	k.SetEpochGroupData(ctx, largeGroupData)

	for _, mId := range modelIds {
		subData := largeGroupData
		subData.ModelId = mId
		subData.SubGroupModels = nil
		k.SetEpochGroupData(ctx, subData)
	}

	modelId := modelIds[0]

	// Measure StartInference
	var startTotal time.Duration
	for i := 0; i < iterations; i++ {
		promptPayload := fmt.Sprintf("promptPayload-%d", i)
		originalPromptHash := sha256Hash(promptPayload)
		promptHash := sha256Hash(promptPayload)

		devSig, _ := calculations.Sign(devAcc, calculations.SignatureComponents{
			Payload: originalPromptHash, Timestamp: requestTimestamp, TransferAddress: testutil.Creator,
		}, calculations.Developer)

		taSig, _ := calculations.Sign(taAcc, calculations.SignatureComponents{
			Payload: promptHash, Timestamp: requestTimestamp, TransferAddress: testutil.Creator, ExecutorAddress: testutil.Executor,
		}, calculations.TransferAgent)

		msg := &types.MsgStartInference{
			InferenceId:        devSig,
			PromptHash:         promptHash,
			PromptPayload:      promptPayload,
			RequestedBy:        testutil.Requester,
			Creator:            testutil.Creator,
			Model:              modelId,
			OriginalPrompt:     promptPayload,
			OriginalPromptHash: originalPromptHash,
			RequestTimestamp:   requestTimestamp,
			TransferSignature:  taSig,
			AssignedTo:         testutil.Executor,
		}

		inferenceHelper.Mocks.BankKeeper.EXPECT().SendCoinsFromAccountToModule(gomock.Any(), gomock.Any(), types.ModuleName, gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
		inferenceHelper.Mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), devAcc.GetBechAddress()).Return(devAcc).AnyTimes()
		inferenceHelper.Mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), taAcc.GetBechAddress()).Return(taAcc).AnyTimes()
		inferenceHelper.Mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), execAcc.GetBechAddress()).Return(execAcc).AnyTimes()

		startTime := time.Now()
		_, err := inferenceHelper.MessageServer.StartInference(ctx, msg)
		startTotal += time.Since(startTime)
		require.NoError(t, err)
	}

	// Measure FinishInference
	var finishTotal time.Duration
	for i := 0; i < iterations; i++ {
		promptPayload := fmt.Sprintf("promptPayload-%d", i)
		originalPromptHash := sha256Hash(promptPayload)

		devSig, _ := calculations.Sign(devAcc, calculations.SignatureComponents{
			Payload: originalPromptHash, Timestamp: requestTimestamp, TransferAddress: testutil.Creator,
		}, calculations.Developer)

		inf, _ := k.GetInference(ctx, devSig)

		taSig, _ := calculations.Sign(taAcc, calculations.SignatureComponents{
			Payload: inf.PromptHash, Timestamp: requestTimestamp, TransferAddress: testutil.Creator, ExecutorAddress: testutil.Executor,
		}, calculations.TransferAgent)

		execSig, _ := calculations.Sign(execAcc, calculations.SignatureComponents{
			Payload: inf.PromptHash, Timestamp: requestTimestamp, TransferAddress: testutil.Creator, ExecutorAddress: testutil.Executor,
		}, calculations.ExecutorAgent)

		msg := &types.MsgFinishInference{
			Creator:              testutil.Executor,
			InferenceId:          devSig,
			ResponseHash:         "responseHash",
			ResponsePayload:      "responsePayload",
			PromptTokenCount:     10,
			CompletionTokenCount: 20,
			ExecutedBy:           testutil.Executor,
			TransferredBy:        testutil.Creator,
			RequestTimestamp:     requestTimestamp,
			TransferSignature:    taSig,
			ExecutorSignature:    execSig,
			RequestedBy:          testutil.Requester,
			OriginalPrompt:       promptPayload,
			Model:                modelId,
			PromptHash:           inf.PromptHash,
			OriginalPromptHash:   originalPromptHash,
		}

		inferenceHelper.Mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
		inferenceHelper.Mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), devAcc.GetBechAddress()).Return(devAcc).AnyTimes()
		inferenceHelper.Mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), taAcc.GetBechAddress()).Return(taAcc).AnyTimes()
		inferenceHelper.Mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), execAcc.GetBechAddress()).Return(execAcc).AnyTimes()

		startTime := time.Now()
		resp, err := inferenceHelper.MessageServer.FinishInference(ctx, msg)
		finishTotal += time.Since(startTime)
		require.NoError(t, err)
		require.Empty(t, resp.ErrorMessage)
	}

	fmt.Printf("\n--- TIMING RESULTS (Large Group: %d nodes, %d participants) ---\n", numParticipants*nodesPerPart, numParticipants)
	fmt.Printf("Average StartInference: %s\n", startTotal/iterations)
	fmt.Printf("Average FinishInference: %s\n", finishTotal/iterations)

	// Detailed sub-part measurements
	var parentGetTotal, subGetTotal, setDurationTotal time.Duration
	var participantGetTotal, participantSetTotal time.Duration
	var signatureVerifyEthTotal time.Duration
	var inferenceSetTotal time.Duration
	var getAccPubKeyTotal, getAccPubKeysWithGranteesTotal time.Duration

	for i := 0; i < iterations; i++ {
		start := time.Now()
		_, _ = k.GetEpochGroupData(ctx, epochId, "")
		parentGetTotal += time.Since(start)

		start = time.Now()
		_, _ = k.GetEpochGroupData(ctx, epochId, modelIds[0])
		subGetTotal += time.Since(start)

		start = time.Now()
		k.SetEpochGroupData(ctx, largeGroupData)
		setDurationTotal += time.Since(start)

		start = time.Now()
		_, _ = k.GetParticipant(ctx, testutil.Executor)
		participantGetTotal += time.Since(start)

		p, _ := k.GetParticipant(ctx, testutil.Executor)
		start = time.Now()
		_ = k.SetParticipant(ctx, p)
		participantSetTotal += time.Since(start)

		infObj := types.Inference{InferenceId: "test-inf"}
		start = time.Now()
		_ = k.SetInference(ctx, infObj)
		inferenceSetTotal += time.Since(start)

		promptPayload := fmt.Sprintf("promptPayload-sub-%d", i)
		originalPromptHash := sha256Hash(promptPayload)
		devAcc := NewMockAccount(testutil.Requester)
		devSig, _ := calculations.Sign(devAcc, calculations.SignatureComponents{
			Payload: originalPromptHash, Timestamp: requestTimestamp, TransferAddress: testutil.Creator,
		}, calculations.Developer)

		messagePayload := []byte(originalPromptHash)
		messagePayload = append(messagePayload, []byte(fmt.Sprintf("%d", requestTimestamp))...)
		messagePayload = append(messagePayload, []byte(testutil.Creator)...)
		digest := sha256.Sum256(messagePayload)
		sigBytes, _ := base64.StdEncoding.DecodeString(devSig)
		pubKeyBytes := devAcc.GetPubKey().Bytes()

		start = time.Now()
		_ = secp256k1.VerifySignature(pubKeyBytes, digest[:], sigBytes)
		signatureVerifyEthTotal += time.Since(start)

		// 7. Measure GetAccountPubKey
		start = time.Now()
		_, _ = inferenceHelper.MessageServer.(calculations.PubKeyGetter).GetAccountPubKey(ctx, testutil.Requester)
		getAccPubKeyTotal += time.Since(start)

		// 8. Measure GetAccountPubKeysWithGrantees
		start = time.Now()
		_, _ = inferenceHelper.MessageServer.(calculations.PubKeyGetter).GetAccountPubKeysWithGrantees(ctx, testutil.Requester)
		getAccPubKeysWithGranteesTotal += time.Since(start)
	}

	fmt.Printf("Average Get Parent Group: %s\n", parentGetTotal/iterations)
	fmt.Printf("Average Get Subgroup Group: %s\n", subGetTotal/iterations)
	fmt.Printf("Average Set Parent Group: %s\n", setDurationTotal/iterations)
	fmt.Printf("Average Get Participant: %s\n", participantGetTotal/iterations)
	fmt.Printf("Average Set Participant: %s\n", participantSetTotal/iterations)
	fmt.Printf("Average Set Inference: %s\n", inferenceSetTotal/iterations)
	fmt.Printf("Average Single Signature Verification (Eth): %s\n", signatureVerifyEthTotal/iterations)
	fmt.Printf("Average GetAccountPubKey: %s\n", getAccPubKeyTotal/iterations)
	fmt.Printf("Average GetAccountPubKeysWithGrantees: %s\n", getAccPubKeysWithGranteesTotal/iterations)
}
