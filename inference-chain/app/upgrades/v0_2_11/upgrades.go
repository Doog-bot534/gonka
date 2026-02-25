package v0_2_11

import (
	"context"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/productscience/inference/x/inference/keeper"
)

const (
	UpgradeName = "v0.2.11"
)

func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	k keeper.Keeper,
) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, plan upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		k.Logger().Info("Starting upgrade to v0.2.11")

		sdkCtx := sdk.UnwrapSDKContext(ctx)
		// Perform parameter migration
		err := migrateParams(sdkCtx, k)
		if err != nil {
			return nil, err
		}

		return mm.RunMigrations(ctx, configurator, fromVM)
	}
}

func migrateParams(ctx sdk.Context, k keeper.Keeper) error {
	// Params are automatically migrated by RunMigrations if we updated DefaultParams,
	// but since we removed fields, we should ensure the state is consistent.
	// The fields are already gone from the Go structs, so when we Get and then Set,
	// the old fields (which are now in the reserved/unknown range in proto) will be ignored
	// or handled by the proto marshaler.

	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	// Re-setting params will effectively "clean" them of removed fields
	err = k.SetParams(ctx, params)
	if err != nil {
		return err
	}

	genesisOnlyParams, found := k.GetGenesisOnlyParams(ctx)
	if found {
		err = k.SetGenesisOnlyParams(ctx, &genesisOnlyParams)
		if err != nil {
			return err
		}
	}
	return nil
}
