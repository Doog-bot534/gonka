package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"subnet/state"
	"subnet/types"
	"subnet/user"
)

// streamRegistry routes SSE lines to per-request writers by nonce.
type streamRegistry struct {
	mu              sync.RWMutex
	writers         map[uint64]io.Writer
	receiptHandlers map[uint64]func()
}

func newStreamRegistry() *streamRegistry {
	return &streamRegistry{
		writers:         make(map[uint64]io.Writer),
		receiptHandlers: make(map[uint64]func()),
	}
}

func (r *streamRegistry) register(nonce uint64, w io.Writer) {
	r.mu.Lock()
	r.writers[nonce] = w
	r.mu.Unlock()
}

func (r *streamRegistry) registerReceiptHandler(nonce uint64, fn func()) {
	r.mu.Lock()
	r.receiptHandlers[nonce] = fn
	r.mu.Unlock()
}

func (r *streamRegistry) unregister(nonce uint64) {
	r.mu.Lock()
	delete(r.writers, nonce)
	delete(r.receiptHandlers, nonce)
	r.mu.Unlock()
}

func (r *streamRegistry) recordReceipt(nonce uint64) {
	r.mu.RLock()
	fn := r.receiptHandlers[nonce]
	r.mu.RUnlock()
	if fn != nil {
		fn()
	}
}

func (r *streamRegistry) callback(nonce uint64, line string) {
	r.mu.RLock()
	w := r.writers[nonce]
	r.mu.RUnlock()
	if w != nil {
		fmt.Fprintf(w, "%s\n\n", line)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

// writeStreamReset writes a stream_reset SSE event to signal the client
// that the connection was lost and the response will be replayed from scratch.
func writeStreamReset(w io.Writer) {
	fmt.Fprintf(w, "data: {\"subnet_stream_reset\":true}\n\n")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// Proxy is the OpenAI-compatible HTTP proxy backed by a subnet session.
type Proxy struct {
	session  *user.Session
	sm       *state.StateMachine
	escrowID string
	model    string
	registry *streamRegistry
	engine   *SpeculativeEngine
	perf     *PerfTracker
}

type chatRequest struct {
	Model     string `json:"model"`
	Stream    bool   `json:"stream"`
	MaxTokens uint64 `json:"max_tokens"`
}

// normalizeContent converts multi-part content arrays to simple strings.
// [{"type":"text","text":"A"},{"type":"text","text":"B"}] → "A\nB"
func normalizeContent(body []byte) []byte {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return body
	}
	msgsRaw, ok := raw["messages"]
	if !ok {
		return body
	}

	var msgs []map[string]json.RawMessage
	if err := json.Unmarshal(msgsRaw, &msgs); err != nil {
		return body
	}

	changed := false
	for i, msg := range msgs {
		contentRaw, ok := msg["content"]
		if !ok {
			continue
		}
		var parts []map[string]string
		if err := json.Unmarshal(contentRaw, &parts); err != nil {
			continue
		}
		var texts []string
		for _, p := range parts {
			if p["type"] == "text" && p["text"] != "" {
				texts = append(texts, p["text"])
			}
		}
		if len(texts) > 0 {
			combined, _ := json.Marshal(strings.Join(texts, "\n"))
			msgs[i]["content"] = combined
			changed = true
		}
	}

	if !changed {
		return body
	}

	newMsgs, err := json.Marshal(msgs)
	if err != nil {
		return body
	}
	raw["messages"] = newMsgs
	out, err := json.Marshal(raw)
	if err != nil {
		return body
	}
	return out
}

func (p *Proxy) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	ctx, _ := ensureRequestLogContext(r.Context())
	r = r.WithContext(ctx)
	if r.Method != http.MethodPost {
		logRequestStage(ctx, "proxy_method_not_allowed", "method", r.Method)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		logRequestStage(ctx, "proxy_read_body_failed", "error", err)
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	body = normalizeContent(body)

	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		logRequestStage(ctx, "proxy_parse_failed", "error", err)
		http.Error(w, "parse request: "+err.Error(), http.StatusBadRequest)
		return
	}

	model := req.Model
	if model == "" {
		model = p.model
	}
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = DefaultRequestMaxTokens
	}
	if DefaultRequestMaxTokens > 0 && maxTokens > DefaultRequestMaxTokens {
		maxTokens = DefaultRequestMaxTokens
	}

	params := user.InferenceParams{
		Model:       model,
		Prompt:      body,
		InputLength: uint64(len(body)),
		MaxTokens:   maxTokens,
		StartedAt:   time.Now().Unix(),
		Stream:      req.Stream,
	}
	logRequestStage(ctx, "proxy_request_started", "escrow", p.escrowID, "model", model, "stream", req.Stream, "input_tokens", params.InputLength)

	if req.Stream {
		p.handleStreaming(w, r, params)
	} else {
		p.handleNonStreaming(w, r, params)
	}
}

// hasMsgFinish returns true if mempool contains MsgFinishInference for the given nonce.
func hasMsgFinish(txs []*types.SubnetTx, nonce uint64) bool {
	for _, tx := range txs {
		if fi := tx.GetFinishInference(); fi != nil && fi.InferenceId == nonce {
			return true
		}
	}
	return false
}

// deferredWriter delays WriteHeader(200) until the first Write call.
// If runInference errors before any streaming data arrives, the proxy
// can still return a proper HTTP error status.
type deferredWriter struct {
	w       http.ResponseWriter
	started bool
}

func (d *deferredWriter) Write(p []byte) (int, error) {
	if !d.started {
		d.w.Header().Set("Content-Type", "text/event-stream")
		d.w.Header().Set("Cache-Control", "no-cache")
		d.w.Header().Set("Connection", "keep-alive")
		d.w.WriteHeader(http.StatusOK)
		d.started = true
	}
	return d.w.Write(p)
}

func (d *deferredWriter) Flush() {
	if f, ok := d.w.(http.Flusher); ok {
		f.Flush()
	}
}

func (p *Proxy) handleStreaming(w http.ResponseWriter, r *http.Request, params user.InferenceParams) {
	dw := &deferredWriter{w: w}

	err := p.engine.RunInference(r.Context(), params, dw)
	if err != nil {
		logRequestStage(r.Context(), "proxy_stream_failed", "escrow", p.escrowID, "error", err)
		statusCode := gatewayStatusCodeForError(err)
		if !dw.started {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(statusCode)
			fmt.Fprintf(w, `{"error":{"message":%q}}`, err.Error())
			return
		}
		log.Printf("inference error (mid-stream): %v", err)
		fmt.Fprintf(dw, "data: {\"error\":{\"message\":%q}}\n\n", err.Error())
		dw.Flush()
		return
	}

	logRequestStage(r.Context(), "proxy_stream_completed", "escrow", p.escrowID)
	fmt.Fprint(dw, "data: [DONE]\n\n")
	dw.Flush()
}

func (p *Proxy) handleNonStreaming(w http.ResponseWriter, r *http.Request, params user.InferenceParams) {
	var buf bytes.Buffer

	err := p.engine.RunInference(r.Context(), params, &buf)
	if err != nil {
		logRequestStage(r.Context(), "proxy_request_failed", "escrow", p.escrowID, "error", err)
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), gatewayStatusCodeForError(err))
		return
	}

	assembled := assembleSSEChunks(buf.String())
	w.Header().Set("Content-Type", "application/json")
	w.Write(assembled)
	logRequestStage(r.Context(), "proxy_request_completed", "escrow", p.escrowID)
}

// assembleSSEChunks extracts the last data line from SSE output as the response.
func assembleSSEChunks(raw string) []byte {
	var lastData string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			continue
		}
		lastData = data
	}
	if lastData != "" {
		return []byte(lastData)
	}
	return []byte(`{"error":{"message":"no response data"}}`)
}

func (p *Proxy) handleFinalize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := p.session.Finalize(r.Context()); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusInternalServerError)
		return
	}

	st := p.sm.SnapshotState()
	finalNonce := p.session.Nonce()
	payload, err := state.BuildSettlement(p.escrowID, st, p.session.Signatures()[finalNonce], finalNonce)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusInternalServerError)
		return
	}

	data, err := marshalSettlement(payload)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

type statusResponse struct {
	EscrowID string `json:"escrow_id"`
	Nonce    uint64 `json:"nonce"`
	Phase    string `json:"phase"`
	Balance  uint64 `json:"balance"`
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func (p *Proxy) handleDebugPending(w http.ResponseWriter, r *http.Request) {
	pending := p.session.PendingTxs()
	warmKeys := p.sm.WarmKeys()

	type txInfo struct {
		Type string `json:"type"`
		ID   uint64 `json:"id,omitempty"`
	}
	var txs []txInfo
	for _, tx := range pending {
		switch inner := tx.GetTx().(type) {
		case *types.SubnetTx_ConfirmStart:
			txs = append(txs, txInfo{Type: "confirm_start", ID: inner.ConfirmStart.InferenceId})
		case *types.SubnetTx_FinishInference:
			txs = append(txs, txInfo{Type: "finish", ID: inner.FinishInference.InferenceId})
		case *types.SubnetTx_Validation:
			txs = append(txs, txInfo{Type: "validation", ID: inner.Validation.InferenceId})
		case *types.SubnetTx_ValidationVote:
			txs = append(txs, txInfo{Type: "vote", ID: inner.ValidationVote.InferenceId})
		case *types.SubnetTx_RevealSeed:
			txs = append(txs, txInfo{Type: "reveal_seed", ID: uint64(inner.RevealSeed.SlotId)})
		default:
			txs = append(txs, txInfo{Type: fmt.Sprintf("%T", tx.GetTx())})
		}
	}

	writeJSON(w, map[string]any{
		"nonce":     p.session.Nonce(),
		"pending":   txs,
		"warm_keys": warmKeys,
	})
}

func (p *Proxy) handleDebugPerf(w http.ResponseWriter, r *http.Request) {
	stats := p.perf.AllStats()
	requests := p.perf.RecentRequests()
	writeJSON(w, map[string]any{
		"hosts":                  stats,
		"requests":               requests,
		"receipt_timeout_ms":     ReceiptTimeout.Milliseconds(),
		"advantage_threshold":    ParallelAdvantageThreshold,
		"unresponsive_threshold": UnresponsiveThreshold,
		"host_window_size":       perfWindowSize,
		"request_log_size":       requestLogSize,
	})
}

func (p *Proxy) handleDebugState(w http.ResponseWriter, r *http.Request) {
	st := p.sm.SnapshotState()

	statusNames := map[types.InferenceStatus]string{
		types.StatusPending:     "pending",
		types.StatusStarted:     "started",
		types.StatusFinished:    "finished",
		types.StatusChallenged:  "challenged",
		types.StatusValidated:   "validated",
		types.StatusInvalidated: "invalidated",
		types.StatusTimedOut:    "timed_out",
	}

	counts := make(map[string]int)
	for _, rec := range st.Inferences {
		name := statusNames[rec.Status]
		if name == "" {
			name = fmt.Sprintf("unknown(%d)", rec.Status)
		}
		counts[name]++
	}

	writeJSON(w, map[string]any{
		"nonce":            st.LatestNonce,
		"balance":          st.Balance,
		"total_inferences": len(st.Inferences),
		"status_counts":    counts,
	})
}

func (p *Proxy) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	phase := p.sm.Phase()
	var phaseStr string
	switch phase {
	case 0:
		phaseStr = "active"
	case 1:
		phaseStr = "finalizing"
	case 2:
		phaseStr = "settlement"
	default:
		phaseStr = fmt.Sprintf("unknown(%d)", phase)
	}

	st := p.sm.SnapshotState()
	writeJSON(w, statusResponse{
		EscrowID: p.escrowID,
		Nonce:    p.session.Nonce(),
		Phase:    phaseStr,
		Balance:  st.Balance,
	})
}
