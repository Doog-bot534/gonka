package keeper_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestInvalidateEscrow_SetsInvalidatedState(t *testing.T) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)
	ctx = ctx.WithChainID("inference-test-1")
	k.SetModel(ctx, &types.Model{Id: "model-1", ProposedBy: "test", UnitsOfComputePerToken: 1})

	createResp, err := ms.CreateEscrow(ctx, &types.MsgCreateEscrow{
		Creator: testutil.Creator,
		ModelId: "model-1",
	})
	require.NoError(t, err)

	developerAccount := NewMockAccount(testutil.Creator)
	mocks.AccountKeeper.EXPECT().
		GetAccount(gomock.Any(), sdk.MustAccAddressFromBech32(testutil.Creator)).
		Return(developerAccount).
		Times(1)

	leftBlockMessagesHash := sha256.Sum256([]byte("left-messages"))
	rightBlockMessagesHash := sha256.Sum256([]byte("right-messages"))
	leftBlockHash := computeConflictBlockHashForTest(ctx.ChainID(), createResp.EscrowId, 1, leftBlockMessagesHash)
	rightBlockHash := computeConflictBlockHashForTest(ctx.ChainID(), createResp.EscrowId, 1, rightBlockMessagesHash)
	leftBlockSignature, err := developerAccount.SignBytes([]byte(leftBlockHash))
	require.NoError(t, err)
	rightBlockSignature, err := developerAccount.SignBytes([]byte(rightBlockHash))
	require.NoError(t, err)

	_, err = ms.InvalidateEscrow(ctx, &types.MsgInvalidateEscrow{
		Creator:                testutil.Executor,
		EscrowId:               createResp.EscrowId,
		DeveloperAddress:       testutil.Creator,
		BlockSequence:          1,
		LeftBlockHash:          leftBlockHash,
		RightBlockHash:         rightBlockHash,
		LeftBlockSignature:     leftBlockSignature,
		RightBlockSignature:    rightBlockSignature,
		LeftBlockMessagesHash:  fmt.Sprintf("%x", leftBlockMessagesHash[:]),
		RightBlockMessagesHash: fmt.Sprintf("%x", rightBlockMessagesHash[:]),
	})
	require.NoError(t, err)

	invalidated, err := k.EscrowInvalidatedByID.Has(ctx, createResp.EscrowId)
	require.NoError(t, err)
	require.True(t, invalidated)
}

func TestInvalidateEscrow_RejectsWrongDeveloper(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	ctx = ctx.WithChainID("inference-test-1")
	k.SetModel(ctx, &types.Model{Id: "model-1", ProposedBy: "test", UnitsOfComputePerToken: 1})

	createResp, err := ms.CreateEscrow(ctx, &types.MsgCreateEscrow{
		Creator: testutil.Creator,
		ModelId: "model-1",
	})
	require.NoError(t, err)

	_, err = ms.InvalidateEscrow(ctx, &types.MsgInvalidateEscrow{
		Creator:                testutil.Executor,
		EscrowId:               createResp.EscrowId,
		DeveloperAddress:       testutil.Executor,
		BlockSequence:          1,
		LeftBlockHash:          "aaa",
		RightBlockHash:         "bbb",
		LeftBlockSignature:     "sig-left",
		RightBlockSignature:    "sig-right",
		LeftBlockMessagesHash:  fmt.Sprintf("%x", sha256.Sum256([]byte("left"))),
		RightBlockMessagesHash: fmt.Sprintf("%x", sha256.Sum256([]byte("right"))),
	})
	require.Error(t, err)
	require.ErrorIs(t, err, types.ErrEscrowConflictEvidenceInvalid)
}

func TestInvalidateEscrow_RejectsInvalidDeveloperSignature(t *testing.T) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)
	ctx = ctx.WithChainID("inference-test-1")
	k.SetModel(ctx, &types.Model{Id: "model-1", ProposedBy: "test", UnitsOfComputePerToken: 1})

	createResp, err := ms.CreateEscrow(ctx, &types.MsgCreateEscrow{
		Creator: testutil.Creator,
		ModelId: "model-1",
	})
	require.NoError(t, err)

	developerAccount := NewMockAccount(testutil.Creator)
	attackerAccount := NewMockAccount(testutil.Executor)
	mocks.AccountKeeper.EXPECT().
		GetAccount(gomock.Any(), sdk.MustAccAddressFromBech32(testutil.Creator)).
		Return(developerAccount).
		Times(1)

	leftBlockMessagesHash := sha256.Sum256([]byte("left-messages"))
	rightBlockMessagesHash := sha256.Sum256([]byte("right-messages"))
	leftBlockHash := computeConflictBlockHashForTest(ctx.ChainID(), createResp.EscrowId, 1, leftBlockMessagesHash)
	rightBlockHash := computeConflictBlockHashForTest(ctx.ChainID(), createResp.EscrowId, 1, rightBlockMessagesHash)
	leftBlockSignature, err := developerAccount.SignBytes([]byte(leftBlockHash))
	require.NoError(t, err)
	// Sign right block hash with a different key to prove cryptographic validation is enforced.
	rightBlockSignature, err := attackerAccount.SignBytes([]byte(rightBlockHash))
	require.NoError(t, err)

	_, err = ms.InvalidateEscrow(ctx, &types.MsgInvalidateEscrow{
		Creator:                testutil.Executor,
		EscrowId:               createResp.EscrowId,
		DeveloperAddress:       testutil.Creator,
		BlockSequence:          1,
		LeftBlockHash:          leftBlockHash,
		RightBlockHash:         rightBlockHash,
		LeftBlockSignature:     leftBlockSignature,
		RightBlockSignature:    rightBlockSignature,
		LeftBlockMessagesHash:  fmt.Sprintf("%x", leftBlockMessagesHash[:]),
		RightBlockMessagesHash: fmt.Sprintf("%x", rightBlockMessagesHash[:]),
	})
	require.Error(t, err)
	require.ErrorIs(t, err, types.ErrEscrowConflictEvidenceInvalid)
}

func computeConflictBlockHashForTest(
	chainID string,
	escrowID string,
	blockSequence uint64,
	blockMessagesHash [32]byte,
) string {
	preimage := buildConflictBlockSigningPreimageForTest(chainID, escrowID, blockSequence, blockMessagesHash)
	hash := sha256.Sum256(preimage)
	return fmt.Sprintf("%x", hash[:])
}

func buildConflictBlockSigningPreimageForTest(
	chainID string,
	escrowID string,
	blockSequence uint64,
	blockMessagesHash [32]byte,
) []byte {
	var buffer bytes.Buffer
	writeLengthPrefixedStringForTest(&buffer, "v2_dev_block_sig_v1")
	writeLengthPrefixedStringForTest(&buffer, chainID)
	writeLengthPrefixedStringForTest(&buffer, escrowID)
	writeUint64ForTest(&buffer, blockSequence)
	_, _ = buffer.Write(blockMessagesHash[:])
	return buffer.Bytes()
}

func writeLengthPrefixedStringForTest(buffer *bytes.Buffer, value string) {
	var lengthBytes [4]byte
	binary.BigEndian.PutUint32(lengthBytes[:], uint32(len(value)))
	_, _ = buffer.Write(lengthBytes[:])
	_, _ = buffer.WriteString(value)
}

func writeUint64ForTest(buffer *bytes.Buffer, value uint64) {
	var valueBytes [8]byte
	binary.BigEndian.PutUint64(valueBytes[:], value)
	_, _ = buffer.Write(valueBytes[:])
}
