package keeper_test

import (
	"math"
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func setupSubnetEscrowTest(t testing.TB) (keeper.Keeper, types.MsgServer, sdk.Context, *keepertest.InferenceMocks) {
	k, ctx, mock := keepertest.InferenceKeeperReturningMocks(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	return k, keeper.NewMsgServerImpl(k), ctx, &mock
}

const testSubnetModelID = "llama3"

func setupEpochGroupForSubnet(ctx sdk.Context, k keeper.Keeper, epochIndex uint64, modelID string, addrs []string) {
	_ = k.EffectiveEpochIndex.Set(ctx, epochIndex)

	epoch := types.Epoch{
		Index:               epochIndex,
		PocStartBlockHeight: int64(epochIndex * 100),
	}
	_ = k.Epochs.Set(ctx, epochIndex, epoch)

	weights := make([]*types.ValidationWeight, len(addrs))
	for i, addr := range addrs {
		weights[i] = &types.ValidationWeight{MemberAddress: addr, Weight: 100}
	}

	groupData := types.EpochGroupData{
		PocStartBlockHeight: epochIndex * 100,
		ModelId:             modelID,
		EpochIndex:          epochIndex,
		ValidationWeights:   weights,
		TotalWeight:         int64(len(weights) * 100),
	}
	_ = k.EpochGroupDataMap.Set(ctx, collections.Join(epochIndex, modelID), groupData)
}

func makeSubnetAddrs(start byte, count int) []string {
	addrs := make([]string, count)
	for i := 0; i < count; i++ {
		addr := sdk.AccAddress(make([]byte, 20))
		addr[0] = start + byte(i)
		addrs[i] = addr.String()
	}
	return addrs
}

func TestCreateSubnetEscrow_HappyPath(t *testing.T) {
	k, ms, ctx, mocks := setupSubnetEscrowTest(t)

	rootAddrs := makeSubnetAddrs(1, 20)
	subgroupAddrs := rootAddrs[:3]
	setupEpochGroupForSubnet(ctx, k, 5, "", rootAddrs)
	setupEpochGroupForSubnet(ctx, k, 5, testSubnetModelID, subgroupAddrs)

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xFF
	amount := uint64(7_000_000_000) // 7 GNK

	mocks.BankKeeper.EXPECT().
		SendCoinsFromAccountToModule(gomock.Any(), creator, types.ModuleName, gomock.Any(), gomock.Any()).
		Return(nil)

	resp, err := ms.CreateSubnetEscrow(ctx, &types.MsgCreateSubnetEscrow{
		Creator: creator.String(),
		Amount:  amount,
		ModelId: testSubnetModelID,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, uint64(1), resp.EscrowId)

	escrow, found := k.GetSubnetEscrow(ctx, 1)
	require.True(t, found)
	require.Equal(t, creator.String(), escrow.Creator)
	require.Equal(t, amount, escrow.Amount)
	require.Len(t, escrow.Slots, keeper.SubnetGroupSize)
	require.Equal(t, uint64(5), escrow.EpochIndex)
	require.Equal(t, testSubnetModelID, escrow.ModelId)
	require.False(t, escrow.Settled)
	for _, slotAddr := range escrow.Slots {
		require.Contains(t, subgroupAddrs, slotAddr)
	}

	count := k.GetSubnetEscrowEpochCount(ctx, 5)
	require.Equal(t, uint64(1), count)
}

func TestCreateSubnetEscrow_AmountBelowMin(t *testing.T) {
	k, ms, ctx, _ := setupSubnetEscrowTest(t)

	setupEpochGroupForSubnet(ctx, k, 5, testSubnetModelID, makeSubnetAddrs(1, 5))

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xFF

	_, err := ms.CreateSubnetEscrow(ctx, &types.MsgCreateSubnetEscrow{
		Creator: creator.String(),
		Amount:  4_000_000_000,
		ModelId: testSubnetModelID,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "out of range")
}

func TestCreateSubnetEscrow_AmountAboveMax(t *testing.T) {
	k, ms, ctx, _ := setupSubnetEscrowTest(t)

	setupEpochGroupForSubnet(ctx, k, 5, testSubnetModelID, makeSubnetAddrs(1, 5))

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xFF

	_, err := ms.CreateSubnetEscrow(ctx, &types.MsgCreateSubnetEscrow{
		Creator: creator.String(),
		Amount:  11_000_000_000,
		ModelId: testSubnetModelID,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "out of range")
}

func TestCreateSubnetEscrow_EpochLimitReached(t *testing.T) {
	k, ms, ctx, _ := setupSubnetEscrowTest(t)

	setupEpochGroupForSubnet(ctx, k, 5, testSubnetModelID, makeSubnetAddrs(1, 5))
	_ = k.SubnetEscrowEpochCount.Set(ctx, 5, 100)

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xFF

	_, err := ms.CreateSubnetEscrow(ctx, &types.MsgCreateSubnetEscrow{
		Creator: creator.String(),
		Amount:  5_000_000_000,
		ModelId: testSubnetModelID,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "max")
}

func TestCreateSubnetEscrow_InsufficientFunds(t *testing.T) {
	k, ms, ctx, mocks := setupSubnetEscrowTest(t)

	setupEpochGroupForSubnet(ctx, k, 5, testSubnetModelID, makeSubnetAddrs(1, 5))

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xFF

	mocks.BankKeeper.EXPECT().
		SendCoinsFromAccountToModule(gomock.Any(), creator, types.ModuleName, gomock.Any(), gomock.Any()).
		Return(types.ErrNegativeCoinBalance)

	_, err := ms.CreateSubnetEscrow(ctx, &types.MsgCreateSubnetEscrow{
		Creator: creator.String(),
		Amount:  5_000_000_000,
		ModelId: testSubnetModelID,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to lock funds")
}

func TestCreateSubnetEscrow_CounterOverflow(t *testing.T) {
	k, ms, ctx, _ := setupSubnetEscrowTest(t)

	setupEpochGroupForSubnet(ctx, k, 5, testSubnetModelID, makeSubnetAddrs(1, 5))

	// Set counter to max uint64
	err := k.SubnetEscrowCounter.Set(ctx, math.MaxUint64)
	require.NoError(t, err)

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xFF

	_, err = ms.CreateSubnetEscrow(ctx, &types.MsgCreateSubnetEscrow{
		Creator: creator.String(),
		Amount:  5_000_000_000,
		ModelId: testSubnetModelID,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "overflow")
}

func TestCreateSubnetEscrow_AllowlistBlocks(t *testing.T) {
	k, ms, ctx, _ := setupSubnetEscrowTest(t)

	setupEpochGroupForSubnet(ctx, k, 5, testSubnetModelID, makeSubnetAddrs(1, 5))

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xFF

	// Set params with allowlist that does NOT include the creator.
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.SubnetEscrowParams = &types.SubnetEscrowParams{
		MinAmount:               types.DefaultSubnetEscrowMinAmount,
		MaxAmount:               types.DefaultSubnetEscrowMaxAmount,
		MaxEscrowsPerEpoch:      types.DefaultSubnetMaxEscrowsPerEpoch,
		GroupSize:               types.DefaultSubnetGroupSize,
		AllowedCreatorAddresses: []string{"gonka1someotheraddressxxxxxxxxxxxxxxxxxx"},
		TokenPrice:              types.DefaultSubnetTokenPrice,
	}
	require.NoError(t, k.SetParams(ctx, params))

	_, err = ms.CreateSubnetEscrow(ctx, &types.MsgCreateSubnetEscrow{
		Creator: creator.String(),
		Amount:  7_000_000_000,
		ModelId: testSubnetModelID,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "address is not allowed to create subnet escrows")
}

func TestCreateSubnetEscrow_ParamsOverrideDefaults(t *testing.T) {
	k, ms, ctx, mocks := setupSubnetEscrowTest(t)

	setupEpochGroupForSubnet(ctx, k, 5, testSubnetModelID, makeSubnetAddrs(1, 20))

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xFF

	// Set params with custom min=1000, max=2000, group_size=8.
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.SubnetEscrowParams = &types.SubnetEscrowParams{
		MinAmount:               1_000_000_000,
		MaxAmount:               2_000_000_000,
		MaxEscrowsPerEpoch:      types.DefaultSubnetMaxEscrowsPerEpoch,
		GroupSize:               8,
		AllowedCreatorAddresses: nil, // no restriction
		TokenPrice:              types.DefaultSubnetTokenPrice,
	}
	require.NoError(t, k.SetParams(ctx, params))

	mocks.BankKeeper.EXPECT().
		SendCoinsFromAccountToModule(gomock.Any(), creator, types.ModuleName, gomock.Any(), gomock.Any()).
		Return(nil)

	resp, err := ms.CreateSubnetEscrow(ctx, &types.MsgCreateSubnetEscrow{
		Creator: creator.String(),
		Amount:  1_500_000_000,
		ModelId: testSubnetModelID,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	escrow, found := k.GetSubnetEscrow(ctx, resp.EscrowId)
	require.True(t, found)
	require.Len(t, escrow.Slots, 8)
}

func TestCreateSubnetEscrow_ModelIDRequired(t *testing.T) {
	k, ms, ctx, _ := setupSubnetEscrowTest(t)

	setupEpochGroupForSubnet(ctx, k, 5, testSubnetModelID, makeSubnetAddrs(1, 5))

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xFF

	_, err := ms.CreateSubnetEscrow(ctx, &types.MsgCreateSubnetEscrow{
		Creator: creator.String(),
		Amount:  7_000_000_000,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "model_id is required")
}

func TestCreateSubnetEscrow_ModelGroupMustExist(t *testing.T) {
	k, ms, ctx, _ := setupSubnetEscrowTest(t)

	setupEpochGroupForSubnet(ctx, k, 5, "", makeSubnetAddrs(1, 20))

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xFF

	_, err := ms.CreateSubnetEscrow(ctx, &types.MsgCreateSubnetEscrow{
		Creator: creator.String(),
		Amount:  7_000_000_000,
		ModelId: testSubnetModelID,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to get epoch group for model")
}
