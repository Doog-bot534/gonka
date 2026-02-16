package propagation

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFLTQBuild(t *testing.T) {
	participants := make([]WeightedParticipant, 10)
	for i := 0; i < 10; i++ {
		participants[i] = WeightedParticipant{
			Address: fmt.Sprintf("participant%d", i),
			Weight:  uint64(100 + i),
		}
	}

	blockHash := sha256.Sum256([]byte("test-block"))
	cube := BuildFLTQWithWeights(participants, blockHash[:])

	require.Equal(t, 4, cube.Dimensions)
	require.Equal(t, 16, cube.Size)
	require.Equal(t, 10, len(cube.Nodes))

	for addr, node := range cube.Nodes {
		require.NotNil(t, node)
		require.Equal(t, addr, node.Address)
		require.LessOrEqual(t, len(node.Neighbors), cube.Dimensions+1)
	}
}

func TestFLTQDeterministic(t *testing.T) {
	participants := make([]WeightedParticipant, 20)
	for i := 0; i < 20; i++ {
		participants[i] = WeightedParticipant{
			Address: fmt.Sprintf("participant%d", i),
			Weight:  uint64(100 + i*10),
		}
	}

	blockHash := sha256.Sum256([]byte("test-block"))

	cube1 := BuildFLTQWithWeights(participants, blockHash[:])
	cube2 := BuildFLTQWithWeights(participants, blockHash[:])

	require.Equal(t, cube1.Dimensions, cube2.Dimensions)
	require.Equal(t, cube1.Size, cube2.Size)
	require.Equal(t, len(cube1.Nodes), len(cube2.Nodes))

	for addr, node1 := range cube1.Nodes {
		node2 := cube2.Nodes[addr]
		require.NotNil(t, node2)
		require.Equal(t, node1.Position, node2.Position)
		require.Equal(t, len(node1.Neighbors), len(node2.Neighbors))

		sort.Strings(node1.Neighbors)
		sort.Strings(node2.Neighbors)
		for i, neighbor1 := range node1.Neighbors {
			require.Equal(t, neighbor1, node2.Neighbors[i])
		}
	}
}

func TestFLTQDifferentSeeds(t *testing.T) {
	participants := make([]WeightedParticipant, 20)
	for i := 0; i < 20; i++ {
		participants[i] = WeightedParticipant{
			Address: fmt.Sprintf("participant%d", i),
			Weight:  uint64(100 + i*10),
		}
	}

	blockHash1 := sha256.Sum256([]byte("test-block-1"))
	blockHash2 := sha256.Sum256([]byte("test-block-2"))

	cube1 := BuildFLTQWithWeights(participants, blockHash1[:])
	cube2 := BuildFLTQWithWeights(participants, blockHash2[:])

	positionsDifferent := false
	for addr := range cube1.Nodes {
		if cube1.Nodes[addr].Position != cube2.Nodes[addr].Position {
			positionsDifferent = true
			break
		}
	}

	require.True(t, positionsDifferent, "different seeds should produce different positions")
}

func TestFLTQBidirectionalNeighbors(t *testing.T) {
	participants := make([]WeightedParticipant, 50)
	for i := 0; i < 50; i++ {
		participants[i] = WeightedParticipant{
			Address: fmt.Sprintf("participant%d", i),
			Weight:  uint64(100 + i),
		}
	}

	blockHash := sha256.Sum256([]byte("test-block"))
	cube := BuildFLTQWithWeights(participants, blockHash[:])

	for addr, node := range cube.Nodes {
		for _, neighborAddr := range node.Neighbors {
			neighbor := cube.Nodes[neighborAddr]
			require.NotNil(t, neighbor, "neighbor %s should exist", neighborAddr)

			found := false
			for _, backNeighbor := range neighbor.Neighbors {
				if backNeighbor == addr {
					found = true
					break
				}
			}
			require.True(t, found, "neighbor relationship should be bidirectional: %s <-> %s", addr, neighborAddr)
		}
	}
}

func TestFLTQDegreeVerification(t *testing.T) {
	tests := []struct {
		name  string
		count int
	}{
		{"small-8", 8},
		{"medium-50", 50},
		{"large-100", 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			participants := make([]WeightedParticipant, tt.count)
			for i := 0; i < tt.count; i++ {
				participants[i] = WeightedParticipant{
					Address: fmt.Sprintf("participant%d", i),
					Weight:  uint64(100 + i),
				}
			}

			blockHash := sha256.Sum256([]byte("test-degree"))
			cube := BuildFLTQWithWeights(participants, blockHash[:])

			expectedDegree := cube.Dimensions + 1

			for addr, node := range cube.Nodes {
				actualDegree := len(node.Neighbors)
				require.LessOrEqual(t, actualDegree, expectedDegree,
					"node %s has degree %d, expected <= %d (dimensions=%d)",
					addr, actualDegree, expectedDegree, cube.Dimensions)
			}

			t.Logf("Dimensions: %d, Expected degree: %d", cube.Dimensions, expectedDegree)
		})
	}
}

func TestFLTQComplementaryEdge(t *testing.T) {
	participants := make([]WeightedParticipant, 16)
	for i := 0; i < 16; i++ {
		participants[i] = WeightedParticipant{
			Address: fmt.Sprintf("P%d", i),
			Weight:  uint64(100 + i),
		}
	}

	blockHash := sha256.Sum256([]byte("test-complement"))
	cube := BuildFLTQWithWeights(participants, blockHash[:])

	require.Equal(t, 4, cube.Dimensions)
	require.Equal(t, 16, cube.Size)

	for addr, node := range cube.Nodes {
		pos := int(node.Position)
		complementPos := complementPosition(pos, cube.Dimensions)

		if cube.Positions[complementPos] != nil {
			complementAddr := cube.Positions[complementPos].Address
			found := false
			for _, neighbor := range node.Neighbors {
				if neighbor == complementAddr {
					found = true
					break
				}
			}
			require.True(t, found, "node %s at position %d should have complementary neighbor at position %d",
				addr, pos, complementPos)
		}
	}
}

func TestFLTQLTQNeighborFunction(t *testing.T) {
	tests := []struct {
		name string
		n    int
		pos  int
		dim  int
		want int
	}{
		{"n=2,pos=0,dim=0", 2, 0, 0, 1},
		{"n=2,pos=1,dim=0", 2, 1, 0, 0},
		{"n=3,pos=0,dim=0", 3, 0, 0, 1},
		{"n=3,pos=0,dim=1", 3, 0, 1, 2},
		{"n=3,pos=1,dim=2", 3, 1, 2, 4},
		{"n=4,pos=0,dim=0", 4, 0, 0, 1},
		{"n=4,pos=0,dim=1", 4, 0, 1, 2},
		{"n=4,pos=0,dim=2", 4, 0, 2, 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ltqNeighbor(tt.pos, tt.dim, tt.n)
			if tt.dim < tt.n-1 {
				require.Equal(t, tt.want, got)
			}
		})
	}
}

func TestFLTQComplementPosition(t *testing.T) {
	tests := []struct {
		n    int
		pos  int
		want int
	}{
		{2, 0, 3},
		{2, 1, 2},
		{2, 2, 1},
		{2, 3, 0},
		{3, 0, 7},
		{3, 1, 6},
		{3, 7, 0},
		{4, 0, 15},
		{4, 5, 10},
		{4, 15, 0},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("n=%d,pos=%d", tt.n, tt.pos), func(t *testing.T) {
			got := complementPosition(tt.pos, tt.n)
			require.Equal(t, tt.want, got)

			inverse := complementPosition(got, tt.n)
			require.Equal(t, tt.pos, inverse, "complement should be involutive")
		})
	}
}

func TestFLTQSmallNetworks(t *testing.T) {
	tests := []struct {
		name             string
		count            int
		expectedDim      int
		expectedMaxDegree int
	}{
		{"2-nodes", 2, 1, 2},
		{"4-nodes", 4, 2, 3},
		{"8-nodes", 8, 3, 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			participants := make([]WeightedParticipant, tt.count)
			for i := 0; i < tt.count; i++ {
				participants[i] = WeightedParticipant{
					Address: fmt.Sprintf("P%d", i),
					Weight:  uint64(100),
				}
			}

			blockHash := sha256.Sum256([]byte(fmt.Sprintf("test-%s", tt.name)))
			cube := BuildFLTQWithWeights(participants, blockHash[:])

			require.Equal(t, tt.expectedDim, cube.Dimensions)
			require.Equal(t, tt.count, len(cube.Nodes))

			for addr, node := range cube.Nodes {
				require.LessOrEqual(t, len(node.Neighbors), tt.expectedMaxDegree,
					"node %s has %d neighbors, expected <= %d",
					addr, len(node.Neighbors), tt.expectedMaxDegree)
			}

			t.Logf("\n=== FLTQ %s (d=%d) ===", tt.name, cube.Dimensions)
			for pos := 0; pos < cube.Size; pos++ {
				if cube.Positions[pos] != nil {
					node := cube.Positions[pos]
					t.Logf("Position %d (%s): %d neighbors: %v",
						pos, node.Address, len(node.Neighbors), node.Neighbors)
				}
			}
		})
	}
}

func TestFLTQDiameter(t *testing.T) {
	participants := make([]WeightedParticipant, 100)
	for i := 0; i < 100; i++ {
		participants[i] = WeightedParticipant{
			Address: fmt.Sprintf("participant%d", i),
			Weight:  uint64(100 + i),
		}
	}

	blockHash := sha256.Sum256([]byte("test-diameter"))
	cube := BuildFLTQWithWeights(participants, blockHash[:])

	expectedMaxDiameter := (cube.Dimensions / 2) + 1
	if cube.Dimensions%2 == 1 {
		expectedMaxDiameter++
	}

	maxDiameter := 0
	sampleSize := 10
	if len(cube.Nodes) < sampleSize {
		sampleSize = len(cube.Nodes)
	}

	addrs := make([]string, 0, len(cube.Nodes))
	for addr := range cube.Nodes {
		addrs = append(addrs, addr)
		if len(addrs) >= sampleSize {
			break
		}
	}

	for _, startAddr := range addrs {
		distances := bfsFLTQ(cube, startAddr)
		for _, dist := range distances {
			if dist > maxDiameter {
				maxDiameter = dist
			}
		}
	}

	t.Logf("Dimensions: %d, Measured diameter: %d, Expected max: %d",
		cube.Dimensions, maxDiameter, expectedMaxDiameter)
	require.LessOrEqual(t, maxDiameter, expectedMaxDiameter,
		"diameter should be <= ceil(n/2) + 1")
}

func bfsFLTQ(cube *FLTQCube, startAddr string) map[string]int {
	distances := make(map[string]int)
	queue := []string{startAddr}
	distances[startAddr] = 0

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		currentNode := cube.GetNode(current)
		if currentNode == nil {
			continue
		}

		for _, neighbor := range currentNode.Neighbors {
			if _, visited := distances[neighbor]; !visited {
				distances[neighbor] = distances[current] + 1
				queue = append(queue, neighbor)
			}
		}
	}

	return distances
}

func TestFLTQWeightDistribution(t *testing.T) {
	participants := make([]WeightedParticipant, 100)
	for i := 0; i < 100; i++ {
		participants[i] = WeightedParticipant{
			Address: formatAddress(i),
			Weight:  uint64((i + 1) * 10),
		}
	}

	blockHash := sha256.Sum256([]byte("test-weight-fltq"))
	cube := BuildFLTQWithWeights(participants, blockHash[:])

	t.Logf("\n=== FLTQ Structure ===")
	t.Logf("Dimensions: %d", cube.Dimensions)
	t.Logf("Size: %d", cube.Size)
	t.Logf("Participants: %d", len(cube.Nodes))

	minNeighbors := cube.Dimensions + 1
	maxNeighbors := 0
	totalNeighbors := 0
	neighborCounts := make(map[int]int)

	for _, node := range cube.Nodes {
		neighborCount := len(node.Neighbors)
		totalNeighbors += neighborCount
		neighborCounts[neighborCount]++

		if neighborCount < minNeighbors {
			minNeighbors = neighborCount
		}
		if neighborCount > maxNeighbors {
			maxNeighbors = neighborCount
		}
	}

	avgNeighbors := float64(totalNeighbors) / float64(len(cube.Nodes))

	t.Logf("\nNeighbor Statistics:")
	t.Logf("  Min neighbors: %d", minNeighbors)
	t.Logf("  Max neighbors: %d", maxNeighbors)
	t.Logf("  Avg neighbors: %.2f", avgNeighbors)
	t.Logf("  Expected degree: %d", cube.Dimensions+1)
	t.Logf("  Total connections: %d", totalNeighbors/2)

	t.Logf("\nNeighbor count distribution:")
	counts := make([]int, 0, len(neighborCounts))
	for count := range neighborCounts {
		counts = append(counts, count)
	}
	sort.Ints(counts)
	for _, count := range counts {
		t.Logf("  %d neighbors: %d nodes", count, neighborCounts[count])
	}

	weightMap := make(map[string]uint64)
	for _, p := range participants {
		weightMap[p.Address] = p.Weight
	}

	type posInfo struct {
		pos    int
		addr   string
		weight uint64
	}
	positions := make([]posInfo, 0)
	for i := 0; i < 20 && i < len(cube.Positions); i++ {
		if cube.Positions[i] != nil {
			positions = append(positions, posInfo{
				pos:    i,
				addr:   cube.Positions[i].Address,
				weight: weightMap[cube.Positions[i].Address],
			})
		}
	}

	t.Logf("\nFirst 20 positions (should be high-weight participants):")
	for _, p := range positions {
		t.Logf("  Position %d: %s (weight=%d)", p.pos, p.addr, p.weight)
	}
}

func TestFLTQEmpty(t *testing.T) {
	participants := []WeightedParticipant{}
	blockHash := sha256.Sum256([]byte("test-empty"))
	cube := BuildFLTQWithWeights(participants, blockHash[:])

	require.Equal(t, 0, cube.Dimensions)
	require.Equal(t, 0, len(cube.Nodes))
	require.NotNil(t, cube.Nodes)
}

func TestFLTQSingleNode(t *testing.T) {
	participants := []WeightedParticipant{
		{Address: "single", Weight: 100},
	}
	blockHash := sha256.Sum256([]byte("test-single"))
	cube := BuildFLTQWithWeights(participants, blockHash[:])

	require.Equal(t, 0, cube.Dimensions)
	require.Equal(t, 1, cube.Size)
	require.Equal(t, 1, len(cube.Nodes))

	node := cube.Nodes["single"]
	require.NotNil(t, node)
	require.Equal(t, 0, len(node.Neighbors))
}

func TestFLTQMultipleCubes(t *testing.T) {
	participants := make([]WeightedParticipant, 20)
	for i := 0; i < 20; i++ {
		participants[i] = WeightedParticipant{
			Address: fmt.Sprintf("participant%d", i),
			Weight:  uint64(100 + i*10),
		}
	}

	blockHash := sha256.Sum256([]byte("test-multi"))
	cubes := BuildFLTQsWithWeights(participants, blockHash[:], 3)

	require.Equal(t, 3, len(cubes))

	for i, cube := range cubes {
		require.Equal(t, i, cube.Index)
		require.Equal(t, len(participants), len(cube.Nodes))
	}

	for i := 0; i < len(cubes); i++ {
		for j := i + 1; j < len(cubes); j++ {
			positionsDifferent := false
			for addr := range cubes[i].Nodes {
				if cubes[i].Nodes[addr].Position != cubes[j].Nodes[addr].Position {
					positionsDifferent = true
					break
				}
			}
			require.True(t, positionsDifferent,
				"cubes %d and %d should have different position assignments", i, j)
		}
	}
}

func TestMakeFLTQSeed(t *testing.T) {
	blockHash := sha256.Sum256([]byte("test-block"))

	seed0 := makeFLTQSeed(blockHash[:], 0)
	seed1 := makeFLTQSeed(blockHash[:], 1)
	seed2 := makeFLTQSeed(blockHash[:], 2)

	require.Equal(t, 32, len(seed0))
	require.Equal(t, 32, len(seed1))
	require.Equal(t, 32, len(seed2))

	require.NotEqual(t, seed0, seed1)
	require.NotEqual(t, seed1, seed2)
	require.NotEqual(t, seed0, seed2)

	seed0_again := makeFLTQSeed(blockHash[:], 0)
	require.Equal(t, seed0, seed0_again)
}
