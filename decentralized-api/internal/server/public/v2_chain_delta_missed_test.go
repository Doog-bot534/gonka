package public

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateV2DeveloperChainDeltaForCurrentRequest_AcceptsMissedInferenceWithValidEvidence(t *testing.T) {
	delta := DeveloperChainDelta{
		BaseBlockSequence: 1,
		Blocks: []DeveloperChainBlock{
			{
				BlockSequence: 2,
				EscrowID:      "escrow-1",
				StateHash:     "placeholder",
				Signature:     "sig-2",
				Messages: []DeveloperChainMessage{
					{
						Type:                    v2ChainMessageTypeMissedInference,
						RequestID:               "escrow-1:1",
						MissedInferenceEvidence: `{"relay_errors":[{"relay_address":"gonka1relay","intended_executor_address":"gonka1intended"}]}`,
						Timestamp:               1710000001,
					},
					{
						Type:               v2ChainMessageTypeStartInference,
						RequestID:          "escrow-1:2",
						ModelID:            "model-1",
						RequestPayloadHash: "payload-hash-2",
						Timestamp:          1710000002,
					},
				},
			},
		},
		LatestBlockSequence: 2,
	}
	state := v2DeterministicChainState{}
	require.NoError(t, applyV2DeterministicStateBlock(&state, delta.Blocks[0].Messages))
	delta.Blocks[0].StateHash = computeV2DeterministicStateHashHex(state)

	called := false
	err := validateV2DeveloperChainDeltaForCurrentRequest(
		context.Background(),
		delta,
		1,
		"escrow-1:2",
		"payload-hash-2",
		"escrow-1",
		"model-1",
		"",
		"",
		map[string]struct{}{"escrow-1:1": {}},
		map[string]string{"escrow-1:1": "sig-1"},
		v2DeterministicChainState{},
		func(sequence uint64) (DeveloperChainBlock, bool) {
			return DeveloperChainBlock{}, false
		},
		nil,
		func(_ context.Context, message DeveloperChainMessage) error {
			called = true
			require.Equal(t, "escrow-1:1", message.RequestID)
			return nil
		},
	)
	require.NoError(t, err)
	require.True(t, called)
}

func TestValidateV2DeveloperChainDeltaForCurrentRequest_RejectsMissedInferenceWithInsufficientQuorum(t *testing.T) {
	delta := DeveloperChainDelta{
		BaseBlockSequence: 1,
		Blocks: []DeveloperChainBlock{
			{
				BlockSequence: 2,
				EscrowID:      "escrow-1",
				StateHash:     "placeholder",
				Signature:     "sig-2",
				Messages: []DeveloperChainMessage{
					{
						Type:                    v2ChainMessageTypeMissedInference,
						RequestID:               "escrow-1:1",
						MissedInferenceEvidence: `{"relay_errors":[{"relay_address":"gonka1relay","intended_executor_address":"gonka1intended"}]}`,
						Timestamp:               1710000001,
					},
					{
						Type:               v2ChainMessageTypeStartInference,
						RequestID:          "escrow-1:2",
						ModelID:            "model-1",
						RequestPayloadHash: "payload-hash-2",
						Timestamp:          1710000002,
					},
				},
			},
		},
		LatestBlockSequence: 2,
	}
	state := v2DeterministicChainState{}
	require.NoError(t, applyV2DeterministicStateBlock(&state, delta.Blocks[0].Messages))
	delta.Blocks[0].StateHash = computeV2DeterministicStateHashHex(state)

	err := validateV2DeveloperChainDeltaForCurrentRequest(
		context.Background(),
		delta,
		1,
		"escrow-1:2",
		"payload-hash-2",
		"escrow-1",
		"model-1",
		"",
		"",
		map[string]struct{}{"escrow-1:1": {}},
		map[string]string{"escrow-1:1": "sig-1"},
		v2DeterministicChainState{},
		func(sequence uint64) (DeveloperChainBlock, bool) {
			return DeveloperChainBlock{}, false
		},
		nil,
		func(_ context.Context, _ DeveloperChainMessage) error {
			return ErrV2MissedInferenceQuorumInsufficient
		},
	)
	require.ErrorIs(t, err, ErrV2MissedInferenceQuorumInsufficient)
}
