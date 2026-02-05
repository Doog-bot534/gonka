package main

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand"
	"runtime"
	"sort"
	"sync"
)

type WeightedParticipant struct {
	Address string
	Weight  uint64
}

type Node struct {
	Address  string
	Index    int
	Parent   *Node
	Children []*Node
}

type Tree struct {
	Nodes map[string]*Node
}

type Result struct {
	ShufflePct       float64
	Fanout           int
	Trees            int
	AttackerFraction float64
	AttackerDist     string
	PParent          float64
	PBlock           float64
	Blocked          float64
	HonestNodes      int
}

func main() {
	numParticipants := 10000
	numSimulations := 100

	shufflePcts := []float64{0.10, 0.15, 0.20, 0.25, 0.30}
	fanouts := []int{4, 8, 16, 32}
	treeCounts := []int{4, 6, 8, 10}
	attackerFractions := []float64{0.33, 0.45}

	fmt.Println("Multi-Tree Blocking Probability Analysis")
	fmt.Printf("Participants: %d, Simulations: %d, Workers: %d\n", numParticipants, numSimulations, runtime.NumCPU())
	fmt.Println("==========================================")

	attackerDists := []string{"uniform", "highweight"}

	type job struct {
		shufflePct       float64
		fanout           int
		numTrees         int
		attackerFraction float64
		attackerDist     string
	}

	totalJobs := len(shufflePcts) * len(fanouts) * len(treeCounts) * len(attackerFractions) * len(attackerDists)
	jobs := make(chan job, totalJobs)
	resultsChan := make(chan Result, totalJobs)

	var wg sync.WaitGroup
	numWorkers := runtime.NumCPU()

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				numAttackers := int(float64(numParticipants) * j.attackerFraction)

				blockedTotal := 0
				parentAttacker := 0
				totalParentChecks := 0

				for sim := 0; sim < numSimulations; sim++ {
					participants := make([]WeightedParticipant, numParticipants)
					for i := 0; i < numParticipants; i++ {
						participants[i] = WeightedParticipant{
							Address: formatAddress(i),
							Weight:  uint64(1000 + i),
						}
					}

					blockHash := []byte{byte(sim), byte(sim >> 8), byte(sim >> 16), byte(sim >> 24), 0, 0, 0, 0}
					trees := buildTreesWithWeights(participants, blockHash, j.numTrees, j.fanout, j.shufflePct)

					attackers := make(map[string]bool)
					step := numParticipants / numAttackers
					if j.attackerDist == "uniform" {
						for i := 0; i < numAttackers; i++ {
							idx := (i * step) % numParticipants
							attackers[formatAddress(idx)] = true
						}
					} else {
						for i := 0; i < numAttackers; i++ {
							idx := (numParticipants - 1) - (i * step)
							attackers[formatAddress(idx)] = true
						}
					}

					for _, tree := range trees {
						for _, node := range tree.Nodes {
							if node.Parent == nil {
								continue
							}
							totalParentChecks++
							if attackers[node.Parent.Address] {
								parentAttacker++
							}
						}
					}

					for i := 0; i < numParticipants; i++ {
						addr := formatAddress(i)

						if !attackers[addr] {
							blocked := true
							for _, tree := range trees {
								node := tree.Nodes[addr]
								if node == nil || node.Parent == nil {
									blocked = false
									break
								}
								if !attackers[node.Parent.Address] {
									blocked = false
									break
								}
							}
							if blocked {
								blockedTotal++
							}
						}
					}
				}

				honestNodes := numParticipants - numAttackers
				pParent := float64(parentAttacker) / float64(totalParentChecks)
				avgBlocked := float64(blockedTotal) / float64(numSimulations)

				resultsChan <- Result{
					ShufflePct:       j.shufflePct,
					Fanout:           j.fanout,
					Trees:            j.numTrees,
					AttackerFraction: j.attackerFraction,
					AttackerDist:     j.attackerDist,
					PParent:          pParent,
					PBlock:           avgBlocked / float64(honestNodes),
					Blocked:          avgBlocked,
					HonestNodes:      honestNodes,
				}
			}
		}()
	}

	for _, shufflePct := range shufflePcts {
		for _, fanout := range fanouts {
			for _, numTrees := range treeCounts {
				for _, attackerFraction := range attackerFractions {
					for _, attackerDist := range attackerDists {
						jobs <- job{shufflePct, fanout, numTrees, attackerFraction, attackerDist}
					}
				}
			}
		}
	}
	close(jobs)

	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	var results []Result
	for r := range resultsChan {
		results = append(results, r)
	}

	printTables(results, attackerFractions, fanouts, treeCounts, shufflePcts, attackerDists)
}

func printTables(results []Result, attackerFractions []float64, fanouts, treeCounts []int, shufflePcts []float64, attackerDists []string) {
	for _, dist := range attackerDists {
		distName := "Uniform Distribution"
		if dist == "highweight" {
			distName = "High-Weight Attackers"
		}
		fmt.Printf("\n\n========== ATTACKER DISTRIBUTION: %s ==========\n", distName)

		for _, sp := range shufflePcts {
			fmt.Printf("\n\n########## SHUFFLE PERCENTAGE: %.0f%% ##########\n", sp*100)

			for _, af := range attackerFractions {
				fmt.Printf("\n=== %.0f%% Attackers - Blocked Participants ===\n", af*100)
				fmt.Printf("| Trees \\ Fanout |")
				for _, f := range fanouts {
					fmt.Printf(" %-12d |", f)
				}
				fmt.Println()
				fmt.Printf("|----------------|")
				for range fanouts {
					fmt.Printf("--------------|")
				}
				fmt.Println()

				for _, t := range treeCounts {
					fmt.Printf("| %-14d |", t)
					for _, f := range fanouts {
						for _, r := range results {
							if r.AttackerDist == dist && r.ShufflePct == sp && r.Fanout == f && r.Trees == t && r.AttackerFraction == af {
								fmt.Printf(" %-12.2f |", r.Blocked)
								break
							}
						}
					}
					fmt.Println()
				}
			}

			fmt.Println("\n=== P(parent=attacker) by Fanout ===")
			fmt.Printf("| Fanout | 33%% Attackers | 45%% Attackers |\n")
			fmt.Printf("|--------|---------------|---------------|\n")
			for _, f := range fanouts {
				var p33, p45 float64
				for _, r := range results {
					if r.AttackerDist == dist && r.ShufflePct == sp && r.Fanout == f && r.Trees == treeCounts[0] {
						if r.AttackerFraction == 0.33 {
							p33 = r.PParent
						} else {
							p45 = r.PParent
						}
					}
				}
				fmt.Printf("| %-6d | %-13.4f | %-13.4f |\n", f, p33, p45)
			}

			for _, af := range attackerFractions {
				fmt.Printf("\n=== P(block one) - %.0f%% Attackers ===\n", af*100)
				fmt.Printf("| Trees \\ Fanout |")
				for _, f := range fanouts {
					fmt.Printf(" %-12d |", f)
				}
				fmt.Println()
				fmt.Printf("|----------------|")
				for range fanouts {
					fmt.Printf("--------------|")
				}
				fmt.Println()

				for _, t := range treeCounts {
					fmt.Printf("| %-14d |", t)
					for _, f := range fanouts {
						for _, r := range results {
							if r.AttackerDist == dist && r.ShufflePct == sp && r.Fanout == f && r.Trees == t && r.AttackerFraction == af {
								fmt.Printf(" %-12.2e |", r.PBlock)
								break
							}
						}
					}
					fmt.Println()
				}
			}
		}
	}
}

func formatAddress(i int) string {
	return fmt.Sprintf("addr%05d", i)
}

func buildTreesWithWeights(participants []WeightedParticipant, blockHash []byte, numTrees, fanout int, shufflePct float64) []*Tree {
	if len(participants) == 0 {
		return []*Tree{}
	}

	trees := make([]*Tree, numTrees)
	for i := 0; i < numTrees; i++ {
		seed := sha256.Sum256(append(blockHash, byte(i)))
		shuffled := weightedDeterministicShuffle(participants, seed[:], shufflePct)
		trees[i] = buildTree(shuffled, fanout)
	}
	return trees
}

func buildTree(shuffled []string, fanout int) *Tree {
	n := len(shuffled)
	t := &Tree{
		Nodes: make(map[string]*Node, n),
	}

	for i, addr := range shuffled {
		node := &Node{
			Address:  addr,
			Index:    i,
			Children: make([]*Node, 0, fanout),
		}
		t.Nodes[addr] = node
	}

	for i := 1; i < n; i++ {
		addr := shuffled[i]
		node := t.Nodes[addr]
		parentIndex := (i - 1) / fanout
		parent := t.Nodes[shuffled[parentIndex]]
		node.Parent = parent
		parent.Children = append(parent.Children, node)
	}

	return t
}

func weightedDeterministicShuffle(participants []WeightedParticipant, seed []byte, shufflePct float64) []string {
	n := len(participants)
	if n == 0 {
		return nil
	}

	sorted := make([]WeightedParticipant, n)
	copy(sorted, participants)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Weight > sorted[j].Weight
	})

	seedInt := binary.BigEndian.Uint64(seed[:8])
	rng := rand.New(rand.NewSource(int64(seedInt)))

	swapProbability := shufflePct
	maxSwapDistance := n / 10
	if maxSwapDistance < 1 {
		maxSwapDistance = 1
	}

	result := make([]string, n)
	for i, p := range sorted {
		result[i] = p.Address
	}

	for i := 0; i < n; i++ {
		if rng.Float64() < swapProbability {
			maxJ := i + maxSwapDistance
			if maxJ >= n {
				maxJ = n - 1
			}
			minJ := i - maxSwapDistance
			if minJ < 0 {
				minJ = 0
			}

			j := minJ + rng.Intn(maxJ-minJ+1)
			result[i], result[j] = result[j], result[i]
		}
	}

	return result
}
