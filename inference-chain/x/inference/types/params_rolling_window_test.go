package types_test

import (
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestParamsRollingWindowValidation(t *testing.T) {
	params := types.DefaultParams()

	// Default should be valid
	err := params.Validate()
	require.NoError(t, err)

	// Test DynamicPricingParams.UtilizationWindowDuration
	// MaxRollingWindowBlocks = 500
	// DynamicPricingEstimatedBlockSeconds = 5
	// 500 * 5 = 2500 seconds

	params.DynamicPricingParams.UtilizationWindowDuration = 2500
	err = params.Validate()
	require.NoError(t, err)

	params.DynamicPricingParams.UtilizationWindowDuration = 2506
	err = params.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "results in 501 blocks, which exceeds the maximum of 500 blocks")

	// Reset
	params.DynamicPricingParams.UtilizationWindowDuration = 60

	// Test BandwidthLimitsParams.InvalidationsSamplePeriod
	if params.BandwidthLimitsParams == nil {
		params.BandwidthLimitsParams = types.DefaultBandwidthLimitsParams()
	}

	params.BandwidthLimitsParams.InvalidationsSamplePeriod = 2500
	err = params.Validate()
	require.NoError(t, err)

	params.BandwidthLimitsParams.InvalidationsSamplePeriod = 2506
	err = params.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "results in 501 blocks, which exceeds the maximum of 500 blocks")
}
