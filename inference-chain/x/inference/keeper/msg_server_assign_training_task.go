package keeper

import (
	"context"
	"fmt"
	"strconv"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) AssignTrainingTask(goCtx context.Context, msg *types.MsgAssignTrainingTask) (*types.MsgAssignTrainingTaskResponse, error) {
	if err := k.CheckPermission(goCtx, msg, TrainingStartPermission); err != nil {
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	// Verify the caller is the rightful assigner who claimed this task.
	// Without this, any account with TrainingStartPermission can front-run
	// the legitimate assigner after they did the off-chain availability check,
	// hijacking the task assignment with their own colluding nodes.
	task, found := k.GetTrainingTask(ctx, msg.TaskId)
	if !found {
		return nil, types.ErrTrainingTaskNotFound
	}
	if task.Assigner != "" && task.Assigner != msg.Creator {
		return nil, fmt.Errorf("only the task assigner (%s) can assign this task, got %s", task.Assigner, msg.Creator)
	}

	err = k.StartTask(ctx, msg.TaskId, msg.Assignees)
	if err != nil {
		k.LogError("MsgAssignTrainingTask: failed to StartTask", types.Training, "error", err)
		return nil, err
	}

	k.LogInfo("MsgAssignTrainingTask: task assigned and started, emitting training_task_assigned event", types.Training, "taskId", msg.TaskId, "assignees", msg.Assignees)
	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			"training_task_assigned",
			sdk.NewAttribute("task_id", strconv.FormatUint(msg.TaskId, 10)),
		),
	)

	return &types.MsgAssignTrainingTaskResponse{}, nil
}
