package keeper

import (
	"context"
	"fmt"
	"math"
	"math/bits"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SettleSubnetEscrow(goCtx context.Context, msg *types.MsgSettleSubnetEscrow) (*types.MsgSettleSubnetEscrowResponse, error) {
	if err := k.CheckPermission(goCtx, msg, EscrowAllowListPermission); err != nil {
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	escrow, found := k.GetSubnetEscrow(goCtx, msg.EscrowId)
	if !found {
		return nil, fmt.Errorf("escrow %d not found", msg.EscrowId)
	}

	warmKeyChecker := func(granter, grantee string) bool {
		return k.HasWarmKeyGrant(goCtx, granter, grantee)
	}
	if err := VerifySubnetSettlement(escrow, msg, warmKeyChecker); err != nil {
		return nil, err
	}

	if len(escrow.Slots) == 0 {
		return nil, fmt.Errorf("escrow %d has no slots", escrow.Id)
	}

	// Check if the subnet is being settled in the same epoch it was created.
	currentEpochIndex, epochFound := k.GetEffectiveEpochIndex(goCtx)
	if !epochFound {
		return nil, fmt.Errorf("failed to get effective epoch index")
	}
	isSameEpochSettlement := escrow.EpochIndex == currentEpochIndex

	totalSlots := uint64(len(escrow.Slots))
	// How much of the total fees will be assigned to each slot
	feePerSlot := msg.Fees / totalSlots
	// Leftover fees; will be distributed 1 per slot
	remainderFees := msg.Fees % totalSlots

	// Aggregate costs + fees per unique validator address (deterministic: iterate by slot order)
	validatorPayouts := make(map[string]uint64)
	for _, hs := range msg.HostStats {
		if int(hs.SlotId) >= len(escrow.Slots) {
			return nil, fmt.Errorf("host_stats slot_id %d out of range", hs.SlotId)
		}
		addr := escrow.Slots[hs.SlotId]

		// IFF the subnet is being settled in a different epoch than it was created,
		// and the validator was punished in the previous epoch, then they shouldn't be paid.
		//
		// We're inferring that `RewardedCoins` being 0 means the validator was punished.
		if !isSameEpochSettlement {
			summary, summaryFound := k.GetEpochPerformanceSummary(goCtx, escrow.EpochIndex, addr)
			if !summaryFound || summary.RewardedCoins == 0 {
				k.LogInfo("Skipping subnet escrow payout: host was punished in previous epoch", types.Settle,
					"participant", addr,
					"escrow_id", escrow.Id,
					"escrow_epoch", escrow.EpochIndex,
				)
				continue
			}
		}

		// Assign cost of running inferences to this slot's validator
		nextValidatorPayout, carry := bits.Add64(validatorPayouts[addr], hs.Cost, 0)
		if carry != 0 {
			return nil, fmt.Errorf("validator cost overflow for %s", addr)
		}

		// Assign fees paid by the user to this slot's validator
		nextValidatorPayout, carry = bits.Add64(nextValidatorPayout, feePerSlot, 0)
		if carry != 0 {
			return nil, fmt.Errorf("validator fee share overflow for %s", addr)
		}

		// If there are remainder fees, distribute 1 additional coin to this slot.
		if remainderFees > 0 {
			nextValidatorPayout, carry = bits.Add64(nextValidatorPayout, 1, 0)
			if carry != 0 {
				return nil, fmt.Errorf("validator remainder fee overflow for %s", addr)
			}
			remainderFees--
		}
		validatorPayouts[addr] = nextValidatorPayout
	}

	// Sanity check
	if remainderFees != 0 {
		return nil, fmt.Errorf("failed to allocate all remainder fees, %d left", remainderFees)
	}

	// Pay validators in slot order (deterministic iteration over escrow.Slots).
	// Each validator receives total accumulated slot costs and fee shares.
	var totalPayout uint64
	paidValidators := make(map[string]bool)
	for _, addr := range escrow.Slots {
		payout, hasPayout := validatorPayouts[addr]
		if !hasPayout || payout == 0 {
			continue
		}
		if paidValidators[addr] {
			continue
		}
		paidValidators[addr] = true

		// Track total payout for remainder/refund calculation.
		nextTotalPayout, carry := bits.Add64(totalPayout, payout, 0)
		if carry != 0 {
			return nil, fmt.Errorf("total validator payout overflow")
		}
		totalPayout = nextTotalPayout

		if isSameEpochSettlement {
			// If the subnet is being settled in the same epoch it was created,
			// we treat the payout the same as onchain inferences: we use [processParticipantPayment]
			// to increment [participant.CurrentEpochStats.EarnedCoins], claimable at the end of the epoch.
			participant, found := k.GetParticipant(ctx, addr)
			if !found {
				return nil, fmt.Errorf("participant %s not found", addr)
			}
			if payout > math.MaxInt64 {
				return nil, fmt.Errorf("validator payout out of range for %s", addr)
			}
			if err := k.processParticipantPayment(ctx, &participant, int64(payout), "subnet_escrow_payment"); err != nil {
				return nil, err
			}
			if err := k.SetParticipant(ctx, participant); err != nil {
				return nil, fmt.Errorf("failed to update participant %s: %w", addr, err)
			}
		} else {
			// If the subnet is being settled in a different epoch than it was created,
			// we perform a direct bank transfer to the validator's account.
			recipientAddr, err := sdk.AccAddressFromBech32(addr)
			if err != nil {
				return nil, fmt.Errorf("invalid validator address %s: %w", addr, err)
			}
			if payout > math.MaxInt64 {
				return nil, fmt.Errorf("validator payout out of range for %s", addr)
			}
			coins, err := types.GetCoins(int64(payout))
			if err != nil {
				return nil, fmt.Errorf("invalid payout amount: %w", err)
			}
			err = k.BankKeeper.SendCoinsFromModuleToAccount(goCtx, types.ModuleName, recipientAddr, coins, "subnet_escrow_payment")
			if err != nil {
				return nil, fmt.Errorf("failed to pay validator %s: %w", addr, err)
			}
		}
	}

	// Refund remainder to creator after validator costs and fee shares.
	remainder := escrow.Amount - totalPayout
	if remainder > 0 {
		creatorAddr, err := sdk.AccAddressFromBech32(escrow.Creator)
		if err != nil {
			return nil, fmt.Errorf("invalid creator address: %w", err)
		}
		coins, err := types.GetCoins(int64(remainder))
		if err != nil {
			return nil, fmt.Errorf("invalid refund amount: %w", err)
		}
		err = k.BankKeeper.SendCoinsFromModuleToAccount(goCtx, types.ModuleName, creatorAddr, coins, "subnet_escrow_refund")
		if err != nil {
			return nil, fmt.Errorf("failed to refund creator: %w", err)
		}
	}

	// Aggregate host stats per validator per epoch (deterministic: iterate msg.HostStats by slot_id order)
	seenValidators := make(map[string]bool)
	for _, hs := range msg.HostStats {
		addr := escrow.Slots[hs.SlotId]
		participantAddr, err := sdk.AccAddressFromBech32(addr)
		if err != nil {
			return nil, fmt.Errorf("invalid participant address %s: %w", addr, err)
		}

		// Update `SubnetHostEpochStatsMap`
		if err := k.AggregateSubnetHostStats(goCtx, escrow.EpochIndex, participantAddr, *hs); err != nil {
			return nil, fmt.Errorf("failed to aggregate host stats: %w", err)
		}
		if !seenValidators[addr] {
			seenValidators[addr] = true
			if err := k.IncrementSubnetHostEscrowCount(goCtx, escrow.EpochIndex, participantAddr); err != nil {
				return nil, fmt.Errorf("failed to increment escrow count: %w", err)
			}
		}

		if isSameEpochSettlement {
			// If the subnet is being settled in the same epoch it was created,
			// we treat it the same as onchain inferences: we update [participant.CurrentEpochStats], which will:
			// * be reflected in the epoch performance summary at the end of the epoch, which is used for reputation calculations.
			// * be taken into account when calculating punishments WorkCoins/RewardCoins (see `bitcoin_rewards.go`)
			// * be taken into account when calculating participant's inactivity status (see `status.go` -> `ComputeStatus`)
			participant, found := k.GetParticipant(ctx, addr)
			if !found {
				return nil, fmt.Errorf("participant %s not found", addr)
			}
			if err := AggregateSubnetHostStatsIntoCurrentEpochStats(&participant, *hs); err != nil {
				return nil, fmt.Errorf("failed to aggregate host stats into participant epoch stats: %w", err)
			}
			if err := k.SetParticipant(ctx, participant); err != nil {
				return nil, fmt.Errorf("failed to update participant %s: %w", addr, err)
			}
		} else {
			// If the subnet is being settled in a different epoch,
			// we update the epoch performance summary for that epoch, which is used for reputation calculations.
			if err := k.AggregateSubnetHostStatsIntoPreviousEpochStats(goCtx, escrow, *hs, addr); err != nil {
				return nil, fmt.Errorf("failed to aggregate host stats into participant previous epoch stats: %w", err)
			}
		}

	}

	escrow.Settled = true
	if err := k.SetSubnetEscrow(goCtx, escrow); err != nil {
		return nil, fmt.Errorf("failed to update escrow: %w", err)
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		"subnet_escrow_settled",
		sdk.NewAttribute("escrow_id", fmt.Sprint(escrow.Id)),
		sdk.NewAttribute("settler", msg.Settler),
		sdk.NewAttribute("total_payout", fmt.Sprint(totalPayout)),
		sdk.NewAttribute("fees", fmt.Sprint(msg.Fees)),
		sdk.NewAttribute("remainder", fmt.Sprint(remainder)),
	))

	return &types.MsgSettleSubnetEscrowResponse{}, nil
}
