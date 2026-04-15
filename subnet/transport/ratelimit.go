package transport

import (
	"net/http"
	"sync"

	"github.com/labstack/echo/v4"
	"golang.org/x/time/rate"
)

// RateLimitConfig controls per-sender request rate limiting.
type RateLimitConfig struct {
	RequestsPerSecond float64 // per sender, default 100
	BurstSize         int     // token bucket burst, default 200
}

func DefaultRateLimitConfig() RateLimitConfig {
	return RateLimitConfig{
		RequestsPerSecond: 100,
		BurstSize:         200,
	}
}

// rateLimiter tracks per-sender rate limiters.
type rateLimiter struct {
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
	order    []string // insertion order for LRU-style eviction
	config   RateLimitConfig
}

func newRateLimiter(cfg RateLimitConfig) *rateLimiter {
	return &rateLimiter{
		limiters: make(map[string]*rate.Limiter),
		order:    make([]string, 0, maxLimiterEntries),
		config:   cfg,
	}
}

const maxLimiterEntries = 1000

func (rl *rateLimiter) allow(sender string) bool {
	rl.mu.Lock()
	// Evict oldest entries instead of clearing the entire map.
	// Bulk clear resets all rate limits simultaneously, giving every
	// sender a fresh burst window — an attacker can trigger this by
	// sending from 1000+ distinct addresses.
	if len(rl.limiters) >= maxLimiterEntries {
		evictCount := maxLimiterEntries / 4 // evict 25% oldest
		for i := 0; i < evictCount && i < len(rl.order); i++ {
			delete(rl.limiters, rl.order[i])
		}
		rl.order = rl.order[evictCount:]
	}
	lim, ok := rl.limiters[sender]
	if !ok {
		lim = rate.NewLimiter(rate.Limit(rl.config.RequestsPerSecond), rl.config.BurstSize)
		rl.limiters[sender] = lim
		rl.order = append(rl.order, sender)
	}
	rl.mu.Unlock()
	return lim.Allow()
}

// Must run after auth middleware so contextKeySender is set.
func rateLimitMiddleware(rl *rateLimiter) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			sender, _ := c.Get(contextKeySender).(string)
			if sender == "" {
				return next(c)
			}
			if !rl.allow(sender) {
				return echo.NewHTTPError(http.StatusTooManyRequests, "rate limit exceeded")
			}
			return next(c)
		}
	}
}
