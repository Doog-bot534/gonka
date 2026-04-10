package v0_2_12

import (
	"context"
	"errors"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	distrkeeper "github.com/cosmos/cosmos-sdk/x/distribution/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	k keeper.Keeper,
	distrKeeper distrkeeper.Keeper,
) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, plan upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		k.LogInfo("starting upgrade", types.Upgrades, "version", UpgradeName)

		// Keep capability module version explicit to avoid re-running InitGenesis
		// on chains where capability state already exists but version map is missing.
		if _, ok := fromVM["capability"]; !ok {
			fromVM["capability"] = mm.Modules["capability"].(module.HasConsensusVersion).ConsensusVersion()
		}

		err := removeTopMiner(ctx, k)
		if err != nil {
			return nil, err
		}

		err = clearTrainingState(ctx, k)
		if err != nil {
			return nil, err
		}

		// Multi-model migration steps.
		err = clearLegacyPoCv2Data(ctx, k)
		if err != nil {
			return nil, err
		}

		err = migrateParams(ctx, k)
		if err != nil {
			return nil, err
		}

		err = backfillVotingPower(ctx, k)
		if err != nil {
			return nil, err
		}

		err = initNewPruningState(ctx, k)
		if err != nil {
			return nil, err
		}

		err = adjustParameters(ctx, k)
		if err != nil {
			return nil, err
		}
		toVM, err := mm.RunMigrations(ctx, configurator, fromVM)
		if err != nil {
			return toVM, err
		}

		k.LogInfo("successfully upgraded", types.Upgrades, "version", UpgradeName)
		return toVM, nil
	}
}

func adjustParameters(ctx context.Context, k keeper.Keeper) error {
	// For start, a simple roundtrip for params to clear out now-removed values
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	params.XXX_DiscardUnknown()

	if params.ValidationParams == nil {
		params.ValidationParams = types.DefaultValidationParams()
	}
	params.ValidationParams.LogprobsMode = types.DefaultLogprobsMode

	err = k.SetParams(ctx, params)
	if err != nil {
		return err
	}

	genesisParams, found := k.GetGenesisOnlyParams(ctx)
	if !found {
		return errors.New("genesis only params not found")
	}
	genesisParams.XXX_DiscardUnknown()
	err = k.SetGenesisOnlyParams(ctx, &genesisParams)
	if err != nil {
		return err
	}
	return nil
}

func removeTopMiner(ctx context.Context, k keeper.Keeper) error {
	err := k.TopMiners.Clear(ctx, nil)
	if err != nil {
		return err
	}
	tokenomicsData, found := k.GetTokenomicsData(ctx)
	if !found {
		return errors.New("tokenomics data not found")
	}
	tokenomicsData.XXX_DiscardUnknown()
	err = k.SetTokenomicsData(ctx, tokenomicsData)
	if err != nil {
		return err
	}
	return nil
}

func clearTrainingState(ctx context.Context, k keeper.Keeper) error {
	return k.ClearTrainingState(ctx)
}

// clearLegacyPoCv2Data removes all entries under the legacy PoC v2 prefixes
// (38, 39, 40). These collections changed key codec in v0.2.12 -- model_id was
// added to the key -- and were moved to new prefixes (58, 59, 60). The old
// entries cannot be decoded with the new codec, so we clear them with raw
// store iteration. Safe because this data is ephemeral per-epoch and the first
// post-upgrade epoch writes fresh records under the new prefixes.
func clearLegacyPoCv2Data(ctx context.Context, k keeper.Keeper) error {
	return k.ClearLegacyPoCv2Data(ctx)
}

// migrateParams populates PocParams.Models from the deprecated singular fields
// (ModelId, SeqLen, StatTest, WeightScaleFactor) and initializes
// DelegationParams with defaults. Idempotent: skips work if Models is already
// populated.
func migrateParams(ctx context.Context, k keeper.Keeper) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}

	poc := params.PocParams
	if poc != nil && len(poc.Models) == 0 {
		poc.Models = []*types.PoCModelConfig{
			{
				ModelId:           poc.ModelId,
				SeqLen:            poc.SeqLen,
				StatTest:          poc.StatTest,
				WeightScaleFactor: poc.WeightScaleFactor,
				PenaltyStartEpoch: 0,
			},
		}
		k.LogInfo("migrated PocParams singular fields into models[]", types.Upgrades,
			"model_id", poc.ModelId, "seq_len", poc.SeqLen)
	}

	if params.DelegationParams == nil {
		params.DelegationParams = types.DefaultDelegationParams()
		k.LogInfo("initialized DelegationParams with defaults", types.Upgrades)
	}
	if poc != nil && params.DelegationParams.InitialModelId == "" {
		params.DelegationParams.InitialModelId = poc.ModelId
	}

	return k.SetParams(ctx, params)
}

// initNewPruningState seeds the four pruning-state fields introduced in
// v0.2.12 (PocValidationsV2, PocV2StoreCommits, MlnodeWeightDistributions,
// PocValidationSnapshots) to the current effective epoch index. Without this,
// the first post-upgrade Prune() call would walk every historical epoch from
// 1 to currentEpoch-threshold finding empty ranges and writing a PruningState
// update per epoch. Seeding to currentEpoch makes startEpoch > endEpoch, so
// the pruners wait for fresh data to accumulate under the new prefixes.
func initNewPruningState(ctx context.Context, k keeper.Keeper) error {
	epochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		k.LogInfo("initNewPruningState: no effective epoch, skipping", types.Upgrades)
		return nil
	}
	current := int64(epochIndex)

	state, err := k.PruningState.Get(ctx)
	if err != nil {
		return err
	}
	if state.PocValidationsV2PrunedEpoch < current {
		state.PocValidationsV2PrunedEpoch = current
	}
	if state.PocV2StoreCommitsPrunedEpoch < current {
		state.PocV2StoreCommitsPrunedEpoch = current
	}
	if state.MlnodeWeightDistributionsPrunedEpoch < current {
		state.MlnodeWeightDistributionsPrunedEpoch = current
	}
	if state.PocValidationSnapshotsPrunedEpoch < current {
		state.PocValidationSnapshotsPrunedEpoch = current
	}
	if err := k.PruningState.Set(ctx, state); err != nil {
		return err
	}
	k.LogInfo("initNewPruningState: seeded new pruning markers", types.Upgrades,
		"epoch", current)
	return nil
}

// backfillVotingPower populates AP.VotingPowers for the current epoch and
// ValidationWeight.voting_power for the current epoch's model subgroups.
// Pre-upgrade state is single-model with no delegation, so every participant
// is DIRECT and their voting_power equals their consensus weight.
//
// This is required because getEffectiveValidationBaseState reads voting_power
// from EpochGroupData subgroups; zero values would break validation acceptance
// for the first post-upgrade epoch.
func backfillVotingPower(ctx context.Context, k keeper.Keeper) error {
	epochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		k.LogInfo("backfillVotingPower: no effective epoch, skipping", types.Upgrades)
		return nil
	}

	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	if params.PocParams == nil || len(params.PocParams.Models) == 0 {
		k.LogInfo("backfillVotingPower: no models configured, skipping", types.Upgrades)
		return nil
	}
	modelID := params.PocParams.Models[0].ModelId
	if modelID == "" {
		k.LogInfo("backfillVotingPower: primary model_id is empty, skipping", types.Upgrades)
		return nil
	}

	// Backfill ActiveParticipants.VotingPowers for the effective epoch.
	ap, apFound := k.GetActiveParticipants(ctx, epochIndex)
	if apFound {
		changed := false
		for _, p := range ap.Participants {
			if p == nil {
				continue
			}
			if len(p.VotingPowers) == 0 {
				p.VotingPowers = []*types.ModelVotingPower{
					{ModelId: modelID, VotingPower: p.Weight},
				}
				changed = true
			}
		}
		if changed {
			if err := k.SetActiveParticipants(ctx, ap); err != nil {
				return err
			}
			k.LogInfo("backfillVotingPower: updated ActiveParticipants", types.Upgrades,
				"epoch", epochIndex, "count", len(ap.Participants))
		}
	}

	// Backfill EpochGroupData.ValidationWeight.voting_power for the current
	// epoch's model subgroup. In single-model no-delegation, voting_power
	// equals the subgroup's consensus weight for each member.
	subgroupData, found := k.GetEpochGroupData(ctx, epochIndex, modelID)
	if !found {
		k.LogInfo("backfillVotingPower: no subgroup data for model, skipping subgroup backfill", types.Upgrades,
			"epoch", epochIndex, "model_id", modelID)
		return nil
	}
	changed := false
	for _, vw := range subgroupData.ValidationWeights {
		if vw == nil {
			continue
		}
		if vw.VotingPower == 0 && vw.Weight > 0 {
			vw.VotingPower = vw.Weight
			changed = true
		}
	}
	if changed {
		k.SetEpochGroupData(ctx, subgroupData)
		k.LogInfo("backfillVotingPower: updated EpochGroupData subgroup voting_power", types.Upgrades,
			"epoch", epochIndex, "model_id", modelID, "entries", len(subgroupData.ValidationWeights))
	}

	return nil
}
