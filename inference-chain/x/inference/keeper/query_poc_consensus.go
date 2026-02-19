package keeper

import (
	"context"
	"sort"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) TreeRootCommitsForStage(goCtx context.Context, req *types.QueryTreeRootCommitsForStageRequest) (*types.QueryTreeRootCommitsForStageResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	var commits []*types.TreeRootCommit

	iter, err := k.TreeRootCommits.Iterate(ctx, collections.NewPrefixedPairRange[int64, int32](req.PocStageStartBlockHeight))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to iterate tree root commits: %v", err)
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		value, err := iter.Value()
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to get tree root commit: %v", err)
		}
		v := value
		commits = append(commits, &v)
	}

	return &types.QueryTreeRootCommitsForStageResponse{
		Commits: commits,
	}, nil
}

func (k Keeper) PoCConsensus(goCtx context.Context, req *types.QueryPoCConsensusRequest) (*types.QueryPoCConsensusResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	pocHeight := req.PocStageStartBlockHeight

	var treeCommits []types.TreeRootCommit
	treeIter, err := k.TreeRootCommits.Iterate(ctx, collections.NewPrefixedPairRange[int64, int32](pocHeight))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to iterate tree root commits: %v", err)
	}
	defer treeIter.Close()

	for ; treeIter.Valid(); treeIter.Next() {
		value, err := treeIter.Value()
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to get tree root commit: %v", err)
		}
		treeCommits = append(treeCommits, value)
	}

	if len(treeCommits) == 0 {
		return &types.QueryPoCConsensusResponse{}, nil
	}

	totalTrees := int32(len(treeCommits))
	requiredAgreement := totalTrees/2 + 1

	countsByParticipant := make(map[string][]uint32)
	for _, tc := range treeCommits {
		for _, entry := range tc.Entries {
			if entry.Count > 0 {
				countsByParticipant[entry.Participant] = append(countsByParticipant[entry.Participant], entry.Count)
			}
		}
	}

	var entries []*types.PoCConsensusEntry

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
			treesAgreeing := int32(0)
			for _, tc := range treeCommits {
				for _, entry := range tc.Entries {
					if entry.Participant == participant && entry.Count >= targetCount {
						treesAgreeing++
						break
					}
				}
			}
			if treesAgreeing >= requiredAgreement {
				agreedCount = targetCount
				agreeingCount = treesAgreeing
			}
		}

		entries = append(entries, &types.PoCConsensusEntry{
			Participant:     participant,
			AgreedCount:     agreedCount,
			TotalValidators: totalTrees,
			AgreeingCount:   agreeingCount,
		})
	}

	return &types.QueryPoCConsensusResponse{
		Entries: entries,
	}, nil
}
