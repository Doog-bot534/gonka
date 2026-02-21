package calculations

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestNewSPRT_ReusesCachedLogAdjustments(t *testing.T) {
	clearSPRTLogCache()
	t.Cleanup(clearSPRTLogCache)

	p0 := decimal.RequireFromString("0.10")
	p1 := decimal.RequireFromString("0.20")
	h := decimal.NewFromInt(4)

	_, err := NewSPRT(p0, p1, h, decimal.Zero, LogPrecision)
	require.NoError(t, err)
	require.Equal(t, 1, sprtLogCacheLen())

	// Different H/LLR should still reuse same cached logs for same p0/p1/precision.
	_, err = NewSPRT(p0, p1, decimal.NewFromInt(8), decimal.NewFromFloat(1.25), LogPrecision)
	require.NoError(t, err)
	require.Equal(t, 1, sprtLogCacheLen())
}

func TestNewSPRT_SingleEntryCacheReplacesPreviousKey(t *testing.T) {
	clearSPRTLogCache()
	t.Cleanup(clearSPRTLogCache)

	p0 := decimal.RequireFromString("0.05")
	p1 := decimal.RequireFromString("0.10")
	h := decimal.NewFromInt(4)

	_, err := NewSPRT(p0, p1, h, decimal.Zero, 12)
	require.NoError(t, err)
	require.Equal(t, 1, sprtLogCacheLen())

	_, err = NewSPRT(p0, p1, h, decimal.Zero, 16)
	require.NoError(t, err)
	require.Equal(t, 1, sprtLogCacheLen())
}
