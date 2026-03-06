package public

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestComputeV2DeterministicStateHashHex_DeterministicOrder(t *testing.T) {
	stateA := v2DeterministicChainState{
		executorStats: map[string]v2ExecutorDeterministicStats{
			"gonka1b": {processedInferences: 3, inputTokenTotal: 33, outputTokenTotal: 7, missedInferences: 1},
			"gonka1a": {processedInferences: 1, inputTokenTotal: 11, outputTokenTotal: 5, missedInferences: 0},
		},
	}
	stateB := v2DeterministicChainState{
		executorStats: map[string]v2ExecutorDeterministicStats{
			"gonka1a": {processedInferences: 1, inputTokenTotal: 11, outputTokenTotal: 5, missedInferences: 0},
			"gonka1b": {processedInferences: 3, inputTokenTotal: 33, outputTokenTotal: 7, missedInferences: 1},
		},
	}

	require.Equal(t, computeV2DeterministicStateHashHex(stateA), computeV2DeterministicStateHashHex(stateB))
}

func TestValidateV2DeveloperChainDeltaForCurrentRequest_RejectsStateHashMismatch(t *testing.T) {
	delta := DeveloperChainDelta{
		BaseBlockSequence: 0,
		Blocks: []DeveloperChainBlock{
			{
				BlockSequence: 1,
				EscrowID:      "escrow-1",
				StateHash:     "deadbeef",
				Signature:     "sig-1",
				Messages: []DeveloperChainMessage{
					{
						Type:               v2ChainMessageTypeStartInference,
						RequestID:          "escrow-1:1",
						ModelID:            "model-1",
						RequestPayloadHash: "payload-hash-1",
						Timestamp:          1710000001,
					},
				},
			},
		},
		LatestBlockSequence: 1,
	}

	err := validateV2DeveloperChainDeltaForCurrentRequest(
		context.Background(),
		delta,
		0,
		"escrow-1:1",
		"payload-hash-1",
		"escrow-1",
		"model-1",
		"",
		"",
		map[string]struct{}{},
		map[string]string{},
		v2DeterministicChainState{},
		func(sequence uint64) (DeveloperChainBlock, bool) {
			return DeveloperChainBlock{}, false
		},
		nil,
		nil,
	)
	require.ErrorIs(t, err, ErrV2DeveloperBlockStateHashInvalid)
}

func TestValidateV2DeveloperChainDeltaForCurrentRequest_AcceptsValidStateHash(t *testing.T) {
	block := DeveloperChainBlock{
		BlockSequence: 1,
		EscrowID:      "escrow-1",
		Signature:     "sig-1",
		Messages: []DeveloperChainMessage{
			{
				Type:               v2ChainMessageTypeStartInference,
				RequestID:          "escrow-1:1",
				ModelID:            "model-1",
				RequestPayloadHash: "payload-hash-1",
				Timestamp:          1710000001,
			},
		},
	}
	state := v2DeterministicChainState{}
	require.NoError(t, applyV2DeterministicStateBlock(&state, block.Messages))
	block.StateHash = computeV2DeterministicStateHashHex(state)

	delta := DeveloperChainDelta{
		BaseBlockSequence:   0,
		Blocks:              []DeveloperChainBlock{block},
		LatestBlockSequence: 1,
	}

	err := validateV2DeveloperChainDeltaForCurrentRequest(
		context.Background(),
		delta,
		0,
		"escrow-1:1",
		"payload-hash-1",
		"escrow-1",
		"model-1",
		"",
		"",
		map[string]struct{}{},
		map[string]string{},
		v2DeterministicChainState{},
		func(sequence uint64) (DeveloperChainBlock, bool) {
			return DeveloperChainBlock{}, false
		},
		nil,
		nil,
	)
	require.NoError(t, err)
}
