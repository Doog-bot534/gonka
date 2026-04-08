package keeper_test

import (
	"context"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	dcrdsecp "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func TestSettleSubnetEscrow_FeesSplitBySlotCount(t *testing.T) {
	k, ms, ctx, mocks := setupSubnetEscrowTest(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keyH1, err := dcrdsecp.GeneratePrivateKey()
	require.NoError(t, err)
	keyH2, err := dcrdsecp.GeneratePrivateKey()
	require.NoError(t, err)
	keyH3, err := dcrdsecp.GeneratePrivateKey()
	require.NoError(t, err)

	addrH1 := cosmosAddressFromDcrdKey(keyH1).String()
	addrH2 := cosmosAddressFromDcrdKey(keyH2).String()
	addrH3 := cosmosAddressFromDcrdKey(keyH3).String()

	for _, addr := range []string{addrH1, addrH2, addrH3} {
		err = k.SetParticipant(ctx, types.Participant{
			Address: addr,
			Index:   addr,
			Status:  types.ParticipantStatus_ACTIVE,
		})
		require.NoError(t, err)
	}

	initialAmount := uint64(1_000)
	fees := uint64(403)
	expectedUserRefund := initialAmount - fees

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0x11
	escrow := types.SubnetEscrow{
		Id:         1,
		Creator:    creator.String(),
		Amount:     initialAmount,
		Slots:      []string{addrH1, addrH1, addrH2, addrH3},
		EpochIndex: 5,
		Settled:    false,
	}
	setupEpochGroupForSubnet(ctx, k, escrow.EpochIndex)
	_, err = k.StoreSubnetEscrow(ctx, &escrow, 1)
	require.NoError(t, err)

	hostStats := []*types.SubnetSettlementHostStats{
		{SlotId: 0, Cost: 0, RequiredValidations: 10, CompletedValidations: 9},
		{SlotId: 1, Cost: 0, RequiredValidations: 10, CompletedValidations: 9},
		{SlotId: 2, Cost: 0, RequiredValidations: 10, CompletedValidations: 9},
		{SlotId: 3, Cost: 0, RequiredValidations: 10, CompletedValidations: 9},
	}
	msg := buildSettlementTestData(t, escrow, []*dcrdsecp.PrivateKey{keyH1, keyH1, keyH2, keyH3}, hostStats, fees)

	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, creator, gomock.Any(), gomock.Eq("subnet_escrow_refund")).
		DoAndReturn(func(_ context.Context, _ string, _ sdk.AccAddress, coins sdk.Coins, _ string) error {
			require.Len(t, coins, 1)
			require.Equal(t, expectedUserRefund, coins[0].Amount.Uint64())
			return nil
		})

	resp, err := ms.SettleSubnetEscrow(ctx, msg)
	require.NoError(t, err)
	require.NotNil(t, resp)

	// H1 owns two out of four slots, so it receives 2/4 of total fees = 200
	// H2 and H3 each own one out of four slots, so they receive 1/4 of total fees = 100.
	//
	// Remainder fees are distributed 1 coin per slot, starting from the first slot.
	// H1 gets 2 remainder coins for its two slots, H2 gets 1 coin, and H3 gets 0 coins.
	participantH1, found := k.GetParticipant(ctx, addrH1)
	require.True(t, found)
	require.Equal(t, int64(202), participantH1.CoinBalance)
	require.NotNil(t, participantH1.CurrentEpochStats)
	require.Equal(t, uint64(202), participantH1.CurrentEpochStats.EarnedCoins)

	participantH2, found := k.GetParticipant(ctx, addrH2)
	require.True(t, found)
	require.Equal(t, int64(101), participantH2.CoinBalance)
	require.NotNil(t, participantH2.CurrentEpochStats)
	require.Equal(t, uint64(101), participantH2.CurrentEpochStats.EarnedCoins)

	participantH3, found := k.GetParticipant(ctx, addrH3)
	require.True(t, found)
	require.Equal(t, int64(100), participantH3.CoinBalance)
	require.NotNil(t, participantH3.CurrentEpochStats)
	require.Equal(t, uint64(100), participantH3.CurrentEpochStats.EarnedCoins)
}

func TestSettleSubnetEscrow_HappyPath(t *testing.T) {
	k, ms, ctx, mocks := setupSubnetEscrowTest(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys := make([]*dcrdsecp.PrivateKey, keeper.SubnetGroupSize)
	slots := make([]string, keeper.SubnetGroupSize)
	for i := 0; i < keeper.SubnetGroupSize; i++ {
		key, err := dcrdsecp.GeneratePrivateKey()
		require.NoError(t, err)
		keys[i] = key
		slots[i] = cosmosAddressFromDcrdKey(key).String()
		err = k.SetParticipant(ctx, types.Participant{
			Address: slots[i],
			Index:   slots[i],
			Status:  types.ParticipantStatus_ACTIVE,
		})
		require.NoError(t, err)
	}

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xAA
	escrow := types.SubnetEscrow{
		Id:         1,
		Creator:    creator.String(),
		Amount:     7_000_000_000,
		Slots:      slots,
		EpochIndex: 5,
		Settled:    false,
	}
	setupEpochGroupForSubnet(ctx, k, escrow.EpochIndex)
	_, err := k.StoreSubnetEscrow(ctx, &escrow, 1)
	require.NoError(t, err)

	costPerSlot := uint64(100_000_000) // 0.1 GNK per slot
	hostStats := makeHostStats(keeper.SubnetGroupSize, costPerSlot)
	fees := uint64(200_000_000)
	msg := buildSettlementTestData(t, escrow, keys, hostStats, fees)

	// Expect refund to creator
	// Refund is reduced by fees; exact amount is verified in mock callback.
	expectedRefund := escrow.Amount - uint64(keeper.SubnetGroupSize)*100_000_000 - fees
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, creator, gomock.Any(), gomock.Eq("subnet_escrow_refund")).
		DoAndReturn(func(_ context.Context, _ string, _ sdk.AccAddress, coins sdk.Coins, _ string) error {
			require.Len(t, coins, 1)
			require.Equal(t, expectedRefund, coins[0].Amount.Uint64())
			return nil
		})
	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()

	resp, err := ms.SettleSubnetEscrow(ctx, msg)
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Verify escrow is settled
	settled, found := k.GetSubnetEscrow(ctx, 1)
	require.True(t, found)
	require.True(t, settled.Settled)

	expectedPayout := int64(costPerSlot + (fees / uint64(keeper.SubnetGroupSize)))
	for _, addr := range slots {
		participant, found := k.GetParticipant(ctx, addr)
		require.True(t, found)
		require.Equal(t, expectedPayout, participant.CoinBalance)
		require.NotNil(t, participant.CurrentEpochStats)
		require.Equal(t, uint64(expectedPayout), participant.CurrentEpochStats.EarnedCoins)
	}
}

func TestSettleSubnetEscrow_AlreadySettled(t *testing.T) {
	k, ms, ctx, _ := setupSubnetEscrowTest(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xBB
	escrow := types.SubnetEscrow{
		Id:      1,
		Creator: creator.String(),
		Settled: true,
		Slots:   make([]string, keeper.SubnetGroupSize),
	}
	_, err := k.StoreSubnetEscrow(ctx, &escrow, 1)
	require.NoError(t, err)

	_, err = ms.SettleSubnetEscrow(ctx, &types.MsgSettleSubnetEscrow{
		Settler:  creator.String(),
		EscrowId: 1,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "already settled")
}

func TestSettleSubnetEscrow_WrongSettler(t *testing.T) {
	k, ms, ctx, _ := setupSubnetEscrowTest(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xCC
	wrongSettler := sdk.AccAddress(make([]byte, 20))
	wrongSettler[0] = 0xDD
	escrow := types.SubnetEscrow{
		Id:      1,
		Creator: creator.String(),
		Slots:   make([]string, keeper.SubnetGroupSize),
	}
	_, err := k.StoreSubnetEscrow(ctx, &escrow, 1)
	require.NoError(t, err)

	_, err = ms.SettleSubnetEscrow(ctx, &types.MsgSettleSubnetEscrow{
		Settler:  wrongSettler.String(),
		EscrowId: 1,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not the escrow creator")
}

func TestSettleSubnetEscrow_ZeroCostSettlement(t *testing.T) {
	k, ms, ctx, mocks := setupSubnetEscrowTest(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys := make([]*dcrdsecp.PrivateKey, keeper.SubnetGroupSize)
	slots := make([]string, keeper.SubnetGroupSize)
	for i := 0; i < keeper.SubnetGroupSize; i++ {
		key, err := dcrdsecp.GeneratePrivateKey()
		require.NoError(t, err)
		keys[i] = key
		slots[i] = cosmosAddressFromDcrdKey(key).String()
	}
	for _, addr := range slots {
		err := k.SetParticipant(ctx, types.Participant{
			Address: addr,
			Index:   addr,
			Status:  types.ParticipantStatus_ACTIVE,
		})
		require.NoError(t, err)
	}

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xBB
	escrow := types.SubnetEscrow{
		Id:         1,
		Creator:    creator.String(),
		Amount:     7_000_000_000,
		Slots:      slots,
		EpochIndex: 5,
		Settled:    false,
	}
	setupEpochGroupForSubnet(ctx, k, escrow.EpochIndex)
	_, err := k.StoreSubnetEscrow(ctx, &escrow, 1)
	require.NoError(t, err)

	hostStats := makeHostStats(keeper.SubnetGroupSize, 0) // all costs = 0
	msg := buildSettlementTestData(t, escrow, keys, hostStats, 0)

	// No validator payments expected (all costs are 0)
	// Full amount refunded to creator
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, creator, gomock.Any(), gomock.Eq("subnet_escrow_refund")).
		Return(nil)

	resp, err := ms.SettleSubnetEscrow(ctx, msg)
	require.NoError(t, err)
	require.NotNil(t, resp)

	settled, found := k.GetSubnetEscrow(ctx, 1)
	require.True(t, found)
	require.True(t, settled.Settled)
}

// This test checks that if a subnet was created on an epoch X and settled on a different epoch Y, and
// if the host was punished on epoch X, then they should not be paid in the settlement of this subnet.
// The payment should be refunded to the user instead.
func TestSettleSubnetEscrow_CrossEpochSkipsTransferWithoutRewardedCoins(t *testing.T) {
	k, ms, ctx, mocks := setupSubnetEscrowTest(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	key, err := dcrdsecp.GeneratePrivateKey()
	require.NoError(t, err)
	addr := cosmosAddressFromDcrdKey(key).String()

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0x77
	escrow := types.SubnetEscrow{
		Id:         1,
		Creator:    creator.String(),
		Amount:     1_000,
		Slots:      []string{addr},
		EpochIndex: 5,
		Settled:    false,
	}

	// Set current epoch to 6 so settlement is cross-epoch with escrow epoch 5.
	setupEpochGroupForSubnet(ctx, k, 6)

	_, err = k.StoreSubnetEscrow(ctx, &escrow, 1)
	require.NoError(t, err)

	// Summary exists but has no rewarded coins, so transfer should be skipped.
	err = k.SetEpochPerformanceSummary(ctx, types.EpochPerformanceSummary{
		EpochIndex:    escrow.EpochIndex,
		ParticipantId: addr,
		RewardedCoins: 0,
	})
	require.NoError(t, err)

	hostStats := []*types.SubnetSettlementHostStats{{
		SlotId: 0,
		Cost:   100,
	}}
	msg := buildSettlementTestData(t, escrow, []*dcrdsecp.PrivateKey{key}, hostStats, 0)

	// No payout transfer expected; full escrow should be refunded to creator.
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, creator, gomock.Any(), gomock.Eq("subnet_escrow_refund")).
		DoAndReturn(func(_ context.Context, _ string, _ sdk.AccAddress, coins sdk.Coins, _ string) error {
			require.Len(t, coins, 1)
			require.Equal(t, escrow.Amount, coins[0].Amount.Uint64())
			return nil
		})

	resp, err := ms.SettleSubnetEscrow(ctx, msg)
	require.NoError(t, err)
	require.NotNil(t, resp)
}

// This test checks that if a subnet was created on an epoch X and settled on a different epoch Y, and
// if the host was NOT punished on epoch X,
// then the payout is immediately transferred directly to the host.
func TestSettleSubnetEscrow_CrossEpochTransfersWithRewardedCoins(t *testing.T) {
	k, ms, ctx, mocks := setupSubnetEscrowTest(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	key, err := dcrdsecp.GeneratePrivateKey()
	require.NoError(t, err)
	addr := cosmosAddressFromDcrdKey(key).String()
	recipient, err := sdk.AccAddressFromBech32(addr)
	require.NoError(t, err)

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0x88
	escrow := types.SubnetEscrow{
		Id:         1,
		Creator:    creator.String(),
		Amount:     1_000,
		Slots:      []string{addr},
		EpochIndex: 5,
		Settled:    false,
	}

	// Set current epoch to 6 so settlement is cross-epoch with escrow epoch 5.
	setupEpochGroupForSubnet(ctx, k, 6)

	_, err = k.StoreSubnetEscrow(ctx, &escrow, 1)
	require.NoError(t, err)

	// Positive rewarded coins allows payout transfer.
	err = k.SetEpochPerformanceSummary(ctx, types.EpochPerformanceSummary{
		EpochIndex:    escrow.EpochIndex,
		ParticipantId: addr,
		RewardedCoins: 1,
	})
	require.NoError(t, err)

	hostStats := []*types.SubnetSettlementHostStats{{
		SlotId: 0,
		Cost:   100,
	}}
	msg := buildSettlementTestData(t, escrow, []*dcrdsecp.PrivateKey{key}, hostStats, 0)

	// Since this subnet is being settled in a different epoch than it was created,
	// we expect a direct bank transfer to the validator's account.
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, recipient, gomock.Any(), gomock.Eq("subnet_escrow_payment")).
		DoAndReturn(func(_ context.Context, _ string, _ sdk.AccAddress, coins sdk.Coins, _ string) error {
			require.Len(t, coins, 1)
			require.Equal(t, uint64(100), coins[0].Amount.Uint64())
			return nil
		})
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, creator, gomock.Any(), gomock.Eq("subnet_escrow_refund")).
		DoAndReturn(func(_ context.Context, _ string, _ sdk.AccAddress, coins sdk.Coins, _ string) error {
			require.Len(t, coins, 1)
			require.Equal(t, escrow.Amount-100, coins[0].Amount.Uint64())
			return nil
		})

	resp, err := ms.SettleSubnetEscrow(ctx, msg)
	require.NoError(t, err)
	require.NotNil(t, resp)
}

// TestSettleSubnetEscrow_UpdatesCurrentEpochStats verifies settlement aggregates host stats into CurrentEpochStats.
func TestSettleSubnetEscrow_UpdatesCurrentEpochStats(t *testing.T) {
	// Setup keeper, message server, and bech32 config.
	k, ms, ctx, mocks := setupSubnetEscrowTest(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	// Create one validator and persist participant.
	keyA, err := dcrdsecp.GeneratePrivateKey()
	require.NoError(t, err)
	addrA := cosmosAddressFromDcrdKey(keyA).String()
	err = k.SetParticipant(ctx, types.Participant{
		Address: addrA,
		Index:   addrA,
		Status:  types.ParticipantStatus_ACTIVE,
	})
	require.NoError(t, err)

	// Single-slot subnet with uniform host stats.
	slots := []string{addrA}
	keys := []*dcrdsecp.PrivateKey{keyA}
	fixtureHostStats := types.SubnetSettlementHostStats{
		SlotId:         0,
		Missed:         1,
		Invalid:        2,
		InferenceCount: 3,
		Validated:      4,
	}
	hostStats := []*types.SubnetSettlementHostStats{&fixtureHostStats}

	// Create escrow and settlement message with zero fees and costs.
	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0x22
	escrow := types.SubnetEscrow{
		Id:         1,
		Creator:    creator.String(),
		Amount:     1_000_000,
		Slots:      slots,
		EpochIndex: 5,
		Settled:    false,
	}
	setupEpochGroupForSubnet(ctx, k, escrow.EpochIndex)
	_, err = k.StoreSubnetEscrow(ctx, &escrow, 1)
	require.NoError(t, err)
	msg := buildSettlementTestData(t, escrow, keys, hostStats, 0)

	// Expect full refund to the creator.
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, creator, gomock.Any(), gomock.Eq("subnet_escrow_refund")).
		Return(nil)

	// Settle and verify response.
	resp, err := ms.SettleSubnetEscrow(ctx, msg)
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Assert CurrentEpochStats aggregation for the single participant.
	participantA, found := k.GetParticipant(ctx, addrA)
	require.True(t, found)
	require.NotNil(t, participantA.CurrentEpochStats)
	require.Equal(t, uint64(fixtureHostStats.Missed), participantA.CurrentEpochStats.MissedRequests)
	require.Equal(t, uint64(fixtureHostStats.Invalid), participantA.CurrentEpochStats.InvalidatedInferences)
	require.Equal(t, uint64(fixtureHostStats.InferenceCount), participantA.CurrentEpochStats.InferenceCount)
	require.Equal(t, uint64(fixtureHostStats.Validated), participantA.CurrentEpochStats.ValidatedInferences)
}

// TestSettleSubnetEscrow_UpdatesSubnetHostEpochStats verifies SubnetHostEpochStatsMap aggregation on settlement.
func TestSettleSubnetEscrow_UpdatesSubnetHostEpochStats(t *testing.T) {
	// Setup keeper, message server, and bech32 config.
	k, ms, ctx, mocks := setupSubnetEscrowTest(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	// Create one validator and persist participant.
	keyA, err := dcrdsecp.GeneratePrivateKey()
	require.NoError(t, err)
	addrA := cosmosAddressFromDcrdKey(keyA).String()
	err = k.SetParticipant(ctx, types.Participant{
		Address: addrA,
		Index:   addrA,
		Status:  types.ParticipantStatus_ACTIVE,
	})
	require.NoError(t, err)

	// Single-slot subnet with varied host stats and expected totals.
	slots := []string{addrA}
	keys := []*dcrdsecp.PrivateKey{keyA}
	fixtureHostStats := types.SubnetSettlementHostStats{
		SlotId:               0,
		Missed:               1,
		Invalid:              2,
		Cost:                 7,
		RequiredValidations:  5,
		CompletedValidations: 4,
		InferenceCount:       10,
		Validated:            11,
	}
	hostStats := []*types.SubnetSettlementHostStats{&fixtureHostStats}

	// Create escrow and settlement message with zero fees.
	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0x33
	escrow := types.SubnetEscrow{
		Id:         1,
		Creator:    creator.String(),
		Amount:     10_000_000,
		Slots:      slots,
		EpochIndex: 7,
		Settled:    false,
	}
	setupEpochGroupForSubnet(ctx, k, escrow.EpochIndex)
	_, err = k.StoreSubnetEscrow(ctx, &escrow, 1)
	require.NoError(t, err)
	msg := buildSettlementTestData(t, escrow, keys, hostStats, 0)

	// Expect full refund to the creator.
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, creator, gomock.Any(), gomock.Eq("subnet_escrow_refund")).
		Return(nil)
	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()

	// Settle and verify response.
	resp, err := ms.SettleSubnetEscrow(ctx, msg)
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Assert SubnetHostEpochStatsMap aggregation for participant A.
	participantA, err := sdk.AccAddressFromBech32(addrA)
	require.NoError(t, err)
	statsA, found := k.GetSubnetHostEpochStats(ctx, escrow.EpochIndex, participantA)
	require.True(t, found)
	require.Equal(t, addrA, statsA.Participant)
	require.Equal(t, escrow.EpochIndex, statsA.EpochIndex)
	require.Equal(t, fixtureHostStats.Missed, statsA.Missed)
	require.Equal(t, fixtureHostStats.Invalid, statsA.Invalid)
	require.Equal(t, fixtureHostStats.Cost, statsA.Cost)
	require.Equal(t, fixtureHostStats.RequiredValidations, statsA.RequiredValidations)
	require.Equal(t, fixtureHostStats.CompletedValidations, statsA.CompletedValidations)
	require.Equal(t, fixtureHostStats.InferenceCount, statsA.InferenceCount)
	require.Equal(t, fixtureHostStats.Validated, statsA.Validated)
	require.Equal(t, uint32(1), statsA.EscrowCount)
}

func TestSettleSubnetEscrow_AllowlistBlocks(t *testing.T) {
	k, ms, ctx, _ := setupSubnetEscrowTest(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xCC
	escrow := types.SubnetEscrow{
		Id:      1,
		Creator: creator.String(),
		Amount:  7_000_000_000,
		Slots:   make([]string, keeper.SubnetGroupSize),
		Settled: false,
	}
	_, err := k.StoreSubnetEscrow(ctx, &escrow, 1)
	require.NoError(t, err)

	// Set params with allowlist NOT containing the escrow creator.
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

	_, err = ms.SettleSubnetEscrow(ctx, &types.MsgSettleSubnetEscrow{
		Settler:  creator.String(),
		EscrowId: 1,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "address is not allowed to create subnet escrows")
}
