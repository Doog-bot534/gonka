package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"subnet/host"
	"subnet/logging"
	"subnet/transport"
	"subnet/user"
)

// Tuning knobs — exported so they can be adjusted without code changes.
var (
	ReceiptTimeout             = 500 * time.Millisecond
	ParallelAdvantageThreshold = 0.5 // 50% better estimated time
	UnresponsiveThreshold      = 1.0 // any non-responsive history → start secondary
	MinSamplesForDecision      = 3
	LogHeartbeatInterval       = time.Minute
	FirstTokenTimeoutCap       = time.Second
	PerInputTokenFirstTokenLag = 10 * time.Millisecond
	NonStreamResponseFloor     = 20 * time.Second
	PerInputTokenResponseLag   = 20 * time.Millisecond
	SecondaryWaitAfterWinner   = time.Minute
)

var maxSpeculativeAttempts atomic.Int64

func SetMaxSpeculativeAttempts(v int) {
	maxSpeculativeAttempts.Store(int64(v))
}

func CurrentMaxSpeculativeAttempts() int {
	return int(maxSpeculativeAttempts.Load())
}

// Decision describes whether and when to start a parallel secondary inference.
type Decision struct {
	RunSecondary bool
	Delay        time.Duration // 0 = immediate
	Reason       string
}

// Redundancy runs one request reliably, using extra attempts when needed.
// It sits between Proxy and Session: Proxy delegates request execution here,
// and Redundancy decides whether to use just one nonce or several.
type Redundancy struct {
	session         *user.Session
	perf            *PerfTracker
	groupSize       int
	subnetID        string
	metrics         *SubnetMetrics
	onEscrowMissing func() // called (at most once per request) when a host reports escrow not found
}

func NewRedundancy(session *user.Session, perf *PerfTracker, groupSize int) *Redundancy {
	return &Redundancy{
		session:   session,
		perf:      perf,
		groupSize: groupSize,
	}
}

func (e *Redundancy) Decide(primaryHostIdx int, inputTokens uint64) Decision {
	secondaryHostIdx := (primaryHostIdx + 1) % e.groupSize

	// Rule 1: primary is known unresponsive → immediate parallel
	if e.perf.IsUnresponsive(primaryHostIdx) {
		return Decision{RunSecondary: true, Delay: 0, Reason: "primary_unresponsive"}
	}

	// Rule 2: secondary is >50% faster → immediate parallel
	primaryEst := e.perf.EstimatedTimeMs(primaryHostIdx, inputTokens)
	secondaryEst := e.perf.EstimatedTimeMs(secondaryHostIdx, inputTokens)
	if primaryEst > 0 && secondaryEst > 0 && secondaryEst < primaryEst*(1-ParallelAdvantageThreshold) {
		return Decision{RunSecondary: true, Delay: 0, Reason: "secondary_faster"}
	}

	// Rule 3: default — start secondary after ReceiptTimeout if no receipt
	return Decision{RunSecondary: true, Delay: ReceiptTimeout, Reason: "receipt_timeout"}
}

// inflight tracks one in-flight inference and its timing.
type inflight struct {
	prepared  *user.PreparedInference
	hostIdx   int
	hostID    string
	nonce     uint64
	escrowID  string
	sendTime  time.Time
	escalated bool
	probe     bool

	receiptOnce sync.Once
	receiptTime time.Time
	receiptCh   chan struct{} // closed when receipt arrives

	tokenOnce     sync.Once
	firstToken    time.Time
	firstTokenCh  chan struct{}
	outputChunks  atomic.Int64
	lastChunkAt   atomic.Int64
	forwardedLog  sync.Once
	suppressedLog sync.Once
	sampleOnce    sync.Once
	processOnce   sync.Once
	processErr    error

	resp *host.HostResponse
	err  error
	done chan struct{}
}

// raceGroup arbitrates which inflight's stream is forwarded to the client.
type raceGroup struct {
	mu       sync.Mutex
	winner   uint64 // 0 = undecided
	w        io.Writer
	decided  atomic.Bool
	logCtx   context.Context
	writeCtx context.Context
	escrow   string
}

func newRaceGroup(logCtx, writeCtx context.Context, escrow string, w io.Writer) *raceGroup {
	return &raceGroup{logCtx: logCtx, writeCtx: writeCtx, escrow: escrow, w: w}
}

func (rg *raceGroup) setWinner(nonce uint64) {
	rg.mu.Lock()
	defer rg.mu.Unlock()
	if rg.winner == 0 {
		rg.winner = nonce
		rg.decided.Store(true)
		logInferenceStage(rg.logCtx, rg.escrow, nonce, "winner_selected")
	}
}

func (rg *raceGroup) hasDecided() bool {
	return rg.decided.Load()
}

func (rg *raceGroup) winnerNonce() uint64 {
	rg.mu.Lock()
	defer rg.mu.Unlock()
	return rg.winner
}

// raceWriter is an io.Writer that only forwards writes from the winning nonce.
type raceWriter struct {
	group *raceGroup
	nonce uint64
	inf   *inflight
}

func (rw *raceWriter) ctxErr() error {
	if rw.group == nil || rw.group.writeCtx == nil {
		return nil
	}
	return rw.group.writeCtx.Err()
}

func (rw *raceWriter) Write(p []byte) (int, error) {
	if err := rw.ctxErr(); err != nil {
		return 0, err
	}
	now := time.Now()
	rw.inf.tokenOnce.Do(func() {
		rw.inf.firstToken = now
		if rw.inf.firstTokenCh != nil {
			close(rw.inf.firstTokenCh)
		}
	})
	rw.inf.outputChunks.Add(1)
	rw.inf.lastChunkAt.Store(now.UnixNano())
	if !rw.inf.probe {
		rw.group.setWinner(rw.nonce)
	}

	rw.group.mu.Lock()
	isWinner := rw.group.winner == rw.nonce
	winnerNonce := rw.group.winner
	rw.group.mu.Unlock()

	if rw.inf.firstToken.Equal(now) {
		route := "loser"
		if isWinner {
			route = "winner"
		} else if rw.inf.probe {
			route = "probe"
		}
		logInferenceStage(rw.group.logCtx, rw.inf.escrowID, rw.nonce, "first_token", "host", rw.inf.hostID, "route", route, "winner_nonce", winnerNonce)
	}

	if rw.inf.probe {
		rw.inf.suppressedLog.Do(func() {
			logInferenceStage(rw.group.logCtx, rw.inf.escrowID, rw.nonce, "poc_probe_stream_suppressed", "host", rw.inf.hostID, "winner_nonce", winnerNonce, "poc_reason", currentPoCPhaseReason())
		})
		return len(p), nil
	}

	if isWinner {
		rw.inf.forwardedLog.Do(func() {
			logInferenceStage(rw.group.logCtx, rw.inf.escrowID, rw.nonce, "stream_forwarding_started", "host", rw.inf.hostID)
		})
	} else {
		rw.inf.suppressedLog.Do(func() {
			logInferenceStage(rw.group.logCtx, rw.inf.escrowID, rw.nonce, "stream_suppressed", "host", rw.inf.hostID, "winner_nonce", winnerNonce)
		})
	}

	if isWinner && rw.group.w != nil {
		return rw.group.w.Write(p)
	}
	return len(p), nil
}

func (rw *raceWriter) Flush() {
	if rw.ctxErr() != nil {
		return
	}
	if rw.inf.probe {
		return
	}
	rw.group.mu.Lock()
	isWinner := rw.group.winner == rw.nonce
	rw.group.mu.Unlock()
	if isWinner {
		if f, ok := rw.group.w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

// RunInference prepares and sends an inference, optionally racing a secondary.
// It replaces the old retry-based runInference in proxy.go.
func (e *Redundancy) RunInference(ctx context.Context, params user.InferenceParams, w io.Writer) error {
	ctx, _ = ensureRequestLogContext(ctx)
	settleCtx, _ := ensureRequestLogContext(context.Background())
	settleCtx = logging.PropagateRequestID(settleCtx, ctx)
	logRequestStage(ctx, "runner_started", "escrow", e.subnetID, "input_tokens", params.InputLength, "model", params.Model)
	primary, err := e.prepareInflight(params)
	if err != nil {
		logRequestStage(ctx, "runner_prepare_failed", "escrow", e.subnetID, "error", err)
		return err
	}

	decision := e.Decide(primary.hostIdx, params.InputLength)
	maxAttempts := e.maxAttempts()
	if e.metrics != nil {
		e.metrics.RecordSpeculativeDecision(decision.Reason)
	}
	logInferenceStage(ctx, primary.escrowID, primary.nonce, "decision_made",
		"host", primary.hostID,
		"decision", decision.Reason,
		"delay_ms", decision.Delay.Milliseconds(),
		"max_attempts", maxAttempts,
		"group_size", e.groupSize,
	)
	race := newRaceGroup(settleCtx, ctx, e.subnetID, w)
	attempts := []*inflight{primary}

	// Always start the primary.
	e.startInflight(settleCtx, primary, race)

	if decision.RunSecondary && decision.Delay == 0 && len(attempts) < maxAttempts {
		logRequestStage(ctx, "secondary_immediate_start", "escrow", e.subnetID, "decision", decision.Reason)
		primary.escalated = true
		if secondary := e.startAdditionalInflight(ctx, settleCtx, race, params, "secondary_immediate_start", primary, decision.Reason); secondary != nil {
			attempts = append(attempts, secondary)
		}
	} else if decision.RunSecondary && decision.Delay == 0 {
		logInferenceStage(ctx, primary.escrowID, primary.nonce, "secondary_immediate_skipped",
			"host", primary.hostID,
			"reason", "attempt_limit",
			"decision", decision.Reason,
			"current_attempts", len(attempts),
			"max_attempts", maxAttempts,
		)
	}

	return e.awaitRace(ctx, settleCtx, attempts, race, params, decision)
}

func (e *Redundancy) prepareInflight(params user.InferenceParams) (*inflight, error) {
	hostIdx := e.nextHostIdx()
	probe := e.shouldUseProbeForHost(hostIdx)
	preparedParams := params
	if probe {
		preparedParams = probeParams(params)
	}
	prepared, err := e.session.PrepareInference(preparedParams)
	if err != nil {
		return nil, fmt.Errorf("prepare: %w", err)
	}
	return &inflight{
		prepared:     prepared,
		hostIdx:      prepared.HostIdx(),
		hostID:       e.session.HostLabel(prepared.HostIdx()),
		nonce:        prepared.Nonce(),
		escrowID:     e.subnetID,
		probe:        probe,
		done:         make(chan struct{}),
		receiptCh:    make(chan struct{}),
		firstTokenCh: make(chan struct{}),
	}, nil
}

func (e *Redundancy) startInflight(ctx context.Context, inf *inflight, race *raceGroup) {
	rw := &raceWriter{group: race, nonce: inf.nonce, inf: inf}
	receiptHandler := func() {
		inf.receiptOnce.Do(func() {
			inf.receiptTime = time.Now()
			logInferenceStage(ctx, inf.escrowID, inf.nonce, "receipt_received", "host", inf.hostID, "elapsed_ms", inf.receiptTime.Sub(inf.sendTime).Milliseconds())
			close(inf.receiptCh)
		})
	}
	logInferenceStage(ctx, inf.escrowID, inf.nonce, "prepared", "host", inf.hostID)
	if inf.probe {
		logInferenceStage(ctx, inf.escrowID, inf.nonce, "poc_probe_prepared", "host", inf.hostID, "max_tokens", pocProbeMaxTokens, "poc_reason", currentPoCPhaseReason())
	}
	go e.monitorInflight(ctx, inf, race)

	go func() {
		defer close(inf.done)
		inf.sendTime = time.Now()
		logInferenceStage(ctx, inf.escrowID, inf.nonce, "started", "host", inf.hostID)
		inf.resp, inf.err = e.session.SendOnly(ctx, inf.prepared, rw, receiptHandler)
		if inf.err != nil {
			logInferenceStage(ctx, inf.escrowID, inf.nonce, "send_failed", "host", inf.hostID, "error", inf.err)
		} else {
			logInferenceStage(ctx, inf.escrowID, inf.nonce, "send_completed", "host", inf.hostID)
		}
	}()
}

// startDelayed waits for receipt or timeout, then starts a secondary if needed.
// Returns nil if receipt arrived before timeout (no secondary needed).
func (e *Redundancy) startAdditionalInflight(streamCtx, settleCtx context.Context, race *raceGroup, params user.InferenceParams, stage string, trigger *inflight, reason string) *inflight {
	if streamCtx.Err() != nil {
		return nil
	}
	if race.hasDecided() {
		return nil
	}
	fields := []any{"host", trigger.hostID}
	if delay := escalationDelay(stage, params.InputLength); delay > 0 {
		fields = append(fields, "delay_ms", delay.Milliseconds())
	}
	logInferenceStage(settleCtx, trigger.escrowID, trigger.nonce, stage, fields...)
	next, err := e.prepareInflight(params)
	if err != nil {
		logRequestStage(settleCtx, "secondary_prepare_failed", "escrow", e.subnetID, "decision", reason, "error", err)
		return nil
	}
	if e.metrics != nil {
		e.metrics.RecordSpeculativeAttemptStart(reason)
	}
	e.startInflight(settleCtx, next, race)
	return next
}

func firstTokenFallbackDelay(inputTokens uint64) time.Duration {
	delay := time.Duration(inputTokens) * PerInputTokenFirstTokenLag
	if delay < FirstTokenTimeoutCap {
		return FirstTokenTimeoutCap
	}
	return delay
}

func nonStreamingFallbackDelay(inputTokens uint64) time.Duration {
	delay := time.Duration(inputTokens) * PerInputTokenResponseLag
	if delay < NonStreamResponseFloor {
		return NonStreamResponseFloor
	}
	return delay
}

func waitForFirstTokenUntil(ctx context.Context, inf *inflight, deadline time.Time) bool {
	if !inf.firstToken.IsZero() {
		return true
	}
	d := time.Until(deadline)
	if d <= 0 {
		return false
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-inf.firstTokenCh:
		return true
	case <-inf.done:
		return !inf.firstToken.IsZero()
	case <-timer.C:
		return !inf.firstToken.IsZero()
	case <-ctx.Done():
		return false
	}
}

func waitForInflightDoneUntil(ctx context.Context, inf *inflight, deadline time.Time) bool {
	d := time.Until(deadline)
	if d <= 0 {
		select {
		case <-inf.done:
			return true
		default:
			return false
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-inf.done:
		return true
	case <-timer.C:
		select {
		case <-inf.done:
			return true
		default:
			return false
		}
	case <-ctx.Done():
		return false
	}
}

type escalationTrigger struct {
	inf      *inflight
	deadline time.Time
	stage    string
	reason   string
}

func (e *Redundancy) awaitRace(streamCtx, settleCtx context.Context, attempts []*inflight, race *raceGroup, params user.InferenceParams, decision Decision) error {
	doneCh := make(chan *inflight, e.maxAttempts())
	for _, inf := range attempts {
		e.watchInflightDone(inf, doneCh)
	}

	var (
		graceTimer *time.Timer
		graceC     <-chan time.Time
	)

	for {
		winner := race.winnerNonce()
		if graceTimer == nil && winner != 0 {
			if winning := inflightByNonce(attempts, winner); winning != nil && inflightDone(winning) && inflightFinished(winning) {
				if pending := pendingInflights(attempts); len(pending) > 0 {
					graceTimer = time.NewTimer(SecondaryWaitAfterWinner)
					graceC = graceTimer.C
				}
			}
		}

		trigger, hasTrigger := nextEscalationTrigger(attempts, params)
		maxAttempts := e.maxAttempts()
		var escalationTimer *time.Timer
		var escalationC <-chan time.Time
		if hasTrigger && winner == 0 && len(attempts) < maxAttempts {
			wait := time.Until(trigger.deadline)
			if wait < 0 {
				wait = 0
			}
			escalationTimer = time.NewTimer(wait)
			escalationC = escalationTimer.C
		} else if hasTrigger && winner == 0 {
			logInferenceStage(settleCtx, trigger.inf.escrowID, trigger.inf.nonce, "escalation_skipped",
				"host", trigger.inf.hostID,
				"stage", trigger.stage,
				"reason", "attempt_limit",
				"current_attempts", len(attempts),
				"max_attempts", maxAttempts,
			)
		}
		if allInflightsDone(attempts) && escalationC == nil {
			if graceTimer != nil {
				graceTimer.Stop()
			}
			return e.finishRaceOutcome(settleCtx, attempts, params, decision, winner)
		}

		select {
		case <-doneCh:
		case <-escalationC:
			trigger.inf.escalated = true
			if len(attempts) < maxAttempts {
				if next := e.startAdditionalInflight(streamCtx, settleCtx, race, params, trigger.stage, trigger.inf, trigger.reason); next != nil {
					attempts = append(attempts, next)
					e.watchInflightDone(next, doneCh)
				}
			}
		case <-graceC:
			if graceTimer != nil {
				graceTimer.Stop()
				graceTimer = nil
				graceC = nil
			}
			for _, inf := range attempts {
				if err := e.processResolvedInflight(inf); err != nil {
					logInferenceStage(settleCtx, inf.escrowID, inf.nonce, "process_response_failed", "host", inf.hostID, "error", err)
				}
			}
			pending := pendingInflights(attempts)
			logRequestStage(settleCtx, "speculative_wait_abandoned", "escrow", e.subnetID, "winner_nonce", winner, "pending", len(pending), "wait_ms", SecondaryWaitAfterWinner.Milliseconds())
			go e.finishRaceWhenPendingDone(settleCtx, attempts, params, decision, winner)
			logRequestStage(settleCtx, "request_returned_while_speculation_pending", "escrow", e.subnetID, "winner_nonce", winner, "decision", decision.Reason)
			return nil
		case <-streamCtx.Done():
			if escalationTimer != nil {
				stopTimer(escalationTimer)
			}
			if graceTimer != nil {
				stopTimer(graceTimer)
			}
			pending := pendingInflights(attempts)
			logRequestStage(settleCtx, "request_stream_canceled", "escrow", e.subnetID, "winner_nonce", winner, "pending", len(pending), "decision", decision.Reason, "error", streamCtx.Err())
			go e.finishRaceWhenPendingDone(settleCtx, attempts, params, decision, winner)
			return streamCtx.Err()
		}

		if escalationTimer != nil {
			stopTimer(escalationTimer)
		}
	}
}

func (e *Redundancy) watchInflightDone(inf *inflight, doneCh chan<- *inflight) {
	go func() {
		<-inf.done
		doneCh <- inf
	}()
}

func nextEscalationTrigger(attempts []*inflight, params user.InferenceParams) (escalationTrigger, bool) {
	var (
		chosen escalationTrigger
		ok     bool
	)
	for _, inf := range attempts {
		trigger, candidate := escalationForInflight(inf, params)
		if !candidate {
			continue
		}
		if !ok || trigger.deadline.Before(chosen.deadline) {
			chosen = trigger
			ok = true
		}
	}
	return chosen, ok
}

func escalationForInflight(inf *inflight, params user.InferenceParams) (escalationTrigger, bool) {
	if inf == nil || inf.escalated {
		return escalationTrigger{}, false
	}
	if inf.probe {
		return escalationTrigger{
			inf:      inf,
			deadline: time.Now(),
			stage:    "poc_probe_immediate_escalation",
			reason:   "poc_probe",
		}, true
	}
	if inflightDone(inf) {
		if inflightFinished(inf) {
			return escalationTrigger{}, false
		}
		return escalationTrigger{
			inf:      inf,
			deadline: time.Now(),
			stage:    "attempt_failed",
			reason:   "attempt_failed",
		}, true
	}
	if inf.sendTime.IsZero() {
		return escalationTrigger{}, false
	}
	if inf.receiptTime.IsZero() {
		return escalationTrigger{
			inf:      inf,
			deadline: inf.sendTime.Add(ReceiptTimeout),
			stage:    "receipt_timeout_wait_elapsed",
			reason:   "receipt_timeout",
		}, true
	}
	if !params.Stream {
		return escalationTrigger{
			inf:      inf,
			deadline: inf.sendTime.Add(nonStreamingFallbackDelay(params.InputLength)),
			stage:    "response_timeout_wait_elapsed",
			reason:   "response_timeout",
		}, true
	}
	if !inf.firstToken.IsZero() {
		return escalationTrigger{}, false
	}
	return escalationTrigger{
		inf:      inf,
		deadline: inf.sendTime.Add(firstTokenFallbackDelay(params.InputLength)),
		stage:    "first_token_timeout_wait_elapsed",
		reason:   "first_token_timeout",
	}, true
}

func escalationDelay(stage string, inputTokens uint64) time.Duration {
	switch stage {
	case "receipt_timeout_wait_elapsed":
		return ReceiptTimeout
	case "first_token_timeout_wait_elapsed":
		return firstTokenFallbackDelay(inputTokens)
	case "response_timeout_wait_elapsed":
		return nonStreamingFallbackDelay(inputTokens)
	case "attempt_failed":
		return 0
	default:
		return 0
	}
}

func (e *Redundancy) monitorInflight(ctx context.Context, inf *inflight, race *raceGroup) {
	ticker := time.NewTicker(LogHeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-inf.done:
			return
		case <-ticker.C:
			if inf.sendTime.IsZero() {
				continue
			}
			stage := "waiting_for_receipt"
			fields := []any{
				"host", inf.hostID,
				"elapsed_ms", time.Since(inf.sendTime).Milliseconds(),
				"output_chunks", inf.outputChunks.Load(),
			}
			if !inf.receiptTime.IsZero() {
				stage = "waiting_for_first_token"
				fields = append(fields, "since_receipt_ms", time.Since(inf.receiptTime).Milliseconds())
			}
			if !inf.firstToken.IsZero() {
				stage = "streaming_inflight"
				fields = append(fields, "since_first_token_ms", time.Since(inf.firstToken).Milliseconds())
				if lastChunkAt := inf.lastChunkAt.Load(); lastChunkAt > 0 {
					fields = append(fields, "since_last_chunk_ms", time.Since(time.Unix(0, lastChunkAt)).Milliseconds())
				}
				winnerNonce := race.winnerNonce()
				role := "unknown"
				if winnerNonce == inf.nonce {
					role = "winner"
				} else if winnerNonce != 0 {
					role = "loser"
				}
				fields = append(fields, "role", role, "winner_nonce", winnerNonce)
			}
			logInferenceStage(ctx, inf.escrowID, inf.nonce, stage, fields...)
		case <-ctx.Done():
			return
		}
	}
}

func (e *Redundancy) finishRaceWhenPendingDone(ctx context.Context, attempts []*inflight, params user.InferenceParams, decision Decision, winnerNonce uint64) {
	bgCtx, _ := ensureRequestLogContext(context.Background())
	bgCtx = logging.PropagateRequestID(bgCtx, ctx)
	for _, inf := range pendingInflights(attempts) {
		<-inf.done
	}
	if err := e.finishRaceOutcome(bgCtx, attempts, params, decision, winnerNonce); err != nil {
		logRequestStage(bgCtx, "background_race_finalize_failed", "escrow", e.subnetID, "error", err)
	}
}

func pendingInflights(attempts []*inflight) []*inflight {
	var pending []*inflight
	for _, inf := range attempts {
		select {
		case <-inf.done:
		default:
			pending = append(pending, inf)
		}
	}
	return pending
}

func allInflightsDone(attempts []*inflight) bool {
	for _, inf := range attempts {
		if !inflightDone(inf) {
			return false
		}
	}
	return true
}

func inflightDone(inf *inflight) bool {
	select {
	case <-inf.done:
		return true
	default:
		return false
	}
}

// inflightFinished checks the raw response for MsgFinishInference.
// Used during the race loop before ProcessResponse has been called.
func inflightFinished(inf *inflight) bool {
	return inf.err == nil && inf.resp != nil && user.HasMsgFinish(inf.resp.Mempool, inf.nonce)
}

func inflightByNonce(attempts []*inflight, nonce uint64) *inflight {
	for _, inf := range attempts {
		if inf.nonce == nonce {
			return inf
		}
	}
	return nil
}

func (e *Redundancy) recordSampleOnce(inf *inflight, params user.InferenceParams) {
	inf.sampleOnce.Do(func() {
		e.recordSample(inf, params)
	})
}

func (e *Redundancy) processInflightOnce(inf *inflight) error {
	inf.processOnce.Do(func() {
		if inf.resp == nil {
			return
		}
		inf.processErr = e.session.ProcessResponse(inf.hostIdx, inf.resp, inf.nonce)
	})
	return inf.processErr
}

func (e *Redundancy) processResolvedInflight(inf *inflight) error {
	select {
	case <-inf.done:
		return e.processInflightOnce(inf)
	default:
		return nil
	}
}

func (e *Redundancy) finishRaceOutcome(ctx context.Context, attempts []*inflight, params user.InferenceParams, decision Decision, winnerNonce uint64) error {
	// Process all responses first so Session has complete protocol state.
	for _, inf := range attempts {
		if err := e.processInflightOnce(inf); err != nil {
			logInferenceStage(ctx, inf.escrowID, inf.nonce, "process_response_failed", "host", inf.hostID, "error", err)
		}
	}

	winnerNonce = e.resolvedWinnerNonce(attempts, winnerNonce)
	var winnerIdx int
	if len(attempts) > 0 {
		winnerIdx = attempts[0].hostIdx
	}
	if winner := inflightByNonce(attempts, winnerNonce); winner != nil {
		winnerIdx = winner.hostIdx
	}

	var (
		anySucceeded bool
		failed       []*inflight
	)
	for _, inf := range attempts {
		ok := e.session.IsNonceFinished(inf.nonce)
		if !inf.probe {
			anySucceeded = anySucceeded || ok
		}
		logInferenceStage(ctx, inf.escrowID, inf.nonce, "race_completed",
			"host", inf.hostID,
			"winner", inf.nonce == winnerNonce,
			"finished", ok,
			"responsive", inf.resp != nil && inf.resp.ConfirmedAt > 0,
			"output_chunks", inf.outputChunks.Load(),
			"probe", inf.probe,
		)
		if !ok {
			failed = append(failed, inf)
		}
	}
	if !anySucceeded {
		for _, inf := range failed {
			if inf.probe {
				logInferenceStage(ctx, inf.escrowID, inf.nonce, "poc_probe_failed_no_timeout", "host", inf.hostID, "poc_reason", currentPoCPhaseReason())
				continue
			}
			payload := &host.InferencePayload{
				Prompt:      params.Prompt,
				Model:       params.Model,
				InputLength: params.InputLength,
				MaxTokens:   params.MaxTokens,
				StartedAt:   params.StartedAt,
			}
			result, err := e.session.HandleTimeout(ctx, inf.nonce, inf.sendTime, payload)
			if result.Reason != "" && e.metrics != nil {
				e.metrics.RecordInferenceTimeout(result.Reason)
			}
			if err != nil {
				logInferenceStage(ctx, inf.escrowID, inf.nonce, "timeout_failed", "host", inf.hostID, "error", err)
			}
		}
		logRequestStage(ctx, "request_failed", "escrow", e.subnetID, "error", "inference: no non-probe attempt finished")
		e.logRequestSettled(ctx, 0, decision, "failed")
		e.checkEscrowMissing(ctx, attempts)
		return fmt.Errorf("inference: no non-probe attempt finished")
	}

	var involvement []HostInvolvement
	for _, inf := range attempts {
		if inf.probe {
			continue
		}
		e.recordSampleOnce(inf, params)
		involvement = append(involvement, e.buildInvolvement(inf, winnerNonce))
	}
	e.perf.RecordRequest(RequestRecord{
		Timestamp:     time.Now(),
		InputTokens:   params.InputLength,
		WinnerHostIdx: winnerIdx,
		WinnerNonce:   winnerNonce,
		Decision:      decision.Reason,
		Hosts:         involvement,
	})
	if len(failed) > 0 {
		payload := &host.InferencePayload{
			Prompt:      params.Prompt,
			Model:       params.Model,
			InputLength: params.InputLength,
			MaxTokens:   params.MaxTokens,
			StartedAt:   params.StartedAt,
		}
		if anySucceeded {
			go func() {
				bgCtx, _ := ensureRequestLogContext(context.Background())
				bgCtx = logging.PropagateRequestID(bgCtx, ctx)
				for _, inf := range failed {
					if inf.probe {
						logInferenceStage(bgCtx, inf.escrowID, inf.nonce, "poc_probe_failed_no_timeout", "host", inf.hostID, "poc_reason", currentPoCPhaseReason())
						continue
					}
					result, err := e.session.HandleTimeout(bgCtx, inf.nonce, inf.sendTime, payload)
					if result.Reason != "" && e.metrics != nil {
						e.metrics.RecordInferenceTimeout(result.Reason)
					}
					if err != nil {
						logInferenceStage(bgCtx, inf.escrowID, inf.nonce, "background_timeout_failed", "host", inf.hostID, "error", err)
					}
				}
				e.logRequestSettled(bgCtx, winnerNonce, decision, "success")
			}()
		}
	}

	logRequestStage(ctx, "request_succeeded", "escrow", e.subnetID, "winner_nonce", winnerNonce, "decision", decision.Reason)
	if len(failed) == 0 {
		e.logRequestSettled(ctx, winnerNonce, decision, "success")
	}

	e.checkEscrowMissing(ctx, attempts)

	return nil
}

func (e *Redundancy) maxAttempts() int {
	if e.groupSize <= 0 {
		return 1
	}
	maxSpeculativeAttempts := CurrentMaxSpeculativeAttempts()
	if maxSpeculativeAttempts <= 0 || maxSpeculativeAttempts > e.groupSize {
		return e.groupSize
	}
	return maxSpeculativeAttempts
}

func (e *Redundancy) resolvedWinnerNonce(attempts []*inflight, winnerNonce uint64) uint64 {
	if winnerNonce != 0 {
		return winnerNonce
	}
	for _, inf := range attempts {
		if inf.probe {
			continue
		}
		if e.session.IsNonceFinished(inf.nonce) {
			return inf.nonce
		}
	}
	return 0
}

func stopTimer(t *time.Timer) {
	if t == nil {
		return
	}
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
}

func (e *Redundancy) logRequestSettled(ctx context.Context, winnerNonce uint64, decision Decision, outcome string) {
	logRequestStage(ctx, "request_fully_settled",
		"escrow", e.subnetID,
		"winner_nonce", winnerNonce,
		"decision", decision.Reason,
		"outcome", outcome,
	)
}

func (e *Redundancy) buildInvolvement(inf *inflight, winnerNonce uint64) HostInvolvement {
	hi := HostInvolvement{
		HostIdx:      inf.hostIdx,
		Nonce:        inf.nonce,
		OutputChunks: inf.outputChunks.Load(),
		Responsive:   inf.resp != nil && inf.resp.ConfirmedAt > 0,
		Finished:     e.session.IsNonceFinished(inf.nonce),
		Winner:       inf.nonce == winnerNonce,
	}
	if !inf.sendTime.IsZero() {
		if !inf.receiptTime.IsZero() {
			hi.ReceiptTimeMs = float64(inf.receiptTime.Sub(inf.sendTime).Milliseconds())
		}
		if !inf.firstToken.IsZero() {
			hi.FirstTokenMs = float64(inf.firstToken.Sub(inf.sendTime).Milliseconds())
		}
		hi.TotalTimeMs = float64(time.Since(inf.sendTime).Milliseconds())
	}
	return hi
}

func (e *Redundancy) recordSample(inf *inflight, params user.InferenceParams) {
	if inf.probe {
		return
	}
	responsive := inf.resp != nil && inf.resp.ConfirmedAt > 0
	sample := RequestSample{
		HostIdx:     inf.hostIdx,
		Responsive:  responsive,
		SendTime:    inf.sendTime,
		ReceiptTime: inf.receiptTime,
		FirstToken:  inf.firstToken,
		InputTokens: params.InputLength,
	}
	if !inf.sendTime.IsZero() {
		sample.TotalTime = time.Since(inf.sendTime)
	}
	e.perf.Record(sample)
	if e.metrics != nil {
		e.metrics.ObserveRequestSample(e.subnetID, sample)
	}
}

func (e *Redundancy) nextHostIdx() int {
	if e == nil || e.groupSize <= 0 {
		return 0
	}
	nextNonce := e.session.Nonce() + 1
	return int(nextNonce % uint64(e.groupSize))
}

func (e *Redundancy) shouldUseProbeForHost(hostIdx int) bool {
	if e == nil || e.session == nil {
		return false
	}
	if !relaxedPoCBypassActive() {
		return false
	}
	if !poCPreservedParticipantsLoaded() {
		return false
	}
	return !isPoCPreservedParticipant(e.session.HostParticipantKey(hostIdx))
}

func probeParams(params user.InferenceParams) user.InferenceParams {
	params.Prompt = pocProbePromptBody
	params.InputLength = uint64(len(pocProbePromptBody))
	params.MaxTokens = pocProbeMaxTokens
	return params
}

// checkEscrowMissing fires onEscrowMissing if any attempt got "escrow not found"
// from its host. The callback is expected to trigger a verified chain check.
func (e *Redundancy) checkEscrowMissing(ctx context.Context, attempts []*inflight) {
	if e.onEscrowMissing == nil {
		return
	}
	for _, inf := range attempts {
		if inf.err != nil && transport.IsUpstreamEscrowNotFound(inf.err) {
			logRequestStage(ctx, "escrow_not_found_reported_by_host",
				"escrow", e.subnetID, "host", inf.hostID, "nonce", inf.nonce)
			e.onEscrowMissing()
			return
		}
	}
}
