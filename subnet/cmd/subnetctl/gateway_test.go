package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
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

func TestGatewayLimiterEnforcesConcurrentAndTokenLimits(t *testing.T) {
	limiter := NewGatewayLimiter(1, 10)

	require.NoError(t, limiter.Acquire(8))
	require.ErrorContains(t, limiter.Acquire(1), "too many concurrent requests")
	limiter.Release(8)

	tokenLimiter := NewGatewayLimiter(2, 10)
	require.NoError(t, tokenLimiter.Acquire(5))
	require.ErrorContains(t, tokenLimiter.Acquire(6), "too many input tokens in flight")
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
