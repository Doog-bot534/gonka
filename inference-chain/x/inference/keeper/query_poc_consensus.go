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

func (k Keeper) PoCObservations(goCtx context.Context, req *types.QueryPoCObservationsRequest) (*types.QueryPoCObservationsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	var observations []*types.PoCObservation

	iter, err := k.PoCObservationsMap.Iterate(ctx, collections.NewPrefixedPairRange[int64, sdk.AccAddress](req.PocStageStartBlockHeight))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to iterate observations: %v", err)
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		value, err := iter.Value()
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to get value: %v", err)
		}
		obs := value
		observations = append(observations, &obs)
	}

	return &types.QueryPoCObservationsResponse{
		Observations: observations,
	}, nil
}

func (k Keeper) PoCConsensus(goCtx context.Context, req *types.QueryPoCConsensusRequest) (*types.QueryPoCConsensusResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	pocHeight := req.PocStageStartBlockHeight

	var observations []types.PoCObservation
	obsIter, err := k.PoCObservationsMap.Iterate(ctx, collections.NewPrefixedPairRange[int64, sdk.AccAddress](pocHeight))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to iterate observations: %v", err)
	}
	defer obsIter.Close()

	for ; obsIter.Valid(); obsIter.Next() {
		value, err := obsIter.Value()
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to get observation: %v", err)
		}
		observations = append(observations, value)
	}

	if len(observations) == 0 {
		return &types.QueryPoCConsensusResponse{}, nil
	}

	commitsByParticipant := make(map[string]uint32)
	commitIter, err := k.PoCV2StoreCommits.Iterate(ctx, collections.NewPrefixedPairRange[int64, sdk.AccAddress](pocHeight))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to iterate commits: %v", err)
	}
	defer commitIter.Close()

	for ; commitIter.Valid(); commitIter.Next() {
		value, err := commitIter.Value()
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to get commit: %v", err)
		}
		commitsByParticipant[value.ParticipantAddress] = value.Count
	}

	headersByParticipant := make(map[string][]uint32)
	for _, obs := range observations {
		for _, arrival := range obs.Arrivals {
			if arrival.Count > 0 {
				headersByParticipant[arrival.Participant] = append(headersByParticipant[arrival.Participant], arrival.Count)
			}
		}
	}

	allParticipants := make(map[string]bool)
	for p := range commitsByParticipant {
		allParticipants[p] = true
	}
	for p := range headersByParticipant {
		allParticipants[p] = true
	}

	var entries []*types.PoCConsensusEntry

	for participant := range allParticipants {
		counts := headersByParticipant[participant]
		if len(counts) == 0 {
			continue
		}

		observingValidators := 0
		for _, obs := range observations {
			for _, arrival := range obs.Arrivals {
				if arrival.Participant == participant {
					observingValidators++
					break
				}
			}
		}
		requiredAgreement := observingValidators/2 + 1

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
			validatorsAgreeing := 0
			for _, obs := range observations {
				for _, arrival := range obs.Arrivals {
					if arrival.Participant == participant && arrival.Count >= targetCount {
						validatorsAgreeing++
						break
					}
				}
			}
			if validatorsAgreeing >= requiredAgreement {
				agreedCount = targetCount
				agreeingCount = int32(validatorsAgreeing)
			}
		}

		entries = append(entries, &types.PoCConsensusEntry{
			Participant:     participant,
			AgreedCount:     agreedCount,
			TotalValidators: int32(observingValidators),
			AgreeingCount:   agreeingCount,
		})
	}

	return &types.QueryPoCConsensusResponse{
		Entries: entries,
	}, nil
}
