package propagation

import (
	"crypto/sha256"
	"encoding/binary"
	"math/bits"
	"math/rand"
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

	buildShortcutEdges(cube, blockHash, 2)

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

func hammingDistance(a, b int) int {
	return bits.OnesCount(uint(a ^ b))
}

func randomBitmaskWithKBits(rng *rand.Rand, k, n int) int {
	if k > n {
		k = n
	}
	if k <= 0 {
		return 0
	}

	bitPositions := make([]int, n)
	for i := 0; i < n; i++ {
		bitPositions[i] = i
	}

	for i := 0; i < k; i++ {
		j := i + rng.Intn(n-i)
		bitPositions[i], bitPositions[j] = bitPositions[j], bitPositions[i]
	}

	mask := 0
	for i := 0; i < k; i++ {
		mask |= 1 << bitPositions[i]
	}
	return mask
}

func binomialCoefficient(n, k int) int {
	if k > n || k < 0 {
		return 0
	}
	if k == 0 || k == n {
		return 1
	}
	if k > n-k {
		k = n - k
	}
	result := 1
	for i := 0; i < k; i++ {
		result = result * (n - i) / (i + 1)
	}
	return result
}

func enumeratePositionsAtDistance(pos, d, n int, occupied []bool, maxCandidates int) []int {
	if d > n || d < 0 {
		return []int{}
	}

	candidates := []int{}
	combination := make([]int, d)
	for i := 0; i < d; i++ {
		combination[i] = i
	}

	for {
		mask := 0
		for _, bit := range combination {
			mask |= 1 << bit
		}
		target := pos ^ mask
		if target < len(occupied) && occupied[target] {
			candidates = append(candidates, target)
			if len(candidates) >= maxCandidates {
				return candidates
			}
		}

		i := d - 1
		for i >= 0 && combination[i] == n-d+i {
			i--
		}
		if i < 0 {
			break
		}
		combination[i]++
		for j := i + 1; j < d; j++ {
			combination[j] = combination[j-1] + 1
		}
	}

	return candidates
}

func findCandidatesAtDistance(pos, d, n int, occupied []bool, seed []byte, maxCandidates int) []int {
	if d > n || d < 0 {
		return []int{}
	}

	totalCombinations := binomialCoefficient(n, d)

	if totalCombinations <= 500 || d <= 2 || d >= n-2 {
		return enumeratePositionsAtDistance(pos, d, n, occupied, maxCandidates)
	}

	h := sha256.New()
	h.Write(seed)
	var buf [8]byte
	binary.BigEndian.PutUint32(buf[:4], uint32(pos))
	binary.BigEndian.PutUint32(buf[4:], uint32(d))
	h.Write(buf[:])
	rngSeed := binary.BigEndian.Uint64(h.Sum(nil)[:8])
	rng := rand.New(rand.NewSource(int64(rngSeed)))

	candidates := []int{}
	seen := make(map[int]bool)
	maxAttempts := 100

	for attempt := 0; attempt < maxAttempts && len(candidates) < maxCandidates; attempt++ {
		mask := randomBitmaskWithKBits(rng, d, n)
		target := pos ^ mask
		if target < len(occupied) && occupied[target] && !seen[target] {
			candidates = append(candidates, target)
			seen[target] = true
		}
	}

	return candidates
}

func deterministicSelectMultiple(candidates []int, seed []byte, pos, d, count int) []int {
	if len(candidates) == 0 {
		return []int{}
	}
	if count > len(candidates) {
		count = len(candidates)
	}

	h := sha256.New()
	h.Write(seed)
	var buf [12]byte
	binary.BigEndian.PutUint32(buf[:4], uint32(pos))
	binary.BigEndian.PutUint32(buf[4:8], uint32(d))
	binary.BigEndian.PutUint32(buf[8:], uint32(count))
	h.Write(buf[:])
	rngSeed := binary.BigEndian.Uint64(h.Sum(nil)[:8])
	rng := rand.New(rand.NewSource(int64(rngSeed)))

	indices := make([]int, len(candidates))
	for i := range indices {
		indices[i] = i
	}

	for i := 0; i < count; i++ {
		j := i + rng.Intn(len(indices)-i)
		indices[i], indices[j] = indices[j], indices[i]
	}

	selected := make([]int, count)
	for i := 0; i < count; i++ {
		selected[i] = candidates[indices[i]]
	}

	return selected
}

func containsNeighbor(node *FLTQNode, addr string) bool {
	for _, neighbor := range node.Neighbors {
		if neighbor == addr {
			return true
		}
	}
	return false
}

func addBidirectionalNeighbor(a, b *FLTQNode) {
	if a.Address == b.Address {
		return
	}
	if !containsNeighbor(a, b.Address) {
		a.Neighbors = append(a.Neighbors, b.Address)
	}
	if !containsNeighbor(b, a.Address) {
		b.Neighbors = append(b.Neighbors, a.Address)
	}
}

func buildShortcutEdges(cube *FLTQCube, seed []byte, shortcutsPerBucket int) {
	n := cube.Dimensions
	if n <= 3 {
		return
	}

	minDist := n/3 + 1
	maxDist := n - 1

	occupied := make([]bool, cube.Size)
	for _, node := range cube.Positions {
		if node != nil {
			occupied[node.Position] = true
		}
	}

	for pos := 0; pos < cube.Size; pos++ {
		node := cube.Positions[pos]
		if node == nil {
			continue
		}

		for d := minDist; d <= maxDist; d++ {
			candidates := findCandidatesAtDistance(pos, d, n, occupied, seed, shortcutsPerBucket*4)
			selected := deterministicSelectMultiple(candidates, seed, pos, d, shortcutsPerBucket)

			for _, targetPos := range selected {
				targetNode := cube.Positions[targetPos]
				if targetNode == nil {
					continue
				}
				addBidirectionalNeighbor(node, targetNode)
			}
		}
	}
}
