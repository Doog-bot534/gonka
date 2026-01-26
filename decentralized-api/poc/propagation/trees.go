package propagation

import (
	"crypto/sha256"
	"encoding/binary"
	"math/rand"
)

type Node struct {
	Address  string
	Index    int
	Parent   *Node
	Children []*Node
	Siblings []*Node
}

type Tree struct {
	Index    int
	Shuffled []string
	Fanout   int
	Nodes    map[string]*Node
	Root     *Node
}

func BuildTrees(participants []string, blockHash []byte, numTrees, fanout int) []*Tree {
	trees := make([]*Tree, numTrees)
	for i := 0; i < numTrees; i++ {
		seed := sha256.Sum256(append(blockHash, byte(i)))
		shuffled := deterministicShuffle(participants, seed[:])
		trees[i] = buildTree(i, shuffled, fanout)
	}
	return trees
}

func buildTree(treeIndex int, shuffled []string, fanout int) *Tree {
	n := len(shuffled)
	t := &Tree{
		Index:    treeIndex,
		Shuffled: shuffled,
		Fanout:   fanout,
		Nodes:    make(map[string]*Node, n),
	}
	
	maxSiblings := fanout - 1
	if maxSiblings < 0 {
		maxSiblings = 0
	}
	
	for i, addr := range shuffled {
		node := &Node{
			Address:  addr,
			Index:    i,
			Children: make([]*Node, 0, fanout),
			Siblings: make([]*Node, 0, maxSiblings),
		}
		t.Nodes[addr] = node
		
		if i == 0 {
			t.Root = node
		}
	}
	
	for i := 1; i < n; i++ {
		addr := shuffled[i]
		node := t.Nodes[addr]
		parentIndex := (i - 1) / fanout
		parent := t.Nodes[shuffled[parentIndex]]
		
		node.Parent = parent
		
		for _, existingSibling := range parent.Children {
			node.Siblings = append(node.Siblings, existingSibling)
			existingSibling.Siblings = append(existingSibling.Siblings, node)
		}
		
		parent.Children = append(parent.Children, node)
	}
	
	return t
}

func (t *Tree) GetNode(addr string) *Node {
	return t.Nodes[addr]
}

func (t *Tree) Role(addr string) (parent string, children []string) {
	node := t.Nodes[addr]
	if node == nil {
		return "", nil
	}
	
	if node.Parent != nil {
		parent = node.Parent.Address
	}
	
	children = make([]string, len(node.Children))
	for i, child := range node.Children {
		children[i] = child.Address
	}
	
	return
}

func deterministicShuffle(list []string, seed []byte) []string {
	out := make([]string, len(list))
	copy(out, list)
	rng := rand.New(rand.NewSource(int64(binary.BigEndian.Uint64(seed[:8]))))
	for i := len(out) - 1; i > 0; i-- {
		j := rng.Intn(i + 1)
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func indexOf(list []string, target string) int {
	for i, v := range list {
		if v == target {
			return i
		}
	}
	return -1
}
