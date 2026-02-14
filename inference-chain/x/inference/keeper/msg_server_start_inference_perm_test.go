package keeper_test

import (
	"testing"

	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestMsgServer_StartInference_Permissions(t *testing.T) {
	k, ms, ctx, _ := setupPermissionsHarness(t)
	signer := testutil.Creator
	msg := &types.MsgStartInference{Creator: signer}

	// Not active -> fail
	err := keeper.CheckPermission(ms, ctx, msg, keeper.ActiveParticipantPermission)
	require.Error(t, err)

	// Make active
	require.NoError(t, k.EffectiveEpochIndex.Set(ctx, 10))
	ap := types.ActiveParticipants{EpochId: 10, Participants: []*types.ActiveParticipant{{Index: signer}}}
	require.NoError(t, k.SetActiveParticipants(ctx, ap))

	// Success
	err = keeper.CheckPermission(ms, ctx, msg, keeper.ActiveParticipantPermission)
	require.NoError(t, err)
}
