package main

import (
	"log"
	"math"
	"sync"
	"time"
)

const perfWindowSize = 128

type RequestSample struct {
	HostIdx     int
	Responsive  bool
	SendTime    time.Time
	ReceiptTime time.Time // zero if no receipt
	FirstToken  time.Time // zero if no tokens
	TotalTime   time.Duration
	InputTokens uint64
}

func (s RequestSample) ReceiptMs() float64 {
	if s.ReceiptTime.IsZero() || s.SendTime.IsZero() {
		return 0
	}
	return float64(s.ReceiptTime.Sub(s.SendTime).Milliseconds())
}

// CTTFL = (firstTokenTime - receiptTime) / inputTokens
func (s RequestSample) CTTFL() float64 {
	if s.FirstToken.IsZero() || s.ReceiptTime.IsZero() || s.InputTokens == 0 {
		return 0
	}
	gap := s.FirstToken.Sub(s.ReceiptTime)
	if gap <= 0 {
		return 0
	}
	return float64(gap.Milliseconds()) / float64(s.InputTokens)
}

type hostRing struct {
	samples [perfWindowSize]RequestSample
	pos     int
	count   int
}

func (r *hostRing) add(s RequestSample) {
	r.samples[r.pos] = s
	r.pos = (r.pos + 1) % perfWindowSize
	if r.count < perfWindowSize {
		r.count++
	}
}

type HostPerfStats struct {
	HostIdx          int     `json:"host_idx"`
	TotalSamples     int     `json:"total_samples"`
	ResponsiveRate   float64 `json:"responsive_rate"`
	AvgReceiptTimeMs float64 `json:"avg_receipt_time_ms"`
	AvgCTTFL         float64 `json:"avg_cttfl"`
	AvgTotalTimeMs   float64 `json:"avg_total_time_ms"`
}

func (r *hostRing) stats(hostIdx int) HostPerfStats {
	if r.count == 0 {
		return HostPerfStats{HostIdx: hostIdx}
	}

	var responsive int
	var receiptSum, cttflSum, totalSum float64
	var receiptN, cttflN, totalN int

	for i := 0; i < r.count; i++ {
		s := r.samples[i]
		if s.Responsive {
			responsive++
		}
		if rm := s.ReceiptMs(); rm > 0 {
			receiptSum += rm
			receiptN++
		}
		if c := s.CTTFL(); c > 0 && !math.IsNaN(c) && !math.IsInf(c, 0) {
			cttflSum += c
			cttflN++
		}
		if s.TotalTime > 0 {
			totalSum += float64(s.TotalTime.Milliseconds())
			totalN++
		}
	}

	st := HostPerfStats{
		HostIdx:        hostIdx,
		TotalSamples:   r.count,
		ResponsiveRate: float64(responsive) / float64(r.count),
	}
	if receiptN > 0 {
		st.AvgReceiptTimeMs = receiptSum / float64(receiptN)
	}
	if cttflN > 0 {
		st.AvgCTTFL = cttflSum / float64(cttflN)
	}
	if totalN > 0 {
		st.AvgTotalTimeMs = totalSum / float64(totalN)
	}
	return st
}

// HostInvolvement describes one host's participation in a user request.
type HostInvolvement struct {
	HostIdx       int     `json:"host_idx"`
	Nonce         uint64  `json:"nonce"`
	OutputChunks  int64   `json:"output_chunks"`
	ReceiptTimeMs float64 `json:"receipt_time_ms"`
	FirstTokenMs  float64 `json:"first_token_ms"`
	TotalTimeMs   float64 `json:"total_time_ms"`
	Responsive    bool    `json:"responsive"`
	Finished      bool    `json:"finished"`
	Winner        bool    `json:"winner"`
}

// RequestRecord logs a single user-facing inference request.
type RequestRecord struct {
	Timestamp     time.Time         `json:"timestamp"`
	InputTokens   uint64            `json:"input_tokens"`
	WinnerHostIdx int               `json:"winner_host_idx"`
	WinnerNonce   uint64            `json:"winner_nonce"`
	Decision      string            `json:"decision"`
	Hosts         []HostInvolvement `json:"hosts"`
}

const requestLogSize = 256

type requestRing struct {
	records [requestLogSize]RequestRecord
	pos     int
	count   int
}

func (r *requestRing) add(rec RequestRecord) {
	r.records[r.pos] = rec
	r.pos = (r.pos + 1) % requestLogSize
	if r.count < requestLogSize {
		r.count++
	}
}

func (r *requestRing) all() []RequestRecord {
	if r.count == 0 {
		return nil
	}
	result := make([]RequestRecord, r.count)
	for i := 0; i < r.count; i++ {
		idx := (r.pos - r.count + i + requestLogSize) % requestLogSize
		result[i] = r.records[idx]
	}
	return result
}

type PerfTracker struct {
	mu       sync.RWMutex
	hosts    map[int]*hostRing
	requests requestRing
	store    *PerfStore
}

func NewPerfTracker(store *PerfStore) *PerfTracker {
	pt := &PerfTracker{hosts: make(map[int]*hostRing), store: store}
	if store != nil {
		pt.loadFromStore()
	}
	return pt
}

func (t *PerfTracker) loadFromStore() {
	samples, err := t.store.LoadSamples()
	if err != nil {
		log.Printf("perf: failed to load samples: %v", err)
		return
	}
	for _, s := range samples {
		ring, ok := t.hosts[s.HostIdx]
		if !ok {
			ring = &hostRing{}
			t.hosts[s.HostIdx] = ring
		}
		ring.add(s)
	}

	records, err := t.store.LoadRequests()
	if err != nil {
		log.Printf("perf: failed to load requests: %v", err)
		return
	}
	for _, r := range records {
		t.requests.add(r)
	}

	if len(samples) > 0 || len(records) > 0 {
		log.Printf("perf: restored %d host samples, %d request records from disk", len(samples), len(records))
	}
}

func (t *PerfTracker) Record(s RequestSample) {
	t.mu.Lock()
	ring, ok := t.hosts[s.HostIdx]
	if !ok {
		ring = &hostRing{}
		t.hosts[s.HostIdx] = ring
	}
	ring.add(s)
	t.mu.Unlock()

	if t.store != nil {
		if err := t.store.InsertSample(s); err != nil {
			log.Printf("perf: persist sample: %v", err)
		}
	}
}

func (t *PerfTracker) Stats(hostIdx int) HostPerfStats {
	t.mu.RLock()
	defer t.mu.RUnlock()
	ring, ok := t.hosts[hostIdx]
	if !ok {
		return HostPerfStats{HostIdx: hostIdx}
	}
	return ring.stats(hostIdx)
}

func (t *PerfTracker) AllStats() []HostPerfStats {
	t.mu.RLock()
	defer t.mu.RUnlock()
	result := make([]HostPerfStats, 0, len(t.hosts))
	for idx, ring := range t.hosts {
		result = append(result, ring.stats(idx))
	}
	return result
}

// EstimatedTimeMs returns estimated total time for an inference.
// Uses: receiptTime + cTTFL * inputTokens.
// Returns 0 if insufficient data.
func (t *PerfTracker) EstimatedTimeMs(hostIdx int, inputTokens uint64) float64 {
	st := t.Stats(hostIdx)
	if st.TotalSamples == 0 || st.AvgReceiptTimeMs == 0 {
		return 0
	}
	return st.AvgReceiptTimeMs + st.AvgCTTFL*float64(inputTokens)
}

func (t *PerfTracker) RecordRequest(rec RequestRecord) {
	t.mu.Lock()
	t.requests.add(rec)
	t.mu.Unlock()

	if t.store != nil {
		if err := t.store.InsertRequest(rec); err != nil {
			log.Printf("perf: persist request: %v", err)
		}
	}
}

func (t *PerfTracker) RecentRequests() []RequestRecord {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.requests.all()
}

func (t *PerfTracker) IsUnresponsive(hostIdx int) bool {
	st := t.Stats(hostIdx)
	if st.TotalSamples == 0 {
		return false
	}
	return st.ResponsiveRate < UnresponsiveThreshold
}
