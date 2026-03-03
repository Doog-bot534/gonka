package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
)

// PrecomputeSPRTValues calculates the log-likelihood ratios for SPRT based on current parameters
// and stores them in the transient store for fast access during the block.
func (k Keeper) PrecomputeSPRTValues(ctx context.Context) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}

	vp := params.ValidationParams
	if vp == nil {
		vp = types.DefaultValidationParams()
	}

	precomputed := &types.SPRTPrecomputedValues{
		InvalidationLogFail: types.DecimalFromDecimal(CalculateLogLLR(vp.BadParticipantInvalidationRate.ToDecimal(), vp.FalsePositiveRate.ToDecimal(), true)),
		InvalidationLogPass: types.DecimalFromDecimal(CalculateLogLLR(vp.BadParticipantInvalidationRate.ToDecimal(), vp.FalsePositiveRate.ToDecimal(), false)),
		InactiveLogFail:     types.DecimalFromDecimal(CalculateLogLLR(vp.DowntimeBadPercentage.ToDecimal(), vp.DowntimeGoodPercentage.ToDecimal(), true)),
		InactiveLogPass:     types.DecimalFromDecimal(CalculateLogLLR(vp.DowntimeBadPercentage.ToDecimal(), vp.DowntimeGoodPercentage.ToDecimal(), false)),
	}

	bz, err := precomputed.Marshal()
	if err != nil {
		return err
	}

	transientStore := k.transientStoreService.OpenTransientStore(ctx)
	return transientStore.Set(types.TransientSPRTValuesKey, bz)
}

// GetPrecomputedSPRTValues retrieves the precomputed SPRT values from the transient store.
func (k Keeper) GetPrecomputedSPRTValues(ctx context.Context) (types.SPRTPrecomputedValues, bool) {
	transientStore := k.transientStoreService.OpenTransientStore(ctx)
	bz, err := transientStore.Get(types.TransientSPRTValuesKey)
	if err != nil || len(bz) == 0 {
		return types.SPRTPrecomputedValues{}, false
	}

	var precomputed types.SPRTPrecomputedValues
	if err := precomputed.Unmarshal(bz); err != nil {
		return types.SPRTPrecomputedValues{}, false
	}

	return precomputed, true
}

const precision = int32(12)

// CalculateLogLLR calculates the log-likelihood ratio for a given success/failure.
func CalculateLogLLR(p1, p0 decimal.Decimal, isFail bool) decimal.Decimal {
	one := decimal.NewFromInt(1)
	if isFail {
		// ln(p1/p0)
		if p1.IsZero() || p0.IsZero() {
			return decimal.Zero
		}
		res, _ := p1.Div(p0).Ln(precision)
		return res
	}
	// ln((1-p1)/(1-p0))
	if p1.Equal(one) || p0.Equal(one) {
		return decimal.Zero
	}
	res, _ := one.Sub(p1).Div(one.Sub(p0)).Ln(precision)
	return res
}
