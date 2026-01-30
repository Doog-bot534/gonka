package propagation

import (
	"crypto/sha256"
	"testing"
)

func TestDeterministicShuffle(t *testing.T) {
	participants := []string{"A", "B", "C", "D", "E"}
	seed := []byte{1, 2, 3, 4, 5, 6, 7, 8}

	result1 := deterministicShuffle(participants, seed)
	result2 := deterministicShuffle(participants, seed)

	if len(result1) != len(participants) {
		t.Errorf("shuffle changed length: got %d, want %d", len(result1), len(participants))
	}

	for i := range result1 {
		if result1[i] != result2[i] {
			t.Errorf("shuffle not deterministic at index %d: %s != %s", i, result1[i], result2[i])
		}
	}

	seen := make(map[string]bool)
	for _, addr := range result1 {
		if seen[addr] {
			t.Errorf("duplicate address in shuffle: %s", addr)
		}
		seen[addr] = true
	}

	for _, addr := range participants {
		if !seen[addr] {
			t.Errorf("missing address in shuffle: %s", addr)
		}
	}
}

func TestDeterministicShuffleWithDifferentSeeds(t *testing.T) {
	participants := []string{"A", "B", "C", "D", "E"}
	seed1 := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	seed2 := []byte{8, 7, 6, 5, 4, 3, 2, 1}

	result1 := deterministicShuffle(participants, seed1)
	result2 := deterministicShuffle(participants, seed2)

	different := false
	for i := range result1 {
		if result1[i] != result2[i] {
			different = true
			break
		}
	}

	if !different {
		t.Error("different seeds produced same shuffle order")
	}
}

func TestIndexOf(t *testing.T) {
	list := []string{"A", "B", "C", "D", "E"}

	tests := []struct {
		target string
		want   int
	}{
		{"A", 0},
		{"C", 2},
		{"E", 4},
		{"F", -1},
		{"", -1},
	}

	for _, tt := range tests {
		got := indexOf(list, tt.target)
		if got != tt.want {
			t.Errorf("indexOf(%q) = %d, want %d", tt.target, got, tt.want)
		}
	}
}

func TestBuildTree(t *testing.T) {
	participants := []string{"A", "B", "C", "D", "E", "F", "G"}
	fanout := 2

	tree := buildTree(0, participants, fanout)

	if tree.Index != 0 {
		t.Errorf("tree.Index = %d, want 0", tree.Index)
	}

	if tree.Fanout != fanout {
		t.Errorf("tree.Fanout = %d, want %d", tree.Fanout, fanout)
	}

	if len(tree.Nodes) != len(participants) {
		t.Errorf("len(tree.Nodes) = %d, want %d", len(tree.Nodes), len(participants))
	}

	if tree.Root == nil {
		t.Fatal("tree.Root is nil")
	}

	if tree.Root.Address != participants[0] {
		t.Errorf("tree.Root.Address = %s, want %s", tree.Root.Address, participants[0])
	}

	if tree.Root.Parent != nil {
		t.Error("root should not have parent")
	}
}

func TestNodeRelationships(t *testing.T) {
	participants := []string{"A", "B", "C", "D", "E", "F", "G"}
	fanout := 2
	tree := buildTree(0, participants, fanout)

	nodeA := tree.GetNode("A")
	if nodeA == nil {
		t.Fatal("node A not found")
	}

	if nodeA.Index != 0 {
		t.Errorf("nodeA.Index = %d, want 0", nodeA.Index)
	}

	if nodeA.Parent != nil {
		t.Error("A (root) should not have parent")
	}

	if len(nodeA.Children) != 2 {
		t.Errorf("A should have 2 children, got %d", len(nodeA.Children))
	}

	if len(nodeA.Siblings) != 0 {
		t.Errorf("A (root) should have 0 siblings, got %d", len(nodeA.Siblings))
	}

	nodeB := tree.GetNode("B")
	if nodeB == nil {
		t.Fatal("node B not found")
	}

	if nodeB.Parent == nil {
		t.Error("B should have parent")
	} else if nodeB.Parent.Address != "A" {
		t.Errorf("B's parent = %s, want A", nodeB.Parent.Address)
	}

	if len(nodeB.Siblings) != 1 {
		t.Errorf("B should have 1 sibling, got %d", len(nodeB.Siblings))
	} else if nodeB.Siblings[0].Address != "C" {
		t.Errorf("B's sibling = %s, want C", nodeB.Siblings[0].Address)
	}
}

func TestTreeRole(t *testing.T) {
	participants := []string{"A", "B", "C", "D", "E", "F", "G"}
	fanout := 2
	tree := buildTree(0, participants, fanout)

	tests := []struct {
		addr           string
		wantParent     string
		wantChildCount int
	}{
		{"A", "", 2},
		{"B", "A", 2},
		{"C", "A", 2},
		{"D", "B", 0},
		{"E", "B", 0},
		{"F", "C", 0},
		{"G", "C", 0},
	}

	for _, tt := range tests {
		parent, children := tree.Role(tt.addr)

		if parent != tt.wantParent {
			t.Errorf("%s: parent = %s, want %s", tt.addr, parent, tt.wantParent)
		}

		if len(children) != tt.wantChildCount {
			t.Errorf("%s: children count = %d, want %d", tt.addr, len(children), tt.wantChildCount)
		}
	}

	parent, children := tree.Role("UNKNOWN")
	if parent != "" || children != nil {
		t.Errorf("unknown address should return empty parent and nil children")
	}
}

func TestBuildTreesMultiple(t *testing.T) {
	participants := []string{"A", "B", "C", "D", "E"}
	blockHash := sha256.Sum256([]byte("test-block"))
	numTrees := 3
	fanout := 2

	trees := BuildTrees(participants, blockHash[:], numTrees, fanout)

	if len(trees) != numTrees {
		t.Errorf("len(trees) = %d, want %d", len(trees), numTrees)
	}

	for i, tree := range trees {
		if tree.Index != i {
			t.Errorf("tree[%d].Index = %d, want %d", i, tree.Index, i)
		}

		if tree.Fanout != fanout {
			t.Errorf("tree[%d].Fanout = %d, want %d", i, tree.Fanout, fanout)
		}

		if len(tree.Nodes) != len(participants) {
			t.Errorf("tree[%d] has %d nodes, want %d", i, len(tree.Nodes), len(participants))
		}

		if tree.Root == nil {
			t.Errorf("tree[%d] has no root", i)
		}
	}

	if trees[0].Root.Address == trees[1].Root.Address &&
		trees[1].Root.Address == trees[2].Root.Address {
		t.Error("all trees have same root (shuffles should differ)")
	}
}

func TestTreeWithSingleNode(t *testing.T) {
	participants := []string{"A"}
	tree := buildTree(0, participants, 2)

	if tree.Root == nil {
		t.Fatal("root is nil")
	}

	if tree.Root.Address != "A" {
		t.Errorf("root address = %s, want A", tree.Root.Address)
	}

	if tree.Root.Parent != nil {
		t.Error("single node should not have parent")
	}

	if len(tree.Root.Children) != 0 {
		t.Error("single node should not have children")
	}

	if len(tree.Root.Siblings) != 0 {
		t.Error("single node should not have siblings")
	}
}

func TestTreeFanoutOne(t *testing.T) {
	participants := []string{"A", "B", "C", "D"}
	tree := buildTree(0, participants, 1)

	nodeA := tree.GetNode("A")
	if len(nodeA.Children) != 1 {
		t.Errorf("A should have 1 child with fanout=1, got %d", len(nodeA.Children))
	}

	nodeB := tree.GetNode("B")
	if len(nodeB.Children) != 1 {
		t.Errorf("B should have 1 child with fanout=1, got %d", len(nodeB.Children))
	}

	nodeC := tree.GetNode("C")
	if len(nodeC.Children) != 1 {
		t.Errorf("C should have 1 child with fanout=1, got %d", len(nodeC.Children))
	}

	nodeD := tree.GetNode("D")
	if len(nodeD.Children) != 0 {
		t.Errorf("D (leaf) should have 0 children, got %d", len(nodeD.Children))
	}
}

func TestTreeLargeFanout(t *testing.T) {
	participants := []string{"A", "B", "C", "D", "E", "F", "G", "H", "I", "J"}
	fanout := 5
	tree := buildTree(0, participants, fanout)

	root := tree.Root
	if len(root.Children) != 5 {
		t.Errorf("root should have 5 children, got %d", len(root.Children))
	}

	for _, child := range root.Children {
		if child.Parent != root {
			t.Errorf("child %s parent mismatch", child.Address)
		}
	}
}

func TestSiblingCalculation(t *testing.T) {
	participants := []string{"A", "B", "C", "D", "E"}
	fanout := 3
	tree := buildTree(0, participants, fanout)

	nodeB := tree.GetNode("B")
	nodeE := tree.GetNode("E")

	if len(nodeB.Siblings) != 2 {
		t.Errorf("B should have 2 siblings, got %d", len(nodeB.Siblings))
	}

	siblingAddrs := make(map[string]bool)
	for _, sib := range nodeB.Siblings {
		siblingAddrs[sib.Address] = true
	}

	if !siblingAddrs["C"] || !siblingAddrs["D"] {
		t.Error("B's siblings should be C and D")
	}

	if len(nodeE.Siblings) != 0 {
		t.Errorf("E should have 0 siblings (only child of C), got %d", len(nodeE.Siblings))
	}
}

func TestDeterministicTreeConstruction(t *testing.T) {
	participants := []string{"A", "B", "C", "D", "E"}
	blockHash := sha256.Sum256([]byte("test"))

	trees1 := BuildTrees(participants, blockHash[:], 2, 2)
	trees2 := BuildTrees(participants, blockHash[:], 2, 2)

	for i := range trees1 {
		if trees1[i].Root.Address != trees2[i].Root.Address {
			t.Errorf("tree %d roots differ: %s vs %s",
				i, trees1[i].Root.Address, trees2[i].Root.Address)
		}

		for addr := range trees1[i].Nodes {
			node1 := trees1[i].GetNode(addr)
			node2 := trees2[i].GetNode(addr)

			parent1 := ""
			if node1.Parent != nil {
				parent1 = node1.Parent.Address
			}

			parent2 := ""
			if node2.Parent != nil {
				parent2 = node2.Parent.Address
			}

			if parent1 != parent2 {
				t.Errorf("tree %d node %s: parent differs: %s vs %s",
					i, addr, parent1, parent2)
			}

			if len(node1.Children) != len(node2.Children) {
				t.Errorf("tree %d node %s: children count differs", i, addr)
			}
		}
	}
}

func TestBuildTreesWithWeights(t *testing.T) {
	participants := []WeightedParticipant{
		{Address: "addr1", Weight: 100},
		{Address: "addr2", Weight: 50},
		{Address: "addr3", Weight: 75},
		{Address: "addr4", Weight: 0},
		{Address: "addr5", Weight: 25},
	}

	blockHash := []byte("test-block-hash")
	trees := BuildTreesWithWeights(participants, blockHash, 2, 3)

	if len(trees) != 2 {
		t.Fatalf("expected 2 trees, got %d", len(trees))
	}

	for i, tree := range trees {
		t.Logf("\nTree %d:", i)
		t.Logf("  Order: %v", tree.Shuffled)
		t.Logf("  Root: %s", tree.Root.Address)

		for _, addr := range tree.Shuffled {
			node := tree.GetNode(addr)
			parent := ""
			if node.Parent != nil {
				parent = node.Parent.Address
			}
			childAddrs := make([]string, len(node.Children))
			for j, child := range node.Children {
				childAddrs[j] = child.Address
			}
			t.Logf("    %s: parent=%s, children=%v", addr, parent, childAddrs)
		}

		if tree.Index != i {
			t.Errorf("tree %d has wrong index: %d", i, tree.Index)
		}

		if len(tree.Nodes) != 5 {
			t.Errorf("tree %d should have 5 nodes (including zero-weight), got %d", i, len(tree.Nodes))
		}

		for _, p := range participants {
			if _, exists := tree.Nodes[p.Address]; !exists {
				t.Errorf("tree %d should contain %s", i, p.Address)
			}
		}
	}
}

func TestBuildTreesWithWeights_WeightOrdering(t *testing.T) {
	participants := make([]WeightedParticipant, 100)
	for i := 0; i < 100; i++ {
		participants[i] = WeightedParticipant{
			Address: formatAddress(i),
			Weight:  uint64((i + 1) * 10),
		}
	}

	blockHash := []byte("test-block-hash")
	numTrees := 3
	fanout := 4
	trees := BuildTreesWithWeights(participants, blockHash, numTrees, fanout)

	if len(trees) != numTrees {
		t.Fatalf("expected %d trees, got %d", numTrees, len(trees))
	}

	for treeIdx, tree := range trees {
		t.Logf("\n=== Tree %d ===", treeIdx)
		t.Logf("Root: %s", tree.Root.Address)
		t.Logf("Total nodes: %d", len(tree.Nodes))
		t.Logf("Fanout: %d", tree.Fanout)

		t.Logf("\nFirst 20 nodes:")
		count := 0
		for _, addr := range tree.Shuffled {
			if count >= 20 {
				break
			}
			node := tree.GetNode(addr)
			parent := ""
			if node.Parent != nil {
				parent = node.Parent.Address
			}

			childAddrs := make([]string, len(node.Children))
			for i, child := range node.Children {
				childAddrs[i] = child.Address
			}

			var weight uint64
			for _, p := range participants {
				if p.Address == addr {
					weight = p.Weight
					break
				}
			}

			t.Logf("  %s (weight=%d): parent=%s, children=%v",
				addr, weight, parent, childAddrs)
			count++
		}

		if len(tree.Shuffled) > 20 {
			t.Logf("  ... and %d more nodes", len(tree.Shuffled)-20)
		}

		rootChildCount := len(tree.Root.Children)
		if rootChildCount > fanout {
			t.Errorf("root has %d children, expected max %d", rootChildCount, fanout)
		}

		leafCount := 0
		for _, node := range tree.Nodes {
			if len(node.Children) == 0 {
				leafCount++
			}
		}
		t.Logf("Leaf nodes: %d", leafCount)
	}
}

func formatAddress(i int) string {
	if i < 10 {
		return "participant_00" + string(rune('0'+i))
	} else if i < 100 {
		tens := i / 10
		ones := i % 10
		return "participant_0" + string(rune('0'+tens)) + string(rune('0'+ones))
	}
	return "participant_" + string(rune('0'+i/100)) + string(rune('0'+(i/10)%10)) + string(rune('0'+i%10))
}
