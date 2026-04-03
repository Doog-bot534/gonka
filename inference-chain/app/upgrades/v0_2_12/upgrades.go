package v0_2_12

import (
	"context"

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

		toVM, err := mm.RunMigrations(ctx, configurator, fromVM)
		if err != nil {
			return toVM, err
		}

		k.LogInfo("successfully upgraded", types.Upgrades, "version", UpgradeName)
		return toVM, nil
	}
}

func removeTopMiner(ctx context.Context, k keeper.Keeper) error {
	err := k.TopMiners.Clear(ctx, nil)
	if err != nil {
		return err
	}
	topRewardAccount := k.AccountKeeper.GetModuleAccount(ctx, types.TopRewardPoolAccName)
	if topRewardAccount != nil {
		rewardAddress := topRewardAccount.GetAddress()
		balances := k.BankView.GetAllBalances(ctx, rewardAddress)
		err := k.BankKeeper.BurnCoins(ctx, types.TopRewardPoolAccName, balances, "Removing top miner reward pool")
		if err != nil {
			return err
		}
	}
	return nil
}

func clearTrainingState(ctx context.Context, k keeper.Keeper) error {
	return k.ClearTrainingState(ctx)
}
