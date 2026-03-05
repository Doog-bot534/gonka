package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestCreateEscrow_Success(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	wctx := sdk.UnwrapSDKContext(ctx)
	k.SetModel(wctx, &types.Model{Id: "model-1", ProposedBy: "test", UnitsOfComputePerToken: 1})

	resp, err := ms.CreateEscrow(wctx, &types.MsgCreateEscrow{
		Creator: testutil.Creator,
		ModelId: "model-1",
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, "1", resp.EscrowId)

	access, err := k.EscrowAccessByID.Get(wctx, resp.EscrowId)
	require.NoError(t, err)
	require.Equal(t, testutil.Creator, access.DeveloperAddress)
	require.Equal(t, "model-1", access.ModelId)
}

func TestCreateEscrow_UsesGlobalSequenceAsID(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	wctx := sdk.UnwrapSDKContext(ctx)
	k.SetModel(wctx, &types.Model{Id: "model-1", ProposedBy: "test", UnitsOfComputePerToken: 1})
	k.SetModel(wctx, &types.Model{Id: "model-2", ProposedBy: "test", UnitsOfComputePerToken: 1})

	resp1, err := ms.CreateEscrow(wctx, &types.MsgCreateEscrow{
		Creator: testutil.Creator,
		ModelId: "model-1",
	})
	require.NoError(t, err)
	require.NotNil(t, resp1)
	require.Equal(t, "1", resp1.EscrowId)

	resp2, err := ms.CreateEscrow(wctx, &types.MsgCreateEscrow{
		Creator: testutil.Executor,
		ModelId: "model-2",
	})
	require.NoError(t, err)
	require.NotNil(t, resp2)
	require.Equal(t, "2", resp2.EscrowId)

	access1, err := k.EscrowAccessByID.Get(wctx, resp1.EscrowId)
	require.NoError(t, err)
	require.Equal(t, testutil.Creator, access1.DeveloperAddress)
	require.Equal(t, "model-1", access1.ModelId)

	access2, err := k.EscrowAccessByID.Get(wctx, resp2.EscrowId)
	require.NoError(t, err)
	require.Equal(t, testutil.Executor, access2.DeveloperAddress)
	require.Equal(t, "model-2", access2.ModelId)
}

func TestCreateEscrow_RejectsUnknownGovernanceModel(t *testing.T) {
	_, ms, ctx := setupMsgServer(t)
	wctx := sdk.UnwrapSDKContext(ctx)

	_, err := ms.CreateEscrow(wctx, &types.MsgCreateEscrow{
		Creator: testutil.Creator,
		ModelId: "unknown-model",
	})
	require.Error(t, err)
	require.ErrorIs(t, err, types.ErrInvalidModel)
}

func TestCreateEscrow_EmitsDeveloperPubKeyAttribute(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	wctx := sdk.UnwrapSDKContext(ctx)
	k.SetModel(wctx, &types.Model{Id: "model-1", ProposedBy: "test", UnitsOfComputePerToken: 1})
	_, err := ms.CreateEscrow(wctx, &types.MsgCreateEscrow{
		Creator: testutil.Creator,
		ModelId: "model-1",
	})
	require.NoError(t, err)

	events := wctx.EventManager().Events()
	foundEscrowCreated := false
	foundDeveloperPubKeyAttr := false
	foundEpochIDAttr := false
	for _, event := range events {
		if event.Type != "escrow_created" {
			continue
		}
		foundEscrowCreated = true
		for _, attr := range event.Attributes {
			if attr.Key == "developer_pubkey" {
				foundDeveloperPubKeyAttr = true
			}
			if attr.Key == "epoch_id" {
				foundEpochIDAttr = true
			}
		}
	}
	require.True(t, foundEscrowCreated)
	require.True(t, foundDeveloperPubKeyAttr)
	require.True(t, foundEpochIDAttr)
}
