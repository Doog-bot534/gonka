package propagation

import (
	"crypto/sha256"
	"encoding/binary"
)

type FLTQConfig struct {
	NumToSplitPastryDigit int
	PastryEntriesPerLevel int
}

func DefaultFLTQConfig() FLTQConfig {
	return FLTQConfig{
		NumToSplitPastryDigit: 2,
		PastryEntriesPerLevel: 4,
	}
}

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
	return buildFLTQWithIndex(0, participants, blockHash, DefaultFLTQConfig())
}

func BuildFLTQWithConfig(participants []WeightedParticipant, blockHash []byte, config FLTQConfig) *FLTQCube {
	return buildFLTQWithIndex(0, participants, blockHash, config)
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

	config := DefaultFLTQConfig()
	cubes := make([]*FLTQCube, numCubes)
	for i := 0; i < numCubes; i++ {
		seed := makeFLTQSeed(blockHash, i)
		cubes[i] = buildFLTQWithIndex(i, participants, seed, config)
	}
	return cubes
}

func buildFLTQWithIndex(index int, participants []WeightedParticipant, blockHash []byte, config FLTQConfig) *FLTQCube {
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

		neighborCapacity := n + 16
		if neighborCapacity < 32 {
			neighborCapacity = 32
		}

		node := &FLTQNode{
			Address:    addr,
			Position:   uint16(pos),
			Neighbors:  make([]string, 0, neighborCapacity),
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

	buildPastryEdges(cube, blockHash, config)

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

func splitBits(n int, numToSplitPastryDigit int) []int {
	if n <= 0 || numToSplitPastryDigit <= 0 {
		return []int{}
	}

	sizes := make([]int, numToSplitPastryDigit)
	base := n / numToSplitPastryDigit
	remainder := n % numToSplitPastryDigit

	for i := 0; i < numToSplitPastryDigit; i++ {
		sizes[i] = base
		if i < remainder {
			sizes[i]++
		}
	}

	return sizes
}

func digitAt(pos int, level int, digitSizes []int) int {
	if level < 0 || level >= len(digitSizes) {
		return 0
	}

	shift := 0
	for i := len(digitSizes) - 1; i > level; i-- {
		shift += digitSizes[i]
	}

	mask := (1 << digitSizes[level]) - 1
	return (pos >> shift) & mask
}

func prefixUpTo(pos int, level int, digitSizes []int) int {
	if level < 0 {
		return 0
	}

	totalBits := 0
	for i := 0; i <= level && i < len(digitSizes); i++ {
		totalBits += digitSizes[i]
	}

	shift := 0
	for i := len(digitSizes) - 1; i > level; i-- {
		shift += digitSizes[i]
	}

	mask := ((1 << totalBits) - 1) << shift
	return (pos & mask) >> shift
}

func replaceDigit(prefix int, level int, newValue int, digitSizes []int) int {
	if level < 0 || level >= len(digitSizes) {
		return prefix
	}

	mask := (1 << digitSizes[level]) - 1
	cleared := prefix &^ mask
	return cleared | (newValue & mask)
}

func buildPrefixIndex(cube *FLTQCube, digitSizes []int) map[int]map[int][]int {
	index := make(map[int]map[int][]int)

	for level := 0; level < len(digitSizes); level++ {
		index[level] = make(map[int][]int)
	}

	for pos := 0; pos < cube.Size; pos++ {
		if cube.Positions[pos] == nil {
			continue
		}

		for level := 0; level < len(digitSizes); level++ {
			prefix := prefixUpTo(pos, level, digitSizes)
			index[level][prefix] = append(index[level][prefix], pos)
		}
	}

	return index
}

func deterministicSelect(candidates []int, seed []byte, pos int, level int, v int) int {
	if len(candidates) == 0 {
		return -1
	}
	if len(candidates) == 1 {
		return candidates[0]
	}

	h := sha256.New()
	h.Write(seed)
	var buf [12]byte
	binary.BigEndian.PutUint32(buf[0:4], uint32(pos))
	binary.BigEndian.PutUint32(buf[4:8], uint32(level))
	binary.BigEndian.PutUint32(buf[8:12], uint32(v))
	h.Write(buf[:])
	hashResult := h.Sum(nil)

	idx := binary.BigEndian.Uint64(hashResult[:8]) % uint64(len(candidates))
	return candidates[idx]
}

func shuffleInts(values []int, seed []byte, pos int, level int) {
	if len(values) <= 1 {
		return
	}

	h := sha256.New()
	h.Write(seed)
	var buf [8]byte
	binary.BigEndian.PutUint32(buf[0:4], uint32(pos))
	binary.BigEndian.PutUint32(buf[4:8], uint32(level))
	h.Write(buf[:])
	hashResult := h.Sum(nil)

	seedValue := int64(binary.BigEndian.Uint64(hashResult[:8]))

	for i := len(values) - 1; i > 0; i-- {
		seedValue = seedValue*1103515245 + 12345
		j := int((seedValue >> 16) % int64(i+1))
		if j < 0 {
			j = -j
		}
		values[i], values[j] = values[j], values[i]
	}
}

func buildPastryEdges(cube *FLTQCube, seed []byte, config FLTQConfig) {
	digitSizes := splitBits(cube.Dimensions, config.NumToSplitPastryDigit)
	prefixIndex := buildPrefixIndex(cube, digitSizes)

	allNeighborsMap := make(map[string]map[string]bool)

	for pos := 0; pos < cube.Size; pos++ {
		node := cube.Positions[pos]
		if node == nil {
			continue
		}

		if allNeighborsMap[node.Address] == nil {
			allNeighborsMap[node.Address] = make(map[string]bool)
		}

		for _, neighbor := range node.Neighbors {
			allNeighborsMap[node.Address][neighbor] = true
		}
	}

	for pos := 0; pos < cube.Size; pos++ {
		node := cube.Positions[pos]
		if node == nil {
			continue
		}

		for level := 0; level < len(digitSizes); level++ {
			myDigit := digitAt(pos, level, digitSizes)
			myPrefix := prefixUpTo(pos, level, digitSizes)
			base := 1 << digitSizes[level]

			possibleValues := make([]int, 0, base-1)
			for v := 0; v < base; v++ {
				if v != myDigit {
					possibleValues = append(possibleValues, v)
				}
			}

			shuffleInts(possibleValues, seed, pos, level)

			limit := config.PastryEntriesPerLevel
			if limit <= 0 {
				continue
			}
			if limit > len(possibleValues) {
				limit = len(possibleValues)
			}

			for i := 0; i < limit; i++ {
				v := possibleValues[i]

				targetPrefix := replaceDigit(myPrefix, level, v, digitSizes)
				candidates := prefixIndex[level][targetPrefix]
				if len(candidates) == 0 {
					continue
				}

				pick := deterministicSelect(candidates, seed, pos, level, v)
				if pick < 0 || pick >= cube.Size || cube.Positions[pick] == nil {
					continue
				}

				neighborAddr := cube.Positions[pick].Address
				if neighborAddr != node.Address {
					allNeighborsMap[node.Address][neighborAddr] = true
					if allNeighborsMap[neighborAddr] == nil {
						allNeighborsMap[neighborAddr] = make(map[string]bool)
					}
					allNeighborsMap[neighborAddr][node.Address] = true
				}
			}
		}
	}

	for addr, neighborsMap := range allNeighborsMap {
		node := cube.Nodes[addr]
		if node != nil {
			cap := len(neighborsMap)
			if cap < 32 {
				cap = 32
			}
			node.Neighbors = make([]string, 0, cap)
			for neighborAddr := range neighborsMap {
				node.Neighbors = append(node.Neighbors, neighborAddr)
			}
		}
	}
}
