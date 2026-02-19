package keeper_test

import (
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestPoCConsensusQuery_Empty(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	resp, err := k.PoCConsensus(sdkCtx, &types.QueryPoCConsensusRequest{
		PocStageStartBlockHeight: 100,
	})
	require.NoError(t, err)
	require.Empty(t, resp.Entries)
}

func TestPoCConsensusQuery_NilRequest(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	_, err := k.PoCConsensus(sdkCtx, nil)
	require.Error(t, err)
}

func TestPoCConsensusQuery_TreeRoots_MajorityAgreement(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	participantA := "gonka1xxczezuqw0pe67xag5s3zgyrzh4w3zyermjgs9"

	for i := int32(0); i < 3; i++ {
		commit := types.TreeRootCommit{
			Creator:                  "root" + string(rune('0'+i)),
			PocStageStartBlockHeight: 100,
			TreeIndex:                i,
			Entries: []*types.TreeRootCommitEntry{
				{Participant: participantA, Count: 10},
			},
			BlockHeight: 105,
		}
		require.NoError(t, k.TreeRootCommits.Set(sdkCtx, collections.Join(int64(100), i), commit))
	}

	resp, err := k.PoCConsensus(sdkCtx, &types.QueryPoCConsensusRequest{
		PocStageStartBlockHeight: 100,
	})
	require.NoError(t, err)
	require.Len(t, resp.Entries, 1)
	require.Equal(t, participantA, resp.Entries[0].Participant)
	require.Equal(t, uint32(10), resp.Entries[0].AgreedCount)
	require.Equal(t, int32(3), resp.Entries[0].TotalValidators)
	require.Equal(t, int32(3), resp.Entries[0].AgreeingCount)
}

func TestPoCConsensusQuery_TreeRoots_SplitVote(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	participantA := "gonka1xxczezuqw0pe67xag5s3zgyrzh4w3zyermjgs9"

	counts := []uint32{20, 10, 5}
	for i := int32(0); i < 3; i++ {
		commit := types.TreeRootCommit{
			Creator:                  "root",
			PocStageStartBlockHeight: 100,
			TreeIndex:                i,
			Entries: []*types.TreeRootCommitEntry{
				{Participant: participantA, Count: counts[i]},
			},
			BlockHeight: 105,
		}
		require.NoError(t, k.TreeRootCommits.Set(sdkCtx, collections.Join(int64(100), i), commit))
	}

	resp, err := k.PoCConsensus(sdkCtx, &types.QueryPoCConsensusRequest{
		PocStageStartBlockHeight: 100,
	})
	require.NoError(t, err)
	require.Len(t, resp.Entries, 1)
	require.Equal(t, uint32(10), resp.Entries[0].AgreedCount)
	require.Equal(t, int32(2), resp.Entries[0].AgreeingCount)
}

func TestTreeRootCommitsForStage_Empty(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	resp, err := k.TreeRootCommitsForStage(sdkCtx, &types.QueryTreeRootCommitsForStageRequest{
		PocStageStartBlockHeight: 100,
	})
	require.NoError(t, err)
	require.Empty(t, resp.Commits)
}

func TestTreeRootCommitsForStage_ReturnsStored(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	commit := types.TreeRootCommit{
		Creator:                  "root0",
		PocStageStartBlockHeight: 100,
		TreeIndex:                0,
		Entries: []*types.TreeRootCommitEntry{
			{Participant: "p1", Count: 10},
			{Participant: "p2", Count: 5},
		},
		BlockHeight: 105,
	}
	require.NoError(t, k.TreeRootCommits.Set(sdkCtx, collections.Join(int64(100), int32(0)), commit))

	resp, err := k.TreeRootCommitsForStage(sdkCtx, &types.QueryTreeRootCommitsForStageRequest{
		PocStageStartBlockHeight: 100,
	})
	require.NoError(t, err)
	require.Len(t, resp.Commits, 1)
	require.Len(t, resp.Commits[0].Entries, 2)
}
