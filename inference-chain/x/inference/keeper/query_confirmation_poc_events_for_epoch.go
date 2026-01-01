package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) ConfirmationPoCEventsForEpoch(goCtx context.Context, req *types.QueryConfirmationPoCEventsForEpochRequest) (*types.QueryConfirmationPoCEventsForEpochResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	epochIndex := req.EpochIndex

	// If epoch_index is 0, use the current effective epoch
	if epochIndex == 0 {
		effectiveEpoch, found := k.GetEffectiveEpoch(goCtx)
		if !found || effectiveEpoch == nil {
			return nil, status.Error(codes.NotFound, "no effective epoch found")
		}
		epochIndex = effectiveEpoch.Index
	}

	events, err := k.GetAllConfirmationPoCEventsForEpoch(goCtx, epochIndex)
	if err != nil {
		k.LogError("Error getting confirmation PoC events for epoch", types.PoC, "epoch", epochIndex, "error", err)
		return nil, status.Error(codes.Internal, "failed to query confirmation PoC events")
	}

	// Convert to pointers for proto response
	eventPtrs := make([]*types.ConfirmationPoCEvent, len(events))
	for i := range events {
		eventPtrs[i] = &events[i]
	}

	return &types.QueryConfirmationPoCEventsForEpochResponse{
		Events: eventPtrs,
	}, nil
}

