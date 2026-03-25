package inference

import (
	"testing"

	"cosmossdk.io/core/header"
	"cosmossdk.io/log"
	mathsdk "cosmossdk.io/math"
	"cosmossdk.io/store"
	"cosmossdk.io/store/metrics"
	storetypes "cosmossdk.io/store/types"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	blskeeper "github.com/productscience/inference/x/bls/keeper"
	blstypes "github.com/productscience/inference/x/bls/types"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

type noopLogger struct{}

func (noopLogger) LogInfo(string, types.SubSystem, ...interface{})  {}
func (noopLogger) LogError(string, types.SubSystem, ...interface{}) {}
func (noopLogger) LogWarn(string, types.SubSystem, ...interface{})  {}
func (noopLogger) LogDebug(string, types.SubSystem, ...interface{}) {}

func TestWeightCalculator_CalculateParticipantWeight_UsesPerModelWeightScaleFactor(t *testing.T) {
	modelAKey := types.PoCParticipantModelKey{
		ParticipantAddress: testutil.Executor,
		ModelID:            "model-a",
	}
	modelBKey := types.PoCParticipantModelKey{
		ParticipantAddress: testutil.Executor,
		ModelID:            "model-b",
	}

	wc := &WeightCalculator{
		StoreCommits: map[types.PoCParticipantModelKey]types.PoCV2StoreCommit{
			modelAKey: {
				ParticipantAddress:       testutil.Executor,
				PocStageStartBlockHeight: 100,
				Count:                    10,
				ModelId:                  "model-a",
			},
			modelBKey: {
				ParticipantAddress:       testutil.Executor,
				PocStageStartBlockHeight: 100,
				Count:                    10,
				ModelId:                  "model-b",
			},
		},
		NodeWeightDistributions: map[types.PoCParticipantModelKey]types.MLNodeWeightDistribution{
			modelAKey: {
				ParticipantAddress:       testutil.Executor,
				PocStageStartBlockHeight: 100,
				ModelId:                  "model-a",
				Weights: []*types.MLNodeWeight{{
					NodeId: "node-a",
					Weight: 10,
				}},
			},
			modelBKey: {
				ParticipantAddress:       testutil.Executor,
				PocStageStartBlockHeight: 100,
				ModelId:                  "model-b",
				Weights: []*types.MLNodeWeight{{
					NodeId: "node-b",
					Weight: 10,
				}},
			},
		},
		PocParams: &types.PocParams{
			Models: []*types.PoCModelConfig{
				{
					ModelId:           "model-a",
					WeightScaleFactor: types.DecimalFromFloat(1.0),
				},
				{
					ModelId:           "model-b",
					WeightScaleFactor: types.DecimalFromFloat(2.0),
				},
			},
		},
		Logger:                  noopLogger{},
		TimeNormalizationFactor: mathsdk.LegacyOneDec(),
	}

	modelANodes, modelAWeight := wc.calculateParticipantWeight(modelAKey)
	modelBNodes, modelBWeight := wc.calculateParticipantWeight(modelBKey)

	require.Equal(t, int64(10), modelAWeight)
	require.Equal(t, int64(20), modelBWeight)
	require.Len(t, modelANodes, 1)
	require.Len(t, modelBNodes, 1)
	require.Equal(t, int64(10), modelANodes[0].weight)
	require.Equal(t, int64(20), modelBNodes[0].weight)
}

func TestUpdateConfirmationWeightsV2_UsesPerModelWeightScaleFactor(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonkapub")

	k, ctx := newMinimalInferenceKeeper(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams = &types.PocParams{
		ValidationSlots:         0,
		PocNormalizationEnabled: false,
		Models: []*types.PoCModelConfig{
			{
				ModelId:           "model-a",
				WeightScaleFactor: types.DecimalFromFloat(1.0),
			},
			{
				ModelId:           "model-b",
				WeightScaleFactor: types.DecimalFromFloat(2.0),
			},
		},
	}
	require.NoError(t, k.SetParams(ctx, params))

	am := NewAppModule(nil, k, nil, nil, nil, nil)

	require.NoError(t, k.SetParticipant(ctx, types.Participant{
		Index:        testutil.Executor,
		Address:      testutil.Executor,
		ValidatorKey: "validator-key",
		InferenceUrl: "http://example.com/",
	}))
	k.SetRandomSeed(ctx, types.RandomSeed{
		Participant: testutil.Executor,
		EpochIndex:  2,
		Signature:   "sig",
	})

	require.NoError(t, k.SetPoCV2StoreCommit(ctx, types.PoCV2StoreCommit{
		ParticipantAddress:       testutil.Executor,
		PocStageStartBlockHeight: 180,
		Count:                    10,
		RootHash:                 make([]byte, 32),
		CommitBlockHeight:        180,
		ModelId:                  "model-a",
	}))
	require.NoError(t, k.SetPoCV2StoreCommit(ctx, types.PoCV2StoreCommit{
		ParticipantAddress:       testutil.Executor,
		PocStageStartBlockHeight: 180,
		Count:                    10,
		RootHash:                 make([]byte, 32),
		CommitBlockHeight:        180,
		ModelId:                  "model-b",
	}))

	require.NoError(t, k.SetMLNodeWeightDistribution(ctx, types.MLNodeWeightDistribution{
		ParticipantAddress:       testutil.Executor,
		PocStageStartBlockHeight: 180,
		ModelId:                  "model-a",
		Weights: []*types.MLNodeWeight{{
			NodeId: "node-a",
			Weight: 10,
		}},
	}))
	require.NoError(t, k.SetMLNodeWeightDistribution(ctx, types.MLNodeWeightDistribution{
		ParticipantAddress:       testutil.Executor,
		PocStageStartBlockHeight: 180,
		ModelId:                  "model-b",
		Weights: []*types.MLNodeWeight{{
			NodeId: "node-b",
			Weight: 10,
		}},
	}))

	require.NoError(t, k.SetPocValidationV2(ctx, types.PoCValidationV2{
		ParticipantAddress:          testutil.Executor,
		ValidatorParticipantAddress: testutil.Validator,
		PocStageStartBlockHeight:    180,
		ValidatedWeight:             10,
		ModelId:                     "model-a",
	}))
	require.NoError(t, k.SetPocValidationV2(ctx, types.PoCValidationV2{
		ParticipantAddress:          testutil.Executor,
		ValidatorParticipantAddress: testutil.Validator,
		PocStageStartBlockHeight:    180,
		ValidatedWeight:             10,
		ModelId:                     "model-b",
	}))

	event := &types.ConfirmationPoCEvent{
		EpochIndex:            2,
		EventSequence:         0,
		TriggerHeight:         180,
		GenerationStartHeight: 190,
		Phase:                 types.ConfirmationPoCPhase_CONFIRMATION_POC_COMPLETED,
	}

	result := am.updateConfirmationWeightsV2(ctx, event, map[string]int64{
		testutil.Validator: 100,
	})

	require.Len(t, result, 1)
	require.Equal(t, testutil.Executor, result[0].Index)
	require.Equal(t, int64(30), result[0].Weight)
}

func newMinimalInferenceKeeper(t *testing.T) (keeper.Keeper, sdk.Context) {
	t.Helper()

	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	storeKey := storetypes.NewKVStoreKey(types.StoreKey)
	transientStoreKey := storetypes.NewTransientStoreKey(types.TransientStoreKey)
	blsStoreKey := storetypes.NewKVStoreKey(blstypes.StoreKey)

	db := dbm.NewMemDB()
	stateStore := store.NewCommitMultiStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	stateStore.MountStoreWithDB(storeKey, storetypes.StoreTypeIAVL, db)
	stateStore.MountStoreWithDB(transientStoreKey, storetypes.StoreTypeTransient, db)
	stateStore.MountStoreWithDB(blsStoreKey, storetypes.StoreTypeIAVL, db)
	require.NoError(t, stateStore.LoadLatestVersion())

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	authority := authtypes.NewModuleAddress(govtypes.ModuleName)

	blsKeeper := blskeeper.NewKeeper(
		cdc,
		runtime.NewKVStoreService(blsStoreKey),
		log.NewNopLogger(),
		authority.String(),
	)
	k := keeper.NewKeeper(
		cdc,
		runtime.NewKVStoreService(storeKey),
		runtime.NewTransientStoreService(transientStoreKey),
		log.NewNopLogger(),
		authority.String(),
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		blsKeeper,
		nil,
		nil,
		nil,
		func() wasmkeeper.Keeper { return wasmkeeper.Keeper{} },
		nil,
	)

	ctx := sdk.NewContext(stateStore, cmtproto.Header{}, false, log.NewNopLogger()).
		WithHeaderInfo(header.Info{
			Hash: []byte{1, 2, 3, 4, 5, 6, 7, 8},
		})

	require.NoError(t, k.SetParams(ctx, types.DefaultParams()))
	require.NoError(t, blsKeeper.SetParams(ctx, blstypes.DefaultParams()))
	require.NoError(t, k.SetTokenomicsData(ctx, types.TokenomicsData{}))
	genesisParams := types.DefaultGenesisOnlyParams()
	require.NoError(t, k.SetGenesisOnlyParams(ctx, &genesisParams))

	return k, ctx
}
