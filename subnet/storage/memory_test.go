package storage

import (
	"testing"

	"github.com/stretchr/testify/require"

	"subnet/types"
)

func TestCreateSession_GetState(t *testing.T) {
	store := NewMemory()
	group := []types.SlotAssignment{
		{SlotID: 0, ValidatorAddress: "addr0", Weight: 1},
		{SlotID: 1, ValidatorAddress: "addr1", Weight: 1},
	}

	err := store.CreateSession("escrow-1", types.SessionConfig{}, group, 1000)
	require.NoError(t, err)

	state, err := store.GetState("escrow-1")
	require.NoError(t, err)
	require.Equal(t, "escrow-1", state.EscrowID)
	require.Equal(t, uint64(1000), state.Balance)
	require.Len(t, state.Group, 2)
	require.Equal(t, uint64(0), state.LatestNonce)
}

func TestAppendDiff_GetDiffs(t *testing.T) {
	store := NewMemory()
	group := []types.SlotAssignment{{SlotID: 0, ValidatorAddress: "addr0", Weight: 1}}
	err := store.CreateSession("escrow-1", types.SessionConfig{}, group, 1000)
	require.NoError(t, err)

	for i := uint64(1); i <= 5; i++ {
		err = store.AppendDiff("escrow-1", types.DiffRecord{
			Diff: types.Diff{
				Nonce:   i,
				UserSig: []byte("sig"),
			},
			StateHash:  []byte{byte(i)},
			Signatures: map[uint32][]byte{0: {byte(i)}},
		})
		require.NoError(t, err)
	}

	diffs, err := store.GetDiffs("escrow-1", 2, 4)
	require.NoError(t, err)
	require.Len(t, diffs, 3)
	require.Equal(t, uint64(2), diffs[0].Nonce)
	require.Equal(t, uint64(3), diffs[1].Nonce)
	require.Equal(t, uint64(4), diffs[2].Nonce)
}

func TestAddSignature(t *testing.T) {
	store := NewMemory()
	group := []types.SlotAssignment{{SlotID: 0, ValidatorAddress: "addr0", Weight: 1}}
	err := store.CreateSession("escrow-1", types.SessionConfig{}, group, 1000)
	require.NoError(t, err)

	err = store.AppendDiff("escrow-1", types.DiffRecord{
		Diff: types.Diff{
			Nonce:   1,
			UserSig: []byte("sig"),
		},
		StateHash:  []byte{0x01},
		Signatures: map[uint32][]byte{},
	})
	require.NoError(t, err)

	err = store.AddSignature("escrow-1", 1, 3, []byte("host-sig-3"))
	require.NoError(t, err)

	diffs, err := store.GetDiffs("escrow-1", 1, 1)
	require.NoError(t, err)
	require.Len(t, diffs, 1)
	require.Equal(t, []byte("host-sig-3"), diffs[0].Signatures[3])
}
