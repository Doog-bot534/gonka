package main

import (
	"crypto/sha256"
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
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
	Root  *Node
	Nodes map[string]*Node
}

type Result struct {
	ShufflePct       float64
	Fanout           int
	Trees            int
	AttackerFraction float64
	AttackerDist     string
	AvgUnreached     float64
	HonestNodes      int
}

func propagate(tree *Tree, attackers map[string]bool) map[string]bool {
	reached := make(map[string]bool)
	if tree.Root == nil {
		return reached
	}

	reached[tree.Root.Address] = true

	queue := []*Node{tree.Root}
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]

		if attackers[node.Address] {
			continue
		}

		for _, child := range node.Children {
			if !reached[child.Address] {
				reached[child.Address] = true
				queue = append(queue, child)
			}
		}
	}
	return reached
}

func main() {
	distFlag := flag.String("dist", "all", "Attacker distribution: uniform, highweight, slots, wald, or all")
	numSimulations := flag.Int("sims", 10, "Number of simulations per scenario")
	numParticipants := flag.Int("participants", 10000, "Number of participants")
	flag.Parse()

	startTime := time.Now()

	shufflePcts := []float64{0.05, 0.10, 0.15, 0.20, 0.25, 0.30}
	fanouts := []int{4, 8, 16, 32}
	treeCounts := []int{4, 6, 8, 10}
	attackerFractions := []float64{0.33, 0.45}

	allDists := []string{"uniform", "highweight", "slots", "wald"}
	var attackerDists []string
	if *distFlag == "all" {
		attackerDists = allDists
	} else {
		for _, d := range strings.Split(*distFlag, ",") {
			d = strings.TrimSpace(d)
			valid := false
			for _, ad := range allDists {
				if d == ad {
					valid = true
					break
				}
			}
			if !valid {
				fmt.Printf("Unknown distribution: %s (valid: uniform, highweight, slots, wald, all)\n", d)
				return
			}
			attackerDists = append(attackerDists, d)
		}
	}

	fmt.Println("Multi-Tree Message Propagation Blocking Analysis")
	fmt.Printf("Started at: %s\n", startTime.Format("2006-01-02 15:04:05"))
	fmt.Printf("Participants: %d, Simulations: %d, Workers: %d\n", *numParticipants, *numSimulations, runtime.NumCPU())
	fmt.Printf("Attacker distributions: %s\n", strings.Join(attackerDists, ", "))
	fmt.Println("Model: sender -> all tree roots -> propagate down; attackers block relay")
	fmt.Println("==========================================")

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
				numAttackers := int(float64(*numParticipants) * j.attackerFraction)

				unreachedTotal := 0
				honestTotal := 0

				for sim := 0; sim < *numSimulations; sim++ {
					participants := make([]WeightedParticipant, *numParticipants)
					for i := 0; i < *numParticipants; i++ {
						participants[i] = WeightedParticipant{
							Address: formatAddress(i),
							Weight:  uint64(1000 + i),
						}
					}

					blockHash := []byte{byte(sim), byte(sim >> 8), byte(sim >> 16), byte(sim >> 24), 0, 0, 0, 0}
					trees := buildTreesWithWeights(participants, blockHash, j.numTrees, j.fanout, j.shufflePct)

					var attackers map[string]bool
					if j.attackerDist == "slots" {
						simSeed := fmt.Sprintf("%s_%d_%f_%d_%d_%f_%x", j.attackerDist, sim, j.shufflePct, j.fanout, j.numTrees, j.attackerFraction, blockHash)
						attackers = selectAttackersBySlots(participants, numAttackers, simSeed)
					} else if j.attackerDist == "wald" {
						attackers = selectAttackersByWald(participants, numAttackers, sim)
					} else {
						attackers = make(map[string]bool)
						step := *numParticipants / numAttackers
						if j.attackerDist == "uniform" {
							for i := 0; i < numAttackers; i++ {
								idx := (i * step) % *numParticipants
								attackers[formatAddress(idx)] = true
							}
						} else if j.attackerDist == "highweight" {
							for i := 0; i < numAttackers; i++ {
								idx := (*numParticipants - 1) - (i * step)
								attackers[formatAddress(idx)] = true
							}
						}
					}

					globalReached := make(map[string]bool)
					for _, tree := range trees {
						treeReached := propagate(tree, attackers)
						for addr := range treeReached {
							globalReached[addr] = true
						}
					}

					unreachedHonest := 0
					for i := 0; i < *numParticipants; i++ {
						addr := formatAddress(i)
						if !attackers[addr] && !globalReached[addr] {
							unreachedHonest++
						}
					}

					honestTotal += *numParticipants - len(attackers)
					unreachedTotal += unreachedHonest
				}

				honestNodes := honestTotal / *numSimulations
				avgUnreached := float64(unreachedTotal) / float64(*numSimulations)

				resultsChan <- Result{
					ShufflePct:       j.shufflePct,
					Fanout:           j.fanout,
					Trees:            j.numTrees,
					AttackerFraction: j.attackerFraction,
					AttackerDist:     j.attackerDist,
					AvgUnreached:     avgUnreached,
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
	completed := 0
	fmt.Printf("\nProgress: [")
	barWidth := 50
	for r := range resultsChan {
		results = append(results, r)
		completed++

		filled := int(float64(completed) / float64(totalJobs) * float64(barWidth))
		bar := ""
		for i := 0; i < barWidth; i++ {
			if i < filled {
				bar += "="
			} else if i == filled {
				bar += ">"
			} else {
				bar += " "
			}
		}

		percentage := float64(completed) / float64(totalJobs) * 100
		fmt.Printf("\rProgress: [%s] %.1f%% (%d/%d)", bar, percentage, completed, totalJobs)
	}
	fmt.Println()

	printTables(results, attackerFractions, fanouts, treeCounts, shufflePcts, attackerDists)

	elapsed := time.Since(startTime)
	fmt.Printf("\nExecution completed in: %s\n", elapsed.Round(time.Millisecond))
}

func printTables(results []Result, attackerFractions []float64, fanouts, treeCounts []int, shufflePcts []float64, attackerDists []string) {
	for _, dist := range attackerDists {
		distName := "Uniform Distribution"
		if dist == "highweight" {
			distName = "High-Weight Attackers"
		} else if dist == "slots" {
			distName = "Slot-Based Distribution (Weighted Random Sampling)"
		} else if dist == "wald" {
			distName = "Wald (Inverse Gaussian) Distribution"
		}
		fmt.Printf("\n\n========== ATTACKER DISTRIBUTION: %s ==========\n", distName)

		for _, sp := range shufflePcts {
			fmt.Printf("\n\n########## SHUFFLE PERCENTAGE: %.0f%% ##########\n", sp*100)

			for _, af := range attackerFractions {
				honestExample := 0
				for _, r := range results {
					if r.AttackerDist == dist && r.AttackerFraction == af {
						honestExample = r.HonestNodes
						break
					}
				}
				fmt.Printf("\n=== %.0f%% Attackers - Avg Unreached Honest Participants (honest nodes: %d) ===\n", af*100, honestExample)
				fmt.Printf("| Trees \\ Fanout |")
				for _, f := range fanouts {
					fmt.Printf(" %-14d |", f)
				}
				fmt.Println()
				fmt.Printf("|----------------|")
				for range fanouts {
					fmt.Printf("----------------|")
				}
				fmt.Println()

				for _, t := range treeCounts {
					fmt.Printf("| %-14d |", t)
					for _, f := range fanouts {
						for _, r := range results {
							if r.AttackerDist == dist && r.ShufflePct == sp && r.Fanout == f && r.Trees == t && r.AttackerFraction == af {
								fmt.Printf(" %-14.2f |", r.AvgUnreached)
								break
							}
						}
					}
					fmt.Println()
				}
			}

			for _, af := range attackerFractions {
				fmt.Printf("\n=== %.0f%% Attackers - P(single honest participant blocked) ===\n", af*100)
				fmt.Printf("| Trees \\ Fanout |")
				for _, f := range fanouts {
					fmt.Printf(" %-14d |", f)
				}
				fmt.Println()
				fmt.Printf("|----------------|")
				for range fanouts {
					fmt.Printf("----------------|")
				}
				fmt.Println()

				for _, t := range treeCounts {
					fmt.Printf("| %-14d |", t)
					for _, f := range fanouts {
						for _, r := range results {
							if r.AttackerDist == dist && r.ShufflePct == sp && r.Fanout == f && r.Trees == t && r.AttackerFraction == af {
								fmt.Printf(" %-14.6f |", r.AvgUnreached/float64(r.HonestNodes))
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

	t.Root = t.Nodes[shuffled[0]]

	return t
}

func weightedDeterministicShuffle(participants []WeightedParticipant, seed []byte, shufflePct float64) []string {
	n := len(participants)
	if n == 0 {
		return nil
	}

	type indexed struct {
		participant WeightedParticipant
		randomScore float64
	}

	items := make([]indexed, n)
	rng := rand.New(rand.NewSource(int64(binary.BigEndian.Uint64(seed[:8]))))

	for i, p := range participants {
		baseScore := float64(p.Weight)
		randomComponent := rng.Float64() * float64(p.Weight) * shufflePct
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

func slotRandomVal(appHash, participantAddress string, slotIdx int, totalWeight int64) int64 {
	seedData := fmt.Sprintf("%s%s%d", appHash, participantAddress, slotIdx)
	hash := sha256.Sum256([]byte(seedData))
	return int64(binary.BigEndian.Uint64(hash[:8]) % uint64(totalWeight))
}

func selectAttackersBySlots(participants []WeightedParticipant, numAttackers int, simSeed string) map[string]bool {
	hash := sha256.Sum256([]byte(simSeed))
	appHash := fmt.Sprintf("%x", hash[:16])

	type weightEntry struct {
		address string
		weight  int64
	}
	entries := make([]weightEntry, 0, len(participants))
	var totalWeight int64
	for _, p := range participants {
		if p.Weight <= 0 {
			continue
		}
		entries = append(entries, weightEntry{address: p.Address, weight: int64(p.Weight)})
		totalWeight += int64(p.Weight)
	}
	if totalWeight == 0 || len(entries) == 0 {
		return nil
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].address < entries[j].address
	})

	attackers := make(map[string]bool)
	slotIdx := 0
	for len(attackers) < numAttackers && len(attackers) < len(entries) {
		rv := slotRandomVal(appHash, "attacker_selection", slotIdx, totalWeight)
		cumulative := int64(0)
		for _, entry := range entries {
			cumulative += entry.weight
			if rv < cumulative {
				if !attackers[entry.address] {
					attackers[entry.address] = true
				}
				break
			}
		}
		slotIdx++
	}
	return attackers
}

// Michael–Schucany–Haas algorithm
func sampleWald(mu, lambda float64, rng *rand.Rand) float64 {
	nu := rng.NormFloat64()
	y := nu * nu
	x := mu + (mu*mu*y)/(2*lambda) - (mu/(2*lambda))*math.Sqrt(4*mu*lambda*y+mu*mu*y*y)

	u := rng.Float64()
	if u <= mu/(mu+x) {
		return x
	}
	return mu * mu / x
}

func selectAttackersByWald(participants []WeightedParticipant, numAttackers int, simSeed int) map[string]bool {
	rng := rand.New(rand.NewSource(int64(simSeed)))

	const lambda = 1.0

	type scored struct {
		address string
		score   float64
	}

	maxWeight := float64(0)
	for _, p := range participants {
		if float64(p.Weight) > maxWeight {
			maxWeight = float64(p.Weight)
		}
	}
	
	scores := make([]scored, len(participants))
	for i, p := range participants {
		mu := maxWeight / float64(p.Weight)
		waldVal := sampleWald(mu, lambda, rng)
		scores[i] = scored{
			address: p.Address,
			score:   waldVal,
		}
	}

	sort.Slice(scores, func(i, j int) bool {
		return scores[i].score < scores[j].score
	})

	attackers := make(map[string]bool)
	for i := 0; i < numAttackers && i < len(scores); i++ {
		attackers[scores[i].address] = true
	}

	return attackers
}
