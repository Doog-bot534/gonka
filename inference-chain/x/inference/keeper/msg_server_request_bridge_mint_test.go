package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestMsgServer_RequestBridgeMint_Permissions(t *testing.T) {
	_, ms, ctx, mocks := setupKeeperWithMocks(t)
	wctx := sdk.UnwrapSDKContext(ctx)

	// Non-existent account should fail
	signer, _ := sdk.AccAddressFromBech32(testutil.Creator)
	mocks.AccountKeeper.EXPECT().GetAccount(wctx, signer).Return(nil)
	msg := &types.MsgRequestBridgeMint{Creator: testutil.Creator}
	err := keeper.CheckPermission(ms, wctx, msg, keeper.AccountPermission)
	require.Error(t, err)

	// Existing account should pass
	acct := authtypes.NewBaseAccountWithAddress(signer)
	mocks.AccountKeeper.EXPECT().GetAccount(wctx, signer).Return(acct)
	err = keeper.CheckPermission(ms, wctx, msg, keeper.AccountPermission)
	require.NoError(t, err)
}
