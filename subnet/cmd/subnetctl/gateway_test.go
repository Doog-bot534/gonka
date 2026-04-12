package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"subnet/transport"
)

func TestParseSubnetPath(t *testing.T) {
	id, inner, ok := parseSubnetPath("/subnet/12/v1/debug/perf")
	require.True(t, ok)
	require.Equal(t, "12", id)
	require.Equal(t, "/v1/debug/perf", inner)

	_, _, ok = parseSubnetPath("/v1/status")
	require.False(t, ok)
}

func TestGatewayChooseRuntimeUsesLowestLoad(t *testing.T) {
	a := &subnetRuntime{id: "6", model: "m"}
	b := &subnetRuntime{id: "12", model: "m"}
	a.reservedTokens.Store(500)
	b.reservedTokens.Store(100)

	g := NewGateway([]*subnetRuntime{a, b}, NewGatewayLimiter(0, 0), "m")
	chosen, err := g.reserveRuntimeForModel("m", 5)
	require.NoError(t, err)
	require.Equal(t, "12", chosen.id)
	require.EqualValues(t, 1, chosen.activeRequests.Load())
	require.EqualValues(t, 105, chosen.reservedTokens.Load())
}

func TestGatewayHandleSubnetRewritesInnerPath(t *testing.T) {
	var seenPath string
	rt := &subnetRuntime{
		id:    "12",
		model: "m",
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seenPath = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	g := NewGateway([]*subnetRuntime{rt}, NewGatewayLimiter(0, 0), "m")

	req := httptest.NewRequest(http.MethodGet, "/subnet/12/v1/status", nil)
	rec := httptest.NewRecorder()
	g.handleSubnet(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, "/v1/status", seenPath)
	require.Equal(t, "12", rec.Header().Get("X-Subnet-ID"))
}

func TestGatewayHandlePooledChatSetsChosenSubnetHeader(t *testing.T) {
	slow := &subnetRuntime{
		id:    "6",
		model: "Qwen/Test",
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusAccepted)
		}),
	}
	fast := &subnetRuntime{
		id:    "12",
		model: "Qwen/Test",
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.Contains(t, string(body), `"model":"Qwen/Test"`)
			w.WriteHeader(http.StatusCreated)
		}),
	}
	slow.reservedTokens.Store(1000)
	fast.reservedTokens.Store(10)

	g := NewGateway([]*subnetRuntime{slow, fast}, NewGatewayLimiter(0, 0), "Qwen/Test")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"Qwen/Test","messages":[{"role":"user","content":"hello"}]}`))
	rec := httptest.NewRecorder()

	g.handlePooledChat(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	require.Equal(t, "12", rec.Header().Get("X-Subnet-ID"))
}

func TestGatewayChooseRuntimeSkipsInactiveSubnet(t *testing.T) {
	a := &subnetRuntime{id: "6", model: "m"}
	b := &subnetRuntime{id: "12", model: "m"}
	g := NewGateway([]*subnetRuntime{a, b}, NewGatewayLimiter(0, 0), "m")
	b.active.Store(false)

	chosen, err := g.reserveRuntimeForModel("m", 5)
	require.NoError(t, err)
	require.Equal(t, "6", chosen.id)
}

func TestGatewayChooseRuntimeSkipsParticipantLimitedSubnet(t *testing.T) {
	limiter := NewParticipantRequestLimiter(1, 10)
	limiter.ObserveResult("shared-host", "/sessions/12/chat/completions", http.StatusServiceUnavailable)

	limited := &subnetRuntime{id: "6", model: "m", participantKeys: []string{"shared-host"}}
	available := &subnetRuntime{id: "12", model: "m", participantKeys: []string{"fresh-host"}}
	g := NewGateway([]*subnetRuntime{limited, available}, NewGatewayLimiter(0, 0), "m")
	g.participantLimiter = limiter

	chosen, err := g.reserveRuntimeForModel("m", 5)
	require.NoError(t, err)
	require.Equal(t, "12", chosen.id)
}

func TestGatewayChooseRuntimeFailsWhenAllSubnetsParticipantLimited(t *testing.T) {
	limiter := NewParticipantRequestLimiter(1, 10)
	limiter.ObserveResult("shared-host", "/sessions/12/chat/completions", http.StatusServiceUnavailable)

	a := &subnetRuntime{id: "6", model: "m", participantKeys: []string{"shared-host"}}
	b := &subnetRuntime{id: "12", model: "m", participantKeys: []string{"shared-host"}}
	g := NewGateway([]*subnetRuntime{a, b}, NewGatewayLimiter(0, 0), "m")
	g.participantLimiter = limiter

	_, err := g.reserveRuntimeForModel("m", 5)
	require.Error(t, err)
	require.True(t, isParticipantRateLimitError(err))
}

func TestGatewayExplicitRouteStillWorksForInactiveSubnet(t *testing.T) {
	var seenPath string
	rt := &subnetRuntime{
		id:    "12",
		model: "m",
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seenPath = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	g := NewGateway([]*subnetRuntime{rt}, NewGatewayLimiter(0, 0), "m")
	rt.active.Store(false)

	req := httptest.NewRequest(http.MethodGet, "/subnet/12/v1/status", nil)
	rec := httptest.NewRecorder()
	g.handleSubnet(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, "/v1/status", seenPath)
	require.Equal(t, "12", rec.Header().Get("X-Subnet-ID"))
}

func TestGatewayExplicitChatRouteRejectsParticipantLimitedSubnet(t *testing.T) {
	rt := &subnetRuntime{
		id:              "12",
		model:           "m",
		participantKeys: []string{"shared-host"},
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("request should not be forwarded when subnet participant budget is exhausted")
		}),
	}
	g := NewGateway([]*subnetRuntime{rt}, NewGatewayLimiter(0, 0), "m")
	limiter := NewParticipantRequestLimiter(1, 10)
	limiter.ObserveResult("shared-host", "/sessions/12/chat/completions", http.StatusTooManyRequests)
	g.participantLimiter = limiter

	req := httptest.NewRequest(http.MethodPost, "/subnet/12/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`))
	rec := httptest.NewRecorder()
	g.handleSubnet(rec, req)

	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Contains(t, rec.Body.String(), "participant request budget exhausted")
}

func TestGatewayLimiterEnforcesConcurrentAndTokenLimits(t *testing.T) {
	limiter := NewGatewayLimiter(1, 10)

	require.NoError(t, limiter.Acquire(8))
	require.ErrorContains(t, limiter.Acquire(1), "too many concurrent requests")
	limiter.Release(8)

	tokenLimiter := NewGatewayLimiter(2, 10)
	require.NoError(t, tokenLimiter.Acquire(5))
	require.ErrorContains(t, tokenLimiter.Acquire(6), "too many input tokens in flight")
}

func TestParticipantRequestLimiterUntrackedHostAlwaysAllowed(t *testing.T) {
	limiter := NewParticipantRequestLimiter(1, 10)
	now := time.Now()

	require.True(t, limiter.allow("shared-host", now))
	require.True(t, limiter.allow("shared-host", now))
	require.True(t, limiter.allow("shared-host", now))
}

func TestParticipantRequestLimiterRecoversAfterThrottle(t *testing.T) {
	limiter := NewParticipantRequestLimiter(1, 10)
	limiter.ObserveResult("shared-host", "/sessions/12/chat/completions", http.StatusServiceUnavailable)

	now := time.Now()
	require.False(t, limiter.allow("shared-host", now))
	require.True(t, limiter.allow("shared-host", now.Add(6*time.Second)))
}

func TestParticipantRequestLimiterMarksParticipantExhaustedOn503(t *testing.T) {
	limiter := NewParticipantRequestLimiter(2, 10)
	limiter.ObserveResult("shared-host", "/sessions/12/chat/completions", http.StatusServiceUnavailable)

	require.Equal(t, 1, limiter.ExhaustedCount())
	require.Equal(t, 1, limiter.TrackedCount())
	require.Error(t, limiter.CanAcceptEscrow([]string{"shared-host"}))
}

func TestParticipantRequestLimiterExpiresOnFullRecovery(t *testing.T) {
	limiter := NewParticipantRequestLimiter(10, 10)
	limiter.ObserveResult("shared-host", "/sessions/12/chat/completions", http.StatusServiceUnavailable)

	require.Equal(t, 1, limiter.TrackedCount())
	require.Equal(t, 1, limiter.ExhaustedCount())

	now := time.Now().Add(61 * time.Second)
	require.True(t, limiter.allow("shared-host", now))
	require.Equal(t, 0, limiter.TrackedCount())
}

func TestParticipantRequestLimiterPersistsThrottleState(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	limiter := NewParticipantRequestLimiter(10, 10)
	limiter.SetStore(store)
	limiter.ObserveResult("shared-host", "/sessions/12/chat/completions", http.StatusServiceUnavailable)

	rows, err := store.LoadParticipantThrottles()
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "shared-host", rows[0].Key)
	require.Equal(t, float64(0), rows[0].Tokens)
	require.Equal(t, http.StatusServiceUnavailable, rows[0].Status)
}

func TestParticipantRequestLimiterLoadStateRecoversTokens(t *testing.T) {
	limiter := NewParticipantRequestLimiter(10, 60)
	pastRefill := time.Now().Add(-5 * time.Second)
	limiter.LoadState("shared-host", 0, pastRefill)

	require.Equal(t, 1, limiter.TrackedCount())
	require.NoError(t, limiter.AllowRequest("shared-host", "/sessions/12/chat/completions"))
}

func TestParticipantRequestLimiterLoadStateDeletesFullyRecovered(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	require.NoError(t, store.SaveParticipantThrottle("shared-host", 0, time.Now().Add(-time.Hour), 503))

	limiter := NewParticipantRequestLimiter(10, 10)
	limiter.SetStore(store)
	limiter.LoadState("shared-host", 0, time.Now().Add(-time.Hour))

	require.Equal(t, 0, limiter.TrackedCount())

	rows, err := store.LoadParticipantThrottles()
	require.NoError(t, err)
	require.Len(t, rows, 0)
}

func TestParticipantRequestLimiterDeletesOnExpiry(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	limiter := NewParticipantRequestLimiter(10, 10)
	limiter.SetStore(store)
	limiter.ObserveResult("shared-host", "/sessions/12/chat/completions", http.StatusServiceUnavailable)

	rows, err := store.LoadParticipantThrottles()
	require.NoError(t, err)
	require.Len(t, rows, 1)

	now := time.Now().Add(61 * time.Second)
	require.True(t, limiter.allow("shared-host", now))

	rows, err = store.LoadParticipantThrottles()
	require.NoError(t, err)
	require.Len(t, rows, 0)
}

func TestNormalizeChatRequestDefaultsAndCapsMaxTokens(t *testing.T) {
	oldDefault := DefaultRequestMaxTokens
	DefaultRequestMaxTokens = 10_000
	t.Cleanup(func() {
		DefaultRequestMaxTokens = oldDefault
	})

	body, req, err := normalizeChatRequest([]byte(`{"messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	require.EqualValues(t, 10_000, req.MaxTokens)
	require.Contains(t, string(body), `"max_tokens":10000`)

	body, req, err = normalizeChatRequest([]byte(`{"max_tokens":10001,"messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	require.EqualValues(t, 10_000, req.MaxTokens)
	require.Contains(t, string(body), `"max_tokens":10000`)
}

func TestFinalizeRuntimeConfigsUsesPerEscrowStorageDirectories(t *testing.T) {
	baseDir := "/tmp/subnetctl"
	runtimes, err := finalizeRuntimeConfigs([]RuntimeConfig{{
		ID:            "12",
		PrivateKeyHex: "abc123",
	}}, "Qwen/Test", baseDir)
	require.NoError(t, err)
	require.Len(t, runtimes, 1)
	require.Equal(t, filepath.Join(baseDir, "escrow-12", "state.db"), runtimes[0].StoragePath)
	require.Equal(t, "Qwen/Test", runtimes[0].Model)
}

func TestAdminSettingsUpdatesLimiterAndDefaultTokens(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})
	require.NoError(t, store.Initialize(GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		MaxInputTokensInFlight:  200,
	}, nil))

	oldDefault := DefaultRequestMaxTokens
	DefaultRequestMaxTokens = 1000
	t.Cleanup(func() {
		DefaultRequestMaxTokens = oldDefault
	})

	limiter := NewGatewayLimiter(2, 200)
	g := NewManagedGateway(nil, limiter, GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		MaxInputTokensInFlight:  200,
	}, t.TempDir(), store)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/settings",
		strings.NewReader(`{"chain_rest":"http://node:2317","public_api":"http://api:9900","default_model":"Qwen/Qwen3-235B-A22B-Instruct-2507-FP8","max_concurrent_requests":7,"max_input_tokens_in_flight":700,"default_request_max_tokens":7777}`))
	rec := httptest.NewRecorder()
	g.handleAdminSettings(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.EqualValues(t, 7777, DefaultRequestMaxTokens)

	snap := limiter.Snapshot()
	require.EqualValues(t, 7, snap.MaxConcurrent)
	require.EqualValues(t, 700, snap.MaxInputTokens)

	state, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "http://node:2317", state.Settings.ChainREST)
	require.Equal(t, "http://api:9900", state.Settings.PublicAPI)
	require.Equal(t, "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8", state.Settings.DefaultModel)
	require.EqualValues(t, 7777, state.Settings.DefaultRequestMaxTokens)
	require.EqualValues(t, 7, state.Settings.MaxConcurrentRequests)
	require.EqualValues(t, 700, state.Settings.MaxInputTokensInFlight)
}

func TestGatewayMetricsEndpointExposedAndUpdated(t *testing.T) {
	rt := &subnetRuntime{
		id:    "12",
		model: "Qwen/Test",
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	g := NewGateway([]*subnetRuntime{rt}, NewGatewayLimiter(10, 1), "Qwen/Test")
	handler := buildGatewayHandler(g, runtimeOptions{apiKeys: map[string]struct{}{"secret": {}}})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"Qwen/Test","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)

	metricsReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsRec := httptest.NewRecorder()
	handler.ServeHTTP(metricsRec, metricsReq)
	require.Equal(t, http.StatusOK, metricsRec.Code)

	body := metricsRec.Body.String()
	require.Contains(t, body, "subnet_http_requests_total")
	require.Contains(t, body, `path="/v1/chat/completions"`)
	require.Contains(t, body, `status="429"`)
	require.Contains(t, body, `subnet_gateway_limit_rejections_total`)
	require.Contains(t, body, `reason="max_input_tokens_in_flight"`)
	require.Contains(t, body, `subnet_gateway_inflight_requests`)
	require.Contains(t, body, `subnet_runtime_active`)
	require.Contains(t, body, `subnet_id="12"`)
	require.Contains(t, body, `model="Qwen/Test"`)
}

func TestGatewayMetricsCollectorIncludesParticipantLimiterState(t *testing.T) {
	limiter := NewParticipantRequestLimiter(1, 10)
	limiter.ObserveResult("shared-host", "/sessions/12/chat/completions", http.StatusServiceUnavailable)

	rt := &subnetRuntime{
		id:              "12",
		model:           "Qwen/Test",
		participantKeys: []string{"shared-host", "other-host"},
	}
	g := NewGateway([]*subnetRuntime{rt}, NewGatewayLimiter(0, 0), "Qwen/Test")
	g.participantLimiter = limiter
	collector := newGatewayMetricsCollectorWithHostConnections(g, fakeHostConnectionSnapshotter(nil))

	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)

	families, err := registry.Gather()
	require.NoError(t, err)
	requireMetricGaugeValue(t, families, "subnet_gateway_participants_exhausted", nil, 1)
	requireMetricGaugeValue(t, families, "subnet_gateway_participants_tracked", nil, 1)
	requireMetricGaugeValue(t, families, "subnet_gateway_escrow_participant_limited", map[string]string{"subnet_id": "12", "model": "Qwen/Test"}, 1)
	requireMetricGaugeValue(t, families, "subnet_gateway_escrow_blocked_participants", map[string]string{"subnet_id": "12", "model": "Qwen/Test"}, 1)
}

func TestGatewayStatusCodeForErrorMapsUpstream503To429(t *testing.T) {
	code := gatewayStatusCodeForError(&transport.UpstreamStatusError{
		Path:       "/sessions/12/chat/completions",
		StatusCode: http.StatusServiceUnavailable,
		Body:       "nginx limit",
	})
	require.Equal(t, http.StatusTooManyRequests, code)
}

func TestGatewayMetricsCollectorIncludesHostConnectionSnapshots(t *testing.T) {
	g := NewGateway(nil, NewGatewayLimiter(0, 0), "Qwen/Test")
	collector := newGatewayMetricsCollectorWithHostConnections(g, fakeHostConnectionSnapshotter{
		{
			Address:        "10.1.2.3",
			Active:         2,
			Idle:           1,
			HoldAfterClose: 4,
			OpenTotal:      3,
		},
	})

	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)

	families, err := registry.Gather()
	require.NoError(t, err)
	requireMetricGaugeValue(t, families, "subnet_host_transport_open_connections", map[string]string{"address": "10.1.2.3"}, 3)
	requireMetricGaugeValue(t, families, "subnet_host_transport_connections", map[string]string{"address": "10.1.2.3", "state": "active"}, 2)
	requireMetricGaugeValue(t, families, "subnet_host_transport_connections", map[string]string{"address": "10.1.2.3", "state": "idle"}, 1)
	requireMetricGaugeValue(t, families, "subnet_host_transport_connections", map[string]string{"address": "10.1.2.3", "state": "hold_after_close"}, 4)
}

type fakeHostConnectionSnapshotter []transport.HostConnectionSnapshot

func (f fakeHostConnectionSnapshotter) Snapshots() []transport.HostConnectionSnapshot {
	return append([]transport.HostConnectionSnapshot(nil), f...)
}

func requireMetricGaugeValue(t *testing.T, families []*dto.MetricFamily, name string, labels map[string]string, want float64) {
	t.Helper()
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			if metricLabelsMatch(metric, labels) {
				require.NotNil(t, metric.Gauge)
				require.Equal(t, want, metric.Gauge.GetValue())
				return
			}
		}
	}
	t.Fatalf("metric %s with labels %v not found", name, labels)
}

func metricLabelsMatch(metric *dto.Metric, want map[string]string) bool {
	if metric == nil || len(metric.GetLabel()) != len(want) {
		return false
	}
	for _, label := range metric.GetLabel() {
		if want[label.GetName()] != label.GetValue() {
			return false
		}
	}
	return true
}
