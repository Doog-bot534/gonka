package keeper

import (
	"context"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) AgreedCount(goCtx context.Context, req *types.QueryAgreedCountRequest) (*types.QueryAgreedCountResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	pk := collections.Join(req.PocStageStartBlockHeight, req.ParticipantAddress)
	entry, err := k.AgreedCounts.Get(ctx, pk)
	if err != nil {
		return &types.QueryAgreedCountResponse{Found: false}, nil
	}

	return &types.QueryAgreedCountResponse{
		AgreedCount: entry.AgreedCount,
		Found:       true,
	}, nil
}
