package propagation

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHypercubeBuild(t *testing.T) {
	participants := make([]WeightedParticipant, 10)
	for i := 0; i < 10; i++ {
		participants[i] = WeightedParticipant{
			Address: fmt.Sprintf("participant%d", i),
			Weight:  uint64(100 + i),
		}
	}

	blockHash := sha256.Sum256([]byte("test-block"))
	hypercube := BuildHypercubeWithWeights(participants, blockHash[:])

	require.Equal(t, 4, hypercube.Dimensions)
	require.Equal(t, 16, hypercube.Size)
	require.Equal(t, 10, len(hypercube.Nodes))

	for addr, node := range hypercube.Nodes {
		require.NotNil(t, node)
		require.Equal(t, addr, node.Address)
		require.LessOrEqual(t, len(node.Neighbors), hypercube.Dimensions)
	}
}

func TestHypercubeDeterministic(t *testing.T) {
	participants := make([]WeightedParticipant, 20)
	for i := 0; i < 20; i++ {
		participants[i] = WeightedParticipant{
			Address: fmt.Sprintf("participant%d", i),
			Weight:  uint64(100 + i*10),
		}
	}

	blockHash := sha256.Sum256([]byte("test-block"))

	hc1 := BuildHypercubeWithWeights(participants, blockHash[:])
	hc2 := BuildHypercubeWithWeights(participants, blockHash[:])

	require.Equal(t, hc1.Dimensions, hc2.Dimensions)
	require.Equal(t, hc1.Size, hc2.Size)
	require.Equal(t, len(hc1.Nodes), len(hc2.Nodes))

	for addr, node1 := range hc1.Nodes {
		node2 := hc2.Nodes[addr]
		require.NotNil(t, node2)
		require.Equal(t, node1.Position, node2.Position)
		require.Equal(t, len(node1.Neighbors), len(node2.Neighbors))

		for i, neighbor1 := range node1.Neighbors {
			require.Equal(t, neighbor1, node2.Neighbors[i])
		}
	}
}

func TestHypercubeDifferentSeeds(t *testing.T) {
	participants := make([]WeightedParticipant, 20)
	for i := 0; i < 20; i++ {
		participants[i] = WeightedParticipant{
			Address: fmt.Sprintf("participant%d", i),
			Weight:  uint64(100 + i*10),
		}
	}

	blockHash1 := sha256.Sum256([]byte("test-block-1"))
	blockHash2 := sha256.Sum256([]byte("test-block-2"))

	hc1 := BuildHypercubeWithWeights(participants, blockHash1[:])
	hc2 := BuildHypercubeWithWeights(participants, blockHash2[:])

	positionsDifferent := false
	for addr := range hc1.Nodes {
		if hc1.Nodes[addr].Position != hc2.Nodes[addr].Position {
			positionsDifferent = true
			break
		}
	}

	require.True(t, positionsDifferent, "different seeds should produce different positions")
}

func TestHypercubeBidirectionalNeighbors(t *testing.T) {
	participants := make([]WeightedParticipant, 50)
	for i := 0; i < 50; i++ {
		participants[i] = WeightedParticipant{
			Address: fmt.Sprintf("participant%d", i),
			Weight:  uint64(100 + i),
		}
	}

	blockHash := sha256.Sum256([]byte("test-block"))
	hypercube := BuildHypercubeWithWeights(participants, blockHash[:])

	for addr, node := range hypercube.Nodes {
		for _, neighborAddr := range node.Neighbors {
			neighbor := hypercube.Nodes[neighborAddr]
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

func TestHypercubeWithWeights_WeightDistribution(t *testing.T) {
	participants := make([]WeightedParticipant, 100)
	for i := 0; i < 100; i++ {
		participants[i] = WeightedParticipant{
			Address: formatAddress(i),
			Weight:  uint64((i + 1) * 10),
		}
	}

	blockHash := sha256.Sum256([]byte("test-block-weight-2"))
	hypercube := BuildHypercubeWithWeights(participants, blockHash[:])

	t.Logf("\n=== Hypercube Structure ===")
	t.Logf("Dimensions: %d", hypercube.Dimensions)
	t.Logf("Size: %d", hypercube.Size)
	t.Logf("Participants: %d", len(hypercube.Nodes))

	DisplayHypercubeStructure(t, hypercube, participants, 30)

	minNeighbors := hypercube.Dimensions
	maxNeighbors := 0
	totalNeighbors := 0
	neighborCounts := make(map[int]int)

	for _, node := range hypercube.Nodes {
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

	avgNeighbors := float64(totalNeighbors) / float64(len(hypercube.Nodes))

	t.Logf("\nNeighbor Statistics:")
	t.Logf("  Min neighbors: %d", minNeighbors)
	t.Logf("  Max neighbors: %d", maxNeighbors)
	t.Logf("  Avg neighbors: %.2f", avgNeighbors)
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
}

func TestHypercubeSmall(t *testing.T) {
	participants := make([]WeightedParticipant, 4)
	for i := 0; i < 4; i++ {
		participants[i] = WeightedParticipant{
			Address: fmt.Sprintf("P%d", i),
			Weight:  uint64(100),
		}
	}

	blockHash := sha256.Sum256([]byte("test-small"))
	hypercube := BuildHypercubeWithWeights(participants, blockHash[:])

	require.Equal(t, 2, hypercube.Dimensions)
	require.Equal(t, 4, hypercube.Size)
	require.Equal(t, 4, len(hypercube.Nodes))

	t.Logf("\n=== Small Hypercube (4 nodes, 2 dimensions) ===")
	DisplayHypercubeStructure(t, hypercube, participants, 10)
}

func TestHypercubeMedium(t *testing.T) {
	participants := make([]WeightedParticipant, 50)
	for i := 0; i < 50; i++ {
		participants[i] = WeightedParticipant{
			Address: fmt.Sprintf("participant%d", i),
			Weight:  uint64(100 + i*5),
		}
	}

	blockHash := sha256.Sum256([]byte("test-medium"))
	hypercube := BuildHypercubeWithWeights(participants, blockHash[:])

	require.Equal(t, 6, hypercube.Dimensions)
	require.Equal(t, 64, hypercube.Size)
	require.Equal(t, 50, len(hypercube.Nodes))

	t.Logf("\n=== Medium Hypercube (50 nodes, 6 dimensions) ===")
	DisplayHypercubeStructure(t, hypercube, participants, 20)
}

func TestHypercubeLarge(t *testing.T) {
	participants := make([]WeightedParticipant, 1000)
	for i := 0; i < 1000; i++ {
		participants[i] = WeightedParticipant{
			Address: fmt.Sprintf("participant%d", i),
			Weight:  uint64(100 + i),
		}
	}

	blockHash := sha256.Sum256([]byte("test-large"))
	hypercube := BuildHypercubeWithWeights(participants, blockHash[:])

	require.Equal(t, 10, hypercube.Dimensions)
	require.Equal(t, 1024, hypercube.Size)
	require.Equal(t, 1000, len(hypercube.Nodes))

	t.Logf("\n=== Large Hypercube (1000 nodes, 10 dimensions) ===")
	t.Logf("Dimensions: %d", hypercube.Dimensions)
	t.Logf("Size: %d", hypercube.Size)
	t.Logf("Participants: %d", len(hypercube.Nodes))

	DisplayHypercubeStructure(t, hypercube, participants, 20)

	totalNeighbors := 0
	for _, node := range hypercube.Nodes {
		totalNeighbors += len(node.Neighbors)
	}
	t.Logf("\nTotal network connections: %d", totalNeighbors/2)
	t.Logf("Avg connections per participant: %.2f", float64(totalNeighbors)/float64(len(hypercube.Nodes)))
}

func TestHypercubeEmpty(t *testing.T) {
	participants := []WeightedParticipant{}
	blockHash := sha256.Sum256([]byte("test-empty"))
	hypercube := BuildHypercubeWithWeights(participants, blockHash[:])

	require.Equal(t, 0, hypercube.Dimensions)
	require.Equal(t, 0, len(hypercube.Nodes))
	require.NotNil(t, hypercube.Nodes)
}

func TestHypercubeSingleNode(t *testing.T) {
	participants := []WeightedParticipant{
		{Address: "single", Weight: 100},
	}
	blockHash := sha256.Sum256([]byte("test-single"))
	hypercube := BuildHypercubeWithWeights(participants, blockHash[:])

	require.Equal(t, 0, hypercube.Dimensions)
	require.Equal(t, 1, hypercube.Size)
	require.Equal(t, 1, len(hypercube.Nodes))

	node := hypercube.Nodes["single"]
	require.NotNil(t, node)
	require.Equal(t, 0, len(node.Neighbors))
}

func TestCeilLog2(t *testing.T) {
	tests := []struct {
		n    int
		want int
	}{
		{0, 0},
		{1, 0},
		{2, 1},
		{3, 2},
		{4, 2},
		{5, 3},
		{8, 3},
		{9, 4},
		{16, 4},
		{17, 5},
		{100, 7},
		{1000, 10},
		{10000, 14},
	}

	for _, tt := range tests {
		got := ceilLog2(tt.n)
		if got != tt.want {
			t.Errorf("ceilLog2(%d) = %d, want %d", tt.n, got, tt.want)
		}
	}
}

func TestMakeDimensionSeed(t *testing.T) {
	blockHash := sha256.Sum256([]byte("test-block"))

	seed0 := makeDimensionSeed(blockHash[:], 0)
	seed1 := makeDimensionSeed(blockHash[:], 1)
	seed2 := makeDimensionSeed(blockHash[:], 2)

	require.Equal(t, 32, len(seed0))
	require.Equal(t, 32, len(seed1))
	require.Equal(t, 32, len(seed2))

	require.NotEqual(t, seed0, seed1)
	require.NotEqual(t, seed1, seed2)
	require.NotEqual(t, seed0, seed2)

	seed0_again := makeDimensionSeed(blockHash[:], 0)
	require.Equal(t, seed0, seed0_again)
}

func TestHypercubePositionMapping(t *testing.T) {
	participants := make([]WeightedParticipant, 8)
	for i := 0; i < 8; i++ {
		participants[i] = WeightedParticipant{
			Address: fmt.Sprintf("P%d", i),
			Weight:  uint64(100 + i*10),
		}
	}

	blockHash := sha256.Sum256([]byte("test-positions"))
	hypercube := BuildHypercubeWithWeights(participants, blockHash[:])

	require.Equal(t, 3, hypercube.Dimensions)
	require.Equal(t, 8, hypercube.Size)
	require.Equal(t, 8, len(hypercube.Nodes))

	positions := make(map[uint16]bool)
	for _, node := range hypercube.Nodes {
		require.False(t, positions[node.Position], "position %d used twice", node.Position)
		positions[node.Position] = true
		require.Less(t, int(node.Position), hypercube.Size)
	}

	t.Logf("\n=== Position Mapping ===")
	sortedAddrs := make([]string, 0, len(hypercube.Nodes))
	for addr := range hypercube.Nodes {
		sortedAddrs = append(sortedAddrs, addr)
	}
	sort.Strings(sortedAddrs)

	for _, addr := range sortedAddrs {
		node := hypercube.Nodes[addr]
		var weight uint64
		for _, p := range participants {
			if p.Address == addr {
				weight = p.Weight
				break
			}
		}
		t.Logf("  %s (weight=%d): position=%d, neighbors=%v", addr, weight, node.Position, node.Neighbors)
	}
}

func DisplayHypercubeStructure(t *testing.T, hypercube *Hypercube, participants []WeightedParticipant, displayCount int) {
	type nodeInfo struct {
		address   string
		weight    uint64
		position  uint16
		neighbors []string
	}

	weightMap := make(map[string]uint64)
	for _, p := range participants {
		weightMap[p.Address] = p.Weight
	}

	nodes := make([]nodeInfo, 0, len(hypercube.Nodes))
	for addr, node := range hypercube.Nodes {
		nodes = append(nodes, nodeInfo{
			address:   addr,
			weight:    weightMap[addr],
			position:  node.Position,
			neighbors: node.Neighbors,
		})
	}

	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].position < nodes[j].position
	})

	t.Logf("\nHypercube Node Structure (showing first %d nodes):", displayCount)
	t.Logf("%-20s | %-8s | %-8s | %-10s | Neighbors", "Address", "Weight", "Position", "Neighbors#")
	t.Logf("%s", strings.Repeat("-", 100))

	count := 0
	for _, node := range nodes {
		if count >= displayCount {
			break
		}

		neighborsStr := fmt.Sprintf("%v", node.neighbors)
		if len(neighborsStr) > 40 {
			neighborsStr = neighborsStr[:37] + "..."
		}

		t.Logf("%-20s | %-8d | %-8d | %-10d | %s",
			node.address, node.weight, node.position, len(node.neighbors), neighborsStr)
		count++
	}

	if len(nodes) > displayCount {
		t.Logf("... and %d more nodes", len(nodes)-displayCount)
	}

	t.Logf("\nConnection Matrix (first 10 nodes):")
	displayMatrix := 10
	if len(nodes) < displayMatrix {
		displayMatrix = len(nodes)
	}

	shortName := func(addr string) string {
		if len(addr) <= 4 {
			return addr
		}
		return addr[len(addr)-4:]
	}

	header := "        "
	for i := 0; i < displayMatrix; i++ {
		header += fmt.Sprintf("%-5s", shortName(nodes[i].address))
	}
	t.Logf("%s", header)
	t.Logf("%s", strings.Repeat("-", len(header)))

	for i := 0; i < displayMatrix; i++ {
		row := fmt.Sprintf("%-7s ", shortName(nodes[i].address))
		for j := 0; j < displayMatrix; j++ {
			connected := false
			for _, neighbor := range nodes[i].neighbors {
				if neighbor == nodes[j].address {
					connected = true
					break
				}
			}
			if i == j {
				row += "  -  "
			} else if connected {
				row += "  X  "
			} else {
				row += "  .  "
			}
		}
		t.Logf("%s", row)
	}

	avgDegree := 0.0
	if len(nodes) > 0 {
		totalDegree := 0
		for _, node := range nodes {
			totalDegree += len(node.neighbors)
		}
		avgDegree = float64(totalDegree) / float64(len(nodes))
	}

	t.Logf("\nStructure Summary:")
	t.Logf("  Total nodes: %d", len(nodes))
	t.Logf("  Dimensions: %d", hypercube.Dimensions)
	t.Logf("  Expected connections per node: %d", hypercube.Dimensions)
	t.Logf("  Actual avg connections per node: %.2f", avgDegree)
	t.Logf("  Total network connections: %d", int(avgDegree*float64(len(nodes)))/2)
}
