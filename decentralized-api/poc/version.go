package poc

import (
	"github.com/productscience/inference/x/inference/types"
)

// IsPoCv2Enabled returns true if PoC V2 (off-chain artifacts) is enabled.
// Returns true by default if params are nil (V2 is the default going forward).
func IsPoCv2Enabled(params *types.Params) bool {
	if params == nil || params.PocParams == nil {
		return true // default V2
	}
	return params.PocParams.PocV2Enabled
}
