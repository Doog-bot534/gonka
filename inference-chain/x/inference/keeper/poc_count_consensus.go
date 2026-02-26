package keeper

import (
	"context"
	"sort"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) GetAllPocCountsForStage(ctx context.Context, pocHeight int64) ([]types.PocCount, error) {
	var result []types.PocCount

	iter, err := k.PocCounts.Iterate(ctx, collections.NewPrefixedPairRange[int64, string](pocHeight))
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		value, err := iter.Value()
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func (k Keeper) HasAgreedCountsForStage(ctx context.Context, pocHeight int64) (bool, error) {
	iter, err := k.AgreedCounts.Iterate(ctx, collections.NewPrefixedPairRange[int64, string](pocHeight))
	if err != nil {
		return false, err
	}
	defer iter.Close()
	return iter.Valid(), nil
}

func (k Keeper) GetAllPocWeightCommitsForStage(ctx context.Context, pocHeight int64) (map[string]types.PocWeightCommit, error) {
	result := make(map[string]types.PocWeightCommit)

	iter, err := k.PocWeightCommits.Iterate(ctx, collections.NewPrefixedPairRange[int64, sdk.AccAddress](pocHeight))
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			return nil, err
		}
		value, err := iter.Value()
		if err != nil {
			return nil, err
		}
		addr := key.K2()
		result[addr.String()] = value
	}
	return result, nil
}

func (k Keeper) SetAgreedCount(ctx context.Context, pocHeight int64, participant string, agreed types.AgreedCount) error {
	pk := collections.Join(pocHeight, participant)
	return k.AgreedCounts.Set(ctx, pk, agreed)
}

func (k Keeper) ComputeAgreedCounts(ctx context.Context, pocHeight int64) error {
	allCounts, err := k.GetAllPocCountsForStage(ctx, pocHeight)
	if err != nil {
		return err
	}

	if len(allCounts) == 0 {
		k.LogInfo("[ComputeAgreedCounts] No PocCount entries found", types.PoC, "pocHeight", pocHeight)
		return nil
	}

	totalValidators := int32(len(allCounts))
	requiredAgreement := totalValidators/2 + 1

	countsByParticipant := make(map[string][]uint32)
	for _, pc := range allCounts {
		for _, entry := range pc.Entries {
			if entry.Count > 0 {
				countsByParticipant[entry.Participant] = append(countsByParticipant[entry.Participant], entry.Count)
			}
		}
	}

	for participant, counts := range countsByParticipant {
		uniqueCounts := make(map[uint32]bool)
		for _, c := range counts {
			uniqueCounts[c] = true
		}
		sortedCounts := make([]uint32, 0, len(uniqueCounts))
		for c := range uniqueCounts {
			sortedCounts = append(sortedCounts, c)
		}
		sort.Slice(sortedCounts, func(i, j int) bool {
			return sortedCounts[i] < sortedCounts[j]
		})

		var agreedCount uint32
		var agreeingCount int32

		for _, targetCount := range sortedCounts {
			validatorsAgreeing := int32(0)
			for _, pc := range allCounts {
				for _, entry := range pc.Entries {
					if entry.Participant == participant && entry.Count >= targetCount {
						validatorsAgreeing++
						break
					}
				}
			}
			if validatorsAgreeing >= requiredAgreement {
				agreedCount = targetCount
				agreeingCount = validatorsAgreeing
			}
		}

		if agreedCount > 0 {
			if err := k.SetAgreedCount(ctx, pocHeight, participant, types.AgreedCount{
				Participant:     participant,
				AgreedCount:     agreedCount,
				TotalValidators: totalValidators,
				AgreeingCount:   agreeingCount,
			}); err != nil {
				return err
			}
		}
	}

	k.LogInfo("[ComputeAgreedCounts] Computed", types.PoC,
		"pocHeight", pocHeight,
		"validators", totalValidators,
		"participants", len(countsByParticipant))

	return nil
}
