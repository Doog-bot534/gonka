package propagation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

type HTTPTransport struct {
	client           *http.Client
	participantAddrs map[string]string
	mu               sync.RWMutex
	receivers        map[string]ReceiverHandler
	localAddr        string
}

func NewHTTPTransport(localAddr string, timeout time.Duration) *HTTPTransport {
	return &HTTPTransport{
		client: &http.Client{
			Timeout: timeout,
		},
		participantAddrs: make(map[string]string),
		receivers:        make(map[string]ReceiverHandler),
		localAddr:        localAddr,
	}
}

func (t *HTTPTransport) SetParticipantURLs(urls map[string]string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.participantAddrs = urls
}

func (t *HTTPTransport) RegisterReceiver(addr string, handler ReceiverHandler) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.receivers[addr] = handler
}

func (t *HTTPTransport) SendHeader(treeIdx int, to string, h BundleHeader) error {
	if to == t.localAddr {
		return t.handleLocalHeader(h, treeIdx, t.localAddr)
	}

	t.mu.RLock()
	url := t.participantAddrs[to]
	t.mu.RUnlock()

	if url == "" {
		return fmt.Errorf("no URL for participant %s", to)
	}

	payload := &HeaderMessage{
		TreeIdx: treeIdx,
		Header:  h,
		From:    t.localAddr,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal header: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", url+"/v1/propagation/header", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http status %d", resp.StatusCode)
	}

	return nil
}

func (t *HTTPTransport) handleLocalHeader(h BundleHeader, treeIdx int, from string) error {
	t.mu.RLock()
	handler := t.receivers[t.localAddr]
	t.mu.RUnlock()

	if handler == nil {
		return fmt.Errorf("no local receiver registered")
	}

	return handler.OnHeader(h, treeIdx, from)
}

func (t *HTTPTransport) HandleHeaderHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var msg HeaderMessage
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		logging.Warn("HTTPTransport: failed to decode header", types.PoC, "error", err)
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	t.mu.RLock()
	handler := t.receivers[t.localAddr]
	t.mu.RUnlock()

	if handler == nil {
		http.Error(w, "No receiver registered", http.StatusServiceUnavailable)
		return
	}

	// Use participant address as 'from' if not specified
	from := msg.From
	if from == "" {
		from = msg.Header.Participant
	}

	if err := handler.OnHeader(msg.Header, msg.TreeIdx, from); err != nil {
		logging.Warn("HTTPTransport: header handler failed", types.PoC, "error", err)
		http.Error(w, "Handler error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (t *HTTPTransport) SendProofs(to string, bundleID [32]byte, proofs []ProofItem) error {
	if to == t.localAddr {
		return t.handleLocalProofs(bundleID, proofs, t.localAddr)
	}

	t.mu.RLock()
	url := t.participantAddrs[to]
	t.mu.RUnlock()

	if url == "" {
		return fmt.Errorf("no URL for participant %s", to)
	}

	payload := &ProofsMessage{
		BundleID: bundleID,
		Proofs:   proofs,
		From:     t.localAddr,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal proofs: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", url+"/v1/propagation/proofs", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http status %d", resp.StatusCode)
	}

	return nil
}

func (t *HTTPTransport) handleLocalProofs(bundleID [32]byte, proofs []ProofItem, from string) error {
	t.mu.RLock()
	handler := t.receivers[t.localAddr]
	t.mu.RUnlock()

	if handler == nil {
		return fmt.Errorf("no local receiver registered")
	}

	return handler.OnProofs(bundleID, proofs, from)
}

func (t *HTTPTransport) HandleProofsHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var msg ProofsMessage
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		logging.Warn("HTTPTransport: failed to decode proofs", types.PoC, "error", err)
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	t.mu.RLock()
	handler := t.receivers[t.localAddr]
	t.mu.RUnlock()

	if handler == nil {
		http.Error(w, "No receiver registered", http.StatusServiceUnavailable)
		return
	}

	from := msg.From
	if from == "" {
		from = "unknown"
	}

	if err := handler.OnProofs(msg.BundleID, msg.Proofs, from); err != nil {
		logging.Warn("HTTPTransport: proofs handler failed", types.PoC, "error", err)
		http.Error(w, "Handler error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

type HeaderMessage struct {
	TreeIdx int          `json:"tree_idx"`
	Header  BundleHeader `json:"header"`
	From    string       `json:"from,omitempty"`
}

type ProofsMessage struct {
	BundleID [32]byte    `json:"bundle_id"`
	Proofs   []ProofItem `json:"proofs"`
	From     string      `json:"from,omitempty"`
}
