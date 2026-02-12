package main

import (
	"crypto/sha256"
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"math/bits"
	"math/rand"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"decentralized-api/poc/propagation"
)

type Result struct {
	NumHypercubes      int
	AttackerFraction   float64
	AttackerDist       string
	DistanceFactor     float64
	AvgUnreached       float64
	HonestNodes        int
	AvgMessagesPerNode float64
	TotalMessages      int
}

func propagateHypercube(hypercubes []*propagation.Hypercube, publisher string, attackers map[string]bool) map[string]bool {
	reached := make(map[string]bool)
	reached[publisher] = true

	visited := make(map[string]bool)
	visited[publisher] = true

	queue := []string{publisher}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		if attackers[current] {
			continue
		}

		allNeighbors := make(map[string]bool)
		for _, hc := range hypercubes {
			node := hc.GetNode(current)
			if node == nil {
				continue
			}
			for _, neighborAddr := range node.Neighbors {
				if !visited[neighborAddr] {
					allNeighbors[neighborAddr] = true
				}
			}
		}

		for neighborAddr := range allNeighbors {
			if !visited[neighborAddr] {
				visited[neighborAddr] = true
				reached[neighborAddr] = true
				queue = append(queue, neighborAddr)
			}
		}
	}

	return reached
}

func propagateHypercubeWithStats(hypercubes []*propagation.Hypercube, publisher string, attackers map[string]bool) (map[string]bool, int) {
	reached := make(map[string]bool)
	reached[publisher] = true

	visited := make(map[string]bool)
	visited[publisher] = true

	totalMessagesSent := 0
	queue := []string{publisher}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		if attackers[current] {
			continue
		}

		allNeighbors := make(map[string]bool)
		for _, hc := range hypercubes {
			node := hc.GetNode(current)
			if node == nil {
				continue
			}
			for _, neighborAddr := range node.Neighbors {
				allNeighbors[neighborAddr] = true
			}
		}

		totalMessagesSent += len(allNeighbors)

		for neighborAddr := range allNeighbors {
			if !visited[neighborAddr] {
				visited[neighborAddr] = true
				reached[neighborAddr] = true
				queue = append(queue, neighborAddr)
			}
		}
	}

	return reached, totalMessagesSent
}

func xorDistance(a, b uint16) int {
	return bits.OnesCount16(a ^ b)
}

func propagateHypercubeDistanceBased(hypercubes []*propagation.Hypercube, publisher string, attackers map[string]bool, distanceFactor float64) (map[string]bool, int) {
	reached := make(map[string]bool)
	reached[publisher] = true

	visited := make(map[string]bool)
	visited[publisher] = true

	totalMessagesSent := 0
	queue := []string{publisher}

	publisherNode := hypercubes[0].GetNode(publisher)
	if publisherNode == nil {
		return reached, totalMessagesSent
	}

	dimensions := hypercubes[0].Dimensions
	maxForwardNeighbors := int(math.Round(float64(dimensions) / distanceFactor))
	if maxForwardNeighbors < 1 {
		maxForwardNeighbors = 1
	}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		if attackers[current] {
			continue
		}

		currentNode := hypercubes[0].GetNode(current)
		if currentNode == nil {
			continue
		}

		type neighborDist struct {
			addr string
			dist int
		}

		allNeighbors := make(map[string]bool)
		for _, hc := range hypercubes {
			node := hc.GetNode(current)
			if node == nil {
				continue
			}
			for _, neighborAddr := range node.Neighbors {
				allNeighbors[neighborAddr] = true
			}
		}

		if distanceFactor >= 1.0 {
			neighborDistances := make([]neighborDist, 0, len(allNeighbors))
			for neighborAddr := range allNeighbors {
				neighborNode := hypercubes[0].GetNode(neighborAddr)
				if neighborNode == nil {
					continue
				}
				dist := xorDistance(neighborNode.Position, publisherNode.Position)
				neighborDistances = append(neighborDistances, neighborDist{addr: neighborAddr, dist: dist})
			}

			sort.Slice(neighborDistances, func(i, j int) bool {
				return neighborDistances[i].dist > neighborDistances[j].dist
			})

			selectedNeighbors := make(map[string]bool)
			for i := 0; i < len(neighborDistances) && i < maxForwardNeighbors; i++ {
				selectedNeighbors[neighborDistances[i].addr] = true
			}

			totalMessagesSent += len(selectedNeighbors)

			for neighborAddr := range selectedNeighbors {
				if !visited[neighborAddr] {
					visited[neighborAddr] = true
					reached[neighborAddr] = true
					queue = append(queue, neighborAddr)
				}
			}
		} else {
			totalMessagesSent += len(allNeighbors)

			for neighborAddr := range allNeighbors {
				if !visited[neighborAddr] {
					visited[neighborAddr] = true
					reached[neighborAddr] = true
					queue = append(queue, neighborAddr)
				}
			}
		}
	}

	return reached, totalMessagesSent
}

func main() {
	distFlag := flag.String("dist", "all", "Attacker distribution: uniform, highweight, slots, wald, or all")
	numSimulations := flag.Int("sims", 1, "Number of simulations per scenario")
	numParticipants := flag.Int("participants", 10000, "Number of participants")
	distanceFactorsFlag := flag.String("factors", "1.5", "Distance factors (comma-separated): e.g., '1,1.3,1.5,2,3' (1=full flood)")
	flag.Parse()

	startTime := time.Now()

	hypercubeCounts := []int{1, 2}
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

	var distanceFactors []float64
	for _, fStr := range strings.Split(*distanceFactorsFlag, ",") {
		fStr = strings.TrimSpace(fStr)
		var f float64
		if _, err := fmt.Sscanf(fStr, "%f", &f); err != nil {
			fmt.Printf("Invalid distance factor: %s\n", fStr)
			return
		}
		if f < 0.1 || f > 10 {
			fmt.Printf("Distance factor out of range (0.1-10): %f\n", f)
			return
		}
		distanceFactors = append(distanceFactors, f)
	}

	fmt.Println("Hypercube Message Propagation Blocking Analysis")
	fmt.Printf("Started at: %s\n", startTime.Format("2006-01-02 15:04:05"))
	fmt.Printf("Participants: %d, Simulations: %d, Workers: %d\n", *numParticipants, *numSimulations, runtime.NumCPU())
	fmt.Printf("Attacker distributions: %s\n", strings.Join(attackerDists, ", "))
	fmt.Printf("Distance factors: %v\n", distanceFactors)
	fmt.Printf("Publisher: random honest participant (different per simulation)\n")
	fmt.Println("Model: distance-based forwarding - forward to top (d/factor) farthest neighbors from publisher")
	fmt.Println("==========================================")

	sampleParticipants := make([]propagation.WeightedParticipant, *numParticipants)
	for i := 0; i < *numParticipants; i++ {
		sampleParticipants[i] = propagation.WeightedParticipant{
			Address: formatAddress(i),
			Weight:  uint64(1000 + i),
		}
	}
	sampleHash := []byte{0, 0, 0, 0, 0, 0, 0, 0}

	for _, numHC := range hypercubeCounts {
		sampleHCs := propagation.BuildHypercubesWithWeights(sampleParticipants, sampleHash, numHC)

		totalConns := 0
		connCounts := make(map[string]int)
		for _, hc := range sampleHCs {
			for addr, node := range hc.Nodes {
				for _, neighbor := range node.Neighbors {
					connKey := addr + "->" + neighbor
					if addr < neighbor {
						connKey = addr + "->" + neighbor
					} else {
						connKey = neighbor + "->" + addr
					}
					connCounts[connKey] = 1
				}
			}
		}
		totalConns = len(connCounts)

		dimensions := 0
		if len(sampleHCs) > 0 {
			dimensions = sampleHCs[0].Dimensions
		}

		allNeighbors := make(map[string]map[string]bool)
		for _, hc := range sampleHCs {
			for addr, node := range hc.Nodes {
				if allNeighbors[addr] == nil {
					allNeighbors[addr] = make(map[string]bool)
				}
				for _, neighbor := range node.Neighbors {
					allNeighbors[addr][neighbor] = true
				}
			}
		}

		maxConns, minConns, totalParticipantConns := 0, 999999, 0
		for addr := range allNeighbors {
			conns := len(allNeighbors[addr])
			totalParticipantConns += conns
			if conns > maxConns {
				maxConns = conns
			}
			if conns < minConns {
				minConns = conns
			}
		}
		avgConns := float64(totalParticipantConns) / float64(*numParticipants)

		fmt.Printf("\n--- Topology Statistics (%d hypercube(s)) ---\n", numHC)
		fmt.Printf("Dimensions per hypercube: %d\n", dimensions)
		fmt.Printf("Total unique connections: %d\n", totalConns)
		fmt.Printf("Connections per participant: min=%d, max=%d, avg=%.1f\n", minConns, maxConns, avgConns)
	}
	fmt.Println("==========================================")

	type job struct {
		numHypercubes    int
		attackerFraction float64
		attackerDist     string
		distanceFactor   float64
	}

	totalJobs := len(hypercubeCounts) * len(attackerFractions) * len(attackerDists) * len(distanceFactors)
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
				totalMessagesSum := 0
				messagesPerNodeSum := 0.0

				for sim := 0; sim < *numSimulations; sim++ {
					participants := make([]propagation.WeightedParticipant, *numParticipants)
					for i := 0; i < *numParticipants; i++ {
						participants[i] = propagation.WeightedParticipant{
							Address: formatAddress(i),
							Weight:  uint64(1000 + i),
						}
					}

					blockHash := []byte{byte(sim), byte(sim >> 8), byte(sim >> 16), byte(sim >> 24), 0, 0, 0, 0}
					hypercubes := propagation.BuildHypercubesWithWeights(participants, blockHash, j.numHypercubes)

					var attackers map[string]bool
					if j.attackerDist == "slots" {
						simSeed := fmt.Sprintf("%s_%d_%d_%f_%x", j.attackerDist, sim, j.numHypercubes, j.attackerFraction, blockHash)
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

					honestParticipants := make([]int, 0, *numParticipants-len(attackers))
					for i := 0; i < *numParticipants; i++ {
						if !attackers[formatAddress(i)] {
							honestParticipants = append(honestParticipants, i)
						}
					}

					publisherRng := rand.New(rand.NewSource(int64(binary.BigEndian.Uint64(blockHash[:8]))))
					publisherIdx := honestParticipants[publisherRng.Intn(len(honestParticipants))]
					publisher := formatAddress(publisherIdx)

					if (j.attackerFraction == attackerFractions[0]) && (j.attackerDist == attackerDists[0]) && (j.distanceFactor == distanceFactors[0]) {
						fmt.Printf("Sim %d: Publisher=%s (idx=%d, weight=%d)\n",
							sim+1, publisher, publisherIdx, 1000+publisherIdx)
					}

					var reached map[string]bool
					var totalMessages int
					if j.distanceFactor == 1.0 {
						reached, totalMessages = propagateHypercubeWithStats(hypercubes, publisher, attackers)
					} else {
						reached, totalMessages = propagateHypercubeDistanceBased(hypercubes, publisher, attackers, j.distanceFactor)
					}

					totalMessagesSum += totalMessages
					messagesPerNodeSum += float64(totalMessages) / float64(*numParticipants)

					unreachedHonest := 0
					for i := 0; i < *numParticipants; i++ {
						addr := formatAddress(i)
						if !attackers[addr] && !reached[addr] {
							unreachedHonest++
						}
					}

					honestTotal += *numParticipants - len(attackers)
					unreachedTotal += unreachedHonest
				}

				honestNodes := honestTotal / *numSimulations
				avgUnreached := float64(unreachedTotal) / float64(*numSimulations)
				avgMessagesPerNode := messagesPerNodeSum / float64(*numSimulations)
				avgTotalMessages := totalMessagesSum / *numSimulations

				resultsChan <- Result{
					NumHypercubes:      j.numHypercubes,
					AttackerFraction:   j.attackerFraction,
					AttackerDist:       j.attackerDist,
					DistanceFactor:     j.distanceFactor,
					AvgUnreached:       avgUnreached,
					HonestNodes:        honestNodes,
					AvgMessagesPerNode: avgMessagesPerNode,
					TotalMessages:      avgTotalMessages,
				}
			}
		}()
	}

	for _, numHypercubes := range hypercubeCounts {
		for _, attackerFraction := range attackerFractions {
			for _, attackerDist := range attackerDists {
				for _, distFactor := range distanceFactors {
					jobs <- job{numHypercubes, attackerFraction, attackerDist, distFactor}
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

	printTables(results, attackerFractions, hypercubeCounts, attackerDists, distanceFactors)

	fmt.Println("\n\n==========================================")
	fmt.Println("=== PROPAGATION STATISTICS ===")
	fmt.Println("Note: Messages shown are for ONE participant publishing")
	fmt.Printf("For ALL %d participants publishing: multiply 'Total Messages' by %d\n", *numParticipants, *numParticipants)

	for _, dist := range attackerDists {
		distName := "Uniform"
		if dist == "highweight" {
			distName = "High-Weight"
		} else if dist == "slots" {
			distName = "Slots"
		} else if dist == "wald" {
			distName = "Wald"
		}

		for _, af := range attackerFractions {
			fmt.Printf("\n--- %s Distribution, %.0f%% Attackers ---\n", distName, af*100)
			fmt.Printf("| Factor | Neighbors | Msgs (1 pub) | Msgs/Participant | Bandwidth %% | Total if ALL publish |\n")
			fmt.Printf("|--------|-----------|--------------|------------------|-------------|----------------------|\n")

			for _, factor := range distanceFactors {
				for _, hc := range hypercubeCounts {
					for _, r := range results {
						if r.AttackerDist == dist && r.NumHypercubes == hc && r.AttackerFraction == af && r.DistanceFactor == factor {
							totalIfAll := int64(r.TotalMessages) * int64(*numParticipants)

							sampleHCs := propagation.BuildHypercubesWithWeights(sampleParticipants, sampleHash, hc)
							dimensions := 0
							if len(sampleHCs) > 0 {
								dimensions = sampleHCs[0].Dimensions
							}
							maxNeighbors := int(math.Round(float64(dimensions) / factor))
							if maxNeighbors < 1 {
								maxNeighbors = 1
							}

							baselineResult := Result{}
							for _, br := range results {
								if br.AttackerDist == dist && br.NumHypercubes == hc && br.AttackerFraction == af && br.DistanceFactor == 1.0 {
									baselineResult = br
									break
								}
							}

							bandwidthPct := 100.0
							if baselineResult.TotalMessages > 0 {
								bandwidthPct = (float64(r.TotalMessages) / float64(baselineResult.TotalMessages)) * 100
							}

							factorStr := fmt.Sprintf("d/%.1f", factor)
							if factor == 1.0 {
								factorStr = "d (full)"
							}

							fmt.Printf("| %-6s | %-9d | %-12d | %-16.2f | %-11.1f | %-20s |\n",
								factorStr, maxNeighbors, r.TotalMessages, r.AvgMessagesPerNode, bandwidthPct, formatLargeNumber(totalIfAll))
							break
						}
					}
				}
			}
		}
	}

	elapsed := time.Since(startTime)
	fmt.Printf("\nExecution completed in: %s\n", elapsed.Round(time.Millisecond))
}

func printTables(results []Result, attackerFractions []float64, hypercubeCounts []int, attackerDists []string, distanceFactors []float64) {
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

		for _, af := range attackerFractions {
			honestExample := 0
			for _, r := range results {
				if r.AttackerDist == dist && r.AttackerFraction == af {
					honestExample = r.HonestNodes
					break
				}
			}
			fmt.Printf("\n=== %.0f%% Attackers - Avg Unreached Honest Participants (honest nodes: %d) ===\n", af*100, honestExample)
			fmt.Printf("| Distance Factor | Unreached |\n")
			fmt.Printf("|-----------------|------------|\n")

			for _, factor := range distanceFactors {
				for _, hc := range hypercubeCounts {
					for _, r := range results {
						if r.AttackerDist == dist && r.NumHypercubes == hc && r.AttackerFraction == af && r.DistanceFactor == factor {
							factorStr := fmt.Sprintf("d/%.1f", factor)
							if factor == 1.0 {
								factorStr = "d (full flood)"
							}
							fmt.Printf("| %-15s | %.2f\n", factorStr, r.AvgUnreached)
							break
						}
					}
				}
			}
		}

		for _, af := range attackerFractions {
			fmt.Printf("\n=== %.0f%% Attackers - P(single honest participant blocked) ===\n", af*100)
			fmt.Printf("| Distance Factor | Probability | Expected Blocked |\n")
			fmt.Printf("|-----------------|-------------|------------------|\n")

			for _, factor := range distanceFactors {
				for _, hc := range hypercubeCounts {
					for _, r := range results {
						if r.AttackerDist == dist && r.NumHypercubes == hc && r.AttackerFraction == af && r.DistanceFactor == factor {
							prob := r.AvgUnreached / float64(r.HonestNodes)
							factorStr := fmt.Sprintf("d/%.1f", factor)
							if factor == 1.0 {
								factorStr = "d (full flood)"
							}
							fmt.Printf("| %-15s | %.8f    | %.2f\n", factorStr, prob, r.AvgUnreached)
							break
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

func slotRandomVal(appHash, participantAddress string, slotIdx int, totalWeight int64) int64 {
	seedData := fmt.Sprintf("%s%s%d", appHash, participantAddress, slotIdx)
	hash := sha256.Sum256([]byte(seedData))
	return int64(binary.BigEndian.Uint64(hash[:8]) % uint64(totalWeight))
}

func selectAttackersBySlots(participants []propagation.WeightedParticipant, numAttackers int, simSeed string) map[string]bool {
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

func selectAttackersByWald(participants []propagation.WeightedParticipant, numAttackers int, simSeed int) map[string]bool {
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
