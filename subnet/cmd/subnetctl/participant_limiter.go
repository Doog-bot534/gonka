package main

import (
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultParticipantRequestBurst             = 600
	defaultParticipantRequestRecoveryPerMinute = 10
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

// ParticipantThrottleStore is the persistence interface for reactive throttle state.
type ParticipantThrottleStore interface {
	SaveParticipantThrottle(key string, tokens float64, lastRefillAt time.Time, status int) error
	DeleteParticipantThrottle(key string) error
}

// ParticipantRequestLimiter is a reactive, per-host rate limiter.
//
// Hosts are not tracked until they return their first 429 or 503. After that,
// the host enters a token-bucket cooldown: tokens start at 0 and recover at
// recoveryPerSecond. Each allowed request consumes one token. When tokens
// recover to the full burst value the host is removed from tracking (expired).
type ParticipantRequestLimiter struct {
	mu                sync.Mutex
	burst             float64
	recoveryPerSecond float64
	participants      map[string]*participantRequestState
	metrics           *SubnetMetrics
	store             ParticipantThrottleStore
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

// LoadState restores a previously throttled participant from persistent storage.
// Time-based recovery since lastRefill is applied. If the participant has fully
// recovered (tokens >= burst), the record is deleted from the store instead.
func (l *ParticipantRequestLimiter) LoadState(key string, tokens float64, lastRefill time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(lastRefill).Seconds()
	if elapsed > 0 {
		tokens += elapsed * l.recoveryPerSecond
	}
	if tokens >= l.burst {
		if l.store != nil {
			if err := l.store.DeleteParticipantThrottle(key); err != nil {
				log.Printf("participant_throttle_cleanup_failed participant_key=%s error=%v", key, err)
			}
		}
		log.Printf("participant_limit_recovered_on_load participant_key=%s", key)
		return
	}
	l.participants[key] = &participantRequestState{
		tokens:     tokens,
		lastRefill: now,
	}
	log.Printf("participant_limit_loaded participant_key=%s tokens=%.1f", key, tokens)
}

func (l *ParticipantRequestLimiter) SetStore(store ParticipantThrottleStore) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.store = store
}

// AllowRequest checks whether a request to this participant is allowed.
// Participants that have never been throttled (no state) are always allowed.
func (l *ParticipantRequestLimiter) AllowRequest(participantKey, _ string) error {
	if participantKey == "" {
		return nil
	}
	if relaxedPoCBypassActive() {
		return nil
	}
	if l.allow(participantKey, time.Now()) {
		return nil
	}
	if l.metrics != nil {
		l.metrics.RecordParticipantLimitRejection("transport_request")
	}
	log.Printf("participant_limit_rejected participant_key=%s", participantKey)
	return &ParticipantRateLimitError{ParticipantKey: participantKey}
}

func (l *ParticipantRequestLimiter) allow(participantKey string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	state, tracked := l.participants[participantKey]
	if !tracked {
		return true
	}

	l.refillLocked(state, now)

	if state.tokens >= l.burst {
		delete(l.participants, participantKey)
		l.persistDeleteLocked(participantKey)
		log.Printf("participant_limit_expired participant_key=%s", participantKey)
		return true
	}

	if state.tokens < 1 {
		return false
	}
	state.tokens--
	return true
}

func (l *ParticipantRequestLimiter) CanAcceptEscrow(participantKeys []string) error {
	if relaxedPoCBypassActive() {
		return nil
	}
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

	log.Printf("participant_limit_activated participant_key=%s status=%d path_kind=%s",
		participantKey, statusCode, participantPathKind(path))

	if l.store != nil {
		if err := l.store.SaveParticipantThrottle(participantKey, 0, time.Now(), statusCode); err != nil {
			log.Printf("participant_throttle_persist_failed participant_key=%s error=%v", participantKey, err)
		}
	}
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
		state, tracked := l.participants[key]
		if !tracked {
			continue
		}
		l.refillLocked(state, now)
		if state.tokens < 1 {
			blocked = append(blocked, key)
		}
	}
	sort.Strings(blocked)
	return blocked
}

func (l *ParticipantRequestLimiter) refillLocked(state *participantRequestState, now time.Time) {
	elapsed := now.Sub(state.lastRefill).Seconds()
	if elapsed > 0 {
		state.tokens += elapsed * l.recoveryPerSecond
		if state.tokens > l.burst {
			state.tokens = l.burst
		}
		state.lastRefill = now
	}
}

func (l *ParticipantRequestLimiter) forceExhaustLocked(participantKey string, now time.Time) {
	state, ok := l.participants[participantKey]
	if !ok {
		state = &participantRequestState{}
		l.participants[participantKey] = state
	}
	state.tokens = 0
	state.lastRefill = now
}

func (l *ParticipantRequestLimiter) persistDeleteLocked(key string) {
	if l.store != nil {
		if err := l.store.DeleteParticipantThrottle(key); err != nil {
			log.Printf("participant_throttle_cleanup_failed participant_key=%s error=%v", key, err)
		}
	}
}

// ExhaustedCount returns the number of currently blocked (tokens < 1) participants.
func (l *ParticipantRequestLimiter) ExhaustedCount() int {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	count := 0
	for _, state := range l.participants {
		l.refillLocked(state, now)
		if state.tokens < 1 {
			count++
		}
	}
	return count
}

// TrackedCount returns the number of participants currently in reactive tracking.
func (l *ParticipantRequestLimiter) TrackedCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.participants)
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
