package types

import (
	"github.com/shopspring/decimal"
)

// SPRTPrecomputedValues contains log-likelihood ratios precomputed for SPRT.
type SPRTPrecomputedValues struct {
	InvalidationLogFail decimal.Decimal
	InvalidationLogPass decimal.Decimal
	InactiveLogFail     decimal.Decimal
	InactiveLogPass     decimal.Decimal
}
