package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGatewayStoreInitializeAndLoadState(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})

	settings := GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1234,
		MaxConcurrentRequests:   5,
		MaxInputTokensInFlight:  999,
	}
	subnets := []GatewaySubnetState{{
		RuntimeConfig: RuntimeConfig{
			ID:            "12",
			PrivateKeyHex: "secret",
			Model:         "Qwen/Test",
			StoragePath:   "/root/.subnetctl/escrow-12/state.db",
		},
		Active: true,
	}}

	require.NoError(t, store.Initialize(settings, subnets))

	state, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, settings, state.Settings)
	require.Len(t, state.Subnets, 1)
	require.Equal(t, "12", state.Subnets[0].ID)
	require.True(t, state.Subnets[0].Active)
	require.Equal(t, "/root/.subnetctl/escrow-12/state.db", state.Subnets[0].StoragePath)
}

func TestAdminAuthMiddlewareRequiresAdminKey(t *testing.T) {
	handler := adminAuthMiddleware("adminkey", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/state", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	req = httptest.NewRequest(http.MethodGet, "/v1/admin/state", nil)
	req.Header.Set("Authorization", "Bearer adminkey")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)
}

func TestGatewayStoreUpdateSettings(t *testing.T) {
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

	require.NoError(t, store.UpdateSettings(GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 2000,
		MaxConcurrentRequests:   5,
		MaxInputTokensInFlight:  500,
	}))

	state, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)
	require.EqualValues(t, 2000, state.Settings.DefaultRequestMaxTokens)
	require.EqualValues(t, 5, state.Settings.MaxConcurrentRequests)
	require.EqualValues(t, 500, state.Settings.MaxInputTokensInFlight)
}
