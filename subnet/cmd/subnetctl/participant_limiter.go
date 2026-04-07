package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultParticipantRequestBurst             = 600
	defaultParticipantRequestRecoveryPerMinute = 10
	maxParticipantLimiterEntries               = 10_000
)

var sharedParticipantRequestLimiter = NewParticipantRequestLimiter(
	defaultParticipantRequestBurst,
	defaultParticipantRequestRecoveryPerMinute,
)

type ParticipantRateLimitError struct {
	ParticipantKey string
}

func (e *ParticipantRateLimitError) Error() string {
	if e == nil || e.ParticipantKey == "" {
		return "participant request budget exhausted"
	}
	return fmt.Sprintf("participant request budget exhausted for %s", e.ParticipantKey)
}

type EscrowParticipantRateLimitError struct {
	ParticipantKeys []string
}

func (e *EscrowParticipantRateLimitError) Error() string {
	if e == nil || len(e.ParticipantKeys) == 0 {
		return "no available escrows: participant request budget exhausted"
	}
	return fmt.Sprintf(
		"no available escrows: participant request budget exhausted for %v",
		e.ParticipantKeys,
	)
}

type ParticipantRequestLimiter struct {
	mu                sync.Mutex
	burst             float64
	recoveryPerSecond float64
	participants      map[string]*participantRequestState
	metrics           *SubnetMetrics
}

type participantRequestState struct {
	tokens     float64
	lastRefill time.Time
}

func NewParticipantRequestLimiter(burst int, recoveryPerMinute int) *ParticipantRequestLimiter {
	if burst <= 0 {
		burst = defaultParticipantRequestBurst
	}
	if recoveryPerMinute <= 0 {
		recoveryPerMinute = defaultParticipantRequestRecoveryPerMinute
	}
	return &ParticipantRequestLimiter{
		burst:             float64(burst),
		recoveryPerSecond: float64(recoveryPerMinute) / 60.0,
		participants:      make(map[string]*participantRequestState),
	}
}

func (l *ParticipantRequestLimiter) AllowRequest(participantKey, _ string) error {
	if participantKey == "" {
		return nil
	}
	if l.allow(participantKey, time.Now()) {
		return nil
	}
	if l.metrics != nil {
		l.metrics.RecordParticipantLimitRejection("transport_request")
	}
	return &ParticipantRateLimitError{ParticipantKey: participantKey}
}

func (l *ParticipantRequestLimiter) allow(participantKey string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	state := l.stateLocked(participantKey, now)
	if state.tokens < 1 {
		return false
	}
	state.tokens--
	return true
}

func (l *ParticipantRequestLimiter) CanAcceptEscrow(participantKeys []string) error {
	blocked := l.BlockedParticipants(participantKeys)
	if len(blocked) == 0 {
		return nil
	}
	return &EscrowParticipantRateLimitError{ParticipantKeys: blocked}
}

func (l *ParticipantRequestLimiter) ObserveResult(participantKey, path string, statusCode int) {
	if participantKey == "" || statusCode <= 0 {
		return
	}
	if l.metrics != nil && statusCode >= http.StatusBadRequest {
		l.metrics.RecordParticipantTransportError(participantPathKind(path), statusCode)
	}
	if !isParticipantThrottleStatus(statusCode) {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	l.forceExhaustLocked(participantKey, time.Now())
}

func (l *ParticipantRequestLimiter) SetMetrics(metrics *SubnetMetrics) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.metrics = metrics
}

func (l *ParticipantRequestLimiter) BlockedParticipants(participantKeys []string) []string {
	if len(participantKeys) == 0 {
		return nil
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	seen := make(map[string]struct{}, len(participantKeys))
	var blocked []string
	for _, key := range participantKeys {
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		state := l.stateLocked(key, now)
		if state.tokens < 1 {
			blocked = append(blocked, key)
		}
	}
	sort.Strings(blocked)
	return blocked
}

func (l *ParticipantRequestLimiter) stateLocked(participantKey string, now time.Time) *participantRequestState {
	if len(l.participants) > maxParticipantLimiterEntries {
		clear(l.participants)
	}
	state, ok := l.participants[participantKey]
	if !ok {
		state = &participantRequestState{
			tokens:     l.burst,
			lastRefill: now,
		}
		l.participants[participantKey] = state
		return state
	}
	elapsed := now.Sub(state.lastRefill).Seconds()
	if elapsed > 0 {
		state.tokens += elapsed * l.recoveryPerSecond
		if state.tokens > l.burst {
			state.tokens = l.burst
		}
		state.lastRefill = now
	}
	return state
}

func (l *ParticipantRequestLimiter) forceExhaustLocked(participantKey string, now time.Time) {
	state := l.stateLocked(participantKey, now)
	state.tokens = 0
	state.lastRefill = now
}

func (l *ParticipantRequestLimiter) ExhaustedCount() int {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	count := 0
	for key := range l.participants {
		if l.stateLocked(key, now).tokens < 1 {
			count++
		}
	}
	return count
}

func isParticipantThrottleStatus(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests || statusCode == http.StatusServiceUnavailable
}

func participantPathKind(path string) string {
	switch {
	case strings.Contains(path, "/chat/completions"):
		return "inference"
	case strings.Contains(path, "/verify-timeout"):
		return "verify_timeout"
	case strings.Contains(path, "/challenge-receipt"):
		return "challenge_receipt"
	case strings.Contains(path, "/gossip/"):
		return "gossip"
	case strings.Contains(path, "/diffs"), strings.Contains(path, "/signatures"), strings.Contains(path, "/mempool"):
		return "query"
	default:
		return "other"
	}
}
