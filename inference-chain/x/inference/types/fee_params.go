package types

import (
	"encoding/json"
	"fmt"

	"cosmossdk.io/collections"
	"cosmossdk.io/collections/codec"
)

// FeeParamsPrefix is the collections prefix for the FeeParams item.
// Uses the next available numeric prefix after SubnetEscrowsByEpochPrefix (52).
var FeeParamsPrefix = collections.NewPrefix(53)

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
// At MinGasPriceNgonka=10 and ~80k gas per tx, fee ≈ 800,000 ngonka ≈ $0.00046 per tx
// (at GNK=$0.57). Governance-adjustable.
func DefaultFeeParams() FeeParams {
	return FeeParams{
		MinGasPriceNgonka: 10, // per gas unit; ~$0.00046 per typical tx
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

// --- collections.ValueCodec implementation for FeeParams ---

// FeeParamsValueCodec is a ValueCodec that serializes FeeParams as JSON for
// use with cosmossdk.io/collections.Item.
var FeeParamsValueCodec feeParamsValueCodec

type feeParamsValueCodec struct{}

func (feeParamsValueCodec) Encode(value FeeParams) ([]byte, error) {
	return json.Marshal(value)
}

func (feeParamsValueCodec) Decode(bz []byte) (FeeParams, error) {
	var fp FeeParams
	err := json.Unmarshal(bz, &fp)
	return fp, err
}

func (feeParamsValueCodec) EncodeJSON(value FeeParams) ([]byte, error) {
	return json.Marshal(value)
}

func (feeParamsValueCodec) DecodeJSON(bz []byte) (FeeParams, error) {
	var fp FeeParams
	err := json.Unmarshal(bz, &fp)
	return fp, err
}

func (feeParamsValueCodec) Stringify(value FeeParams) string {
	bz, _ := json.Marshal(value)
	return string(bz)
}

func (feeParamsValueCodec) ValueType() string {
	return "inference/FeeParams"
}

// Verify interface compliance at compile time.
var _ codec.ValueCodec[FeeParams] = feeParamsValueCodec{}
