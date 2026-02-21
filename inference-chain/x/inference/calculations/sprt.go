package calculations

import (
	"fmt"
	"sync"

	"github.com/shopspring/decimal"
)

type Decision int

const (
	Undetermined Decision = iota
	Pass
	Fail
	Error
)

type SPRT struct {
	P0, P1 decimal.Decimal // hypothesized failure probs under H0 and H1
	H      decimal.Decimal // symmetric threshold (±H)
	LLR    decimal.Decimal // running log-likelihood ratio

	// Precomputed adjustments
	// logFail  = ln(P1 / P0)
	// logPass  = ln((1 - P1) / (1 - P0))
	logFail decimal.Decimal
	logPass decimal.Decimal
}

type sprtLogCacheKey struct {
	p0   string
	p1   string
	prec int32
}

type sprtLogCacheValue struct {
	logFail decimal.Decimal
	logPass decimal.Decimal
}

// Log adjustments depend only on (p0, p1, precision). Keep one last-used cache
// entry to avoid repeated decimal Ln setup in the common case where params stay
// unchanged across many status computations.
var (
	sprtLogCacheMu    sync.RWMutex
	sprtLogCacheValid bool
	sprtLogCacheK     sprtLogCacheKey
	sprtLogCacheV     sprtLogCacheValue
)

func getSPRTLogCacheKey(p0, p1 decimal.Decimal, prec int32) sprtLogCacheKey {
	return sprtLogCacheKey{
		p0:   p0.String(),
		p1:   p1.String(),
		prec: prec,
	}
}

func getOrComputeSPRTLogAdjustments(p0, p1 decimal.Decimal, prec int32) (decimal.Decimal, decimal.Decimal, error) {
	key := getSPRTLogCacheKey(p0, p1, prec)
	sprtLogCacheMu.RLock()
	if sprtLogCacheValid && sprtLogCacheK == key {
		cached := sprtLogCacheV
		sprtLogCacheMu.RUnlock()
		return cached.logFail, cached.logPass, nil
	}
	sprtLogCacheMu.RUnlock()

	rFail := p1.Div(p0)
	logFail, err := rFail.Ln(prec)
	if err != nil {
		return decimal.Zero, decimal.Zero, fmt.Errorf("ln(P1/P0): %w", err)
	}

	rPass := one.Sub(p1).Div(one.Sub(p0))
	logPass, err := rPass.Ln(prec)
	if err != nil {
		return decimal.Zero, decimal.Zero, fmt.Errorf("ln((1-P1)/(1-P0)): %w", err)
	}

	computed := sprtLogCacheValue{logFail: logFail, logPass: logPass}
	sprtLogCacheMu.Lock()
	// Another goroutine may have populated the same key while we computed.
	if sprtLogCacheValid && sprtLogCacheK == key {
		cached := sprtLogCacheV
		sprtLogCacheMu.Unlock()
		return cached.logFail, cached.logPass, nil
	}
	sprtLogCacheK = key
	sprtLogCacheV = computed
	sprtLogCacheValid = true
	sprtLogCacheMu.Unlock()
	return logFail, logPass, nil
}

func clearSPRTLogCache() {
	sprtLogCacheMu.Lock()
	sprtLogCacheValid = false
	sprtLogCacheK = sprtLogCacheKey{}
	sprtLogCacheV = sprtLogCacheValue{}
	sprtLogCacheMu.Unlock()
}

func sprtLogCacheLen() int {
	sprtLogCacheMu.RLock()
	defer sprtLogCacheMu.RUnlock()
	if sprtLogCacheValid {
		return 1
	}
	return 0
}

func NewSPRT(p0, p1, h, llr decimal.Decimal, prec int32) (*SPRT, error) {

	// Basic sanity: keep probs in (0,1)
	if !p0.GreaterThan(decimal.Zero) || !p0.LessThan(one) ||
		!p1.GreaterThan(decimal.Zero) || !p1.LessThan(one) {
		return nil, fmt.Errorf("P0 and P1 must be in (0,1)")
	}

	logFail, logPass, err := getOrComputeSPRTLogAdjustments(p0, p1, prec)
	if err != nil {
		return nil, err
	}

	return &SPRT{
		P0:      p0,
		P1:      p1,
		H:       h,
		LLR:     llr,
		logFail: logFail,
		logPass: logPass,
	}, nil
}

// UpdateCounts applies a batch: `failures` and `passes` since last call.
// LLR += failures*logFail + passes*logPass
func (s *SPRT) UpdateCounts(failures, passes int64) {
	if failures <= 0 && passes <= 0 {
		return
	}
	if failures != 0 {
		s.LLR = s.LLR.Add(s.logFail.Mul(decimal.NewFromInt(failures)))
	}
	if passes != 0 {
		s.LLR = s.LLR.Add(s.logPass.Mul(decimal.NewFromInt(passes)))
	}
}

func (s *SPRT) UpdateOne(measurementFailed bool) {
	if measurementFailed {
		s.LLR = s.LLR.Add(s.logFail)
	} else {
		s.LLR = s.LLR.Add(s.logPass)
	}
}

// Decision uses symmetric thresholds ±H
func (s *SPRT) Decision() Decision {
	if s.LLR.GreaterThanOrEqual(s.H) {
		return Fail // favor H1 (reject H0)
	}
	if s.LLR.LessThanOrEqual(s.H.Neg()) {
		return Pass // favor H0
	}
	return Undetermined
}
