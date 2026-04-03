package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"subnet/host"
	"subnet/state"
	"subnet/types"
	"subnet/user"
)

var timeoutBuffer = 5 * time.Second

// Tuning knobs — exported so they can be adjusted without code changes.
var (
	ReceiptTimeout             = 500 * time.Millisecond
	ParallelAdvantageThreshold = 0.5 // 50% better estimated time
	UnresponsiveThreshold      = 1.0 // any non-responsive history → start secondary
	MinSamplesForDecision      = 3
)

// Decision describes whether and when to start a parallel secondary inference.
type Decision struct {
	RunSecondary bool
	Delay        time.Duration // 0 = immediate
	Reason       string
}

// SpeculativeEngine runs inferences with optional parallel speculation.
type SpeculativeEngine struct {
	session   *user.Session
	sm        *state.StateMachine
	perf      *PerfTracker
	registry  *streamRegistry
	groupSize int
}

func NewSpeculativeEngine(session *user.Session, sm *state.StateMachine, perf *PerfTracker, registry *streamRegistry, groupSize int) *SpeculativeEngine {
	return &SpeculativeEngine{
		session:   session,
		sm:        sm,
		perf:      perf,
		registry:  registry,
		groupSize: groupSize,
	}
}

func (e *SpeculativeEngine) Decide(primaryHostIdx int, inputTokens uint64) Decision {
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
	prepared *user.PreparedInference
	hostIdx  int
	nonce    uint64
	sendTime time.Time

	receiptOnce sync.Once
	receiptTime time.Time
	receiptCh   chan struct{} // closed when receipt arrives

	tokenOnce    sync.Once
	firstToken   time.Time
	outputChunks atomic.Int64

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
}

func newRaceGroup(w io.Writer) *raceGroup {
	return &raceGroup{w: w}
}

func (rg *raceGroup) setWinner(nonce uint64) {
	rg.mu.Lock()
	defer rg.mu.Unlock()
	if rg.winner == 0 {
		rg.winner = nonce
		rg.decided.Store(true)
		log.Printf("speculative: nonce %d won the race", nonce)
	}
}

func (rg *raceGroup) hasDecided() bool {
	return rg.decided.Load()
}

// raceWriter is an io.Writer that only forwards writes from the winning nonce.
type raceWriter struct {
	group *raceGroup
	nonce uint64
	inf   *inflight
}

func (rw *raceWriter) Write(p []byte) (int, error) {
	rw.inf.tokenOnce.Do(func() {
		rw.inf.firstToken = time.Now()
	})
	rw.inf.outputChunks.Add(1)
	rw.group.setWinner(rw.nonce)

	rw.group.mu.Lock()
	isWinner := rw.group.winner == rw.nonce
	rw.group.mu.Unlock()

	if isWinner && rw.group.w != nil {
		return rw.group.w.Write(p)
	}
	return len(p), nil
}

func (rw *raceWriter) Flush() {
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
func (e *SpeculativeEngine) RunInference(ctx context.Context, params user.InferenceParams, w io.Writer) error {
	primary, err := e.prepareInflight(params)
	if err != nil {
		return err
	}

	decision := e.Decide(primary.hostIdx, params.InputLength)
	race := newRaceGroup(w)

	// Always start the primary.
	e.startInflight(ctx, primary, race)

	if decision.RunSecondary {
		if decision.Delay == 0 {
			// Immediate parallel start.
			log.Printf("speculative: immediate secondary (%s)", decision.Reason)
			secondary, err := e.prepareInflight(params)
			if err != nil {
				log.Printf("speculative: failed to prepare secondary: %v", err)
			} else {
				e.startInflight(ctx, secondary, race)
				return e.awaitBoth(ctx, primary, secondary, race, params, decision)
			}
		} else {
			// Delayed: wait for receipt, start secondary if none arrives.
			secondary := e.startDelayed(ctx, primary, race, params, decision.Delay)
			if secondary != nil {
				return e.awaitBoth(ctx, primary, secondary, race, params, decision)
			}
		}
	}

	// Single-inference path (no secondary, or secondary prep failed).
	return e.awaitSingle(ctx, primary, race, params, decision)
}

func (e *SpeculativeEngine) prepareInflight(params user.InferenceParams) (*inflight, error) {
	prepared, err := e.session.PrepareInference(params)
	if err != nil {
		return nil, fmt.Errorf("prepare: %w", err)
	}
	return &inflight{
		prepared:  prepared,
		hostIdx:   prepared.HostIdx(),
		nonce:     prepared.Nonce(),
		done:      make(chan struct{}),
		receiptCh: make(chan struct{}),
	}, nil
}

func (e *SpeculativeEngine) startInflight(ctx context.Context, inf *inflight, race *raceGroup) {
	rw := &raceWriter{group: race, nonce: inf.nonce, inf: inf}
	e.registry.register(inf.nonce, rw)
	e.registry.registerReceiptHandler(inf.nonce, func() {
		inf.receiptOnce.Do(func() {
			inf.receiptTime = time.Now()
			close(inf.receiptCh)
		})
	})

	go func() {
		defer close(inf.done)
		inf.sendTime = time.Now()
		inf.resp, inf.err = e.session.SendOnly(ctx, inf.prepared)
		e.registry.unregister(inf.nonce)
	}()
}

// startDelayed waits for receipt or timeout, then starts a secondary if needed.
// Returns nil if receipt arrived before timeout (no secondary needed).
func (e *SpeculativeEngine) startDelayed(ctx context.Context, primary *inflight, race *raceGroup, params user.InferenceParams, delay time.Duration) *inflight {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-timer.C:
		if race.hasDecided() {
			return nil // primary already producing tokens
		}
		log.Printf("speculative: no receipt in %v, starting secondary", delay)
		secondary, err := e.prepareInflight(params)
		if err != nil {
			log.Printf("speculative: failed to prepare secondary: %v", err)
			return nil
		}
		e.startInflight(ctx, secondary, race)
		return secondary

	case <-primary.receiptCh:
		return nil // receipt arrived, primary is responsive

	case <-primary.done:
		return nil

	case <-ctx.Done():
		return nil
	}
}

func (e *SpeculativeEngine) awaitSingle(ctx context.Context, inf *inflight, race *raceGroup, params user.InferenceParams, decision Decision) error {
	<-inf.done
	e.recordSample(inf, params)

	e.perf.RecordRequest(RequestRecord{
		Timestamp:     time.Now(),
		InputTokens:   params.InputLength,
		WinnerHostIdx: inf.hostIdx,
		WinnerNonce:   inf.nonce,
		Decision:      decision.Reason,
		Hosts:         []HostInvolvement{e.buildInvolvement(inf, inf.nonce)},
	})

	if inf.err != nil && inf.resp == nil {
		return e.handleInferenceTimeout(ctx, inf, params)
	}
	if err := e.session.ProcessResponse(inf.hostIdx, inf.resp, inf.nonce); err != nil {
		return fmt.Errorf("process response: %w", err)
	}
	if inf.err == nil && hasMsgFinish(inf.resp.Mempool, inf.nonce) {
		return nil
	}
	return e.handleInferenceTimeout(ctx, inf, params)
}

func (e *SpeculativeEngine) awaitBoth(ctx context.Context, primary, secondary *inflight, race *raceGroup, params user.InferenceParams, decision Decision) error {
	<-primary.done
	<-secondary.done

	e.recordSample(primary, params)
	e.recordSample(secondary, params)

	winnerNonce := race.winner
	winnerIdx := primary.hostIdx
	if winnerNonce == secondary.nonce {
		winnerIdx = secondary.hostIdx
	}
	e.perf.RecordRequest(RequestRecord{
		Timestamp:     time.Now(),
		InputTokens:   params.InputLength,
		WinnerHostIdx: winnerIdx,
		WinnerNonce:   winnerNonce,
		Decision:      decision.Reason,
		Hosts: []HostInvolvement{
			e.buildInvolvement(primary, winnerNonce),
			e.buildInvolvement(secondary, winnerNonce),
		},
	})

	// Process both responses for state consistency.
	for _, inf := range []*inflight{primary, secondary} {
		if inf.resp != nil || inf.err == nil {
			if err := e.session.ProcessResponse(inf.hostIdx, inf.resp, inf.nonce); err != nil {
				log.Printf("speculative: process nonce %d: %v", inf.nonce, err)
			}
		}
	}

	// Classify each inference as finished or failed.
	finished := func(inf *inflight) bool {
		return inf.err == nil && inf.resp != nil && hasMsgFinish(inf.resp.Mempool, inf.nonce)
	}

	primaryOK := finished(primary)
	secondaryOK := finished(secondary)

	// Handle timeouts for any failed inferences. Use a detached context
	// so timeout vote collection can outlive the HTTP request.
	var failed []*inflight
	if !primaryOK {
		failed = append(failed, primary)
	}
	if !secondaryOK {
		failed = append(failed, secondary)
	}
	if len(failed) > 0 {
		if primaryOK || secondaryOK {
			// One succeeded — handle the failed one(s) in background so the
			// user gets their response immediately.
			go func() {
				bgCtx := context.Background()
				for _, inf := range failed {
					if err := e.handleInferenceTimeout(bgCtx, inf, params); err != nil {
						log.Printf("speculative: background timeout nonce %d: %v", inf.nonce, err)
					}
				}
			}()
		} else {
			// Both failed — block and handle timeouts before returning error.
			for _, inf := range failed {
				if err := e.handleInferenceTimeout(ctx, inf, params); err != nil {
					log.Printf("speculative: timeout nonce %d: %v", inf.nonce, err)
				}
			}
			return fmt.Errorf("inference: neither host finished")
		}
	}

	return nil
}

// handleInferenceTimeout waits for the appropriate deadline then collects
// timeout votes and submits MsgTimeoutInference for a failed inference.
func (e *SpeculativeEngine) handleInferenceTimeout(ctx context.Context, inf *inflight, params user.InferenceParams) error {
	cfg := e.sm.SnapshotState().Config

	var reason types.TimeoutReason
	if inf.resp != nil && inf.resp.ConfirmedAt > 0 {
		deadline := time.Unix(inf.resp.ConfirmedAt, 0).Add(
			time.Duration(cfg.ExecutionTimeout)*time.Second + timeoutBuffer)
		if !sleepUntil(ctx, deadline) {
			return ctx.Err()
		}
		reason = types.TimeoutReason_TIMEOUT_REASON_EXECUTION
	} else {
		deadline := inf.sendTime.Add(
			time.Duration(cfg.RefusalTimeout)*time.Second + timeoutBuffer)
		if !sleepUntil(ctx, deadline) {
			return ctx.Err()
		}
		reason = types.TimeoutReason_TIMEOUT_REASON_REFUSED
	}

	payload := &host.InferencePayload{
		Prompt:      params.Prompt,
		Model:       params.Model,
		InputLength: params.InputLength,
		MaxTokens:   params.MaxTokens,
		StartedAt:   params.StartedAt,
	}

	verifiers := e.session.TimeoutVerifiers()
	storedDiffs := e.session.Diffs()

	votes, err := e.session.CollectTimeoutVotes(ctx, inf.nonce, reason, payload, verifiers, storedDiffs)
	if err != nil {
		return fmt.Errorf("collect timeout votes: %w", err)
	}

	if e.session.HasSufficientTimeoutVotes(votes) {
		e.session.AddPendingTimeoutTx(inf.nonce, reason, votes)
		if err := e.session.SendPendingDiff(ctx); err != nil {
			return fmt.Errorf("send timeout diff: %w", err)
		}
		return fmt.Errorf("inference %d timed out: %s", inf.nonce, reason)
	}

	log.Printf("inference %d: insufficient timeout votes", inf.nonce)
	return fmt.Errorf("inference %d timed out but insufficient votes", inf.nonce)
}

func sleepUntil(ctx context.Context, deadline time.Time) bool {
	d := time.Until(deadline)
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (e *SpeculativeEngine) buildInvolvement(inf *inflight, winnerNonce uint64) HostInvolvement {
	hi := HostInvolvement{
		HostIdx:      inf.hostIdx,
		Nonce:        inf.nonce,
		OutputChunks: inf.outputChunks.Load(),
		Responsive:   inf.resp != nil && inf.resp.ConfirmedAt > 0,
		Finished:     inf.err == nil && inf.resp != nil && hasMsgFinish(inf.resp.Mempool, inf.nonce),
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

func (e *SpeculativeEngine) recordSample(inf *inflight, params user.InferenceParams) {
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
}
