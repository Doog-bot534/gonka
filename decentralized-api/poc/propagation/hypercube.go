package propagation

import (
	"crypto/sha256"
	"encoding/binary"
	"math"
	"math/rand"
)

type HypercubeNode struct {
	Address    string
	Position   uint16
	Neighbors  []string
	Dimensions int
}

type Hypercube struct {
	Index      int
	Dimensions int
	Size       int
	Nodes      map[string]*HypercubeNode
	Positions  []*HypercubeNode
}

func BuildHypercubes(participants []string, blockHash []byte, numHypercubes int) []*Hypercube {
	weightedParticipants := make([]WeightedParticipant, len(participants))
	for i, addr := range participants {
		weightedParticipants[i] = WeightedParticipant{
			Address: addr,
			Weight:  100,
		}
	}
	return BuildHypercubesWithWeights(weightedParticipants, blockHash, numHypercubes)
}

func BuildHypercubesWithWeights(participants []WeightedParticipant, blockHash []byte, numHypercubes int) []*Hypercube {
	if len(participants) == 0 {
		return []*Hypercube{}
	}

	hypercubes := make([]*Hypercube, numHypercubes)
	for i := 0; i < numHypercubes; i++ {
		seed := makeHypercubeSeed(blockHash, i)
		hypercubes[i] = buildHypercubeWithIndex(i, participants, seed)
	}
	return hypercubes
}

func BuildHypercube(participants []string, blockHash []byte) *Hypercube {
	weightedParticipants := make([]WeightedParticipant, len(participants))
	for i, addr := range participants {
		weightedParticipants[i] = WeightedParticipant{
			Address: addr,
			Weight:  100,
		}
	}
	return BuildHypercubeWithWeights(weightedParticipants, blockHash)
}

func BuildHypercubeWithWeights(participants []WeightedParticipant, blockHash []byte) *Hypercube {
	return buildHypercubeWithIndex(0, participants, blockHash)
}

func buildHypercubeWithIndex(index int, participants []WeightedParticipant, blockHash []byte) *Hypercube {
	if len(participants) == 0 {
		return &Hypercube{
			Index:     index,
			Nodes:     make(map[string]*HypercubeNode),
			Positions: []*HypercubeNode{},
		}
	}

	realSize := len(participants)
	d := ceilLog2(realSize)
	cubeSize := 1 << d

	hc := &Hypercube{
		Index:      index,
		Dimensions: d,
		Size:       cubeSize,
		Nodes:      make(map[string]*HypercubeNode),
		Positions:  make([]*HypercubeNode, cubeSize),
	}

	positionToParticipant := make([]string, cubeSize)
	for dim := 0; dim < d; dim++ {
		seed := makeDimensionSeed(blockHash, dim)
		shuffled := weightedDeterministicShuffle(participants, seed)

		for i, addr := range shuffled {
			if i >= cubeSize {
				break
			}
			positionToParticipant[i] = addr
		}
	}

	lastDimSeed := makeDimensionSeed(blockHash, d-1)
	shuffled := weightedDeterministicShuffle(participants, lastDimSeed)
	for i := 0; i < realSize && i < cubeSize; i++ {
		positionToParticipant[i] = shuffled[i]
	}

	for pos := 0; pos < realSize && pos < cubeSize; pos++ {
		addr := positionToParticipant[pos]
		if addr == "" {
			continue
		}

		node := &HypercubeNode{
			Address:    addr,
			Position:   uint16(pos),
			Neighbors:  make([]string, 0, d),
			Dimensions: d,
		}
		hc.Nodes[addr] = node
		hc.Positions[pos] = node
	}

	for pos := 0; pos < cubeSize; pos++ {
		node := hc.Positions[pos]
		if node == nil {
			continue
		}

		neighborsMap := make(map[string]bool)
		for dim := 0; dim < d; dim++ {
			neighborPos := pos ^ (1 << dim)
			if neighborPos < cubeSize && hc.Positions[neighborPos] != nil {
				neighborAddr := hc.Positions[neighborPos].Address
				if !neighborsMap[neighborAddr] && neighborAddr != node.Address {
					neighborsMap[neighborAddr] = true
				}
			}
		}

		for neighborAddr := range neighborsMap {
			node.Neighbors = append(node.Neighbors, neighborAddr)
		}
	}

	return hc
}

func makeHypercubeSeed(blockHash []byte, hypercubeIndex int) []byte {
	h := sha256.New()
	h.Write(blockHash)
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], uint32(hypercubeIndex))
	h.Write(buf[:])
	return h.Sum(nil)
}

func makeDimensionSeed(blockHash []byte, dimensionIndex int) []byte {
	h := sha256.New()
	h.Write(blockHash)
	h.Write([]byte("hypercube"))
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], uint32(dimensionIndex))
	h.Write(buf[:])
	sum := h.Sum(nil)
	return sum
}

func ceilLog2(n int) int {
	if n <= 1 {
		return 0
	}
	return int(math.Ceil(math.Log2(float64(n))))
}

func (hc *Hypercube) GetNode(addr string) *HypercubeNode {
	return hc.Nodes[addr]
}

func (hc *Hypercube) Neighbors(addr string) []string {
	node := hc.Nodes[addr]
	if node == nil {
		return nil
	}
	return node.Neighbors
}

func weightedDeterministicShuffleForDimension(participants []WeightedParticipant, seed []byte) []string {
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
		randomComponent := rng.Float64() * float64(p.Weight) * 0.5
		items[i] = indexed{
			participant: p,
			randomScore: baseScore + randomComponent,
		}
	}

	for i := 0; i < n-1; i++ {
		for j := i + 1; j < n; j++ {
			if items[j].randomScore > items[i].randomScore {
				items[i], items[j] = items[j], items[i]
			}
		}
	}

	result := make([]string, n)
	for i, item := range items {
		result[i] = item.participant.Address
	}

	return result
}
