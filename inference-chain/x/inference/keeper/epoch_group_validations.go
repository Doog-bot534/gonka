package keeper

import (
	"context"

	"cosmossdk.io/collections"
	"github.com/productscience/inference/x/inference/types"
)

// SetEpochGroupValidation stores one validation entry by (epoch, participant, inferenceID).
func (k Keeper) SetEpochGroupValidation(ctx context.Context, epochIndex uint64, participant string, inferenceID string) error {
	return k.EpochGroupValidationEntry.Set(ctx, collections.Join3(epochIndex, participant, inferenceID))
}

// SeedEpochGroupValidationEntries is a helper for tests/migrations to set many entries.
func (k Keeper) SeedEpochGroupValidationEntries(ctx context.Context, epochGroupValidations types.EpochGroupValidations) error {
	for _, inferenceID := range epochGroupValidations.ValidatedInferences {
		if err := k.SetEpochGroupValidation(ctx, epochGroupValidations.EpochIndex, epochGroupValidations.Participant, inferenceID); err != nil {
			return err
		}
	}
	return nil
}

// GetEpochGroupValidations returns a epochGroupValidations from its index
func (k Keeper) GetEpochGroupValidations(
	ctx context.Context,
	participant string,
	epochIndex uint64,

) (val types.EpochGroupValidations, found bool) {
	validatedInferences := make([]string, 0)
	iter, err := k.EpochGroupValidationEntry.Iterate(ctx, collections.NewSuperPrefixedTripleRange[uint64, string, string](epochIndex, participant))
	if err != nil {
		return val, false
	}
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		key, keyErr := iter.Key()
		if keyErr != nil {
			return val, false
		}
		validatedInferences = append(validatedInferences, key.K3())
	}
	if len(validatedInferences) == 0 {
		return val, false
	}
	return types.EpochGroupValidations{
		Participant:         participant,
		EpochIndex:          epochIndex,
		ValidatedInferences: validatedInferences,
	}, true
}

// TODO(v0.2.11-cleanup): delete this migration helper after the v0.2.11 upgrade
// is finalized on all environments. Keep colocated with epoch-group-validation
// logic to simplify eventual removal.
//
// MigrateEpochGroupValidationsToEntries migrates legacy aggregate validation rows
// from EpochGroupValidationsMap to per-inference validation entries.
func (k Keeper) MigrateEpochGroupValidationsToEntries(ctx context.Context) error {
	currentEpochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		return nil
	}
	previousEpochIndex := currentEpochIndex
	hasPreviousEpoch := false
	if currentEpochIndex > 0 {
		previousEpochIndex = currentEpochIndex - 1
		hasPreviousEpoch = true
	}

	iter, err := k.EpochGroupValidationsMap.Iterate(ctx, nil)
	if err != nil {
		return err
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		v, valueErr := iter.Value()
		if valueErr != nil {
			return valueErr
		}
		shouldCopy := v.EpochIndex == currentEpochIndex || (hasPreviousEpoch && v.EpochIndex == previousEpochIndex)
		if !shouldCopy {
			continue
		}
		for _, inferenceID := range v.ValidatedInferences {
			has, hasErr := k.EpochGroupValidationEntry.Has(ctx, collections.Join3(v.EpochIndex, v.Participant, inferenceID))
			if hasErr != nil {
				return hasErr
			}
			if has {
				continue
			}
			err = k.SetEpochGroupValidation(ctx, v.EpochIndex, v.Participant, inferenceID)
			if err != nil {
				return err
			}
		}
	}

	// Clear legacy aggregate storage in bulk after migrating the subset we need.
	return k.EpochGroupValidationsMap.Clear(ctx, nil)
}
