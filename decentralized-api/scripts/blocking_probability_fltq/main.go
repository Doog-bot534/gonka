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
	EntriesPerLevel    int
	Hops               int
	AvgUnreached       float64
	HonestNodes        int
	AvgHopCount        float64
	MaxHops            int
	Diameter           int
	AvgMessagesPerNode float64
	TotalMessages      int
	AvgNeighbors       float64
	MaxNeighbors       int
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
	attackerFractions := []float64{0.00, 0.33, 0.45}
	entriesPerLevelValues := []int{4, 6, 8, 10, 12}
	hopsValues := []int{2, 3, 4, 5}

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
		entriesPerLevel  int
		hops             int
	}

	totalJobs := len(shufflePcts) * len(attackerFractions) * len(attackerDists) * len(entriesPerLevelValues) * len(hopsValues)
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
				avgNeighborsSum := 0.0
				maxNeighborsSum := 0

				for sim := 0; sim < *numSimulations; sim++ {
					participants := make([]WeightedParticipant, *numParticipants)
					for i := 0; i < *numParticipants; i++ {
						participants[i] = WeightedParticipant{
							Address: formatAddress(i),
							Weight:  uint64(1000 + i),
						}
					}

					blockHash := []byte{byte(sim), byte(sim >> 8), byte(sim >> 16), byte(sim >> 24), 0, 0, 0, 0}
					cube := buildFLTQWithWeights(participants, blockHash, j.shufflePct, j.entriesPerLevel, j.hops)

					totalNeighbors := 0
					maxNeighbors := 0
					for _, node := range cube.Nodes {
						neighborCount := len(node.Neighbors)
						totalNeighbors += neighborCount
						if neighborCount > maxNeighbors {
							maxNeighbors = neighborCount
						}
					}
					avgNeighbors := float64(totalNeighbors) / float64(len(cube.Nodes))
					avgNeighborsSum += avgNeighbors
					maxNeighborsSum += maxNeighbors

					var attackers map[string]bool
					if numAttackers == 0 {
						attackers = make(map[string]bool)
					} else if j.attackerDist == "slots" {
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
				avgNeighbors := avgNeighborsSum / float64(*numSimulations)
				avgMaxNeighbors := maxNeighborsSum / *numSimulations

				resultsChan <- Result{
					ShufflePct:         j.shufflePct,
					AttackerFraction:   j.attackerFraction,
					AttackerDist:       j.attackerDist,
					EntriesPerLevel:    j.entriesPerLevel,
					Hops:               j.hops,
					AvgUnreached:       avgUnreached,
					HonestNodes:        honestNodes,
					AvgHopCount:        avgHopCount,
					MaxHops:            avgMaxHops,
					Diameter:           avgDiameter,
					AvgMessagesPerNode: avgMessagesPerNode,
					TotalMessages:      avgTotalMessages,
					AvgNeighbors:       avgNeighbors,
					MaxNeighbors:       avgMaxNeighbors,
				}
			}
		}()
	}

	for _, shufflePct := range shufflePcts {
		for _, attackerFraction := range attackerFractions {
			for _, attackerDist := range attackerDists {
				for _, entriesPerLevel := range entriesPerLevelValues {
					for _, hops := range hopsValues {
						jobs <- job{shufflePct, attackerFraction, attackerDist, entriesPerLevel, hops}
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

	printTables(results, attackerFractions, shufflePcts, attackerDists, entriesPerLevelValues, hopsValues, *numParticipants)

	elapsed := time.Since(startTime)
	fmt.Printf("\nExecution completed in: %s\n", elapsed.Round(time.Millisecond))
}

func printTables(results []Result, attackerFractions []float64, shufflePcts []float64, attackerDists []string, entriesPerLevelValues []int, hopsValues []int, numParticipants int) {
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

		for _, entriesPerLevel := range entriesPerLevelValues {
			for _, hops := range hopsValues {
				fmt.Printf("\n\n########## ENTRIES PER LEVEL: %d, HOPS: %d ##########\n", entriesPerLevel, hops)

				for _, sp := range shufflePcts {
					fmt.Printf("\n\n>>> SHUFFLE PERCENTAGE: %.0f%% <<<\n", sp*100)

					fmt.Printf("\n=== Blocking Probability ===\n")
					fmt.Printf("| Attacker%% | Honest Nodes | Unreached | P(blocked) |\n")
					fmt.Printf("|-----------|--------------|-----------|------------|\n")
					for _, af := range attackerFractions {
						for _, r := range results {
							if r.AttackerDist == dist && r.ShufflePct == sp && r.AttackerFraction == af && r.EntriesPerLevel == entriesPerLevel && r.Hops == hops {
								fmt.Printf("| %-9.0f%% | %-12d | %-9.2f | %-10.6f |\n",
									af*100, r.HonestNodes, r.AvgUnreached, r.AvgUnreached/float64(r.HonestNodes))
								break
							}
						}
					}

					fmt.Printf("\n=== Network Statistics ===\n")
					fmt.Printf("| Attacker%% | Avg Neighbors | Max Neighbors | Avg Hops | Max Hops | Diameter |\n")
					fmt.Printf("|-----------|---------------|---------------|----------|----------|----------|\n")
					for _, af := range attackerFractions {
						for _, r := range results {
							if r.AttackerDist == dist && r.ShufflePct == sp && r.AttackerFraction == af && r.EntriesPerLevel == entriesPerLevel && r.Hops == hops {
								fmt.Printf("| %-9.0f%% | %-13.2f | %-13d | %-8.2f | %-8d | %-8d |\n",
									af*100, r.AvgNeighbors, r.MaxNeighbors, r.AvgHopCount, r.MaxHops, r.Diameter)
								break
							}
						}
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

		for _, entriesPerLevel := range entriesPerLevelValues {
			for _, hops := range hopsValues {
				for _, sp := range shufflePcts {
					for _, af := range attackerFractions {
						fmt.Printf("\n--- %s Distribution, %.0f%% Attackers, Shuffle %.0f%%, Entries/Level: %d, Hops: %d ---\n", distName, af*100, sp*100, entriesPerLevel, hops)
						fmt.Printf("| Avg Neighbors | Max Neighbors | Msgs (1 pub) | Msgs/Participant | Total if ALL publish |\n")
						fmt.Printf("|---------------|---------------|--------------|------------------|-----------------------|\n")

						for _, r := range results {
							if r.AttackerDist == dist && r.ShufflePct == sp && r.AttackerFraction == af && r.EntriesPerLevel == entriesPerLevel && r.Hops == hops {
								totalIfAll := int64(r.TotalMessages) * int64(numParticipants)

								fmt.Printf("| %-13.2f | %-13d | %-12d | %-16.2f | %-21s |\n",
									r.AvgNeighbors, r.MaxNeighbors, r.TotalMessages, r.AvgMessagesPerNode, formatLargeNumber(totalIfAll))
								break
							}
						}
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

func buildFLTQWithWeights(participants []WeightedParticipant, blockHash []byte, shufflePct float64, entriesPerLevel int, hops int) *FLTQCube {
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

	buildPastryEdges(cube, blockHash, entriesPerLevel, hops)

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

func splitBits(n int, hops int) []int {
	if n <= 0 || hops <= 0 {
		return []int{}
	}

	sizes := make([]int, hops)
	base := n / hops
	remainder := n % hops

	for i := 0; i < hops; i++ {
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

func buildPastryEdges(cube *FLTQCube, seed []byte, maxEntriesPerLevel int, hops int) {
	digitSizes := splitBits(cube.Dimensions, hops)
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

			limit := maxEntriesPerLevel
			if limit > len(possibleValues) {
				limit = len(possibleValues)
			}
			if limit <= 0 {
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
			node.Neighbors = make([]string, 0, len(neighborsMap))
			for neighborAddr := range neighborsMap {
				node.Neighbors = append(node.Neighbors, neighborAddr)
			}
		}
	}
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
