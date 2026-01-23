package keeper

import (
	"math"
	"time"
)

func durationMs(start time.Time) float64 {
	ms := float64(time.Since(start).Nanoseconds()) / float64(time.Millisecond)
	return math.Round(ms*100) / 100
}
