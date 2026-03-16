package state

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"subnet/types"
)

// DeriveSeed extracts a deterministic non-zero int64 seed from a signature.
// Takes first 8 bytes, masks to positive, ensures non-zero.
func DeriveSeed(signature []byte) (int64, error) {
	if len(signature) < 8 {
		return 0, types.ErrSeedTooShort
	}
	raw := binary.BigEndian.Uint64(signature[:8])
	seed := int64(raw & ((1 << 63) - 1))
	if seed == 0 {
		seed = 1
	}
	return seed, nil
}

// deterministicHash returns a deterministic uint64 from seed and inferenceID.
// Uses sha256("%d:%d") -> first 8 bytes as big-endian uint64.
// Used for integer-only consensus logic (no float math across architectures).
func deterministicHash(seed int64, inferenceID uint64) uint64 {
	input := fmt.Sprintf("%d:%d", seed, inferenceID)
	sum := sha256.Sum256([]byte(input))
	return binary.BigEndian.Uint64(sum[:8])
}

// ShouldValidate returns true if this validator should validate the given inference.
// Uses integer math only (no float64) to avoid architecture-dependent state root splits.
// probability = (rateBasisPoints/10000) * validatorSlotCount / (totalSlots - executorSlotCount)
// Equivalent to: deterministicHash(seed,id)/2^64 < probability
// Implemented as: (hash >> 32) < (probability * 2^32) using integer arithmetic.
func ShouldValidate(seed int64, inferenceID uint64, validatorSlotCount, executorSlotCount, totalSlots, rateBasisPoints uint32) bool {
	if totalSlots <= executorSlotCount {
		return false
	}
	denom := uint64(totalSlots - executorSlotCount) * 10000
	// threshold = (rateBasisPoints * validatorSlotCount * 2^32) / (10000 * (totalSlots - executorSlotCount))
	threshold := (uint64(rateBasisPoints) * uint64(validatorSlotCount) << 32) / denom
	const maxThreshold = 1 << 32
	if threshold > maxThreshold {
		threshold = maxThreshold
	}
	hashInt := deterministicHash(seed, inferenceID)
	return (hashInt >> 32) < threshold
}
