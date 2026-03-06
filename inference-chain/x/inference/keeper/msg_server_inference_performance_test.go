package keeper_test

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestMsgServer_StartAndFinishInference_Performance(t *testing.T) {
	const (
		iterations = 100
		epochID1   = 1
		epochID2   = 2
		modelID    = "model1"
	)

	var totalStartDuration time.Duration
	var totalFinishDuration time.Duration
	var totalStartGas uint64
	var totalFinishGas uint64

	for i := 0; i < iterations; i++ {
		inferenceHelper, k, ctx := NewMockInferenceHelper(t)
		setupMessageFlowMocks(inferenceHelper)
		configureAuthzGrants(inferenceHelper, map[string]string{
			inferenceHelper.MockTransferAgent.address: syntheticAddress(3_000_000),
			inferenceHelper.MockExecutor.address:      syntheticAddress(3_100_000),
		})
		model := types.Model{Id: modelID}

		initialBlockHeight := int64(10)
		ctx, err := advanceEpoch(ctx, &k, inferenceHelper.Mocks, initialBlockHeight, epochID1)
		require.NoError(t, err)
		k.SetModel(ctx, &model)

		nextBlockHeight := initialBlockHeight + 10
		ctx, err = advanceEpoch(ctx, &k, inferenceHelper.Mocks, nextBlockHeight, epochID2)
		require.NoError(t, err)
		StubModelSubgroup(t, ctx, k, inferenceHelper.Mocks, &model)

		// Keep helper context aligned with the epoch-advanced context.
		inferenceHelper.context = ctx

		baseTimestamp := ctx.BlockTime().UnixNano()
		requestTimestamp := baseTimestamp + int64(i+1)
		promptPayload := fmt.Sprintf("promptPayload-%d", i)
		startDuration, finishDuration, startGas, finishGas, _ := runStartAndFinishInferencePair(
			t,
			inferenceHelper,
			promptPayload,
			modelID,
			requestTimestamp,
		)
		totalStartDuration += startDuration
		totalFinishDuration += finishDuration
		totalStartGas += startGas
		totalFinishGas += finishGas
	}

	averageStartDuration := totalStartDuration / iterations
	averageFinishDuration := totalFinishDuration / iterations
	totalDuration := totalStartDuration + totalFinishDuration
	averageTotalDuration := totalDuration / iterations
	averageStartGas := totalStartGas / iterations
	averageFinishGas := totalFinishGas / iterations
	averageTotalGas := (totalStartGas + totalFinishGas) / iterations

	t.Logf("Performance over %d iterations:", iterations)
	t.Logf("StartInference total: %s, average: %s", totalStartDuration, averageStartDuration)
	t.Logf("FinishInference total: %s, average: %s", totalFinishDuration, averageFinishDuration)
	t.Logf("Combined total: %s, average per iteration: %s", totalDuration, averageTotalDuration)
	t.Logf("StartInference average gas: %d", averageStartGas)
	t.Logf("FinishInference average gas: %d", averageFinishGas)
	t.Logf("Combined average gas per iteration: %d", averageTotalGas)
}

func TestMsgServer_StartAndFinishInference_Performance_LargeEpochGroup(t *testing.T) {
	const (
		iterations               = 100
		participantCount         = 1000
		modelCount               = 5
		nodesPerParticipantModel = 20
	)

	inferenceHelper, k, ctx := NewMockInferenceHelper(t)
	setupMessageFlowMocks(inferenceHelper)
	configureAuthzGrants(inferenceHelper, map[string]string{
		inferenceHelper.MockTransferAgent.address: syntheticAddress(3_000_000),
		inferenceHelper.MockExecutor.address:      syntheticAddress(3_100_000),
	})

	modelIDs := buildModelIDs(modelCount)
	require.NoError(t, seedLargeEpochGroupState(
		ctx,
		&k,
		inferenceHelper,
		modelIDs,
		participantCount,
		nodesPerParticipantModel,
	))
	require.NoError(t, seedInactiveParticipants(ctx, &k, 1000, 1_000_000))

	var totalStartDuration time.Duration
	var totalFinishDuration time.Duration
	var totalStartGas uint64
	var totalFinishGas uint64
	baseTimestamp := ctx.BlockTime().UnixNano()

	for i := 0; i < iterations; i++ {
		requestTimestamp := baseTimestamp + int64(i+1)
		startDuration, finishDuration, startGas, finishGas, _ := runStartAndFinishInferencePair(
			t,
			inferenceHelper,
			fmt.Sprintf("epochgroup-prompt-%d", i),
			modelIDs[0],
			requestTimestamp,
		)
		totalStartDuration += startDuration
		totalFinishDuration += finishDuration
		totalStartGas += startGas
		totalFinishGas += finishGas
	}

	avgStart := totalStartDuration / iterations
	avgFinish := totalFinishDuration / iterations
	total := totalStartDuration + totalFinishDuration
	avgTotal := total / iterations
	avgStartGas := totalStartGas / iterations
	avgFinishGas := totalFinishGas / iterations
	avgTotalGas := (totalStartGas + totalFinishGas) / iterations

	t.Logf("Large epoch-group performance over %d iterations:", iterations)
	t.Logf("StartInference total: %s, average: %s", totalStartDuration, avgStart)
	t.Logf("FinishInference total: %s, average: %s", totalFinishDuration, avgFinish)
	t.Logf("Combined total: %s, average per iteration: %s", total, avgTotal)
	t.Logf("StartInference average gas: %d", avgStartGas)
	t.Logf("FinishInference average gas: %d", avgFinishGas)
	t.Logf("Combined average gas per iteration: %d", avgTotalGas)
}

func TestMsgServer_StartAndFinishInference_Performance_LargeDeveloperStats(t *testing.T) {
	const (
		iterations          = 100
		prepopulateStatsCnt = 10000
	)

	inferenceHelper, k, ctx := NewMockInferenceHelper(t)
	setupMessageFlowMocks(inferenceHelper)
	configureAuthzGrants(inferenceHelper, map[string]string{
		inferenceHelper.MockTransferAgent.address: syntheticAddress(3_000_000),
		inferenceHelper.MockExecutor.address:      syntheticAddress(3_100_000),
	})

	modelIDs := buildModelIDs(1)
	require.NoError(t, seedLargeEpochGroupState(
		ctx,
		&k,
		inferenceHelper,
		modelIDs,
		1000,
		20,
	))
	require.NoError(t, seedDeveloperStats(ctx, &k, modelIDs[0], prepopulateStatsCnt))

	var totalStartDuration time.Duration
	var totalFinishDuration time.Duration
	var totalStartGas uint64
	var totalFinishGas uint64
	baseTimestamp := ctx.BlockTime().UnixNano()

	for i := 0; i < iterations; i++ {
		requestTimestamp := baseTimestamp + int64(100000+i)
		startDuration, finishDuration, startGas, finishGas, _ := runStartAndFinishInferencePair(
			t,
			inferenceHelper,
			fmt.Sprintf("devstats-prompt-%d", i),
			modelIDs[0],
			requestTimestamp,
		)
		totalStartDuration += startDuration
		totalFinishDuration += finishDuration
		totalStartGas += startGas
		totalFinishGas += finishGas
	}

	avgStart := totalStartDuration / iterations
	avgFinish := totalFinishDuration / iterations
	total := totalStartDuration + totalFinishDuration
	avgTotal := total / iterations
	avgStartGas := totalStartGas / iterations
	avgFinishGas := totalFinishGas / iterations
	avgTotalGas := (totalStartGas + totalFinishGas) / iterations

	t.Logf("Large developer-stats performance over %d iterations (prepopulated: %d):", iterations, prepopulateStatsCnt)
	t.Logf("StartInference total: %s, average: %s", totalStartDuration, avgStart)
	t.Logf("FinishInference total: %s, average: %s", totalFinishDuration, avgFinish)
	t.Logf("Combined total: %s, average per iteration: %s", total, avgTotal)
	t.Logf("StartInference average gas: %d", avgStartGas)
	t.Logf("FinishInference average gas: %d", avgFinishGas)
	t.Logf("Combined average gas per iteration: %d", avgTotalGas)
}

func TestMsgServer_StartAndFinishInference_Performance_MixedLoad(t *testing.T) {
	const (
		defaultIterations        = 100
		defaultWarmupIterations  = 0
		defaultPrepopulateVals   = 0
		participantCount         = 200
		modelCount               = 2
		nodesPerParticipantModel = 20
		developerCount           = 1
		prepopulateStatsCnt      = 15000
	)
	iterations := defaultIterations
	warmupIterations := defaultWarmupIterations
	prepopulateValidationsCnt := defaultPrepopulateVals
	if testing.Short() {
		iterations = 5
	}
	if envIterations := os.Getenv("PERF_ITERATIONS"); envIterations != "" {
		parsedIterations, err := strconv.Atoi(envIterations)
		require.NoError(t, err)
		require.Greater(t, parsedIterations, 0)
		iterations = parsedIterations
	}
	if envPrepopulatedValidations := os.Getenv("PERF_PREPOPULATE_VALIDATIONS"); envPrepopulatedValidations != "" {
		parsedCount, err := strconv.Atoi(envPrepopulatedValidations)
		require.NoError(t, err)
		require.GreaterOrEqual(t, parsedCount, 0)
		prepopulateValidationsCnt = parsedCount
	}

	inferenceHelper, k, ctx := NewMockInferenceHelper(t)
	setupMessageFlowMocks(inferenceHelper)

	modelIDs := buildModelIDs(modelCount)
	require.NoError(t, seedLargeEpochGroupState(
		ctx,
		&k,
		inferenceHelper,
		modelIDs,
		participantCount,
		nodesPerParticipantModel,
	))

	participantAddresses := buildParticipantAddresses(participantCount)
	developerAddresses := participantAddresses[:developerCount]
	taAddresses := participantAddresses[developerCount : developerCount+50]
	executorAddresses := participantAddresses[developerCount+50 : developerCount+100]

	accountByAddress := make(map[string]*MockAccount, participantCount)
	developers := buildRoleAccounts(inferenceHelper, accountByAddress, developerAddresses)
	transferAgents := buildRoleAccounts(inferenceHelper, accountByAddress, taAddresses)
	executors := buildRoleAccounts(inferenceHelper, accountByAddress, executorAddresses)

	granterToGrantee := make(map[string]string, len(transferAgents)+len(executors))
	for i, ta := range transferAgents {
		granteeAddr := syntheticAddress(2_000_000 + i)
		grantee := getOrCreateMockAccount(inferenceHelper, accountByAddress, granteeAddr)
		granterToGrantee[ta.address] = grantee.address
	}
	for i, ex := range executors {
		granteeAddr := syntheticAddress(2_100_000 + i)
		grantee := getOrCreateMockAccount(inferenceHelper, accountByAddress, granteeAddr)
		granterToGrantee[ex.address] = grantee.address
	}

	registerAccountsForSignatureValidation(inferenceHelper, allAccounts(accountByAddress))
	configureAuthzGrants(inferenceHelper, granterToGrantee)

	require.NoError(t, seedPopulateLikeProductionForDevelopers(
		ctx,
		&k,
		modelIDs,
		developerAddresses,
		prepopulateStatsCnt,
	))
	require.NoError(t, seedEpochGroupValidations(
		ctx,
		&k,
		developerAddresses[0],
		prepopulateValidationsCnt,
	))
	require.NoError(t, k.BuildEpochDataTransientCache(ctx))

	// Warm up by processing real inference lifecycle traffic before timed measurement.
	for i := 0; i < warmupIterations; i++ {
		modelID := modelIDs[i%len(modelIDs)]
		requester := developers[i%len(developers)]
		transferAgent := transferAgents[(i*3)%len(transferAgents)]
		executor := executors[(i*7)%len(executors)]
		requestTimestamp := ctx.BlockTime().UnixNano() + int64(1000+i)
		_, _, _, _, _ = runStartAndFinishInferencePairWithActors(
			t,
			inferenceHelper,
			fmt.Sprintf("mixedload-warmup-prompt-%d", i),
			modelID,
			requestTimestamp,
			requester,
			transferAgent,
			executor,
		)
	}

	var totalStartDuration time.Duration
	var totalFinishDuration time.Duration
	var totalValidationDuration time.Duration
	var totalStartGas uint64
	var totalFinishGas uint64
	var totalValidationGas uint64
	measuredInferenceIDs := make([]string, 0, iterations)
	baseTimestamp := ctx.BlockTime().UnixNano()

	for i := 0; i < iterations; i++ {
		modelID := modelIDs[i%len(modelIDs)]
		requester := developers[i%len(developers)]
		transferAgent := transferAgents[(i*3)%len(transferAgents)]
		executor := executors[(i*7)%len(executors)]

		requestTimestamp := baseTimestamp + int64(200000+warmupIterations+i)
		startDuration, finishDuration, validationDuration, startGas, finishGas, validationGas, inferenceID := runStartFinishValidateInferenceTripletWithActors(
			t,
			inferenceHelper,
			fmt.Sprintf("mixedload-prompt-%d", i),
			modelID,
			requestTimestamp,
			requester,
			transferAgent,
			executor,
		)
		totalStartDuration += startDuration
		totalFinishDuration += finishDuration
		totalValidationDuration += validationDuration
		totalStartGas += startGas
		totalFinishGas += finishGas
		totalValidationGas += validationGas
		measuredInferenceIDs = append(measuredInferenceIDs, inferenceID)
	}

	iterationDurationCount := time.Duration(iterations)
	iterationGasCount := uint64(iterations)
	avgStart := totalStartDuration / iterationDurationCount
	avgFinish := totalFinishDuration / iterationDurationCount
	avgValidation := totalValidationDuration / iterationDurationCount
	total := totalStartDuration + totalFinishDuration + totalValidationDuration
	avgTotal := total / iterationDurationCount
	avgStartGas := totalStartGas / iterationGasCount
	avgFinishGas := totalFinishGas / iterationGasCount
	avgValidationGas := totalValidationGas / iterationGasCount
	avgTotalGas := (totalStartGas + totalFinishGas + totalValidationGas) / iterationGasCount

	t.Logf(
		"Mixed-load performance over %d iterations after %d warmup inferences (participants=%d, models=%d, nodes/model=%d, devStats=%d, developers=%d, prepopulatedValidations=%d):",
		iterations,
		warmupIterations,
		participantCount,
		modelCount,
		nodesPerParticipantModel,
		prepopulateStatsCnt,
		developerCount,
		prepopulateValidationsCnt,
	)
	t.Logf("StartInference total: %s, average: %s", totalStartDuration, avgStart)
	t.Logf("FinishInference total: %s, average: %s", totalFinishDuration, avgFinish)
	t.Logf("Validation total: %s, average: %s", totalValidationDuration, avgValidation)
	t.Logf("Combined total: %s, average per iteration: %s", total, avgTotal)
	t.Logf("StartInference average gas: %d", avgStartGas)
	t.Logf("FinishInference average gas: %d", avgFinishGas)
	t.Logf("Validation average gas: %d", avgValidationGas)
	t.Logf("Combined average gas per iteration: %d", avgTotalGas)
	logDeveloperStatsSnapshotForMeasuredInferences(t, ctx, &k, developerAddresses[0], measuredInferenceIDs)
}

func TestMsgServer_ClaimRewards_Performance(t *testing.T) {
	const (
		defaultIterations             = 50
		defaultPrepopulateValidations = 1000
		defaultPrepopulateFinished    = 1000
		defaultValidatorCount         = 200
		defaultNodesPerValidator      = 20
		claimDebounceBlocks           = 30
	)

	iterations := defaultIterations
	prepopulateValidations := defaultPrepopulateValidations
	prepopulateFinished := defaultPrepopulateFinished
	validatorCount := defaultValidatorCount
	nodesPerValidator := defaultNodesPerValidator
	if testing.Short() {
		iterations = 5
		prepopulateValidations = 100
		prepopulateFinished = 100
		validatorCount = 20
		nodesPerValidator = 5
	}
	if envIterations := os.Getenv("PERF_CLAIM_ITERATIONS"); envIterations != "" {
		parsedIterations, err := strconv.Atoi(envIterations)
		require.NoError(t, err)
		require.Greater(t, parsedIterations, 0)
		iterations = parsedIterations
	}
	if envPrepopulatedValidations := os.Getenv("PERF_CLAIM_PREPOPULATE_VALIDATIONS"); envPrepopulatedValidations != "" {
		parsedCount, err := strconv.Atoi(envPrepopulatedValidations)
		require.NoError(t, err)
		require.GreaterOrEqual(t, parsedCount, 0)
		prepopulateValidations = parsedCount
	}
	if envPrepopulatedFinished := os.Getenv("PERF_CLAIM_PREPOPULATE_FINISHED"); envPrepopulatedFinished != "" {
		parsedCount, err := strconv.Atoi(envPrepopulatedFinished)
		require.NoError(t, err)
		require.GreaterOrEqual(t, parsedCount, 0)
		prepopulateFinished = parsedCount
	}
	if envValidatorCount := os.Getenv("PERF_CLAIM_VALIDATOR_COUNT"); envValidatorCount != "" {
		parsedCount, err := strconv.Atoi(envValidatorCount)
		require.NoError(t, err)
		require.Greater(t, parsedCount, 1)
		validatorCount = parsedCount
	}
	if envNodesPerValidator := os.Getenv("PERF_CLAIM_NODES_PER_VALIDATOR"); envNodesPerValidator != "" {
		parsedCount, err := strconv.Atoi(envNodesPerValidator)
		require.NoError(t, err)
		require.Greater(t, parsedCount, 0)
		nodesPerValidator = parsedCount
	}

	var totalDuration time.Duration
	var totalGas uint64

	for i := 0; i < iterations; i++ {
		k, ms, ctx, mocks := setupKeeperWithMocks(t)

		creator := syntheticAddress(7_000_000 + i)
		creatorAccount := NewMockAccount(creator)
		creatorAddr, err := sdk.AccAddressFromBech32(creator)
		require.NoError(t, err)

		seed := uint64(i + 1)
		seedBytes := make([]byte, 8)
		binary.BigEndian.PutUint64(seedBytes, seed)
		signature, err := creatorAccount.key.Sign(seedBytes)
		require.NoError(t, err)
		signatureHex := hex.EncodeToString(signature)

		epochIndex := uint64(10_000 + i*2)
		currentEpochIndex := epochIndex + 1
		k.SetEpoch(ctx, &types.Epoch{Index: epochIndex, PocStartBlockHeight: int64(epochIndex * 10)})
		k.SetEpoch(ctx, &types.Epoch{Index: currentEpochIndex, PocStartBlockHeight: int64(currentEpochIndex * 10)})
		_ = k.SetEffectiveEpochIndex(ctx, currentEpochIndex)

		validatorAddresses := make([]string, 0, validatorCount)
		validatorAddresses = append(validatorAddresses, creator)
		for v := 1; v < validatorCount; v++ {
			validatorAddresses = append(validatorAddresses, syntheticAddress(8_000_000+i*10_000+v))
		}
		activeParticipants := make([]*types.ActiveParticipant, 0, validatorCount)
		weights := make([]*types.ValidationWeight, 0, validatorCount)
		for v, validatorAddress := range validatorAddresses {
			validatorAddr, err := sdk.AccAddressFromBech32(validatorAddress)
			require.NoError(t, err)
			require.NoError(t, k.Participants.Set(ctx, validatorAddr, types.Participant{
				Index:             validatorAddress,
				Address:           validatorAddress,
				Status:            types.ParticipantStatus_ACTIVE,
				ValidatorKey:      base64.StdEncoding.EncodeToString([]byte("validator-key")),
				CurrentEpochStats: types.NewCurrentEpochStats(),
			}))
			activeParticipants = append(activeParticipants, &types.ActiveParticipant{Index: validatorAddress})
			nodes := make([]*types.MLNodeInfo, 0, nodesPerValidator)
			for nodeIdx := 0; nodeIdx < nodesPerValidator; nodeIdx++ {
				nodes = append(nodes, &types.MLNodeInfo{
					NodeId:             fmt.Sprintf("claim-v%d-n%d-%d", v, nodeIdx, i),
					Throughput:         100,
					PocWeight:          1,
					TimeslotAllocation: []bool{false, false},
				})
			}
			weights = append(weights, &types.ValidationWeight{
				MemberAddress: validatorAddress,
				Weight:        100,
				Reputation:    100,
				MlNodes:       nodes,
			})
		}
		k.SetActiveParticipants(ctx, types.ActiveParticipants{
			EpochId:      epochIndex,
			Participants: activeParticipants,
		})
		k.SetActiveParticipants(ctx, types.ActiveParticipants{
			EpochId:      currentEpochIndex,
			Participants: activeParticipants,
		})
		setClaimEpochGroupData(k, ctx, epochIndex, "claim-model", weights)
		setClaimEpochGroupData(k, ctx, currentEpochIndex, "claim-model", weights)
		require.NoError(t, k.BuildEpochDataTransientCache(ctx))

		settleAmount := types.SettleAmount{
			Participant:   creator,
			EpochIndex:    epochIndex,
			WorkCoins:     1000,
			RewardCoins:   500,
			SeedSignature: signatureHex,
		}
		require.NoError(t, k.SetSettleAmount(ctx, settleAmount))
		k.SetEpochPerformanceSummary(ctx, types.EpochPerformanceSummary{
			EpochIndex:    epochIndex,
			ParticipantId: creator,
			Claimed:       false,
		})
		require.NoError(t, seedClaimEpochGroupValidationEntries(ctx, &k, creator, epochIndex, prepopulateValidations))
		require.NoError(t, seedClaimFinishedInferences(ctx, &k, epochIndex, "claim-model", validatorAddresses[1], validatorCount, prepopulateFinished))

		mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), creatorAddr).Return(true).AnyTimes()
		mocks.AccountKeeper.EXPECT().GetAccount(gomock.Any(), creatorAddr).Return(creatorAccount).AnyTimes()
		mocks.AuthzKeeper.EXPECT().
			GranterGrants(gomock.Any(), gomock.Any()).
			Return(&authztypes.QueryGranterGrantsResponse{Grants: []*authztypes.GrantAuthorization{}}, nil).
			AnyTimes()

		workCoins := sdk.NewCoins(sdk.NewInt64Coin(types.BaseCoin, 1000))
		rewardCoins := sdk.NewCoins(sdk.NewInt64Coin(types.BaseCoin, 500))
		mocks.BankKeeper.EXPECT().
			SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, creatorAddr, workCoins, gomock.Any()).
			Return(nil).
			AnyTimes()
		mocks.BankKeeper.EXPECT().
			SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, creatorAddr, rewardCoins, gomock.Any()).
			Return(nil).
			AnyTimes()
		mocks.BankKeeper.EXPECT().
			SendCoinsFromModuleToModule(gomock.Any(), types.ModuleName, "streamvesting", workCoins, gomock.Any()).
			Return(nil).
			AnyTimes()
		mocks.BankKeeper.EXPECT().
			SendCoinsFromModuleToModule(gomock.Any(), types.ModuleName, "streamvesting", rewardCoins, gomock.Any()).
			Return(nil).
			AnyTimes()
		mocks.StreamVestingKeeper.EXPECT().
			AddVestedRewards(gomock.Any(), creator, gomock.Any(), workCoins, gomock.Any(), gomock.Any()).
			Return(nil).
			AnyTimes()
		mocks.StreamVestingKeeper.EXPECT().
			AddVestedRewards(gomock.Any(), creator, gomock.Any(), rewardCoins, gomock.Any(), gomock.Any()).
			Return(nil).
			AnyTimes()

		claimCtx := ctx.WithBlockHeight(claimDebounceBlocks + 1)
		gasBefore := claimCtx.GasMeter().GasConsumed()
		started := time.Now()
		resp, err := ms.ClaimRewards(claimCtx, &types.MsgClaimRewards{
			Creator:    creator,
			EpochIndex: epochIndex,
			Seed:       int64(seed),
		})
		duration := time.Since(started)
		gasUsed := claimCtx.GasMeter().GasConsumed() - gasBefore

		require.NoError(t, err)
		require.NotNil(t, resp)
		require.Equal(t, uint64(1500), resp.Amount)

		totalDuration += duration
		totalGas += gasUsed
	}

	averageDuration := totalDuration / time.Duration(iterations)
	averageGas := totalGas / uint64(iterations)
	t.Logf(
		"ClaimRewards performance over %d iterations (prepopulated_validations=%d, prepopulated_finished=%d, validators=%d, nodes_per_validator=%d):",
		iterations,
		prepopulateValidations,
		prepopulateFinished,
		validatorCount,
		nodesPerValidator,
	)
	t.Logf("ClaimRewards total: %s, average: %s", totalDuration, averageDuration)
	t.Logf("ClaimRewards average gas: %d", averageGas)
}

func setClaimEpochGroupData(
	k keeper.Keeper,
	ctx sdk.Context,
	epochIndex uint64,
	modelID string,
	weights []*types.ValidationWeight,
) {
	totalWeight := int64(0)
	for _, weight := range weights {
		if weight == nil {
			continue
		}
		totalWeight += weight.Weight
	}
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:          epochIndex,
		EpochGroupId:        epochIndex,
		PocStartBlockHeight: epochIndex,
		SubGroupModels:      []string{modelID},
		ValidationWeights:   weights,
		TotalWeight:         totalWeight,
	})
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:          epochIndex,
		ModelId:             modelID,
		EpochGroupId:        epochIndex,
		PocStartBlockHeight: epochIndex,
		ValidationWeights:   weights,
		TotalWeight:         totalWeight,
	})
}

func seedClaimEpochGroupValidationEntries(
	ctx sdk.Context,
	k *keeper.Keeper,
	participant string,
	epochIndex uint64,
	count int,
) error {
	for i := 0; i < count; i++ {
		if err := k.SetEpochGroupValidation(ctx, epochIndex, participant, longInferenceID(i)); err != nil {
			return err
		}
	}
	return nil
}

func seedClaimFinishedInferences(
	ctx sdk.Context,
	k *keeper.Keeper,
	epochIndex uint64,
	modelID string,
	executor string,
	validatorCount int,
	count int,
) error {
	totalPower := uint64(validatorCount * 100)
	for i := 0; i < count; i++ {
		k.SetInferenceValidationDetails(ctx, types.InferenceValidationDetails{
			EpochId:            epochIndex,
			InferenceId:        longInferenceID(i),
			ExecutorId:         executor,
			ExecutorReputation: 100,
			TrafficBasis:       1000,
			ExecutorPower:      100,
			Model:              modelID,
			TotalPower:         totalPower,
		})
	}
	return nil
}

func setupMessageFlowMocks(h *MockInferenceHelper) {
	h.Mocks.BankKeeper.EXPECT().
		SendCoinsFromAccountToModule(gomock.Any(), gomock.Any(), types.ModuleName, gomock.Any(), gomock.Any()).
		Return(nil).
		AnyTimes()
	h.Mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil).
		AnyTimes()

	h.Mocks.AccountKeeper.EXPECT().
		GetAccount(gomock.Any(), h.MockRequester.GetBechAddress()).
		Return(h.MockRequester).
		AnyTimes()
	h.Mocks.AccountKeeper.EXPECT().
		GetAccount(gomock.Any(), h.MockTransferAgent.GetBechAddress()).
		Return(h.MockTransferAgent).
		AnyTimes()
	h.Mocks.AccountKeeper.EXPECT().
		GetAccount(gomock.Any(), h.MockExecutor.GetBechAddress()).
		Return(h.MockExecutor).
		AnyTimes()

}

func buildModelIDs(count int) []string {
	modelIDs := make([]string, 0, count)
	for i := 0; i < count; i++ {
		modelIDs = append(modelIDs, fmt.Sprintf("model%d", i+1))
	}
	return modelIDs
}

func seedLargeEpochGroupState(
	ctx sdk.Context,
	k *keeper.Keeper,
	h *MockInferenceHelper,
	modelIDs []string,
	participantCount int,
	nodesPerParticipantModel int,
) error {
	for _, modelID := range modelIDs {
		k.SetModel(ctx, &types.Model{
			Id:                  modelID,
			ValidationThreshold: &types.Decimal{Value: 85, Exponent: -2},
		})
	}

	participantAddresses := buildParticipantAddresses(participantCount)

	for _, addr := range participantAddresses {
		if addr == testutil.Requester || addr == testutil.Creator || addr == testutil.Executor {
			continue
		}
		err := k.SetParticipant(ctx, types.Participant{
			Index:             addr,
			Address:           addr,
			Status:            types.ParticipantStatus_ACTIVE,
			ValidatorKey:      base64.StdEncoding.EncodeToString([]byte("validator-key")),
			CurrentEpochStats: types.NewCurrentEpochStats(),
		})
		if err != nil {
			return err
		}
	}

	epochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		return types.ErrEffectiveEpochNotFound
	}
	activeParticipants := make([]*types.ActiveParticipant, 0, len(participantAddresses))
	for _, addr := range participantAddresses {
		activeParticipants = append(activeParticipants, &types.ActiveParticipant{Index: addr})
	}
	if err := k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:      epochIndex,
		Participants: activeParticipants,
	}); err != nil {
		return err
	}

	parentGroup, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		return err
	}

	parentWeights := make([]*types.ValidationWeight, 0, len(participantAddresses))
	for _, addr := range participantAddresses {
		parentWeights = append(parentWeights, &types.ValidationWeight{
			MemberAddress: addr,
			Weight:        100,
			Reputation:    100,
		})
	}

	rootData := *parentGroup.GroupData
	rootData.EpochIndex = epochIndex
	rootData.SubGroupModels = append([]string(nil), modelIDs...)
	rootData.ValidationWeights = parentWeights
	rootData.TotalWeight = int64(len(parentWeights) * 100)
	k.SetEpochGroupData(ctx, rootData)

	for _, modelID := range modelIDs {
		modelWeights := make([]*types.ValidationWeight, 0, len(participantAddresses))
		for pIdx, addr := range participantAddresses {
			nodes := make([]*types.MLNodeInfo, 0, nodesPerParticipantModel)
			for nodeIdx := 0; nodeIdx < nodesPerParticipantModel; nodeIdx++ {
				nodes = append(nodes, &types.MLNodeInfo{
					NodeId:             fmt.Sprintf("%s-p%d-n%d", modelID, pIdx, nodeIdx),
					Throughput:         100,
					PocWeight:          1,
					TimeslotAllocation: []bool{false, false},
				})
			}
			modelWeights = append(modelWeights, &types.ValidationWeight{
				MemberAddress:      addr,
				Weight:             100,
				Reputation:         100,
				MlNodes:            nodes,
				ConfirmationWeight: 20,
			})
		}

		k.SetEpochGroupData(ctx, types.EpochGroupData{
			PocStartBlockHeight: parentGroup.GroupData.PocStartBlockHeight,
			EpochGroupId:        parentGroup.GroupData.EpochGroupId,
			EpochPolicy:         parentGroup.GroupData.EpochPolicy,
			ModelId:             modelID,
			ModelSnapshot: &types.Model{
				Id:                  modelID,
				ValidationThreshold: &types.Decimal{Value: 85, Exponent: -2},
			},
			EpochIndex:        epochIndex,
			ValidationWeights: modelWeights,
			TotalWeight:       int64(len(modelWeights) * 100),
		})
	}

	// Keep context aligned in helper.
	h.context = ctx
	return nil
}

func seedDeveloperStats(ctx sdk.Context, k *keeper.Keeper, modelID string, count int) error {
	baseTs := ctx.BlockTime().UnixMilli()
	for i := 0; i < count; i++ {
		err := k.SetDeveloperStats(ctx, types.Inference{
			InferenceId:          fmt.Sprintf("prefill-inference-%d", i),
			RequestedBy:          testutil.Requester,
			Status:               types.InferenceStatus_FINISHED,
			Model:                modelID,
			PromptTokenCount:     10,
			CompletionTokenCount: 20,
			ActualCost:           30 * calculations.PerTokenCost,
			StartBlockTimestamp:  baseTs + int64(i),
			EndBlockTimestamp:    baseTs + int64(i),
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func seedDeveloperStatsForDevelopers(
	ctx sdk.Context,
	k *keeper.Keeper,
	modelID string,
	developers []string,
	count int,
) error {
	baseTs := ctx.BlockTime().UnixMilli()
	for i := 0; i < count; i++ {
		err := k.SetDeveloperStats(ctx, types.Inference{
			InferenceId:          fmt.Sprintf("prefill-multi-dev-inference-%d", i),
			RequestedBy:          developers[i%len(developers)],
			Status:               types.InferenceStatus_FINISHED,
			Model:                modelID,
			PromptTokenCount:     10,
			CompletionTokenCount: 20,
			ActualCost:           30 * calculations.PerTokenCost,
			StartBlockTimestamp:  baseTs + int64(i),
			EndBlockTimestamp:    baseTs + int64(i),
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func seedDeveloperStatsLifecycleForDevelopers(
	ctx sdk.Context,
	k *keeper.Keeper,
	modelIDs []string,
	developers []string,
	count int,
) error {
	baseTs := ctx.BlockTime().UnixMilli()
	for i := 0; i < count; i++ {
		inferenceID := fmt.Sprintf("prefill-lifecycle-inference-%d", i)
		developer := developers[i%len(developers)]
		modelID := modelIDs[i%len(modelIDs)]
		startTs := baseTs + int64(i*2)
		endTs := startTs + 1

		// Keep prefilled stats in the same effective epoch to stress the hottest epoch list.
		startEpoch := uint64(0)
		finishEpoch := uint64(0)

		if err := k.SetDeveloperStats(ctx, types.Inference{
			InferenceId:          inferenceID,
			RequestedBy:          developer,
			Status:               types.InferenceStatus_STARTED,
			Model:                modelID,
			PromptTokenCount:     0,
			CompletionTokenCount: 0,
			ActualCost:           0,
			StartBlockTimestamp:  startTs,
			EndBlockTimestamp:    0,
			EpochId:              startEpoch,
		}); err != nil {
			return err
		}

		if err := k.SetDeveloperStats(ctx, types.Inference{
			InferenceId:          inferenceID,
			RequestedBy:          developer,
			Status:               types.InferenceStatus_FINISHED,
			Model:                modelID,
			PromptTokenCount:     10,
			CompletionTokenCount: 20,
			ActualCost:           30 * calculations.PerTokenCost,
			StartBlockTimestamp:  startTs,
			EndBlockTimestamp:    endTs,
			EpochId:              finishEpoch,
		}); err != nil {
			return err
		}
	}
	return nil
}

func seedPopulateLikeProductionForDevelopers(
	ctx sdk.Context,
	k *keeper.Keeper,
	modelIDs []string,
	developers []string,
	count int,
) error {
	baseTs := ctx.BlockTime().UnixMilli()
	epochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		return types.ErrEffectiveEpochNotFound
	}
	requestsPerModel := make(map[string]int64, len(modelIDs))

	for i := 0; i < count; i++ {
		inferenceID := longInferenceID(i)
		developer := developers[i%len(developers)]
		modelID := modelIDs[i%len(modelIDs)]
		startTs := baseTs + int64(i*2)
		endTs := startTs + 1

		// Populate inference state size closer to production.
		if err := k.SetInference(ctx, types.Inference{
			Index:                inferenceID,
			InferenceId:          inferenceID,
			RequestedBy:          developer,
			Status:               types.InferenceStatus_FINISHED,
			Model:                modelID,
			PromptHash:           fmt.Sprintf("prefill-hash-%d", i),
			PromptTokenCount:     10,
			CompletionTokenCount: 20,
			ActualCost:           30 * calculations.PerTokenCost,
			StartBlockTimestamp:  startTs,
			EndBlockTimestamp:    endTs,
			TransferredBy:        testutil.Creator,
			ExecutedBy:           testutil.Executor,
			EpochId:              epochIndex,
		}); err != nil {
			return err
		}

		if err := k.SetInferenceTimeout(ctx, types.InferenceTimeout{
			ExpirationHeight: uint64(20 + i),
			InferenceId:      inferenceID,
		}); err != nil {
			return err
		}

		k.SetInferenceValidationDetails(ctx, types.InferenceValidationDetails{
			InferenceId:          inferenceID,
			ExecutorId:           testutil.Executor,
			ExecutorReputation:   100,
			TrafficBasis:         uint64(count),
			ExecutorPower:        100,
			EpochId:              epochIndex,
			Model:                modelID,
			TotalPower:           20000,
			CreatedAtBlockHeight: ctx.BlockHeight(),
		})
		requestsPerModel[modelID]++

		// Lifecycle writes into developer stats (started then finished) as in real flow.
		if err := k.SetDeveloperStats(ctx, types.Inference{
			InferenceId:          inferenceID,
			RequestedBy:          developer,
			Status:               types.InferenceStatus_STARTED,
			Model:                modelID,
			PromptTokenCount:     0,
			CompletionTokenCount: 0,
			ActualCost:           0,
			StartBlockTimestamp:  startTs,
			EndBlockTimestamp:    0,
			EpochId:              0,
		}); err != nil {
			return err
		}
		if err := k.SetDeveloperStats(ctx, types.Inference{
			InferenceId:          inferenceID,
			RequestedBy:          developer,
			Status:               types.InferenceStatus_FINISHED,
			Model:                modelID,
			PromptTokenCount:     10,
			CompletionTokenCount: 20,
			ActualCost:           30 * calculations.PerTokenCost,
			StartBlockTimestamp:  startTs,
			EndBlockTimestamp:    endTs,
			EpochId:              0,
		}); err != nil {
			return err
		}
	}

	rootData, found := k.GetEpochGroupData(ctx, epochIndex, "")
	if found {
		rootData.NumberOfRequests = int64(count)
		rootData.PreviousEpochRequests = int64(count)
		k.SetEpochGroupData(ctx, rootData)
	}
	for _, modelID := range modelIDs {
		modelData, ok := k.GetEpochGroupData(ctx, epochIndex, modelID)
		if !ok {
			continue
		}
		modelData.NumberOfRequests = requestsPerModel[modelID]
		modelData.PreviousEpochRequests = requestsPerModel[modelID]
		k.SetEpochGroupData(ctx, modelData)
	}

	// Ensure the epoch index list includes all expected inference IDs.
	for _, developer := range developers {
		stat, ok := k.GetDevelopersStatsByEpoch(ctx, developer, epochIndex)
		if !ok {
			continue
		}
		exists := make(map[string]struct{}, len(stat.InferenceIds))
		for _, infID := range stat.InferenceIds {
			exists[infID] = struct{}{}
		}
		for i := 0; i < count; i++ {
			if developers[i%len(developers)] != developer {
				continue
			}
			infID := longInferenceID(i)
			if _, ok := exists[infID]; !ok {
				stat.InferenceIds = append(stat.InferenceIds, infID)
				exists[infID] = struct{}{}
			}
		}
		// Re-apply through the same keeper path by forcing a no-op finish update for missing-safe consistency.
		for i := 0; i < count; i++ {
			if developers[i%len(developers)] != developer {
				continue
			}
			infID := longInferenceID(i)
			_ = k.SetDeveloperStats(ctx, types.Inference{
				InferenceId:          infID,
				RequestedBy:          developer,
				Status:               types.InferenceStatus_FINISHED,
				Model:                modelIDs[i%len(modelIDs)],
				PromptTokenCount:     10,
				CompletionTokenCount: 20,
				ActualCost:           30 * calculations.PerTokenCost,
				StartBlockTimestamp:  baseTs + int64(i*2),
				EndBlockTimestamp:    baseTs + int64(i*2) + 1,
				EpochId:              0,
			})
		}
	}
	return nil
}

func seedEpochGroupValidations(
	ctx sdk.Context,
	k *keeper.Keeper,
	participant string,
	count int,
) error {
	if count == 0 {
		return nil
	}
	epochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		return types.ErrEffectiveEpochNotFound
	}
	ids := make([]string, 0, count)
	for i := 0; i < count; i++ {
		ids = append(ids, fmt.Sprintf("prevalidated-%d", i))
	}
	return k.SeedEpochGroupValidationEntries(ctx, types.EpochGroupValidations{
		Participant:         participant,
		EpochIndex:          epochIndex,
		ValidatedInferences: ids,
	})
}

func runStartAndFinishInferencePair(
	t *testing.T,
	h *MockInferenceHelper,
	promptPayload string,
	modelID string,
	requestTimestamp int64,
) (time.Duration, time.Duration, uint64, uint64, string) {
	return runStartAndFinishInferencePairWithActors(
		t,
		h,
		promptPayload,
		modelID,
		requestTimestamp,
		h.MockRequester,
		h.MockTransferAgent,
		h.MockExecutor,
	)
}

func runStartAndFinishInferencePairWithActors(
	t *testing.T,
	h *MockInferenceHelper,
	promptPayload string,
	modelID string,
	requestTimestamp int64,
	requester *MockAccount,
	transferAgent *MockAccount,
	executor *MockAccount,
) (time.Duration, time.Duration, uint64, uint64, string) {
	t.Helper()

	originalPromptHash := sha256Hash(promptPayload)
	promptHash := sha256Hash(promptPayload)

	devComponents := calculations.SignatureComponents{
		Payload:         originalPromptHash,
		Timestamp:       requestTimestamp,
		TransferAddress: transferAgent.address,
		ExecutorAddress: "",
	}
	inferenceID, err := calculations.Sign(requester, devComponents, calculations.Developer)
	require.NoError(t, err)

	taComponents := calculations.SignatureComponents{
		Payload:         promptHash,
		Timestamp:       requestTimestamp,
		TransferAddress: transferAgent.address,
		ExecutorAddress: executor.address,
	}
	taSignature, err := calculations.Sign(transferAgent, taComponents, calculations.TransferAgent)
	require.NoError(t, err)

	startMsg := &types.MsgStartInference{
		InferenceId:        inferenceID,
		PromptHash:         promptHash,
		PromptPayload:      promptPayload,
		RequestedBy:        requester.address,
		Creator:            transferAgent.address,
		Model:              modelID,
		OriginalPrompt:     promptPayload,
		OriginalPromptHash: originalPromptHash,
		RequestTimestamp:   requestTimestamp,
		TransferSignature:  taSignature,
		AssignedTo:         executor.address,
	}

	startedAt := time.Now()
	startGasBefore := h.context.GasMeter().GasConsumed()
	startResp, err := h.MessageServer.StartInference(h.context, startMsg)
	startDuration := time.Since(startedAt)
	startGasUsed := h.context.GasMeter().GasConsumed() - startGasBefore
	require.NoError(t, err)
	require.Empty(t, startResp.ErrorMessage)

	executorSignature, err := calculations.Sign(executor, taComponents, calculations.ExecutorAgent)
	require.NoError(t, err)

	finishMsg := &types.MsgFinishInference{
		Creator:              executor.address,
		InferenceId:          inferenceID,
		ResponseHash:         "responseHash",
		ResponsePayload:      "responsePayload",
		PromptTokenCount:     10,
		CompletionTokenCount: 20,
		ExecutedBy:           executor.address,
		TransferredBy:        transferAgent.address,
		RequestTimestamp:     requestTimestamp,
		TransferSignature:    taSignature,
		ExecutorSignature:    executorSignature,
		RequestedBy:          requester.address,
		OriginalPrompt:       promptPayload,
		Model:                modelID,
		PromptHash:           promptHash,
		OriginalPromptHash:   originalPromptHash,
	}

	finishedAt := time.Now()
	finishGasBefore := h.context.GasMeter().GasConsumed()
	finishResp, err := h.MessageServer.FinishInference(h.context, finishMsg)
	finishDuration := time.Since(finishedAt)
	finishGasUsed := h.context.GasMeter().GasConsumed() - finishGasBefore
	require.NoError(t, err)
	require.Empty(t, finishResp.ErrorMessage)

	savedInference, found := h.keeper.GetInference(h.context, inferenceID)
	require.True(t, found)
	require.Equal(t, types.InferenceStatus_FINISHED, savedInference.Status)

	return startDuration, finishDuration, startGasUsed, finishGasUsed, inferenceID
}

func runStartFinishValidateInferenceTripletWithActors(
	t *testing.T,
	h *MockInferenceHelper,
	promptPayload string,
	modelID string,
	requestTimestamp int64,
	requester *MockAccount,
	transferAgent *MockAccount,
	executor *MockAccount,
) (time.Duration, time.Duration, time.Duration, uint64, uint64, uint64, string) {
	t.Helper()

	startDuration, finishDuration, startGasUsed, finishGasUsed, inferenceID := runStartAndFinishInferencePairWithActors(
		t,
		h,
		promptPayload,
		modelID,
		requestTimestamp,
		requester,
		transferAgent,
		executor,
	)

	validationMsg := &types.MsgValidation{
		InferenceId:  inferenceID,
		Creator:      requester.address,
		ValueDecimal: types.DecimalFromFloat(0.9999),
	}
	validatedAt := time.Now()
	validationGasBefore := h.context.GasMeter().GasConsumed()
	validationResp, err := h.MessageServer.Validation(h.context, validationMsg)
	validationDuration := time.Since(validatedAt)
	validationGasUsed := h.context.GasMeter().GasConsumed() - validationGasBefore
	require.NoError(t, err)
	require.NotNil(t, validationResp)

	savedInference, found := h.keeper.GetInference(h.context, inferenceID)
	require.True(t, found)
	require.Equal(t, types.InferenceStatus_VALIDATED, savedInference.Status)

	return startDuration, finishDuration, validationDuration, startGasUsed, finishGasUsed, validationGasUsed, inferenceID
}

func logDeveloperStatsSnapshotForMeasuredInferences(
	t *testing.T,
	ctx sdk.Context,
	k *keeper.Keeper,
	developerAddress string,
	inferenceIDs []string,
) {
	t.Helper()
	epochIndex, found := k.GetEffectiveEpochIndex(ctx)
	require.True(t, found)
	devStatsByEpoch, epochFound := k.GetDevelopersStatsByEpoch(ctx, developerAddress, epochIndex)
	require.True(t, epochFound)

	_, devStatsByTime := k.DumpAllDeveloperStats(ctx)
	timeStats := devStatsByTime[developerAddress]

	statsByInferenceID := make(map[string]*types.DeveloperStatsByTime, len(timeStats))
	for _, stat := range timeStats {
		if stat != nil && stat.Inference != nil {
			statsByInferenceID[stat.Inference.InferenceId] = stat
		}
	}

	finishedCount := 0
	nonFinishedCount := 0
	totalTokenCount := uint64(0)
	totalActualCost := int64(0)
	missingInTimeStore := 0
	for _, inferenceID := range inferenceIDs {
		stat, ok := statsByInferenceID[inferenceID]
		if !ok {
			missingInTimeStore++
			continue
		}
		if stat.Inference.Status == types.InferenceStatus_FINISHED {
			finishedCount++
		} else {
			nonFinishedCount++
		}
		totalTokenCount += stat.Inference.TotalTokenCount
		totalActualCost += stat.Inference.ActualCostInCoins
	}

	t.Logf("Developer stats snapshot for measured set (%d IDs):", len(inferenceIDs))
	t.Logf("Effective epoch index: %d, epoch inferenceIds size: %d", epochIndex, len(devStatsByEpoch.InferenceIds))
	t.Logf("Measured IDs in by_time store: %d, missing: %d", len(inferenceIDs)-missingInTimeStore, missingInTimeStore)
	t.Logf("Measured by_time status counts: finished=%d non_finished=%d", finishedCount, nonFinishedCount)
	if len(inferenceIDs)-missingInTimeStore > 0 {
		avgTokens := float64(totalTokenCount) / float64(len(inferenceIDs)-missingInTimeStore)
		avgActualCost := float64(totalActualCost) / float64(len(inferenceIDs)-missingInTimeStore)
		t.Logf("Measured by_time averages: tokens=%.2f actual_cost=%.2f", avgTokens, avgActualCost)
	}
}

func buildParticipantAddresses(participantCount int) []string {
	participantAddresses := make([]string, 0, participantCount)
	participantAddresses = append(participantAddresses, testutil.Requester, testutil.Creator, testutil.Executor)
	for i := 0; len(participantAddresses) < participantCount; i++ {
		addr := syntheticAddress(i)
		if addr == testutil.Requester || addr == testutil.Creator || addr == testutil.Executor {
			continue
		}
		participantAddresses = append(participantAddresses, addr)
	}
	return participantAddresses
}

func buildMockAccountsByAddress(addresses []string) []*MockAccount {
	accounts := make([]*MockAccount, 0, len(addresses))
	for _, addr := range addresses {
		accounts = append(accounts, NewMockAccount(addr))
	}
	return accounts
}

func buildRoleAccounts(
	h *MockInferenceHelper,
	accountByAddress map[string]*MockAccount,
	addresses []string,
) []*MockAccount {
	accounts := make([]*MockAccount, 0, len(addresses))
	for _, addr := range addresses {
		accounts = append(accounts, getOrCreateMockAccount(h, accountByAddress, addr))
	}
	return accounts
}

func getOrCreateMockAccount(
	h *MockInferenceHelper,
	accountByAddress map[string]*MockAccount,
	address string,
) *MockAccount {
	if existing, ok := accountByAddress[address]; ok {
		return existing
	}
	var account *MockAccount
	switch address {
	case h.MockRequester.address:
		account = h.MockRequester
	case h.MockTransferAgent.address:
		account = h.MockTransferAgent
	case h.MockExecutor.address:
		account = h.MockExecutor
	default:
		account = NewMockAccount(address)
	}
	accountByAddress[address] = account
	return account
}

func allAccounts(accountByAddress map[string]*MockAccount) []*MockAccount {
	accounts := make([]*MockAccount, 0, len(accountByAddress))
	for _, account := range accountByAddress {
		accounts = append(accounts, account)
	}
	return accounts
}

func registerAccountsForSignatureValidation(h *MockInferenceHelper, accounts []*MockAccount) {
	for _, account := range accounts {
		h.Mocks.AccountKeeper.EXPECT().
			GetAccount(gomock.Any(), account.GetBechAddress()).
			Return(account).
			AnyTimes()
	}
}

func configureAuthzGrants(h *MockInferenceHelper, granterToGrantee map[string]string) {
	grantsByGranter := make(map[string][]*authztypes.GrantAuthorization, len(granterToGrantee))
	for granter, grantee := range granterToGrantee {
		authorization := authztypes.NewGenericAuthorization("/inference.inference.MsgStartInference")
		authorizationAny, err := codectypes.NewAnyWithValue(authorization)
		require.NoError(h.testingT, err)
		expiration := time.Now().Add(24 * time.Hour)
		grantsByGranter[granter] = []*authztypes.GrantAuthorization{
			{
				Granter:       granter,
				Grantee:       grantee,
				Authorization: authorizationAny,
				Expiration:    &expiration,
			},
		}
	}

	h.Mocks.AuthzKeeper.EXPECT().
		GranterGrants(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, req *authztypes.QueryGranterGrantsRequest) (*authztypes.QueryGranterGrantsResponse, error) {
			if grants, ok := grantsByGranter[req.Granter]; ok {
				return &authztypes.QueryGranterGrantsResponse{Grants: grants}, nil
			}
			return &authztypes.QueryGranterGrantsResponse{Grants: []*authztypes.GrantAuthorization{}}, nil
		}).
		AnyTimes()

	for _, granteeAddr := range granterToGrantee {
		grantee := NewMockAccount(granteeAddr)
		h.Mocks.AccountKeeper.EXPECT().
			GetAccount(gomock.Any(), grantee.GetBechAddress()).
			Return(grantee).
			AnyTimes()
	}
}

func seedInactiveParticipants(
	ctx sdk.Context,
	k *keeper.Keeper,
	count int,
	startIndex int,
) error {
	for i := 0; i < count; i++ {
		addr := syntheticAddress(startIndex + i)
		err := k.SetParticipant(ctx, types.Participant{
			Index:             addr,
			Address:           addr,
			Status:            types.ParticipantStatus_INACTIVE,
			ValidatorKey:      base64.StdEncoding.EncodeToString([]byte("inactive-validator-key")),
			CurrentEpochStats: types.NewCurrentEpochStats(),
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func syntheticAddress(i int) string {
	raw := make([]byte, 20)
	binary.BigEndian.PutUint64(raw[12:], uint64(i+1))
	return sdk.AccAddress(raw).String()
}

func longInferenceID(i int) string {
	raw := make([]byte, 64)
	binary.BigEndian.PutUint64(raw[56:], uint64(i+1))
	return base64.StdEncoding.EncodeToString(raw)
}
