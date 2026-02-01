package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) PoCValidationSnapshot(ctx context.Context, req *types.QueryPoCValidationSnapshotRequest) (*types.QueryPoCValidationSnapshotResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	snapshot, found, err := k.GetPoCValidationSnapshot(ctx, req.PocStageStartHeight)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryPoCValidationSnapshotResponse{
		Snapshot: &snapshot,
		Found:    found,
	}, nil
}
