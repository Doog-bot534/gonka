package keeper

import (
	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// invalidateInferenceForMismatch marks an inference INVALIDATED and increments invalidation counters
// on the provided culprit participant (if found). It also refunds any escrow held by the module.
//
// This is used for Start/Finish consistency mismatches (e.g. prompt_hash mismatch) where we want to
// prevent validation and stop further processing.
func (k msgServer) invalidateInferenceForMismatch(ctx sdk.Context, inference *types.Inference, culpritAddr string, reason string) error {
	if inference == nil {
		return sdkerrors.Wrap(types.ErrIllegalState, "nil inference")
	}

	// If already invalidated, treat as idempotent.
	inference.Status = types.InferenceStatus_INVALIDATED

	// Refund escrow if it exists.
	if inference.EscrowAmount > 0 && inference.RequestedBy != "" {
		refundAmount := inference.EscrowAmount
		// Prevent double refund if this is hit twice.
		inference.EscrowAmount = 0
		if err := k.IssueRefund(ctx, refundAmount, inference.RequestedBy, "inference_mismatch:"+inference.InferenceId); err != nil {
			k.LogError("Mismatch refund failed", types.Payments, "error", err, "inferenceId", inference.InferenceId, "requestedBy", inference.RequestedBy)
			// We still proceed with invalidation to block validation.
		}
	}

	// Increment counters on the culprit (best-effort).
	if culpritAddr != "" {
		culprit, found := k.GetParticipant(ctx, culpritAddr)
		if found {
			if culprit.CurrentEpochStats == nil {
				culprit.CurrentEpochStats = &types.CurrentEpochStats{}
			}
			culprit.CurrentEpochStats.InvalidatedInferences++
			culprit.ConsecutiveInvalidInferences++
			if err := k.SetParticipant(ctx, culprit); err != nil {
				return err
			}
		}
	}

	k.LogInfo("Inference invalidated due to mismatch", types.Validation,
		"inferenceId", inference.InferenceId,
		"culprit", culpritAddr,
		"reason", reason,
	)

	return k.SetInference(ctx, *inference)
}

