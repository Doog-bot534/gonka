package keeper_test

import (
	"sort"
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

type agreedCountResult struct {
	agreedCount     uint32
	agreeingCount   int32
	totalValidators int32
}

func computeAgreedCounts(allCounts []types.PocCount) map[string]agreedCountResult {
	if len(allCounts) == 0 {
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

	results := make(map[string]agreedCountResult)
	for participant, counts := range countsByParticipant {
		sort.Slice(counts, func(i, j int) bool {
			return counts[i] > counts[j]
		})

		var agreedCount uint32
		var agreeingCount int32

		for i, c := range counts {
			if int32(i+1) >= requiredAgreement {
				agreedCount = c
				break
			}
		}

		if agreedCount > 0 {
			for _, c := range counts {
				if c >= agreedCount {
					agreeingCount++
				}
			}
			results[participant] = agreedCountResult{agreedCount, agreeingCount, totalValidators}
		}
	}
	return results
}

func pocCounts(validators map[string]map[string]uint32) []types.PocCount {
	result := make([]types.PocCount, 0, len(validators))
	for validator, entries := range validators {
		var protoEntries []*types.PocCountEntry
		for participant, count := range entries {
			protoEntries = append(protoEntries, &types.PocCountEntry{
				Participant: participant,
				Count:       count,
			})
		}
		result = append(result, types.PocCount{
			Creator: validator,
			Entries: protoEntries,
		})
	}
	return result
}

func TestComputeAgreedCounts_AllSameCount(t *testing.T) {
	counts := pocCounts(map[string]map[string]uint32{
		"v1": {"alice": 10},
		"v2": {"alice": 10},
		"v3": {"alice": 10},
	})
	res := computeAgreedCounts(counts)
	require.Equal(t, uint32(10), res["alice"].agreedCount)
	require.Equal(t, int32(3), res["alice"].agreeingCount)
	require.Equal(t, int32(3), res["alice"].totalValidators)
}

func TestComputeAgreedCounts_NoQuorum(t *testing.T) {
	counts := pocCounts(map[string]map[string]uint32{
		"v1": {"alice": 5},
		"v2": {"alice": 3},
		"v3": {"alice": 2},
	})
	res := computeAgreedCounts(counts)
	require.Equal(t, uint32(3), res["alice"].agreedCount)
	require.Equal(t, int32(2), res["alice"].agreeingCount)
}

func TestComputeAgreedCounts_HighestWithQuorum(t *testing.T) {
	counts := pocCounts(map[string]map[string]uint32{
		"v1": {"alice": 10},
		"v2": {"alice": 10},
		"v3": {"alice": 10},
		"v4": {"alice": 5},
		"v5": {"alice": 3},
	})
	res := computeAgreedCounts(counts)
	require.Equal(t, uint32(10), res["alice"].agreedCount)
	require.Equal(t, int32(3), res["alice"].agreeingCount)
	require.Equal(t, int32(5), res["alice"].totalValidators)
}

func TestComputeAgreedCounts_DuplicateCounts_AgreeingCountCorrect(t *testing.T) {
	counts := pocCounts(map[string]map[string]uint32{
		"v1": {"alice": 7},
		"v2": {"alice": 7},
		"v3": {"alice": 7},
		"v4": {"alice": 7},
	})
	res := computeAgreedCounts(counts)
	require.Equal(t, uint32(7), res["alice"].agreedCount)
	require.Equal(t, int32(4), res["alice"].agreeingCount, "all 4 validators agree, not just quorum minimum")
}

func TestComputeAgreedCounts_BelowQuorumReturnsNothing(t *testing.T) {
	counts := pocCounts(map[string]map[string]uint32{
		"v1": {"alice": 1},
		"v2": {"alice": 1},
	})
	res := computeAgreedCounts(counts)
	require.Equal(t, uint32(1), res["alice"].agreedCount)
	require.Equal(t, int32(2), res["alice"].agreeingCount)
}

func TestComputeAgreedCounts_SingleValidator(t *testing.T) {
	counts := pocCounts(map[string]map[string]uint32{
		"v1": {"alice": 42},
	})
	res := computeAgreedCounts(counts)
	require.Equal(t, uint32(42), res["alice"].agreedCount)
	require.Equal(t, int32(1), res["alice"].agreeingCount)
}

func TestComputeAgreedCounts_TwoValidators_BothAgree(t *testing.T) {
	counts := pocCounts(map[string]map[string]uint32{
		"v1": {"alice": 8},
		"v2": {"alice": 8},
	})
	res := computeAgreedCounts(counts)
	require.Equal(t, uint32(8), res["alice"].agreedCount)
	require.Equal(t, int32(2), res["alice"].agreeingCount)
}

func TestComputeAgreedCounts_TwoValidators_Disagree(t *testing.T) {
	counts := pocCounts(map[string]map[string]uint32{
		"v1": {"alice": 10},
		"v2": {"alice": 5},
	})
	res := computeAgreedCounts(counts)
	require.Equal(t, uint32(5), res["alice"].agreedCount)
	require.Equal(t, int32(2), res["alice"].agreeingCount)
}

func TestComputeAgreedCounts_MultipleParticipants(t *testing.T) {
	counts := pocCounts(map[string]map[string]uint32{
		"v1": {"alice": 10, "bob": 5, "carol": 99},
		"v2": {"alice": 10, "bob": 5},
		"v3": {"alice": 10, "bob": 3},
	})
	res := computeAgreedCounts(counts)

	require.Equal(t, uint32(10), res["alice"].agreedCount)
	require.Equal(t, int32(3), res["alice"].agreeingCount)

	require.Equal(t, uint32(5), res["bob"].agreedCount)
	require.Equal(t, int32(2), res["bob"].agreeingCount)

	_, carolPresent := res["carol"]
	require.False(t, carolPresent, "carol should have no agreed count (only 1 validator reported)")
}

func TestComputeAgreedCounts_ZeroCountsExcluded(t *testing.T) {
	counts := pocCounts(map[string]map[string]uint32{
		"v1": {"alice": 0},
		"v2": {"alice": 0},
		"v3": {"alice": 5},
	})
	res := computeAgreedCounts(counts)
	_, alicePresent := res["alice"]
	require.False(t, alicePresent, "count=0 entries must be ignored; no quorum reached")
}

func TestComputeAgreedCounts_MixedZeroAndNonZero(t *testing.T) {
	counts := pocCounts(map[string]map[string]uint32{
		"v1": {"alice": 0},
		"v2": {"alice": 5},
		"v3": {"alice": 5},
	})
	res := computeAgreedCounts(counts)
	require.Equal(t, uint32(5), res["alice"].agreedCount)
	require.Equal(t, int32(2), res["alice"].agreeingCount)
}

func TestComputeAgreedCounts_EmptyInput(t *testing.T) {
	res := computeAgreedCounts(nil)
	require.Empty(t, res)

	res = computeAgreedCounts([]types.PocCount{})
	require.Empty(t, res)
}

func TestComputeAgreedCounts_ValidatorWithNoEntries(t *testing.T) {
	counts := []types.PocCount{
		{Creator: "v1", Entries: nil},
		{Creator: "v2", Entries: []*types.PocCountEntry{{Participant: "alice", Count: 5}}},
		{Creator: "v3", Entries: []*types.PocCountEntry{{Participant: "alice", Count: 5}}},
	}
	res := computeAgreedCounts(counts)
	require.Equal(t, uint32(5), res["alice"].agreedCount)
	require.Equal(t, int32(2), res["alice"].agreeingCount)
	require.Equal(t, int32(3), res["alice"].totalValidators)
}

func TestComputeAgreedCounts_Keeper_BasicQuorum(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(200)

	pocHeight := int64(100)
	validatorEntries := map[string]map[string]uint32{
		"v1": {"alice": 10, "bob": 5},
		"v2": {"alice": 10, "bob": 5},
		"v3": {"alice": 8, "bob": 3},
	}
	for validator, participantMap := range validatorEntries {
		var protoEntries []*types.PocCountEntry
		for participant, count := range participantMap {
			protoEntries = append(protoEntries, &types.PocCountEntry{Participant: participant, Count: count})
		}
		require.NoError(t, k.PocCounts.Set(sdkCtx,
			collections.Join(pocHeight, validator),
			types.PocCount{Creator: validator, Entries: protoEntries, PocStageStartBlockHeight: pocHeight},
		))
	}

	require.NoError(t, k.ComputeAgreedCounts(sdkCtx, pocHeight))

	aliceAgreed, err := k.AgreedCounts.Get(sdkCtx, collections.Join(pocHeight, "alice"))
	require.NoError(t, err)
	require.Equal(t, uint32(10), aliceAgreed.AgreedCount)
	require.Equal(t, int32(2), aliceAgreed.AgreeingCount)
	require.Equal(t, int32(3), aliceAgreed.TotalValidators)

	bobAgreed, err := k.AgreedCounts.Get(sdkCtx, collections.Join(pocHeight, "bob"))
	require.NoError(t, err)
	require.Equal(t, uint32(5), bobAgreed.AgreedCount)
	require.Equal(t, int32(2), bobAgreed.AgreeingCount)
}

func TestComputeAgreedCounts_Keeper_NoQuorum(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(200)

	pocHeight := int64(100)
	for validator, count := range map[string]uint32{"v1": 5, "v2": 0, "v3": 0} {
		entries := []*types.PocCountEntry{{Participant: "alice", Count: count}}
		require.NoError(t, k.PocCounts.Set(sdkCtx,
			collections.Join(pocHeight, validator),
			types.PocCount{Creator: validator, Entries: entries, PocStageStartBlockHeight: pocHeight},
		))
	}

	require.NoError(t, k.ComputeAgreedCounts(sdkCtx, pocHeight))

	_, err := k.AgreedCounts.Get(sdkCtx, collections.Join(pocHeight, "alice"))
	require.Error(t, err, "no agreed count should be stored when quorum is not reached")
}

func TestComputeAgreedCounts_Keeper_EmptyStage(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(200)

	require.NoError(t, k.ComputeAgreedCounts(sdkCtx, 999))

	has, err := k.HasAgreedCountsForStage(sdkCtx, 999)
	require.NoError(t, err)
	require.False(t, has)
}

func TestComputeAgreedCounts_Keeper_DuplicateCounts_FullAgreeingCount(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(200)

	pocHeight := int64(50)
	for _, v := range []string{"v1", "v2", "v3", "v4"} {
		require.NoError(t, k.PocCounts.Set(sdkCtx,
			collections.Join(pocHeight, v),
			types.PocCount{
				Creator:                  v,
				PocStageStartBlockHeight: pocHeight,
				Entries:                  []*types.PocCountEntry{{Participant: "alice", Count: 7}},
			},
		))
	}

	require.NoError(t, k.ComputeAgreedCounts(sdkCtx, pocHeight))

	agreed, err := k.AgreedCounts.Get(sdkCtx, collections.Join(pocHeight, "alice"))
	require.NoError(t, err)
	require.Equal(t, uint32(7), agreed.AgreedCount)
	require.Equal(t, int32(4), agreed.AgreeingCount, "all 4 validators agree, agreeingCount must be 4 not 3")
	require.Equal(t, int32(4), agreed.TotalValidators)
}
