package propagation

import (
	"crypto/sha256"
	"encoding/binary"
)

type FLTQNode struct {
	Address    string
	Position   uint16
	Neighbors  []string
	Dimensions int
}

type FLTQCube struct {
	Index      int
	Dimensions int
	Size       int
	Nodes      map[string]*FLTQNode
	Positions  []*FLTQNode
}

func BuildFLTQ(participants []string, blockHash []byte) *FLTQCube {
	weightedParticipants := make([]WeightedParticipant, len(participants))
	for i, addr := range participants {
		weightedParticipants[i] = WeightedParticipant{
			Address: addr,
			Weight:  100,
		}
	}
	return BuildFLTQWithWeights(weightedParticipants, blockHash)
}

func BuildFLTQWithWeights(participants []WeightedParticipant, blockHash []byte) *FLTQCube {
	return buildFLTQWithIndex(0, participants, blockHash)
}

func BuildFLTQs(participants []string, blockHash []byte, numCubes int) []*FLTQCube {
	weightedParticipants := make([]WeightedParticipant, len(participants))
	for i, addr := range participants {
		weightedParticipants[i] = WeightedParticipant{
			Address: addr,
			Weight:  100,
		}
	}
	return BuildFLTQsWithWeights(weightedParticipants, blockHash, numCubes)
}

func BuildFLTQsWithWeights(participants []WeightedParticipant, blockHash []byte, numCubes int) []*FLTQCube {
	if len(participants) == 0 {
		return []*FLTQCube{}
	}

	cubes := make([]*FLTQCube, numCubes)
	for i := 0; i < numCubes; i++ {
		seed := makeFLTQSeed(blockHash, i)
		cubes[i] = buildFLTQWithIndex(i, participants, seed)
	}
	return cubes
}

func buildFLTQWithIndex(index int, participants []WeightedParticipant, blockHash []byte) *FLTQCube {
	if len(participants) == 0 {
		return &FLTQCube{
			Index:     index,
			Nodes:     make(map[string]*FLTQNode),
			Positions: []*FLTQNode{},
		}
	}

	realSize := len(participants)
	n := ceilLog2(realSize)
	cubeSize := 1 << n

	cube := &FLTQCube{
		Index:      index,
		Dimensions: n,
		Size:       cubeSize,
		Nodes:      make(map[string]*FLTQNode),
		Positions:  make([]*FLTQNode, cubeSize),
	}

	positionToParticipant := make([]string, cubeSize)
	shuffled := weightedDeterministicShuffle(participants, blockHash)
	for i := 0; i < realSize && i < cubeSize; i++ {
		positionToParticipant[i] = shuffled[i]
	}

	for pos := 0; pos < realSize && pos < cubeSize; pos++ {
		addr := positionToParticipant[pos]
		if addr == "" {
			continue
		}

		node := &FLTQNode{
			Address:    addr,
			Position:   uint16(pos),
			Neighbors:  make([]string, 0, n+1),
			Dimensions: n,
		}
		cube.Nodes[addr] = node
		cube.Positions[pos] = node
	}

	for pos := 0; pos < cubeSize; pos++ {
		node := cube.Positions[pos]
		if node == nil {
			continue
		}

		neighborsMap := make(map[string]bool)

		for dim := 0; dim < n; dim++ {
			neighborPos := ltqNeighbor(pos, dim, n)
			if neighborPos >= 0 && neighborPos < cubeSize && cube.Positions[neighborPos] != nil {
				neighborAddr := cube.Positions[neighborPos].Address
				if !neighborsMap[neighborAddr] && neighborAddr != node.Address {
					neighborsMap[neighborAddr] = true
				}
			}
		}

		complementPos := complementPosition(pos, n)
		if complementPos >= 0 && complementPos < cubeSize && cube.Positions[complementPos] != nil {
			neighborAddr := cube.Positions[complementPos].Address
			if !neighborsMap[neighborAddr] && neighborAddr != node.Address {
				neighborsMap[neighborAddr] = true
			}
		}

		for neighborAddr := range neighborsMap {
			node.Neighbors = append(node.Neighbors, neighborAddr)
		}
	}

	return cube
}

func ltqNeighbor(pos int, dim int, n int) int {
	if n <= 0 {
		return -1
	}

	if n == 1 {
		return pos ^ 1
	}

	if dim < n-1 {
		return pos ^ (1 << dim)
	}

	flipped := pos ^ (1 << (n - 1))
	lsb := pos & 1
	if lsb == 1 {
		flipped ^= (1 << (n - 2))
	}
	return flipped
}

func complementPosition(pos int, n int) int {
	if n <= 0 {
		return -1
	}
	mask := (1 << n) - 1
	return pos ^ mask
}

func makeFLTQSeed(blockHash []byte, cubeIndex int) []byte {
	h := sha256.New()
	h.Write(blockHash)
	h.Write([]byte("fltq"))
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], uint32(cubeIndex))
	h.Write(buf[:])
	return h.Sum(nil)
}

func (c *FLTQCube) GetNode(addr string) *FLTQNode {
	return c.Nodes[addr]
}

func (c *FLTQCube) Neighbors(addr string) []string {
	node := c.Nodes[addr]
	if node == nil {
		return nil
	}
	return node.Neighbors
}
