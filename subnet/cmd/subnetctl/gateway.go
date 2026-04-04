package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"subnet/bridge"
	"subnet/user"
)

type RuntimeConfig struct {
	ID            string `json:"id"`
	PrivateKeyHex string `json:"private_key,omitempty"`
	PrivateKeyEnv string `json:"private_key_env,omitempty"`
	Model         string `json:"model,omitempty"`
	StoragePath   string `json:"storage_path,omitempty"`
}

type Gateway struct {
	runtimes       map[string]*subnetRuntime
	runtimeOrder   []*subnetRuntime
	limiter        *GatewayLimiter
	defaultModel   string
	mu             sync.Mutex
	roundRobinSeed atomic.Uint64
}

type subnetRuntime struct {
	id        string
	model     string
	handler   http.Handler
	proxy     *Proxy
	session   *user.Session
	perfStore *PerfStore

	activeRequests atomic.Int64
	reservedTokens atomic.Int64
}

type runtimeStatus struct {
	ID               string `json:"id"`
	Model            string `json:"model"`
	Phase            string `json:"phase,omitempty"`
	Nonce            uint64 `json:"nonce,omitempty"`
	Balance          uint64 `json:"balance,omitempty"`
	ActiveRequests   int64  `json:"active_requests"`
	ReservedTokens   int64  `json:"reserved_tokens"`
}

var (
	DefaultRequestMaxTokens uint64 = 10_000
)

func newRuntimeMux(proxy *Proxy) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", proxy.handleChatCompletions)
	mux.HandleFunc("/v1/finalize", proxy.handleFinalize)
	mux.HandleFunc("/v1/status", proxy.handleStatus)
	mux.HandleFunc("/v1/debug/pending", proxy.handleDebugPending)
	mux.HandleFunc("/v1/debug/state", proxy.handleDebugState)
	mux.HandleFunc("/v1/debug/perf", proxy.handleDebugPerf)
	return mux
}

func buildRuntime(cfg RuntimeConfig, chainREST, defaultModel string) (*subnetRuntime, error) {
	keyHex := strings.TrimSpace(cfg.PrivateKeyHex)
	if keyHex == "" && cfg.PrivateKeyEnv != "" {
		keyHex = strings.TrimSpace(os.Getenv(cfg.PrivateKeyEnv))
	}
	if keyHex == "" {
		return nil, fmt.Errorf("runtime %s: private key missing", cfg.ID)
	}

	model := cfg.Model
	if model == "" {
		model = defaultModel
	}

	if err := os.MkdirAll(filepath.Dir(cfg.StoragePath), 0o755); err != nil {
		return nil, fmt.Errorf("runtime %s: create storage dir: %w", cfg.ID, err)
	}

	registry := newStreamRegistry()
	perfStore, err := NewPerfStore(cfg.StoragePath)
	if err != nil {
		return nil, fmt.Errorf("runtime %s: open perf store: %w", cfg.ID, err)
	}
	perf := NewPerfTracker(perfStore)
	receiptCB := func(nonce uint64) { registry.recordReceipt(nonce) }

	br := bridge.NewRESTBridge(chainREST)
	session, sm, err := user.NewHTTPSession(user.HTTPSessionConfig{
		PrivateKeyHex:   keyHex,
		EscrowID:        cfg.ID,
		Bridge:          br,
		StoragePath:     cfg.StoragePath,
		StreamCallback:  registry.callback,
		ReceiptCallback: receiptCB,
	})
	if err != nil {
		perfStore.Close()
		return nil, fmt.Errorf("runtime %s: create session: %w", cfg.ID, err)
	}

	engine := NewSpeculativeEngine(session, sm, perf, registry, len(session.Clients()))
	proxy := &Proxy{
		session:  session,
		sm:       sm,
		escrowID: cfg.ID,
		model:    model,
		registry: registry,
		engine:   engine,
		perf:     perf,
	}

	return &subnetRuntime{
		id:        cfg.ID,
		model:     model,
		handler:   newRuntimeMux(proxy),
		proxy:     proxy,
		session:   session,
		perfStore: perfStore,
	}, nil
}

func (rt *subnetRuntime) close() error {
	if rt.session != nil {
		rt.session.Close()
	}
	if rt.perfStore != nil {
		return rt.perfStore.Close()
	}
	return nil
}

func (rt *subnetRuntime) snapshot() runtimeStatus {
	status := runtimeStatus{
		ID:             rt.id,
		Model:          rt.model,
		ActiveRequests: rt.activeRequests.Load(),
		ReservedTokens: rt.reservedTokens.Load(),
	}
	if rt.proxy != nil && rt.proxy.sm != nil && rt.proxy.session != nil {
		phase := rt.proxy.sm.Phase()
		switch phase {
		case 0:
			status.Phase = "active"
		case 1:
			status.Phase = "finalizing"
		case 2:
			status.Phase = "settlement"
		default:
			status.Phase = fmt.Sprintf("unknown(%d)", phase)
		}
		st := rt.proxy.sm.SnapshotState()
		status.Nonce = rt.proxy.session.Nonce()
		status.Balance = st.Balance
	}
	return status
}

func (rt *subnetRuntime) score() int64 {
	return rt.reservedTokens.Load()*1000 + rt.activeRequests.Load()
}

func NewGateway(runtimes []*subnetRuntime, limiter *GatewayLimiter, defaultModel string) *Gateway {
	byID := make(map[string]*subnetRuntime, len(runtimes))
	for _, rt := range runtimes {
		byID[rt.id] = rt
	}
	return &Gateway{
		runtimes:     byID,
		runtimeOrder: runtimes,
		limiter:      limiter,
		defaultModel: defaultModel,
	}
}

func (g *Gateway) Close() error {
	var firstErr error
	for _, rt := range g.runtimeOrder {
		if err := rt.close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", g.handlePooledChat)
	mux.HandleFunc("/v1/status", g.handlePooledStatus)
	mux.HandleFunc("/v1/finalize", g.handleSingleOnly)
	mux.HandleFunc("/v1/debug/pending", g.handleSingleOnly)
	mux.HandleFunc("/v1/debug/state", g.handleSingleOnly)
	mux.HandleFunc("/v1/debug/perf", g.handleSingleOnly)
	mux.HandleFunc("/subnet/", g.handleSubnet)
	return mux
}

func (g *Gateway) handlePooledStatus(w http.ResponseWriter, r *http.Request) {
	if len(g.runtimeOrder) == 1 {
		g.runtimeOrder[0].handler.ServeHTTP(w, r)
		return
	}

	runtimes := make([]runtimeStatus, 0, len(g.runtimeOrder))
	for _, rt := range g.runtimeOrder {
		runtimes = append(runtimes, rt.snapshot())
	}
	writeJSON(w, map[string]any{
		"mode":     "gateway",
		"subnets":  runtimes,
		"limiter":  g.limiter.Snapshot(),
		"runtimes": len(g.runtimeOrder),
	})
}

func (g *Gateway) handleSingleOnly(w http.ResponseWriter, r *http.Request) {
	if len(g.runtimeOrder) == 1 {
		g.runtimeOrder[0].handler.ServeHTTP(w, r)
		return
	}
	http.Error(w, `{"error":{"message":"use /subnet/{id} prefix for this endpoint when multiple subnets are configured"}}`, http.StatusBadRequest)
}

func (g *Gateway) handlePooledChat(w http.ResponseWriter, r *http.Request) {
	body, model, inputTokens, err := parseChatReservation(r)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusBadRequest)
		return
	}

	if err := g.limiter.Acquire(inputTokens); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusTooManyRequests)
		return
	}
	defer g.limiter.Release(inputTokens)

	rt, err := g.reserveRuntimeForModel(model, inputTokens)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusBadGateway)
		return
	}
	defer g.releaseRuntime(rt, inputTokens)

	g.serveChatToRuntime(rt, "/v1/chat/completions", body, w, r)
}

func (g *Gateway) handleSubnet(w http.ResponseWriter, r *http.Request) {
	subnetID, innerPath, ok := parseSubnetPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}

	rt, ok := g.runtimes[subnetID]
	if !ok {
		http.Error(w, fmt.Sprintf(`{"error":{"message":"unknown subnet %s"}}`, subnetID), http.StatusNotFound)
		return
	}

	if innerPath == "/v1/chat/completions" {
		body, _, inputTokens, err := parseChatReservation(r)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusBadRequest)
			return
		}
		if err := g.limiter.Acquire(inputTokens); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusTooManyRequests)
			return
		}
		defer g.limiter.Release(inputTokens)

		g.reserveRuntime(rt, inputTokens)
		defer g.releaseRuntime(rt, inputTokens)

		g.serveChatToRuntime(rt, innerPath, body, w, r)
		return
	}

	req := cloneRequestWithBody(r, nil)
	req.URL.Path = innerPath
	req.URL.RawPath = innerPath
	req.RequestURI = innerPath
	w.Header().Set("X-Subnet-ID", subnetID)
	rt.handler.ServeHTTP(w, req)
}

func (g *Gateway) serveChatToRuntime(rt *subnetRuntime, path string, body []byte, w http.ResponseWriter, r *http.Request) {
	req := cloneRequestWithBody(r, body)
	req.URL.Path = path
	req.URL.RawPath = path
	req.RequestURI = path
	w.Header().Set("X-Subnet-ID", rt.id)
	rt.handler.ServeHTTP(w, req)
}

func (g *Gateway) reserveRuntimeForModel(requestModel string, inputTokens int64) (*subnetRuntime, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	candidates := g.runtimeOrder
	if requestModel != "" {
		var matching []*subnetRuntime
		for _, rt := range g.runtimeOrder {
			if rt.model == requestModel {
				matching = append(matching, rt)
			}
		}
		if len(matching) > 0 {
			candidates = matching
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no subnet runtimes configured")
	}

	bestScore := candidates[0].score()
	best := []*subnetRuntime{candidates[0]}
	for _, rt := range candidates[1:] {
		score := rt.score()
		switch {
		case score < bestScore:
			bestScore = score
			best = []*subnetRuntime{rt}
		case score == bestScore:
			best = append(best, rt)
		}
	}

	if len(best) == 1 {
		g.reserveRuntimeLocked(best[0], inputTokens)
		return best[0], nil
	}
	idx := int(g.roundRobinSeed.Add(1)-1) % len(best)
	chosen := best[idx]
	g.reserveRuntimeLocked(chosen, inputTokens)
	return chosen, nil
}

func (g *Gateway) reserveRuntime(rt *subnetRuntime, inputTokens int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reserveRuntimeLocked(rt, inputTokens)
}

func (g *Gateway) reserveRuntimeLocked(rt *subnetRuntime, inputTokens int64) {
	rt.activeRequests.Add(1)
	rt.reservedTokens.Add(inputTokens)
}

func (g *Gateway) releaseRuntime(rt *subnetRuntime, inputTokens int64) {
	rt.activeRequests.Add(-1)
	rt.reservedTokens.Add(-inputTokens)
}

func parseSubnetPath(path string) (subnetID, innerPath string, ok bool) {
	trimmed := strings.TrimPrefix(path, "/subnet/")
	if trimmed == path {
		return "", "", false
	}
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) != 2 || parts[0] == "" {
		return "", "", false
	}
	return parts[0], "/" + parts[1], true
}

func cloneRequestWithBody(r *http.Request, body []byte) *http.Request {
	req := r.Clone(r.Context())
	req.URL = cloneURL(r.URL)
	if body != nil {
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
	}
	return req
}

func cloneURL(u *url.URL) *url.URL {
	if u == nil {
		return &url.URL{}
	}
	clone := *u
	return &clone
}

func parseChatReservation(r *http.Request) ([]byte, string, int64, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, "", 0, fmt.Errorf("read body: %w", err)
	}

	normalized := normalizeContent(body)
	updatedBody, req, err := normalizeChatRequest(normalized)
	if err != nil {
		return nil, "", 0, err
	}

	inputTokens := estimatePromptTokens(updatedBody)
	return updatedBody, req.Model, inputTokens, nil
}

func normalizeChatRequest(body []byte) ([]byte, chatRequest, error) {
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, chatRequest{}, fmt.Errorf("parse request: %w", err)
	}

	if req.MaxTokens == 0 {
		req.MaxTokens = DefaultRequestMaxTokens
	}
	if DefaultRequestMaxTokens > 0 && req.MaxTokens > DefaultRequestMaxTokens {
		req.MaxTokens = DefaultRequestMaxTokens
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, chatRequest{}, fmt.Errorf("parse request map: %w", err)
	}
	raw["max_tokens"] = req.MaxTokens
	updatedBody, err := json.Marshal(raw)
	if err != nil {
		return nil, chatRequest{}, fmt.Errorf("marshal request: %w", err)
	}
	return updatedBody, req, nil
}

func estimatePromptTokens(body []byte) int64 {
	if len(body) == 0 {
		return 1
	}
	// Approximate tokenizer: 1 token ~= 4 bytes. Good enough for admission control.
	estimate := (len(body) + 3) / 4
	if estimate < 1 {
		estimate = 1
	}
	return int64(estimate)
}

func resolveRuntimeConfigs(singleEscrowID, singleKeyHex, singleModel, singleStoragePath string) ([]RuntimeConfig, error) {
	if raw := strings.TrimSpace(os.Getenv("SUBNETS_JSON")); raw != "" {
		var runtimes []RuntimeConfig
		if err := json.Unmarshal([]byte(raw), &runtimes); err != nil {
			return nil, fmt.Errorf("parse SUBNETS_JSON: %w", err)
		}
		return runtimes, nil
	}

	if singleEscrowID == "" || singleKeyHex == "" {
		return nil, fmt.Errorf("--private-key/--escrow-id or SUBNET_PRIVATE_KEY/SUBNET_ESCROW_ID required")
	}

	return []RuntimeConfig{{
		ID:            singleEscrowID,
		PrivateKeyHex: singleKeyHex,
		Model:         singleModel,
		StoragePath:   singleStoragePath,
	}}, nil
}

func finalizeRuntimeConfigs(runtimes []RuntimeConfig, defaultModel, baseStorageDir string) ([]RuntimeConfig, error) {
	out := make([]RuntimeConfig, 0, len(runtimes))
	seen := make(map[string]struct{}, len(runtimes))
	for _, cfg := range runtimes {
		cfg.ID = strings.TrimSpace(cfg.ID)
		if cfg.ID == "" {
			return nil, fmt.Errorf("runtime config missing id")
		}
		if _, ok := seen[cfg.ID]; ok {
			return nil, fmt.Errorf("duplicate runtime id %s", cfg.ID)
		}
		seen[cfg.ID] = struct{}{}
		if cfg.Model == "" {
			cfg.Model = defaultModel
		}
		if cfg.StoragePath == "" {
			cfg.StoragePath = filepath.Join(baseStorageDir, fmt.Sprintf("subnet-%s.db", cfg.ID))
		}
		out = append(out, cfg)
	}
	slices.SortFunc(out, func(a, b RuntimeConfig) int {
		return strings.Compare(a.ID, b.ID)
	})
	return out, nil
}

func buildRuntimes(configs []RuntimeConfig, chainREST, defaultModel string) ([]*subnetRuntime, error) {
	runtimes := make([]*subnetRuntime, 0, len(configs))
	for _, cfg := range configs {
		rt, err := buildRuntime(cfg, chainREST, defaultModel)
		if err != nil {
			for _, built := range runtimes {
				built.close()
			}
			return nil, err
		}
		runtimes = append(runtimes, rt)
		log.Printf("loaded subnet runtime escrow=%s model=%s storage=%s", cfg.ID, rt.model, cfg.StoragePath)
	}
	return runtimes, nil
}
