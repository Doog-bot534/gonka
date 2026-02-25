package v0_2_11

import (
	"context"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	k keeper.Keeper,
) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, plan upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		k.Logger().Info("starting upgrade to " + UpgradeName)

		if _, ok := fromVM["capability"]; !ok {
			fromVM["capability"] = mm.Modules["capability"].(module.HasConsensusVersion).ConsensusVersion()
		}

		currentEpochIndex, err := k.EffectiveEpochIndex.Get(ctx)
		if err != nil {
			return fromVM, err
		}
		if currentEpochIndex < 2 {
			return fromVM, nil
		}

		err = setEpochParticipantsSet(ctx, k, currentEpochIndex)
		if err != nil {
			return fromVM, err
		}

		err = setEpochParticipantsSet(ctx, k, currentEpochIndex-1)
		if err != nil {
			return fromVM, err
		}
		toVM, err := mm.RunMigrations(ctx, configurator, fromVM)
		if err != nil {
			return toVM, err
		}
		k.Logger().Info("successfully upgraded to " + UpgradeName)
		return toVM, nil

	}
}

func setEpochParticipantsSet(ctx context.Context, k keeper.Keeper, epochIndex uint64) error {
	epochActiveParticipants, found := k.GetActiveParticipants(ctx, epochIndex)
	if !found {
		return types.ErrEpochNotFound
	}
	return k.SetActiveParticipantsCache(ctx, epochActiveParticipants)
}
