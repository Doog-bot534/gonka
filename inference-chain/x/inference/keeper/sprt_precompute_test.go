package keeper_test

import (
	"testing"

	testkeeper "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestCalculateLogLLR(t *testing.T) {
	val1, _ := decimal.NewFromFloat(2).Ln(12)
	val2, _ := decimal.NewFromFloat(0.8).Div(decimal.NewFromFloat(0.9)).Ln(12)

	// Test cases for calculateLogLLR
	tests := []struct {
		name   string
		p1     decimal.Decimal
		p0     decimal.Decimal
		isFail bool
		want   decimal.Decimal
	}{
		{
			name:   "fail: p1=0.2, p0=0.1",
			p1:     decimal.NewFromFloat(0.2),
			p0:     decimal.NewFromFloat(0.1),
			isFail: true,
			want:   val1, // ln(0.2/0.1) = ln(2)
		},
		{
			name:   "pass: p1=0.2, p0=0.1",
			p1:     decimal.NewFromFloat(0.2),
			p0:     decimal.NewFromFloat(0.1),
			isFail: false,
			want:   val2, // ln(0.8/0.9)
		},
		{
			name:   "fail: p1=0, returns zero",
			p1:     decimal.Zero,
			p0:     decimal.NewFromFloat(0.1),
			isFail: true,
			want:   decimal.Zero,
		},
		{
			name:   "fail: p0=0, returns zero",
			p1:     decimal.NewFromFloat(0.2),
			p0:     decimal.Zero,
			isFail: true,
			want:   decimal.Zero,
		},
		{
			name:   "pass: p1=1, returns zero",
			p1:     decimal.NewFromInt(1),
			p0:     decimal.NewFromFloat(0.1),
			isFail: false,
			want:   decimal.Zero,
		},
		{
			name:   "pass: p0=1, returns zero",
			p1:     decimal.NewFromFloat(0.2),
			p0:     decimal.NewFromInt(1),
			isFail: false,
			want:   decimal.Zero,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := keeper.CalculateLogLLR(tt.p1, tt.p0, tt.isFail)
			if !got.Equal(tt.want) {
				t.Errorf("CalculateLogLLR() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPrecomputeSPRTValues(t *testing.T) {
	k, ctx, _ := testkeeper.InferenceKeeperReturningMocks(t)

	// Set custom params
	params := types.DefaultParams()
	params.ValidationParams = &types.ValidationParams{
		BadParticipantInvalidationRate: types.DecimalFromFloat(0.3),
		FalsePositiveRate:              types.DecimalFromFloat(0.05),
		DowntimeBadPercentage:          types.DecimalFromFloat(0.4),
		DowntimeGoodPercentage:         types.DecimalFromFloat(0.1),
	}
	k.SetParams(ctx, params)

	err := k.PrecomputeSPRTValues(ctx)
	require.NoError(t, err)

	precomputed, found := k.GetPrecomputedSPRTValues(ctx)
	require.True(t, found)

	// Expected values
	expectedInvalidationLogFail := keeper.CalculateLogLLR(decimal.NewFromFloat(0.3), decimal.NewFromFloat(0.05), true)
	expectedInvalidationLogPass := keeper.CalculateLogLLR(decimal.NewFromFloat(0.3), decimal.NewFromFloat(0.05), false)
	expectedInactiveLogFail := keeper.CalculateLogLLR(decimal.NewFromFloat(0.4), decimal.NewFromFloat(0.1), true)
	expectedInactiveLogPass := keeper.CalculateLogLLR(decimal.NewFromFloat(0.4), decimal.NewFromFloat(0.1), false)

	require.True(t, precomputed.InvalidationLogFail.Equal(expectedInvalidationLogFail))
	require.True(t, precomputed.InvalidationLogPass.Equal(expectedInvalidationLogPass))
	require.True(t, precomputed.InactiveLogFail.Equal(expectedInactiveLogFail))
	require.True(t, precomputed.InactiveLogPass.Equal(expectedInactiveLogPass))
}

func TestGetPrecomputedSPRTValues_Empty(t *testing.T) {
	k, ctx, _ := testkeeper.InferenceKeeperReturningMocks(t)

	precomputed, found := k.GetPrecomputedSPRTValues(ctx)
	require.False(t, found)
	require.True(t, precomputed.InvalidationLogFail.IsZero())
}
