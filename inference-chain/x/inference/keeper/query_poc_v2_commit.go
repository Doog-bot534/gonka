package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// PoCV2StoreCommit returns the stored commit for a participant at a given PoC stage.
func (k Keeper) PoCV2StoreCommit(goCtx context.Context, req *types.QueryPoCV2StoreCommitRequest) (*types.QueryPoCV2StoreCommitResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	addr, err := sdk.AccAddressFromBech32(req.ParticipantAddress)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid participant address: %v", err)
	}

	pk := collections.Join(req.PocStageStartBlockHeight, addr)
	commit, err := k.PoCV2StoreCommits.Get(ctx, pk)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return &types.QueryPoCV2StoreCommitResponse{Found: false}, nil
		}
		return nil, status.Errorf(codes.Internal, "failed to get commit: %v", err)
	}

	return &types.QueryPoCV2StoreCommitResponse{
		Count:    commit.Count,
		RootHash: commit.RootHash,
		Found:    true,
	}, nil
}

// MLNodeWeightDistribution returns the stored weight distribution for a participant at a given PoC stage.
func (k Keeper) MLNodeWeightDistribution(goCtx context.Context, req *types.QueryMLNodeWeightDistributionRequest) (*types.QueryMLNodeWeightDistributionResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	addr, err := sdk.AccAddressFromBech32(req.ParticipantAddress)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid participant address: %v", err)
	}

	pk := collections.Join(req.PocStageStartBlockHeight, addr)
	distribution, err := k.MLNodeWeightDistributions.Get(ctx, pk)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return &types.QueryMLNodeWeightDistributionResponse{Found: false}, nil
		}
		return nil, status.Errorf(codes.Internal, "failed to get distribution: %v", err)
	}

	return &types.QueryMLNodeWeightDistributionResponse{
		Weights: distribution.Weights,
		Found:   true,
	}, nil
}
