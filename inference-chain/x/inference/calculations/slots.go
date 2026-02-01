package calculations

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
)

type weightEntry struct {
	address string
	weight  int64
}

type slotRandom struct {
	randomVal int64
	origIdx   int
}

// GetSlots returns deterministically sampled validators for a participant using weighted random selection.
// Matches the Python reference implementation in proposals/poc/optimize.py.
func GetSlots(appHash, participantAddress string, weights map[string]int64, nSlots int) []string {
	if len(weights) == 0 || nSlots == 0 {
		return nil
	}

	entries := make([]weightEntry, 0, len(weights))
	var totalWeight int64
	for addr, w := range weights {
		entries = append(entries, weightEntry{addr, w})
		totalWeight += w
	}
	if totalWeight == 0 {
		return nil
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].address < entries[j].address
	})

	randoms := make([]slotRandom, nSlots)
	for i := 0; i < nSlots; i++ {
		randoms[i] = slotRandom{
			randomVal: slotRandomVal(appHash, participantAddress, i, totalWeight),
			origIdx:   i,
		}
	}
	sort.Slice(randoms, func(i, j int) bool {
		return randoms[i].randomVal < randoms[j].randomVal
	})

	result := make([]string, nSlots)
	cumulative := int64(0)
	randIdx := 0

	for _, entry := range entries {
		cumulative += entry.weight
		for randIdx < len(randoms) && randoms[randIdx].randomVal < cumulative {
			result[randoms[randIdx].origIdx] = entry.address
			randIdx++
		}
	}

	return result
}

// GetSlot returns a single slot by index.
func GetSlot(appHash, participantAddress string, weights map[string]int64, slotIdx int) string {
	if len(weights) == 0 {
		return ""
	}

	entries := make([]weightEntry, 0, len(weights))
	var totalWeight int64
	for addr, w := range weights {
		entries = append(entries, weightEntry{addr, w})
		totalWeight += w
	}
	if totalWeight == 0 {
		return ""
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].address < entries[j].address
	})

	randomVal := slotRandomVal(appHash, participantAddress, slotIdx, totalWeight)

	cumulative := int64(0)
	for _, entry := range entries {
		cumulative += entry.weight
		if randomVal < cumulative {
			return entry.address
		}
	}

	return entries[len(entries)-1].address
}

func slotRandomVal(appHash, participantAddress string, slotIdx int, totalWeight int64) int64 {
	seedData := fmt.Sprintf("%s%s%d", appHash, participantAddress, slotIdx)
	hash := sha256.Sum256([]byte(seedData))
	// Use uint64 for modulo to avoid negative values
	return int64(binary.BigEndian.Uint64(hash[:8]) % uint64(totalWeight))
}
