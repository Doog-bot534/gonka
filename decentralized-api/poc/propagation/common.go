package propagation

import (
	"encoding/binary"
	"math"
	"math/rand"
	"sort"
)

type PubKeyProvider interface {
	GetPubKey(participantAddr string) (string, error)
}

type WeightedParticipant struct {
	Address string
	Weight  uint64
}

func ceilLog2(n int) int {
	if n <= 1 {
		return 0
	}
	return int(math.Ceil(math.Log2(float64(n))))
}

func weightedDeterministicShuffle(participants []WeightedParticipant, seed []byte) []string {
	n := len(participants)
	if n == 0 {
		return []string{}
	}

	type indexed struct {
		participant WeightedParticipant
		randomScore float64
	}

	items := make([]indexed, n)
	rng := rand.New(rand.NewSource(int64(binary.BigEndian.Uint64(seed[:8]))))

	for i, p := range participants {
		baseScore := float64(p.Weight)
		randomComponent := rng.Float64() * float64(p.Weight) * 0.3
		items[i] = indexed{
			participant: p,
			randomScore: baseScore + randomComponent,
		}
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].randomScore > items[j].randomScore
	})

	result := make([]string, n)
	for i, item := range items {
		result[i] = item.participant.Address
	}

	return result
}
