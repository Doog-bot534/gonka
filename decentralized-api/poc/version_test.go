package poc

import (
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
)

func TestIsPoCv2Enabled_NilParams(t *testing.T) {
	// Default to V2 when params are nil
	assert.True(t, IsPoCv2Enabled(nil))
}

func TestIsPoCv2Enabled_NilPocParams(t *testing.T) {
	params := &types.Params{
		PocParams: nil,
	}
	// Default to V2 when PocParams is nil
	assert.True(t, IsPoCv2Enabled(params))
}

func TestIsPoCv2Enabled_V2Enabled(t *testing.T) {
	params := &types.Params{
		PocParams: &types.PocParams{
			PocV2Enabled: true,
		},
	}
	assert.True(t, IsPoCv2Enabled(params))
}

func TestIsPoCv2Enabled_V2Disabled(t *testing.T) {
	params := &types.Params{
		PocParams: &types.PocParams{
			PocV2Enabled: false,
		},
	}
	assert.False(t, IsPoCv2Enabled(params))
}
