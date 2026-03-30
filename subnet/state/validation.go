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

// uint64ProbabilityScale32 returns floor(numerator * 2^32 / denominator), clamped to [0, 2^32].
// It represents a rational in [0, 1] at 32-bit fixed-point scale without floating-point.
// denominator must be non-zero; callers guard that.
func uint64ProbabilityScale32(numerator, denominator uint64) uint64 {
	p := (numerator << 32) / denominator
	const maxP = uint64(1) << 32
	if p > maxP {
		return maxP
	}
	return p
}

// ShouldValidate returns true if this validator should validate the given inference.
// Uses integer math only (no float64) to avoid architecture-dependent state root splits.
//
// Float reference (not used at runtime):
//   rate = rateBasisPoints / 10000
//   probability = rate * validatorSlotCount / (totalSlots - executorSlotCount)
// Combined (single division):
//   probability = (rateBasisPoints * validatorSlotCount) / ((totalSlots - executorSlotCount) * 10000)
//
// Conceptually: accept iff deterministicHash(seed, id) / 2^64 < probability (uniform draw in [0,1)).
// Implemented with 32-bit precision: (hash >> 32) < floor(probability * 2^32), using uint64ProbabilityScale32.
func ShouldValidate(seed int64, inferenceID uint64, validatorSlotCount, executorSlotCount, totalSlots, rateBasisPoints uint32) bool {
	if totalSlots <= executorSlotCount {
		return false
	}
	numer := uint64(rateBasisPoints) * uint64(validatorSlotCount)
	denom := uint64(totalSlots-executorSlotCount) * 10000
	threshold := uint64ProbabilityScale32(numer, denom)
	hashInt := deterministicHash(seed, inferenceID)
	return (hashInt >> 32) < threshold
}
