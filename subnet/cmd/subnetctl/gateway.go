package main

import (
	"bytes"
	"encoding/json"
	"errors"
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
	"subnet/transport"
	"subnet/types"
	"subnet/user"
)

type RuntimeConfig struct {
	ID              string `json:"id"`
	PrivateKeyHex   string `json:"private_key,omitempty"`
	PrivateKeyEnv   string `json:"private_key_env,omitempty"`
	Model           string `json:"model,omitempty"`
	StoragePath     string `json:"storage_path,omitempty"`
	ProtocolVersion string `json:"protocol_version,omitempty"`
}

type Gateway struct {
	runtimes           map[string]*subnetRuntime
	runtimeOrder       []*subnetRuntime
	limiter            *GatewayLimiter
	participantLimiter *ParticipantRequestLimiter
	phaseGate          *ChainPhaseGate
	escrowChecker      *EscrowChecker
	metrics            *SubnetMetrics
	settings           GatewaySettings
	store              *GatewayStore
	baseStorageDir     string
	mu                 sync.Mutex
	roundRobinSeed     atomic.Uint64
}

type subnetRuntime struct {
	id              string
	model           string
	handler         http.Handler
	proxy           *Proxy
	session         *user.Session
	perfStore       *PerfStore
	participantKeys []string

	active         atomic.Bool
	activeRequests atomic.Int64
	reservedTokens atomic.Int64
}

type runtimeStatus struct {
	ID                   string `json:"id"`
	Model                string `json:"model"`
	Active               bool   `json:"active"`
	Phase                string `json:"phase,omitempty"`
	Nonce                uint64 `json:"nonce,omitempty"`
	Balance              uint64 `json:"balance,omitempty"`
	ProtocolVersion      string `json:"protocol_version,omitempty"`
	ActiveRequests       int64  `json:"active_requests"`
	ReservedTokens       int64  `json:"reserved_tokens"`
	ChainPhase           string `json:"chain_phase,omitempty"`
	ConfirmationPoCPhase string `json:"confirmation_poc_phase,omitempty"`
	RequestsBlocked      bool   `json:"requests_blocked"`
	BlockReason          string `json:"block_reason,omitempty"`
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

	perfStore, err := NewPerfStore(cfg.StoragePath)
	if err != nil {
		return nil, fmt.Errorf("runtime %s: open perf store: %w", cfg.ID, err)
	}
	perf := NewPerfTracker(perfStore)

	pv, pvErr := types.ParseProtocolVersion(cfg.ProtocolVersion)
	if pvErr != nil {
		perfStore.Close()
		return nil, fmt.Errorf("runtime %s: %w", cfg.ID, pvErr)
	}

	br := bridge.NewRESTBridge(chainREST)
	session, sm, err := user.NewHTTPSession(user.HTTPSessionConfig{
		PrivateKeyHex:    keyHex,
		EscrowID:         cfg.ID,
		Bridge:           br,
		StoragePath:      cfg.StoragePath,
		RequestAdmission: sharedParticipantRequestLimiter,
		ProtocolVersion:  pv,
	})
	if err != nil {
		perfStore.Close()
		return nil, fmt.Errorf("runtime %s: create session: %w", cfg.ID, err)
	}

	redundancy := NewRedundancy(session, perf, len(session.Clients()))
	proxy := &Proxy{
		session:    session,
		sm:         sm,
		escrowID:   cfg.ID,
		model:      model,
		redundancy: redundancy,
		perf:       perf,
	}

	rt := &subnetRuntime{
		id:              cfg.ID,
		model:           model,
		handler:         newRuntimeMux(proxy),
		proxy:           proxy,
		session:         session,
		perfStore:       perfStore,
		participantKeys: session.ParticipantKeys(),
	}
	rt.active.Store(true)
	return rt, nil
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
		Active:         rt.active.Load(),
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
		status.ProtocolVersion = string(rt.proxy.sm.ProtocolVersion())
	}
	if rt.proxy != nil && rt.proxy.phaseGate != nil {
		snapshot := rt.proxy.phaseGate.Snapshot()
		status.ChainPhase = snapshot.EpochPhase
		status.ConfirmationPoCPhase = snapshot.ConfirmationPoCPhase
		status.RequestsBlocked = snapshot.RequestsBlocked
		status.BlockReason = snapshot.BlockReason
	}
	return status
}

func (rt *subnetRuntime) score() int64 {
	return rt.reservedTokens.Load()*1000 + rt.activeRequests.Load()
}

func NewGateway(runtimes []*subnetRuntime, limiter *GatewayLimiter, defaultModel string) *Gateway {
	byID := make(map[string]*subnetRuntime, len(runtimes))
	for _, rt := range runtimes {
		rt.active.Store(true)
		byID[rt.id] = rt
	}
	g := &Gateway{
		runtimes:           byID,
		runtimeOrder:       runtimes,
		limiter:            limiter,
		participantLimiter: sharedParticipantRequestLimiter,
		metrics:            NewSubnetMetrics(),
		settings: GatewaySettings{
			DefaultModel: defaultModel,
		},
	}
	g.participantLimiter.SetMetrics(g.metrics)
	g.metrics.AttachGateway(g)
	for _, rt := range runtimes {
		g.attachMetrics(rt)
	}
	return g
}

func NewManagedGateway(runtimes []*subnetRuntime, limiter *GatewayLimiter, settings GatewaySettings, baseStorageDir string, store *GatewayStore) *Gateway {
	g := NewGateway(runtimes, limiter, settings.DefaultModel)
	g.settings = settings
	g.baseStorageDir = baseStorageDir
	g.store = store
	g.phaseGate = NewChainPhaseGate(settings.PublicAPI, 0)
	if g.phaseGate != nil {
		for _, rt := range g.runtimeOrder {
			if rt != nil && rt.proxy != nil {
				rt.proxy.phaseGate = g.phaseGate
			}
		}
		g.phaseGate.Start()
	}
	g.escrowChecker = NewEscrowChecker(func() string {
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.settings.ChainREST
	})
	for _, rt := range g.runtimeOrder {
		g.attachEscrowChecker(rt)
	}
	return g
}

func (g *Gateway) Close() error {
	var firstErr error
	if g.phaseGate != nil {
		g.phaseGate.Stop()
	}
	for _, rt := range g.runtimeOrder {
		if err := rt.close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", g.metrics.Handler())
	mux.HandleFunc("/v1/chat/completions", g.handlePooledChat)
	mux.HandleFunc("/v1/status", g.handlePooledStatus)
	mux.HandleFunc("/v1/admin/state", g.handleAdminState)
	mux.HandleFunc("/v1/admin/settings", g.handleAdminSettings)
	mux.HandleFunc("/v1/admin/subnets", g.handleAdminSubnets)
	mux.HandleFunc("/v1/admin/subnets/", g.handleAdminSubnetAction)
	mux.HandleFunc("/v1/finalize", g.handleSingleOnly)
	mux.HandleFunc("/v1/debug/pending", g.handleSingleOnly)
	mux.HandleFunc("/v1/debug/state", g.handleSingleOnly)
	mux.HandleFunc("/v1/debug/perf", g.handleSingleOnly)
	mux.HandleFunc("/subnet/", g.handleSubnet)
	return mux
}

func (g *Gateway) handlePooledStatus(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	runtimes := append([]*subnetRuntime(nil), g.runtimeOrder...)
	g.mu.Unlock()
	if len(runtimes) == 1 {
		runtimes[0].handler.ServeHTTP(w, r)
		return
	}

	statuses := make([]runtimeStatus, 0, len(runtimes))
	for _, rt := range runtimes {
		statuses = append(statuses, rt.snapshot())
	}
	writeJSON(w, map[string]any{
		"mode":     "gateway",
		"subnets":  statuses,
		"limiter":  g.limiter.Snapshot(),
		"runtimes": len(runtimes),
	})
}

func (g *Gateway) handleSingleOnly(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	runtimes := append([]*subnetRuntime(nil), g.runtimeOrder...)
	g.mu.Unlock()
	if len(runtimes) == 1 {
		runtimes[0].handler.ServeHTTP(w, r)
		return
	}
	http.Error(w, `{"error":{"message":"use /subnet/{id} prefix for this endpoint when multiple subnets are configured"}}`, http.StatusBadRequest)
}

func (g *Gateway) handlePooledChat(w http.ResponseWriter, r *http.Request) {
	ctx, _ := ensureRequestLogContext(r.Context())
	r = r.WithContext(ctx)
	body, model, inputTokens, err := parseChatReservation(r)
	if err != nil {
		logRequestStage(ctx, "gateway_parse_failed", "error", err)
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusBadRequest)
		return
	}
	logRequestStage(ctx, "gateway_request_received", "model", firstNonEmpty(model, g.settings.DefaultModel), "input_tokens", inputTokens)

	if !relaxedPoCBypassActive() {
		if err := g.limiter.Acquire(inputTokens); err != nil {
			g.metrics.RecordLimitRejection(limiterReasonLabel(err))
			logRequestStage(ctx, "gateway_limiter_rejected", "reason", limiterReasonLabel(err), "input_tokens", inputTokens)
			http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusTooManyRequests)
			return
		}
		defer g.limiter.Release(inputTokens)
		logRequestStage(ctx, "gateway_limiter_acquired", "input_tokens", inputTokens)
	} else {
		logRequestStage(ctx, "gateway_limiter_bypassed_during_poc", "input_tokens", inputTokens, "reason", currentPoCPhaseReason())
	}

	rt, err := g.reserveRuntimeForModel(model, inputTokens)
	if err != nil {
		logRequestStage(ctx, "gateway_runtime_select_failed", "error", err)
		if isParticipantRateLimitError(err) {
			g.metrics.RecordParticipantLimitRejection("pooled_route")
		}
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), gatewayStatusCodeForError(err))
		return
	}
	defer g.releaseRuntime(rt, inputTokens)
	logRequestStage(ctx, "gateway_runtime_selected", "escrow", rt.id)

	g.serveChatToRuntime(rt, "/v1/chat/completions", body, w, r)
}

func (g *Gateway) handleSubnet(w http.ResponseWriter, r *http.Request) {
	ctx, _ := ensureRequestLogContext(r.Context())
	r = r.WithContext(ctx)
	subnetID, innerPath, ok := parseSubnetPath(r.URL.Path)
	if !ok {
		logRequestStage(ctx, "gateway_subnet_path_invalid", "path", r.URL.Path)
		http.NotFound(w, r)
		return
	}
	logRequestStage(ctx, "gateway_subnet_request_received", "escrow", subnetID, "path", innerPath)

	g.mu.Lock()
	rt, ok := g.runtimes[subnetID]
	g.mu.Unlock()
	if !ok {
		logRequestStage(ctx, "gateway_subnet_not_found", "escrow", subnetID)
		http.Error(w, fmt.Sprintf(`{"error":{"message":"unknown subnet %s"}}`, subnetID), http.StatusNotFound)
		return
	}

	if innerPath == "/v1/chat/completions" {
		body, _, inputTokens, err := parseChatReservation(r)
		if err != nil {
			logRequestStage(ctx, "gateway_subnet_parse_failed", "escrow", subnetID, "error", err)
			http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusBadRequest)
			return
		}
		if !relaxedPoCBypassActive() {
			if err := g.limiter.Acquire(inputTokens); err != nil {
				g.metrics.RecordLimitRejection(limiterReasonLabel(err))
				logRequestStage(ctx, "gateway_subnet_limiter_rejected", "escrow", subnetID, "reason", limiterReasonLabel(err), "input_tokens", inputTokens)
				http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusTooManyRequests)
				return
			}
			defer g.limiter.Release(inputTokens)
			logRequestStage(ctx, "gateway_subnet_limiter_acquired", "escrow", subnetID, "input_tokens", inputTokens)
		} else {
			logRequestStage(ctx, "gateway_subnet_limiter_bypassed_during_poc", "escrow", subnetID, "input_tokens", inputTokens, "reason", currentPoCPhaseReason())
		}

		if err := g.ensureRuntimeAvailable(rt); err != nil {
			g.metrics.RecordLimitRejection("participant_request_budget")
			logRequestStage(ctx, "gateway_subnet_participant_limiter_rejected", "escrow", subnetID, "error", err)
			http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), gatewayStatusCodeForError(err))
			return
		}

		g.reserveRuntime(rt, inputTokens)
		defer g.releaseRuntime(rt, inputTokens)
		logRequestStage(ctx, "gateway_subnet_runtime_selected", "escrow", subnetID, "input_tokens", inputTokens)

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
	logRequestStage(req.Context(), "gateway_request_forwarded", "escrow", rt.id, "path", path)
	rt.handler.ServeHTTP(w, req)
}

func (g *Gateway) reserveRuntimeForModel(requestModel string, inputTokens int64) (*subnetRuntime, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	var candidates []*subnetRuntime
	skippedForParticipants := false
	for _, rt := range g.runtimeOrder {
		if !rt.active.Load() {
			continue
		}
		if err := g.ensureRuntimeAvailable(rt); err != nil {
			skippedForParticipants = true
			continue
		}
		candidates = append(candidates, rt)
	}
	if requestModel != "" {
		var matching []*subnetRuntime
		for _, rt := range candidates {
			if rt.model == requestModel {
				matching = append(matching, rt)
			}
		}
		if len(matching) > 0 {
			candidates = matching
		}
	}
	if len(candidates) == 0 {
		if skippedForParticipants {
			return nil, &EscrowParticipantRateLimitError{}
		}
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

func (g *Gateway) ensureRuntimeAvailable(rt *subnetRuntime) error {
	if g == nil || g.participantLimiter == nil || rt == nil {
		return nil
	}
	if relaxedPoCBypassActive() {
		return nil
	}
	return g.participantLimiter.CanAcceptEscrow(rt.participantKeys)
}

func gatewayStatusCodeForError(err error) int {
	if isParticipantRateLimitError(err) {
		return http.StatusTooManyRequests
	}
	var admissionErr *RequestAdmissionError
	if errors.As(err, &admissionErr) {
		return http.StatusServiceUnavailable
	}
	var upstreamErr *transport.UpstreamStatusError
	if errors.As(err, &upstreamErr) && isParticipantThrottleStatus(upstreamErr.StatusCode) {
		return http.StatusTooManyRequests
	}
	return http.StatusBadGateway
}

func isParticipantRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	var participantErr *ParticipantRateLimitError
	if errors.As(err, &participantErr) {
		return true
	}
	var escrowErr *EscrowParticipantRateLimitError
	return errors.As(err, &escrowErr)
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

func defaultStoragePath(baseStorageDir, escrowID string) string {
	return filepath.Join(baseStorageDir, fmt.Sprintf("escrow-%s", escrowID), "state.db")
}

type adminSubnetRequest struct {
	ID              string `json:"id"`
	PrivateKey      string `json:"private_key,omitempty"`
	PrivateKeyEnv   string `json:"private_key_env,omitempty"`
	Model           string `json:"model,omitempty"`
	StoragePath     string `json:"storage_path,omitempty"`
	ProtocolVersion string `json:"protocol_version,omitempty"`
}

type adminSettingsRequest struct {
	ChainREST               *string `json:"chain_rest,omitempty"`
	PublicAPI               *string `json:"public_api,omitempty"`
	DefaultModel            *string `json:"default_model,omitempty"`
	MaxConcurrentRequests   *int64  `json:"max_concurrent_requests,omitempty"`
	MaxInputTokensInFlight  *int64  `json:"max_input_tokens_in_flight,omitempty"`
	DefaultRequestMaxTokens *uint64 `json:"default_request_max_tokens,omitempty"`
}

func (g *Gateway) handleAdminState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if g.store == nil {
		http.Error(w, `{"error":{"message":"gateway state store unavailable"}}`, http.StatusServiceUnavailable)
		return
	}
	state, ok, err := g.store.LoadState()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusInternalServerError)
		return
	}
	if !ok {
		writeJSON(w, map[string]any{
			"settings": g.settings,
			"subnets":  []GatewaySubnetState{},
		})
		return
	}

	g.mu.Lock()
	runtimeByID := make(map[string]runtimeStatus, len(g.runtimeOrder))
	for _, rt := range g.runtimeOrder {
		runtimeByID[rt.id] = rt.snapshot()
	}
	g.mu.Unlock()

	type adminSubnetView struct {
		GatewaySubnetState
		Runtime *runtimeStatus `json:"runtime,omitempty"`
	}
	views := make([]adminSubnetView, 0, len(state.Subnets))
	for _, subnet := range state.Subnets {
		view := adminSubnetView{GatewaySubnetState: subnet}
		if snapshot, ok := runtimeByID[subnet.ID]; ok {
			s := snapshot
			view.Runtime = &s
		}
		views = append(views, view)
	}
	writeJSON(w, map[string]any{
		"settings": state.Settings,
		"subnets":  views,
		"limiter":  g.limiter.Snapshot(),
	})
}

func (g *Gateway) handleAdminSettings(w http.ResponseWriter, r *http.Request) {
	if g.store == nil {
		http.Error(w, `{"error":{"message":"gateway state store unavailable"}}`, http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		g.mu.Lock()
		settings := g.settings
		g.mu.Unlock()
		writeJSON(w, settings)
	case http.MethodPost:
		var req adminSettingsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusBadRequest)
			return
		}

		g.mu.Lock()
		settings := g.settings
		if req.ChainREST != nil {
			settings.ChainREST = strings.TrimSpace(*req.ChainREST)
		}
		if req.PublicAPI != nil {
			settings.PublicAPI = strings.TrimSpace(*req.PublicAPI)
		}
		if req.DefaultModel != nil {
			settings.DefaultModel = strings.TrimSpace(*req.DefaultModel)
		}
		if req.MaxConcurrentRequests != nil {
			settings.MaxConcurrentRequests = *req.MaxConcurrentRequests
		}
		if req.MaxInputTokensInFlight != nil {
			settings.MaxInputTokensInFlight = *req.MaxInputTokensInFlight
		}
		if req.DefaultRequestMaxTokens != nil {
			settings.DefaultRequestMaxTokens = *req.DefaultRequestMaxTokens
		}
		if err := g.store.UpdateSettings(settings); err != nil {
			g.mu.Unlock()
			http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusInternalServerError)
			return
		}
		g.settings = settings
		if g.phaseGate != nil {
			g.phaseGate.Stop()
		}
		g.phaseGate = NewChainPhaseGate(settings.PublicAPI, 0)
		for _, rt := range g.runtimeOrder {
			if rt != nil && rt.proxy != nil {
				rt.proxy.phaseGate = g.phaseGate
			}
		}
		if g.phaseGate != nil {
			g.phaseGate.Start()
		}
		g.limiter.UpdateLimits(settings.MaxConcurrentRequests, settings.MaxInputTokensInFlight)
		DefaultRequestMaxTokens = settings.DefaultRequestMaxTokens
		g.mu.Unlock()

		writeJSON(w, settings)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (g *Gateway) handleAdminSubnets(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		g.handleAdminState(w, r)
	case http.MethodPost:
		g.handleAdminAddSubnet(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (g *Gateway) handleAdminSubnetAction(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimPrefix(r.URL.Path, "/v1/admin/subnets/")
	parts := strings.Split(strings.Trim(trimmed, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	id := parts[0]
	if len(parts) == 1 && r.Method == http.MethodDelete {
		g.handleAdminCleanSubnet(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "deactivate" && r.Method == http.MethodPost {
		g.handleAdminDeactivateSubnet(w, r, id)
		return
	}
	http.NotFound(w, r)
}

func (g *Gateway) handleAdminAddSubnet(w http.ResponseWriter, r *http.Request) {
	if g.store == nil {
		http.Error(w, `{"error":{"message":"gateway state store unavailable"}}`, http.StatusServiceUnavailable)
		return
	}
	var req adminSubnetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusBadRequest)
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	if req.ID == "" {
		http.Error(w, `{"error":{"message":"id is required"}}`, http.StatusBadRequest)
		return
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	state, ok, err := g.store.LoadState()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, `{"error":{"message":"gateway state is not initialized"}}`, http.StatusServiceUnavailable)
		return
	}

	record, found := findGatewaySubnet(state.Subnets, req.ID)
	if found {
		if strings.TrimSpace(req.PrivateKey) != "" {
			record.PrivateKeyHex = strings.TrimSpace(req.PrivateKey)
		}
		if strings.TrimSpace(req.PrivateKeyEnv) != "" {
			record.PrivateKeyEnv = strings.TrimSpace(req.PrivateKeyEnv)
		}
		if strings.TrimSpace(req.Model) != "" {
			record.Model = strings.TrimSpace(req.Model)
		}
		if strings.TrimSpace(req.StoragePath) != "" {
			record.StoragePath = strings.TrimSpace(req.StoragePath)
		}
		if strings.TrimSpace(req.ProtocolVersion) != "" {
			record.ProtocolVersion = strings.TrimSpace(req.ProtocolVersion)
		}
		record.Active = true
	} else {
		hasKey := strings.TrimSpace(req.PrivateKey) != "" || strings.TrimSpace(req.PrivateKeyEnv) != ""
		if !hasKey {
			http.Error(w, `{"error":{"message":"private_key or private_key_env is required for a new subnet"}}`, http.StatusBadRequest)
			return
		}
		record = GatewaySubnetState{
			RuntimeConfig: RuntimeConfig{
				ID:              req.ID,
				PrivateKeyHex:   strings.TrimSpace(req.PrivateKey),
				PrivateKeyEnv:   strings.TrimSpace(req.PrivateKeyEnv),
				Model:           strings.TrimSpace(req.Model),
				StoragePath:     strings.TrimSpace(req.StoragePath),
				ProtocolVersion: strings.TrimSpace(req.ProtocolVersion),
			},
			Active: true,
		}
	}

	if existing, exists := g.runtimes[req.ID]; exists {
		if existing.active.Load() {
			http.Error(w, `{"error":{"message":"subnet already active"}}`, http.StatusConflict)
			return
		}
		if err := g.store.UpsertSubnet(record); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusInternalServerError)
			return
		}
		existing.active.Store(true)
		writeJSON(w, map[string]any{
			"id":           record.ID,
			"active":       true,
			"model":        record.Model,
			"storage_path": record.StoragePath,
		})
		return
	}

	if record.Model == "" {
		record.Model = state.Settings.DefaultModel
	}
	if record.StoragePath == "" {
		record.StoragePath = defaultStoragePath(g.baseStorageDir, record.ID)
	}

	rt, err := buildRuntime(record.RuntimeConfig, state.Settings.ChainREST, state.Settings.DefaultModel)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusBadRequest)
		return
	}
	if err := g.store.UpsertSubnet(record); err != nil {
		rt.close()
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusInternalServerError)
		return
	}
	g.runtimes[record.ID] = rt
	g.runtimeOrder = append(g.runtimeOrder, rt)
	g.attachMetrics(rt)
	g.attachEscrowChecker(rt)
	g.sortRuntimeOrderLocked()
	writeJSON(w, map[string]any{
		"id":           record.ID,
		"active":       true,
		"model":        record.Model,
		"storage_path": record.StoragePath,
	})
}

func (g *Gateway) handleAdminDeactivateSubnet(w http.ResponseWriter, r *http.Request, id string) {
	if g.store == nil {
		http.Error(w, `{"error":{"message":"gateway state store unavailable"}}`, http.StatusServiceUnavailable)
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	rt, ok := g.runtimes[id]
	if !ok {
		http.Error(w, fmt.Sprintf(`{"error":{"message":"subnet %s is not active"}}`, id), http.StatusNotFound)
		return
	}
	if rt.activeRequests.Load() > 0 {
		http.Error(w, fmt.Sprintf(`{"error":{"message":"subnet %s has active requests"}}`, id), http.StatusConflict)
		return
	}
	if err := g.store.SetSubnetActive(id, false); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusInternalServerError)
		return
	}
	rt.active.Store(false)
	writeJSON(w, map[string]any{
		"id":     id,
		"active": false,
	})
}

func (g *Gateway) handleAdminCleanSubnet(w http.ResponseWriter, r *http.Request, id string) {
	if g.store == nil {
		http.Error(w, `{"error":{"message":"gateway state store unavailable"}}`, http.StatusServiceUnavailable)
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	state, ok, err := g.store.LoadState()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, `{"error":{"message":"gateway state is not initialized"}}`, http.StatusServiceUnavailable)
		return
	}
	record, found := findGatewaySubnet(state.Subnets, id)
	if !found {
		http.Error(w, fmt.Sprintf(`{"error":{"message":"subnet %s not found"}}`, id), http.StatusNotFound)
		return
	}
	if record.Active {
		http.Error(w, fmt.Sprintf(`{"error":{"message":"subnet %s is active; deactivate it first"}}`, id), http.StatusConflict)
		return
	}
	if rt, ok := g.runtimes[id]; ok {
		if rt.activeRequests.Load() > 0 {
			http.Error(w, fmt.Sprintf(`{"error":{"message":"subnet %s has active requests"}}`, id), http.StatusConflict)
			return
		}
		delete(g.runtimes, id)
		g.runtimeOrder = removeRuntime(g.runtimeOrder, id)
		if err := rt.close(); err != nil {
			log.Printf("close subnet %s: %v", id, err)
		}
	}
	if err := g.store.DeleteSubnet(id); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusInternalServerError)
		return
	}
	if err := removeSubnetStorage(record.StoragePath, g.baseStorageDir); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"id":      id,
		"deleted": true,
	})
}

func findGatewaySubnet(subnets []GatewaySubnetState, id string) (GatewaySubnetState, bool) {
	for _, subnet := range subnets {
		if subnet.ID == id {
			return subnet, true
		}
	}
	return GatewaySubnetState{}, false
}

func removeRuntime(runtimes []*subnetRuntime, id string) []*subnetRuntime {
	out := runtimes[:0]
	for _, rt := range runtimes {
		if rt.id != id {
			out = append(out, rt)
		}
	}
	return out
}

func (g *Gateway) sortRuntimeOrderLocked() {
	slices.SortFunc(g.runtimeOrder, func(a, b *subnetRuntime) int {
		return strings.Compare(a.id, b.id)
	})
}

func (g *Gateway) attachMetrics(rt *subnetRuntime) {
	if g == nil || g.metrics == nil || rt == nil || rt.proxy == nil || rt.proxy.redundancy == nil {
		return
	}
	rt.proxy.redundancy.metrics = g.metrics
	rt.proxy.redundancy.subnetID = rt.id
}

func (g *Gateway) attachEscrowChecker(rt *subnetRuntime) {
	if g == nil || g.escrowChecker == nil || rt == nil || rt.proxy == nil || rt.proxy.redundancy == nil {
		return
	}
	escrowID := rt.id
	rt.proxy.redundancy.onEscrowMissing = func() {
		go g.escrowChecker.TriggerCheck(escrowID, func() {
			g.deactivateSubnetByID(escrowID)
		})
	}
}

// deactivateSubnetByID marks a subnet inactive in memory and persists the change.
// Safe to call from any goroutine.
func (g *Gateway) deactivateSubnetByID(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	rt, ok := g.runtimes[id]
	if !ok || !rt.active.Load() {
		return
	}
	rt.active.Store(false)
	if g.store != nil {
		if err := g.store.SetSubnetActive(id, false); err != nil {
			log.Printf("escrow checker: persist deactivation for %s: %v", id, err)
		}
	}
	log.Printf("subnet %s deactivated: escrow confirmed missing on chain", id)
}

func removeSubnetStorage(storagePath, baseStorageDir string) error {
	if strings.TrimSpace(storagePath) == "" {
		return nil
	}
	storagePath = filepath.Clean(storagePath)
	baseStorageDir = filepath.Clean(baseStorageDir)
	if !strings.HasPrefix(storagePath, baseStorageDir+string(os.PathSeparator)) && storagePath != baseStorageDir {
		return fmt.Errorf("refusing to delete storage outside base dir: %s", storagePath)
	}
	parent := filepath.Dir(storagePath)
	if filepath.Base(storagePath) == "state.db" && strings.HasPrefix(parent, baseStorageDir+string(os.PathSeparator)) {
		return os.RemoveAll(parent)
	}
	for _, path := range []string{storagePath, storagePath + "-shm", storagePath + "-wal"} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", path, err)
		}
	}
	if err := os.Remove(parent); err != nil && !os.IsNotExist(err) {
		return nil
	}
	return nil
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
			cfg.StoragePath = defaultStoragePath(baseStorageDir, cfg.ID)
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
