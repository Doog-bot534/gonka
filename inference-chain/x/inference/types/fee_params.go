package types

import (
	"encoding/json"
	"fmt"
)

// FeeParamsKey is the KV store key for FeeParams (stored separately from main Params proto).
var FeeParamsKey = []byte("p_fee_params")

// FeeParams defines governance-controlled fee parameters for consensus-level fee enforcement.
type FeeParams struct {
	// MinGasPriceNgonka is the minimum gas price in ngonka enforced at consensus level.
	// A transaction must include fee >= gas_limit * MinGasPriceNgonka to be accepted.
	MinGasPriceNgonka uint64 `json:"min_gas_price_ngonka"`

	// BaseValidationGas is the extra gas consumed on the first MsgPoCV2StoreCommit
	// per participant per epoch. Covers the fixed cost of triggering PoC validation
	// (GPU compute on all validators).
	BaseValidationGas uint64 `json:"base_validation_gas"`

	// GasPerPoCCount is additional gas consumed per unit of Count in MsgPoCV2StoreCommit.
	// Charged on the delta (count increase) so total equals final_count * GasPerPoCCount.
	GasPerPoCCount uint64 `json:"gas_per_poc_count"`
}

// DefaultFeeParams returns the default fee parameters.
func DefaultFeeParams() FeeParams {
	return FeeParams{
		MinGasPriceNgonka: 10,
		BaseValidationGas: 500_000,
		GasPerPoCCount:    100,
	}
}

// Validate checks that the fee parameters are well-formed.
func (fp FeeParams) Validate() error {
	if fp.MinGasPriceNgonka > 1_000_000 {
		return fmt.Errorf("min_gas_price_ngonka %d exceeds safety limit of 1,000,000", fp.MinGasPriceNgonka)
	}
	return nil
}

// Marshal serializes FeeParams to JSON bytes.
func (fp FeeParams) Marshal() ([]byte, error) {
	return json.Marshal(fp)
}

// UnmarshalFeeParams deserializes FeeParams from JSON bytes.
func UnmarshalFeeParams(bz []byte) (FeeParams, error) {
	var fp FeeParams
	err := json.Unmarshal(bz, &fp)
	return fp, err
}
