package host

import (
	"subnet/logging"
	"subnet/storage"
	"subnet/types"
)

// computeFinalizedNonce returns the highest nonce F such that >=2/3 of the
// session group (by slot count) has signed at nonce >= F.
//
// A slot that signed at nonce n is considered to have implicitly confirmed all
// nonces <= n by building on top of them.
func computeFinalizedNonce(store storage.Storage, escrowID string, latestNonce uint64, group []types.SlotAssignment) uint64 {
	// confirmedBy[n] = bitmap of slots that have signed at nonce >= n.
	// Bitmap128 is a value type (16 bytes); max group size is 128.
	confirmedBy := make(map[uint64]types.Bitmap128)

	for n := uint64(1); n <= latestNonce; n++ {
		sigs, err := store.GetSignatures(escrowID, n)
		if err != nil {
			logging.Error("get signatures failed", "subsystem", "host", "escrow_id", escrowID, "nonce", n, "error", err)
			continue
		}
		for slotID := range sigs {
			// This slot signed at n, confirming all nonces 1..n.
			for prev := uint64(1); prev <= n; prev++ {
				bm := confirmedBy[prev] // zero value if absent
				bm.Set(slotID)
				confirmedBy[prev] = bm
			}
		}
	}

	threshold := twoThirdsWeight(group)
	for f := latestNonce; f > 0; f-- {
		if bitmapSlotWeight(confirmedBy[f], group) >= threshold {
			return f
		}
	}
	return 0 // warm-up period: not yet finalized
}

// bitmapSlotWeight counts the number of group slots whose bit is set in bm.
func bitmapSlotWeight(bm types.Bitmap128, group []types.SlotAssignment) uint32 {
	var total uint32
	for _, sa := range group {
		if bm.IsSet(sa.SlotID) {
			total++
		}
	}
	return total
}

// twoThirdsWeight returns ceil(2/3 * totalSlots).
func twoThirdsWeight(group []types.SlotAssignment) uint32 {
	total := uint32(len(group))
	return (total*2 + 2) / 3
}
