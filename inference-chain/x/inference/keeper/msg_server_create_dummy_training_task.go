package keeper

import (
	"context"
	"fmt"

	"github.com/productscience/inference/x/inference/training"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) CreateDummyTrainingTask(goCtx context.Context, msg *types.MsgCreateDummyTrainingTask) (*types.MsgCreateDummyTrainingTaskResponse, error) {
	if err := k.CheckPermission(goCtx, msg, TrainingStartPermission); err != nil {
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	// Prevent overwriting existing tasks. Without this check, an attacker
	// can supply an arbitrary task.Id that matches an in-progress task,
	// overwriting its Assignees field to inject themselves and claim rewards.
	if _, exists := k.GetTrainingTask(ctx, msg.Task.Id); exists {
		return nil, fmt.Errorf("training task with ID %d already exists", msg.Task.Id)
	}

	msg.Task.CreatedAtBlockHeight = uint64(ctx.BlockHeight())
	if msg.Task.Epoch == nil {
		msg.Task.Epoch = training.NewEmptyEpochInfo()
	}

	k.SetTrainingTask(ctx, msg.Task)

	return &types.MsgCreateDummyTrainingTaskResponse{
		Task: msg.Task,
	}, nil
}
