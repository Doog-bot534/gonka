package keeper

import (
	"context"
	"fmt"
	"math"
	"math/bits"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) GetSubnetHostEpochStats(ctx context.Context, epochIndex uint64, participant sdk.AccAddress) (types.SubnetHostEpochStats, bool) {
	v, err := k.SubnetHostEpochStatsMap.Get(ctx, collections.Join(epochIndex, participant))
	if err != nil {
		return types.SubnetHostEpochStats{}, false
	}
	return v, true
}

func (k Keeper) AggregateSubnetHostStats(ctx context.Context, epochIndex uint64, participant sdk.AccAddress, slotStats types.SubnetSettlementHostStats) error {
	key := collections.Join(epochIndex, participant)
	existing, err := k.SubnetHostEpochStatsMap.Get(ctx, key)
	if err != nil {
		existing = types.SubnetHostEpochStats{
			Participant: participant.String(),
			EpochIndex:  epochIndex,
		}
	}
	existing.Missed += slotStats.Missed
	existing.Invalid += slotStats.Invalid
	if existing.Cost > math.MaxUint64-slotStats.Cost {
		return fmt.Errorf("cost overflow aggregating subnet host stats")
	}
	existing.Cost += slotStats.Cost
	existing.RequiredValidations += slotStats.RequiredValidations
	existing.CompletedValidations += slotStats.CompletedValidations
	existing.InferenceCount += slotStats.InferenceCount
	existing.Validated += slotStats.Validated
	return k.SubnetHostEpochStatsMap.Set(ctx, key, existing)
}

// Merges subnet settlement stats into a participant's *current* epoch stats.
func AggregateSubnetHostStatsIntoCurrentEpochStats(participant *types.Participant, slotStats types.SubnetSettlementHostStats) error {
	// Validate input participant.
	if participant == nil {
		return fmt.Errorf("participant is nil")
	}

	// Ensure the epoch stats struct exists so we can update counters safely.
	ensureParticipantEpochStats(participant)

	// Aggregate missed requests.
	nextMissedRequests, carry := bits.Add64(participant.CurrentEpochStats.MissedRequests, uint64(slotStats.Missed), 0)
	if carry != 0 {
		return fmt.Errorf("participant missed requests overflow for %s", participant.Address)
	}
	participant.CurrentEpochStats.MissedRequests = nextMissedRequests

	// Aggregate invalidated inferences.
	nextInvalidated, carry := bits.Add64(participant.CurrentEpochStats.InvalidatedInferences, uint64(slotStats.Invalid), 0)
	if carry != 0 {
		return fmt.Errorf("participant invalidated inferences overflow for %s", participant.Address)
	}
	participant.CurrentEpochStats.InvalidatedInferences = nextInvalidated

	// Aggregate inference count.
	nextInferenceCount, carry := bits.Add64(participant.CurrentEpochStats.InferenceCount, uint64(slotStats.InferenceCount), 0)
	if carry != 0 {
		return fmt.Errorf("participant inference count overflow for %s", participant.Address)
	}
	participant.CurrentEpochStats.InferenceCount = nextInferenceCount

	// Aggregate validated inferences.
	nextValidated, carry := bits.Add64(participant.CurrentEpochStats.ValidatedInferences, uint64(slotStats.Validated), 0)
	if carry != 0 {
		return fmt.Errorf("participant validated inferences overflow for %s", participant.Address)
	}
	participant.CurrentEpochStats.ValidatedInferences = nextValidated

	return nil
}

// Merges subnet settlement stats into a participant's *previous* epoch stats.
func (k *Keeper) AggregateSubnetHostStatsIntoPreviousEpochStats(
	goCtx context.Context,
	escrow types.SubnetEscrow,
	hs types.SubnetSettlementHostStats,
	addr string,
) error {
	summary, summaryFound := k.GetEpochPerformanceSummary(goCtx, escrow.EpochIndex, addr)
	if !summaryFound {
		return nil
	}

	nextMissedRequests, carry := bits.Add64(summary.MissedRequests, uint64(hs.Missed), 0)
	if carry != 0 {
		return fmt.Errorf("epoch performance summary missed requests overflow for %s", addr)
	}
	summary.MissedRequests = nextMissedRequests

	nextInvalidated, carry := bits.Add64(summary.InvalidatedInferences, uint64(hs.Invalid), 0)
	if carry != 0 {
		return fmt.Errorf("epoch performance summary invalidated inferences overflow for %s", addr)
	}
	summary.InvalidatedInferences = nextInvalidated

	nextInferenceCount, carry := bits.Add64(summary.InferenceCount, uint64(hs.InferenceCount), 0)
	if carry != 0 {
		return fmt.Errorf("epoch performance summary inference count overflow for %s", addr)
	}
	summary.InferenceCount = nextInferenceCount

	nextValidated, carry := bits.Add64(summary.ValidatedInferences, uint64(hs.Validated), 0)
	if carry != 0 {
		return fmt.Errorf("epoch performance summary validated inferences overflow for %s", addr)
	}
	summary.ValidatedInferences = nextValidated

	if err := k.SetEpochPerformanceSummary(goCtx, summary); err != nil {
		return fmt.Errorf("failed to update epoch performance summary for %s: %w", addr, err)
	}

	return nil
}

func (k Keeper) IncrementSubnetHostEscrowCount(ctx context.Context, epochIndex uint64, participant sdk.AccAddress) error {
	key := collections.Join(epochIndex, participant)
	existing, err := k.SubnetHostEpochStatsMap.Get(ctx, key)
	if err != nil {
		existing = types.SubnetHostEpochStats{
			Participant: participant.String(),
			EpochIndex:  epochIndex,
		}
	}
	existing.EscrowCount++
	return k.SubnetHostEpochStatsMap.Set(ctx, key, existing)
}
