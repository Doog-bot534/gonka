package main

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"subnet/bridge"
)

func TestBuildGatewayRuntimesDeactivatesMissingEscrow(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})

	require.NoError(t, store.Initialize(GatewaySettings{
		ChainREST:               "http://node:1317",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		MaxInputTokensInFlight:  200,
	}, []GatewaySubnetState{
		{RuntimeConfig: RuntimeConfig{ID: "12", PrivateKeyHex: "secret", Model: "Qwen/Test"}, Active: true},
		{RuntimeConfig: RuntimeConfig{ID: "24", PrivateKeyHex: "secret", Model: "Qwen/Test"}, Active: true},
	}))

	state, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)

	savedBuilder := gatewayRuntimeBuilder
	gatewayRuntimeBuilder = func(cfg RuntimeConfig, chainREST, defaultModel string) (*subnetRuntime, error) {
		switch cfg.ID {
		case "12":
			return nil, fmt.Errorf("runtime %s: create session: build group: get escrow: %w", cfg.ID, bridge.ErrEscrowNotFound)
		case "24":
			return &subnetRuntime{id: cfg.ID, model: defaultModel}, nil
		default:
			return nil, fmt.Errorf("unexpected runtime id %s", cfg.ID)
		}
	}
	t.Cleanup(func() {
		gatewayRuntimeBuilder = savedBuilder
	})

	runtimes, err := buildGatewayRuntimes(store, &state, t.TempDir())
	require.NoError(t, err)
	require.Len(t, runtimes, 1)
	require.Equal(t, "24", runtimes[0].id)
	require.False(t, state.Subnets[0].Active)
	require.True(t, state.Subnets[1].Active)

	reloaded, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, reloaded.Subnets[0].Active)
	require.True(t, reloaded.Subnets[1].Active)
}

func TestBuildGatewayRuntimesPreservesActiveOnOtherErrors(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})

	require.NoError(t, store.Initialize(GatewaySettings{
		ChainREST:               "http://node:1317",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		MaxInputTokensInFlight:  200,
	}, []GatewaySubnetState{
		{RuntimeConfig: RuntimeConfig{ID: "12", PrivateKeyHex: "secret", Model: "Qwen/Test"}, Active: true},
	}))

	state, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)

	savedBuilder := gatewayRuntimeBuilder
	gatewayRuntimeBuilder = func(cfg RuntimeConfig, chainREST, defaultModel string) (*subnetRuntime, error) {
		return nil, fmt.Errorf("runtime %s: create session: dial tcp timeout", cfg.ID)
	}
	t.Cleanup(func() {
		gatewayRuntimeBuilder = savedBuilder
	})

	_, err = buildGatewayRuntimes(store, &state, t.TempDir())
	require.Error(t, err)

	reloaded, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, reloaded.Subnets[0].Active)
}
