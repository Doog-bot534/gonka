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

type Result struct {
	ShufflePct         float64
	AttackerFraction   float64
	AttackerDist       string
	AvgUnreached       float64
	HonestNodes        int
	AvgHopCount        float64
	MaxHops            int
	Diameter           int
	AvgDegree          float64
	MaxDegree          int
	AvgMessagesPerNode float64
	TotalMessages      int
}

func propagateFLTQWithStats(cube *FLTQCube, startAddr string, attackers map[string]bool) (map[string]bool, int, int, int) {
	reached := make(map[string]bool)
	hopCounts := make(map[string]int)
	totalHops := 0
	maxHops := 0
	messagesSent := 0

	reached[startAddr] = true
	hopCounts[startAddr] = 0

	queue := []string{startAddr}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		if attackers[current] {
			continue
		}

		currentHops := hopCounts[current]

		node := cube.GetNode(current)
		if node == nil {
			continue
		}

		messagesSent += len(node.Neighbors)

		for _, neighborAddr := range node.Neighbors {
			if !reached[neighborAddr] {
				reached[neighborAddr] = true
				hopCounts[neighborAddr] = currentHops + 1
				totalHops += currentHops + 1
				if currentHops+1 > maxHops {
					maxHops = currentHops + 1
				}
				queue = append(queue, neighborAddr)
			}
		}
	}

	return reached, totalHops, maxHops, messagesSent
}

func main() {
	distFlag := flag.String("dist", "all", "Attacker distribution: uniform, highweight, slots, wald, or all")
	numSimulations := flag.Int("sims", 1, "Number of simulations per scenario")
	numParticipants := flag.Int("participants", 10000, "Number of participants")
	flag.Parse()

	startTime := time.Now()

	shufflePcts := []float64{0.05, 0.10, 0.15, 0.20, 0.25, 0.30}
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

	fmt.Println("FLTQ Message Propagation Blocking Analysis")
	fmt.Printf("Started at: %s\n", startTime.Format("2006-01-02 15:04:05"))
	fmt.Printf("Participants: %d, Simulations: %d, Workers: %d\n", *numParticipants, *numSimulations, runtime.NumCPU())
	fmt.Printf("Attacker distributions: %s\n", strings.Join(attackerDists, ", "))
	fmt.Println("Model: flood-forward through FLTQ neighbors; attackers block relay")
	fmt.Println("==========================================")

	type job struct {
		shufflePct       float64
		attackerFraction float64
		attackerDist     string
	}

	totalJobs := len(shufflePcts) * len(attackerFractions) * len(attackerDists)
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
				totalHopCountSum := 0.0
				maxHopsSum := 0
				diameterSum := 0
				totalMessagesSum := 0
				messagesPerNodeSum := 0.0
				totalDegreeSum := 0.0
				maxDegreeSum := 0

				for sim := 0; sim < *numSimulations; sim++ {
					participants := make([]WeightedParticipant, *numParticipants)
					for i := 0; i < *numParticipants; i++ {
						participants[i] = WeightedParticipant{
							Address: formatAddress(i),
							Weight:  uint64(1000 + i),
						}
					}

					blockHash := []byte{byte(sim), byte(sim >> 8), byte(sim >> 16), byte(sim >> 24), 0, 0, 0, 0}
					cube := buildFLTQWithWeights(participants, blockHash, j.shufflePct)

					var attackers map[string]bool
					if j.attackerDist == "slots" {
						simSeed := fmt.Sprintf("%s_%d_%f_%x", j.attackerDist, sim, j.attackerFraction, blockHash)
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

					startAddr := ""
					for i := 0; i < *numParticipants; i++ {
						addr := formatAddress(i)
						if !attackers[addr] {
							startAddr = addr
							break
						}
					}
					if startAddr == "" {
						continue
					}

					globalReached, totalHops, maxHops, messages := propagateFLTQWithStats(cube, startAddr, attackers)
					if len(globalReached) > 0 {
						totalHopCountSum += float64(totalHops) / float64(len(globalReached))
					}
					maxHopsSum += maxHops
					diameterSum += cube.Dimensions
					totalMessagesSum += messages
					messagesPerNodeSum += float64(messages) / float64(*numParticipants)

					totalDegree := 0
					maxDegree := 0
					for _, node := range cube.Nodes {
						degree := len(node.Neighbors)
						totalDegree += degree
						if degree > maxDegree {
							maxDegree = degree
						}
					}
					totalDegreeSum += float64(totalDegree) / float64(len(cube.Nodes))
					maxDegreeSum += maxDegree

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
				avgHopCount := totalHopCountSum / float64(*numSimulations)
				avgMaxHops := maxHopsSum / *numSimulations
				avgDiameter := diameterSum / *numSimulations
				avgMessagesPerNode := messagesPerNodeSum / float64(*numSimulations)
				avgTotalMessages := totalMessagesSum / *numSimulations
				avgDegree := totalDegreeSum / float64(*numSimulations)
				avgMaxDegree := maxDegreeSum / *numSimulations

				resultsChan <- Result{
					ShufflePct:         j.shufflePct,
					AttackerFraction:   j.attackerFraction,
					AttackerDist:       j.attackerDist,
					AvgUnreached:       avgUnreached,
					HonestNodes:        honestNodes,
					AvgHopCount:        avgHopCount,
					MaxHops:            avgMaxHops,
					Diameter:           avgDiameter,
					AvgDegree:          avgDegree,
					MaxDegree:          avgMaxDegree,
					AvgMessagesPerNode: avgMessagesPerNode,
					TotalMessages:      avgTotalMessages,
				}
			}
		}()
	}

	for _, shufflePct := range shufflePcts {
		for _, attackerFraction := range attackerFractions {
			for _, attackerDist := range attackerDists {
				jobs <- job{shufflePct, attackerFraction, attackerDist}
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

	printTables(results, attackerFractions, shufflePcts, attackerDists, *numParticipants)

	elapsed := time.Since(startTime)
	fmt.Printf("\nExecution completed in: %s\n", elapsed.Round(time.Millisecond))
}

func printTables(results []Result, attackerFractions []float64, shufflePcts []float64, attackerDists []string, numParticipants int) {
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

			fmt.Printf("\n=== Blocking Probability ===\n")
			fmt.Printf("| Attacker%% | Honest Nodes | Unreached | P(blocked) |\n")
			fmt.Printf("|-----------|--------------|-----------|------------|\n")
			for _, af := range attackerFractions {
				for _, r := range results {
					if r.AttackerDist == dist && r.ShufflePct == sp && r.AttackerFraction == af {
						fmt.Printf("| %-9.0f%% | %-12d | %-9.2f | %-10.6f |\n",
							af*100, r.HonestNodes, r.AvgUnreached, r.AvgUnreached/float64(r.HonestNodes))
						break
					}
				}
			}

			fmt.Printf("\n=== Network Statistics ===\n")
			fmt.Printf("| Attacker%% | Avg Degree | Max Degree | Avg Hops | Max Hops |\n")
			fmt.Printf("|-----------|------------|------------|----------|----------|\n")
			for _, af := range attackerFractions {
				for _, r := range results {
					if r.AttackerDist == dist && r.ShufflePct == sp && r.AttackerFraction == af {
						fmt.Printf("| %-9.0f%% | %-10.2f | %-10d | %-8.2f | %-8d |\n",
							af*100, r.AvgDegree, r.MaxDegree, r.AvgHopCount, r.MaxHops)
						break
					}
				}
			}
		}
	}

	fmt.Println("\n\n==========================================")
	fmt.Println("=== PROPAGATION STATISTICS ===")
	fmt.Println("Note: Messages shown are for ONE participant publishing")
	fmt.Printf("For ALL %d participants publishing: multiply 'Total Messages' by %d\n", numParticipants, numParticipants)

	for _, dist := range attackerDists {
		distName := "Uniform"
		if dist == "highweight" {
			distName = "High-Weight"
		} else if dist == "slots" {
			distName = "Slots"
		} else if dist == "wald" {
			distName = "Wald"
		}

		for _, sp := range shufflePcts {
			for _, af := range attackerFractions {
				fmt.Printf("\n--- %s Distribution, %.0f%% Attackers, Shuffle %.0f%% ---\n", distName, af*100, sp*100)
				fmt.Printf("| Avg Degree | Max Degree | Msgs (1 pub) | Msgs/Participant | Total if ALL publish |\n")
				fmt.Printf("|------------|------------|--------------|------------------|-----------------------|\n")

				for _, r := range results {
					if r.AttackerDist == dist && r.ShufflePct == sp && r.AttackerFraction == af {
						totalIfAll := int64(r.TotalMessages) * int64(numParticipants)

						fmt.Printf("| %-10.2f | %-10d | %-12d | %-16.2f | %-21s |\n",
							r.AvgDegree, r.MaxDegree, r.TotalMessages, r.AvgMessagesPerNode, formatLargeNumber(totalIfAll))
						break
					}
				}
			}
		}
	}
}

func formatAddress(i int) string {
	return fmt.Sprintf("addr%05d", i)
}

func formatLargeNumber(n int64) string {
	if n >= 1_000_000_000 {
		return fmt.Sprintf("%.2fB", float64(n)/1_000_000_000)
	} else if n >= 1_000_000 {
		return fmt.Sprintf("%.2fM", float64(n)/1_000_000)
	} else if n >= 1_000 {
		return fmt.Sprintf("%.2fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}

func buildFLTQWithWeights(participants []WeightedParticipant, blockHash []byte, shufflePct float64) *FLTQCube {
	if len(participants) == 0 {
		return &FLTQCube{
			Index:     0,
			Nodes:     make(map[string]*FLTQNode),
			Positions: []*FLTQNode{},
		}
	}

	realSize := len(participants)
	n := ceilLog2(realSize)
	cubeSize := 1 << n

	cube := &FLTQCube{
		Index:      0,
		Dimensions: n,
		Size:       cubeSize,
		Nodes:      make(map[string]*FLTQNode),
		Positions:  make([]*FLTQNode, cubeSize),
	}

	positionToParticipant := make([]string, cubeSize)
	shuffled := weightedDeterministicShuffle(participants, blockHash, shufflePct)
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

func (c *FLTQCube) GetNode(addr string) *FLTQNode {
	return c.Nodes[addr]
}

func ceilLog2(n int) int {
	if n <= 1 {
		return 0
	}
	return int(math.Ceil(math.Log2(float64(n))))
}

func weightedDeterministicShuffle(participants []WeightedParticipant, seed []byte, shufflePct float64) []string {
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
