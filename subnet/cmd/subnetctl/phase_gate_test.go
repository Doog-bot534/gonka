package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChainPhaseGateFetchEpochInfoParsesConfirmationPoC(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/epochs/latest", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"block_height":"150",
			"phase":"Inference",
			"latest_epoch":{
				"index":"12",
				"poc_start_block_height":"100"
			},
			"is_confirmation_poc_active":true,
			"active_confirmation_poc_event":{
				"phase":"CONFIRMATION_POC_VALIDATION"
			}
		}`))
	}))
	defer server.Close()

	gate := NewChainPhaseGate(server.URL, 0)
	require.NotNil(t, gate)

	resp, err := gate.fetchEpochInfo()
	require.NoError(t, err)

	snapshot := deriveChainPhaseSnapshot(resp)
	require.Equal(t, int64(150), snapshot.BlockHeight)
	require.Equal(t, uint64(12), snapshot.EpochIndex)
	require.Equal(t, epochPhaseInference, snapshot.EpochPhase)
	require.Equal(t, confirmationPoCValidation, snapshot.ConfirmationPoCPhase)
	require.True(t, snapshot.RequestsBlocked)
	require.Equal(t, "confirmation_poc", snapshot.BlockReason)
}

func TestChainPhaseGateBlocksDuringRegularPoC(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/epochs/latest", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"block_height":"105",
			"phase":"PoCGenerate",
			"latest_epoch":{
				"index":"12",
				"poc_start_block_height":"100"
			},
			"is_confirmation_poc_active":false
		}`))
	}))
	defer server.Close()

	gate := NewChainPhaseGate(server.URL, 0)
	require.NotNil(t, gate)

	resp, err := gate.fetchEpochInfo()
	require.NoError(t, err)

	snapshot := deriveChainPhaseSnapshot(resp)
	require.Equal(t, epochPhasePoCGenerate, snapshot.EpochPhase)
	require.True(t, snapshot.RequestsBlocked)
	require.Equal(t, "poc", snapshot.BlockReason)
}

func TestChainPhaseGateTemporarilyLimitsSpeculativeAttempts(t *testing.T) {
	previous := CurrentMaxSpeculativeAttempts()
	SetMaxSpeculativeAttempts(4)
	t.Cleanup(func() {
		SetMaxSpeculativeAttempts(previous)
	})

	gate := NewChainPhaseGate("http://api:9000", 0)
	require.NotNil(t, gate)
	require.Equal(t, 4, gate.defaultMaxSpeculativeAttempts)

	gate.applySpeculativeAttemptPolicy(ChainPhaseSnapshot{
		EpochPhase:      epochPhasePoCGenerate,
		RequestsBlocked: true,
		BlockReason:     "poc",
	})
	require.Equal(t, 1, CurrentMaxSpeculativeAttempts())

	gate.applySpeculativeAttemptPolicy(ChainPhaseSnapshot{
		EpochPhase:      epochPhaseInference,
		RequestsBlocked: false,
	})
	require.Equal(t, 4, CurrentMaxSpeculativeAttempts())
}
