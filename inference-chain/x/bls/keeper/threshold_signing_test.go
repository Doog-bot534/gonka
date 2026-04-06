package keeper_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/bls/types"
)

func TestRequestThresholdSignature_RejectsCompletedEpoch(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	const epochID = uint64(77)
	err := k.SetEpochBLSData(ctx, types.EpochBLSData{
		EpochId:        epochID,
		DkgPhase:       types.DKGPhase_DKG_PHASE_COMPLETED,
		GroupPublicKey: []byte{1},
	})
	require.NoError(t, err)

	err = k.RequestThresholdSignature(ctx, types.SigningData{
		CurrentEpochId: epochID,
		ChainId:        bytes.Repeat([]byte{1}, 32),
		RequestId:      bytes.Repeat([]byte{2}, 32),
		Data:           [][]byte{bytes.Repeat([]byte{3}, 32)},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "is not signed")
}

func TestRequestThresholdSignature_AllowsSignedEpoch(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	const epochID = uint64(78)
	signingData := types.SigningData{
		CurrentEpochId: epochID,
		ChainId:        bytes.Repeat([]byte{4}, 32),
		RequestId:      bytes.Repeat([]byte{5}, 32),
		Data:           [][]byte{bytes.Repeat([]byte{6}, 32)},
	}

	err := k.SetEpochBLSData(ctx, types.EpochBLSData{
		EpochId:        epochID,
		DkgPhase:       types.DKGPhase_DKG_PHASE_SIGNED,
		GroupPublicKey: []byte{1},
	})
	require.NoError(t, err)

	err = k.RequestThresholdSignature(ctx, signingData)
	require.NoError(t, err)

	request, err := k.GetSigningStatus(ctx, signingData.RequestId)
	require.NoError(t, err)
	require.Equal(t, types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES, request.Status)
	require.Equal(t, epochID, request.CurrentEpochId)
}
