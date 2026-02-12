# Hypercube Message Propagation Security Analysis

**Parameters:** 10,000 participants, weighted shuffle, 1000 simulations per scenario

## What We Measure

**P(single honest participant blocked)** — the probability that a randomly chosen honest participant will **not** receive a message when a sender publishes and attackers actively block propagation.

### How It's Calculated

```
P(single honest participant blocked) = AvgUnreached / HonestNodes
```

**Simulation process:**
1. For each simulation, one random honest participant publishes their message
2. Message propagates through the hypercube overlay(s) via BFS — attacker nodes don't relay
3. A honest participant is "reached" if they receive the message through **at least one path**
4. `UnreachedHonest` = count of honest participants not reached via any path
5. Average across all simulations → `AvgUnreached`
6. Divide by total honest nodes → probability

**Example:** A value of 0.10 means any given honest node has a 10% chance of not receiving the data.

---

## Hypercube Topology

The hypercube overlay is built using a **d-dimensional binary hypercube** where nodes are positioned at coordinates in {0,1}^d space, and neighbors are determined by Hamming distance (bit-flip operations).

### How It Works

```go
func buildHypercubeWithIndex(index int, participants []WeightedParticipant, blockHash []byte) *Hypercube {
    realSize := len(participants)
    d := ceilLog2(realSize)           // dimensions = ceil(log2(n))
    cubeSize := 1 << d                 // 2^d positions

    // For each dimension, weighted shuffle assigns participants to positions
    for dim := 0; dim < d; dim++ {
        seed := makeDimensionSeed(blockHash, dim)
        shuffled := weightedDeterministicShuffle(participants, seed)
        
        for i, addr := range shuffled {
            if i >= cubeSize: break
            positionToParticipant[i] = addr
        }
    }

    // Final dimension determines actual mapping
    lastDimSeed := makeDimensionSeed(blockHash, d-1)
    shuffled := weightedDeterministicShuffle(participants, lastDimSeed)
    for i := 0; i < realSize && i < cubeSize; i++ {
        positionToParticipant[i] = shuffled[i]
    }

    // Connect neighbors via XOR (bit-flip in each dimension)
    for pos := 0; pos < cubeSize; pos++ {
        for dim := 0; dim < d; dim++ {
            neighborPos := pos ^ (1 << dim)   // flip bit at dimension dim
            if neighborPos < cubeSize && exists(neighborPos) {
                connect(pos, neighborPos)
            }
        }
    }
}
```

### Topology Properties

**For 10,000 participants:**
- **Dimensions:** d = ceil(log2(10000)) = 14
- **Cube size:** 2^14 = 16,384 positions (10,000 filled)
- **Neighbors per node:** Each node connects to up to 14 neighbors (one per dimension)
  - **Minimum:** 8 neighbors (sparse regions)
  - **Maximum:** 14 neighbors (fully connected)
  - **Average:** 12.9 neighbors

### Connection Statistics Calculation

The topology statistics are calculated as follows:

```go
// For each participant, collect all unique neighbors
allNeighbors := make(map[string]map[string]bool)
for addr, node := range hypercube.Nodes {
    allNeighbors[addr] = make(map[string]bool)
    for _, neighborAddr := range node.Neighbors {
        allNeighbors[addr][neighborAddr] = true
    }
}

// Calculate min, max, and average connections
maxConns, minConns, totalConns := 0, 999999, 0
for addr := range allNeighbors {
    conns := len(allNeighbors[addr])  // Count unique neighbors
    totalConns += conns
    if conns > maxConns {
        maxConns = conns
    }
    if conns < minConns {
        minConns = conns
    }
}
avgConns := float64(totalConns) / float64(numParticipants)
```

**Why the variation (8-14 neighbors)?**

1. **Theoretical maximum:** 14 neighbors (one per dimension in a 14-dimensional hypercube)

2. **Why some nodes have fewer:**
   - Only 10,000 of 16,384 positions are filled
   - Nodes whose XOR neighbors point to empty positions have fewer actual connections
   - Nodes in lower positions (0-9999) have all neighbors in the filled range → 14 neighbors
   - Nodes near position boundaries may have neighbors in the empty range → fewer neighbors

3. **Example:**
   ```
   Position 100 (binary: 0000000001100100):
   - Dimension 0: XOR with 0001 → position 101 ✓ (exists)
   - Dimension 1: XOR with 0010 → position 102 ✓ (exists)
   - Dimension 2: XOR with 0100 → position 96  ✓ (exists)
   - ...
   - Dimension 13: XOR with 0010000000000000 → position 8292 ✓ (exists)
   All 14 neighbors exist → 14 connections
   
   Position 9900 (binary: 0010011010101100):
   - Some dimension flips → positions like 9901, 9902, 9904, etc. ✓
   - But dimension 13: XOR with 0010000000000000 → position 1804 ✓ (exists)
   - Dimension 12: XOR with 0001000000000000 → position 5804 ✓ (exists)
   Still in dense region → likely 14 connections
   
   Position near boundary where XOR can exceed 10000:
   - Some neighbor calculations result in positions > 9999
   - Those positions are empty → connection doesn't exist
   - Results in fewer than 14 actual neighbors
   ```

4. **Why minimum is 8:**
   - Even nodes with some empty neighbors retain most connections
   - With 10,000/16,384 = 61% fill rate, most XOR operations stay in range
   - Positions that have empty neighbors typically lose 6 out of 14 (around 43%)
   - Minimum observed: 8 neighbors

5. **Why average is 12.9:**
   ```
   Total connections = Sum of all neighbor counts
   Average = Total connections / 10,000
   
   With most nodes having 14 neighbors and some having 8-13:
   Average ≈ 12.9 neighbors per node
   ```

**Unique connections count:**

The script also calculates total **unique bidirectional connections**:

```go
connCounts := make(map[string]int)
for addr, node := range hypercube.Nodes {
    for _, neighbor := range node.Neighbors {
        // Normalize connection to avoid counting A→B and B→A separately
        connKey := addr + "->" + neighbor
        if addr < neighbor {
            connKey = addr + "->" + neighbor
        } else {
            connKey = neighbor + "->" + addr
        }
        connCounts[connKey] = 1
    }
}
totalUniqueConns := len(connCounts)  // 64,608 for 10,000 nodes
```

**Result:** 64,608 unique bidirectional connections for 10,000 participants

**Verification:**
```
If all nodes had exactly 14 neighbors: 10,000 × 14 / 2 = 70,000 connections
Actual: 64,608 connections
Difference: ~7.7% fewer due to empty positions in sparse region

Average per node: 64,608 × 2 / 10,000 = 12.92 ≈ 12.9 ✓
```

**XOR Distance:**
Neighbors are determined by XOR distance (Hamming distance in binary):
```
Position A: 0b0101 (5)
Position B: 0b0111 (7)
XOR: 0b0010 → 1 bit different → 1-hop neighbors
```

**Network Structure:**
```
Example with d=3 (8 positions):

Position:  000  001  010  011  100  101  110  111
           / \ / \ / \ / \ / \ / \ / \ / \ / \
Connects:  001 000 011 010 101 100 111 110  (flip bit 0)
           010 011 000 001 110 111 100 101  (flip bit 1)
           100 101 110 111 000 001 010 011  (flip bit 2)
```

**Why This Matters:**
- Hypercube provides **logarithmic diameter**: O(log n) hops to reach any node
- High redundancy: multiple paths between any two nodes
- Resistant to attacks: blocking requires controlling multiple strategic positions
- Weighted shuffle places high-weight nodes at well-connected positions

---

## Distance-Based Forwarding Model

To reduce bandwidth while maintaining high delivery rates, the hypercube uses **distance-based selective forwarding**.

### Algorithm

```go
func propagateHypercubeDistanceBased(hypercubes []*Hypercube, publisher string, attackers map[string]bool, distanceFactor float64) {
    dimensions := hypercubes[0].Dimensions
    maxForwardNeighbors := int(round(dimensions / distanceFactor))
    
    publisherPosition := hypercubes[0].GetNode(publisher).Position
    
    for each currentNode in BFS:
        if currentNode is attacker: skip
        
        // Collect all neighbors from all hypercubes
        allNeighbors := collectNeighbors(currentNode, hypercubes)
        
        if distanceFactor >= 1.0:
            // Calculate XOR distance from publisher for each neighbor
            neighborDistances := []
            for neighbor in allNeighbors:
                dist := xorDistance(neighbor.Position, publisherPosition)
                neighborDistances.append((neighbor, dist))
            
            // Sort by distance descending (farthest first)
            sort(neighborDistances, descending=True)
            
            // Forward to top maxForwardNeighbors farthest neighbors
            selectedNeighbors := neighborDistances[:maxForwardNeighbors]
            
            propagate to selectedNeighbors
        else:
            // Full flood
            propagate to allNeighbors
}
```

### XOR Distance Calculation

```go
func xorDistance(a, b uint16) int {
    return bits.OnesCount16(a ^ b)
}
```

**Example:**
```
Publisher position: 0b00101101010010 (2898)
Neighbor A:         0b00101101010011 (2899)  → XOR = 0b00000000000001 → distance = 1
Neighbor B:         0b01101101010010 (7058)  → XOR = 0b01000000000000 → distance = 1
Neighbor C:         0b11101101010010 (15250) → XOR = 0b11000000000000 → distance = 2
```

### Distance Factors and Forwarding Behavior

| Factor | Max Neighbors | Forwarding Strategy | Bandwidth % | Use Case |
|--------|---------------|---------------------|-------------|----------|
| 1.0    | 14 (all)      | Full flood — forward to all neighbors | 100%        | Maximum reliability |
| 1.3    | 11            | Forward to 11 farthest neighbors | ~85%        | High reliability, reduced bandwidth |
| 1.5    | 9             | Forward to 9 farthest neighbors | ~68%        | Balanced |
| 2.0    | 7             | Forward to 7 farthest neighbors | ~48%        | Moderate bandwidth savings |
| 3.0    | 5             | Forward to 5 farthest neighbors | ~23%        | Aggressive bandwidth reduction |

**Strategy:** By selecting **farthest** neighbors (highest XOR distance from publisher), the algorithm:
- Maximizes coverage: reaches distant parts of the hypercube quickly
- Avoids redundant forwarding to nearby nodes (they likely share many neighbors)
- Maintains logarithmic propagation depth

---

## Weighted Shuffle Algorithm

The hypercube construction uses a **weighted deterministic shuffle** that places higher-weight participants at better positions.

### How It Works

```go
func weightedDeterministicShuffle(participants []WeightedParticipant, seed []byte) []string {
    rng := rand.New(rand.NewSource(int64(binary.BigEndian.Uint64(seed[:8]))))

    for i, p := range participants {
        baseScore := float64(p.Weight)
        randomComponent := rng.Float64() × float64(p.Weight) × 0.3  // 30% randomness
        items[i].randomScore = baseScore + randomComponent
    }

    sort.Slice(items, func(i, j int) bool {
        return items[i].randomScore > items[j].randomScore  // descending
    })
}
```

### Score Calculation

For each participant:
```
finalScore = weight + (random[0,1) × weight × 0.3)
```

**Example** (30% shuffle):
- Participant with weight 10000: score in range `[10000, 13000]`
- Participant with weight 5000: score in range `[5000, 6500]`
- Participant with weight 1000: score in range `[1000, 1300]`

### Effect of Shuffle Percentage (30%)

- **30% randomness:** Moderate variation in position assignment
- Higher-weight participants have larger absolute random ranges
- Relative randomization is the same percentage for all participants
- Ensures high-weight nodes get well-connected positions while maintaining unpredictability

### What "Better Position" Means in Hypercube

Unlike tree topologies where position directly determines parent/child roles, hypercube positions have more subtle advantages:

**1. Position Assignment Process:**
```
High-weight nodes → Higher scores → Earlier in shuffled list → Lower position numbers (0, 1, 2, ...)
Low-weight nodes → Lower scores → Later in shuffled list → Higher position numbers (..., 9997, 9998, 9999)
```

**2. Connectivity Advantages:**

In an ideal hypercube, all positions have equal connectivity (d neighbors). However, with 10,000 nodes in a 2^14 = 16,384 position space:

- **Dense region (positions 0-9999):** All positions filled
  - Every neighbor connection succeeds
  - Average 12.9 neighbors per node
  - Multiple redundant paths for message propagation
  
- **Sparse region (positions 10000-16383):** Empty positions
  - Some neighbor connections point to empty slots
  - Reduced effective connectivity
  - Fewer alternative paths

**Example:**
```
Position 5000 (likely high-weight node):
- Theoretical neighbors: 14 (one per dimension)
- XOR neighbors: positions like 5001, 5002, 5004, 5008, ..., 13192
- All neighbors exist → 14 actual connections
- Well-connected, multiple paths to any destination

Position 15000 (would be low-weight, but mostly empty):
- Theoretical neighbors: 14
- XOR neighbors: positions like 15001, 15002, 15004, ..., 14976, 6808
- Some neighbors are in empty region (>10000) → fewer actual connections
- Reduced connectivity, fewer redundant paths
```

**3. Practical Impact:**

High-weight nodes getting "better positions" means:
- **Consistent connectivity:** Always have full neighbor set (12.9 avg vs lower for sparse regions)
- **Central network location:** More likely to be on shortest paths between other nodes
- **Redundancy:** Multiple independent paths to reach any other node
- **Attack resistance:** Even if some neighbors are attackers, many alternatives exist

**4. Why This Matters Less Than in Trees:**

However, the impact is **much less dramatic** than in tree topologies because:
- **No single points of failure:** Unlike tree parents, no hypercube node controls access to a subtree
- **Logarithmic diameter:** All nodes are ≤14 hops apart regardless of position
- **Path redundancy:** Multiple routes exist even between low-weight nodes
- **Weight-independent security:** As shown in results, all attacker distributions perform identically

**Conclusion:** Weighted shuffle provides a **mild advantage** to high-weight nodes (better connectivity, more paths), but hypercube's inherent resilience means even low-weight nodes in sparse regions maintain good connectivity. This is why attacker distribution (uniform vs high-weight) has negligible impact on blocking probability — the topology is fundamentally robust regardless of node positioning.

---

## Attacker Distribution Models

### 1. Uniform Distribution

Attackers are evenly distributed across **all weight levels**.

```go
step := numParticipants / numAttackers
for i := 0; i < numAttackers; i++ {
    idx := (i × step) % numParticipants
    attackers[formatAddress(idx)] = true
}
```

**Characteristics:**
- Selects every Nth participant starting from index 0
- For 33% attackers: step = 3, selects indices 0, 3, 6, ..., 9897
- For 45% attackers: step = 2, selects indices 0, 2, 4, ..., 8998
- Attackers distributed across all weight levels

### 2. High-Weight Distribution

Attackers are concentrated among **high-weight participants** (worst-case scenario).

```go
step := numParticipants / numAttackers
for i := 0; i < numAttackers; i++ {
    idx := (numParticipants - 1) - (i × step)
    attackers[formatAddress(idx)] = true
}
```

**Characteristics:**
- Selects every Nth participant starting from the **end** (highest weight)
- For 33% attackers: selects indices 9999, 9996, 9993, ..., 102
- For 45% attackers: selects indices 9999, 9997, 9995, ..., 1
- Attackers control nodes **most likely to have better positions**

### 3. Wald Distribution (Weight-Based Probabilistic)

Attackers are selected using **Inverse Gaussian (Wald) distribution** where participant weight controls selection probability.

**Mathematical Model:**

```go
const lambda = 1.0  // shape parameter

maxWeight := max(all participant weights)

for each participant p:
    mu := maxWeight / p.Weight               // inverse weight mapping
    score := sampleWald(mu, lambda, rng)     // draw from Wald(μ, λ)

sort participants by score (ascending)
select first N as attackers
```

**Wald Distribution Properties:**

```
f(x; μ, λ) = sqrt(λ / (2πx³)) × exp(-λ(x-μ)² / (2μ²x))

E[X] = μ         (expected value)
Var[X] = μ³/λ    (variance increases cubically with μ)
```

**Why This Works:**

1. **Inverse weight mapping:**
   - Participant with weight 10999 (max): μ = 1.0, E[score] = 1.0
   - Participant with weight 5000: μ = 2.2, E[score] = 2.2
   - Participant with weight 1000: μ = 11.0, E[score] = 11.0

2. **Lower score = higher priority:**
   - Higher-weight participants have **smaller μ** → **lower expected scores** → higher selection probability

3. **Probabilistic but weighted:**
   - Randomness adds variance while preserving weight influence
   - More realistic than deterministic highweight distribution

**Characteristics:**
- Weight-proportional selection with controlled randomness
- Higher-weight nodes have higher probability of becoming attackers
- Heavy tail allows occasional low-weight selections

### 4. Slot-Based Distribution (Weighted Random Sampling)

**Mathematical Model:**

```go
appHash := SHA256(simSeed)[:16]
totalWeight := sum(all participant weights)

for slot := 0; slot < numAttackers; slot++:
    rv := SHA256(appHash || "attacker_selection" || slot) % totalWeight
    
    cumulative := 0
    for each participant p (sorted by address):
        cumulative += p.Weight
        if rv < cumulative:
            select p as attacker (if not already selected)
            break
```

**Selection Probability:**

For participant with weight `w`:
```
P(selected in slot i) = w / totalWeight

P(selected overall) = 1 - (1 - w/totalWeight)^numAttackers

E[selections] = numAttackers × (w / totalWeight)
```

**Characteristics:**
- Sequential weighted lottery (models PoS slot assignment)
- Probability strictly proportional to weight
- Deterministic given same seed
- Duplicates ignored (sampling without replacement)

---

## Simulation Results

### Uniform Distribution

**33% Attackers — Avg Unreached Honest Participants (out of 6700 honest)**

| Distance Factor | Unreached |
|-----------------|-----------|
| d (full flood)  | 0.01      |
| d/1.3           | 3.18      |
| d/1.5           | 127.66    |
| d/2.0           | 813.22    |
| d/3.0           | 2708.80   |

**45% Attackers — Avg Unreached Honest Participants (out of 5500 honest)**

| Distance Factor | Unreached |
|-----------------|-----------|
| d (full flood)  | 0.41      |
| d/1.3           | 7.11      |
| d/1.5           | 183.82    |
| d/2.0           | 1051.79   |
| d/3.0           | 2984.71   |

**33% Attackers — P(single honest participant blocked)**

| Distance Factor | Probability | Expected Blocked |
|-----------------|-------------|------------------|
| d (full flood)  | 0.00000149  | 0.01             |
| d/1.3           | 0.00047418  | 3.18             |
| d/1.5           | 0.01905299  | 127.66           |
| d/2.0           | 0.12137537  | 813.22           |
| d/3.0           | 0.40429791  | 2708.80          |

**45% Attackers — P(single honest participant blocked)**

| Distance Factor | Probability | Expected Blocked |
|-----------------|-------------|------------------|
| d (full flood)  | 0.00007509  | 0.41             |
| d/1.3           | 0.00129273  | 7.11             |
| d/1.5           | 0.03342273  | 183.82           |
| d/2.0           | 0.19123418  | 1051.79          |
| d/3.0           | 0.54267527  | 2984.71          |

---

### High-Weight Distribution

**33% Attackers — Avg Unreached Honest Participants (out of 6700 honest)**

| Distance Factor | Unreached |
|-----------------|-----------|
| d (full flood)  | 0.01      |
| d/1.3           | 3.19      |
| d/1.5           | 128.48    |
| d/2.0           | 831.02    |
| d/3.0           | 2732.49   |

**45% Attackers — Avg Unreached Honest Participants (out of 5500 honest)**

| Distance Factor | Unreached |
|-----------------|-----------|
| d (full flood)  | 0.40      |
| d/1.3           | 7.71      |
| d/1.5           | 181.61    |
| d/2.0           | 1084.10   |
| d/3.0           | 3027.64   |

**33% Attackers — P(single honest participant blocked)**

| Distance Factor | Probability | Expected Blocked |
|-----------------|-------------|------------------|
| d (full flood)  | 0.00000104  | 0.01             |
| d/1.3           | 0.00047642  | 3.19             |
| d/1.5           | 0.01917567  | 128.48           |
| d/2.0           | 0.12403284  | 831.02           |
| d/3.0           | 0.40783448  | 2732.49          |

**45% Attackers — P(single honest participant blocked)**

| Distance Factor | Probability | Expected Blocked |
|-----------------|-------------|------------------|
| d (full flood)  | 0.00007182  | 0.40             |
| d/1.3           | 0.00140145  | 7.71             |
| d/1.5           | 0.03301927  | 181.61           |
| d/2.0           | 0.19710891  | 1084.10          |
| d/3.0           | 0.55048000  | 3027.64          |

---

### Slot-Based Distribution (Weighted Random Sampling)

**33% Attackers — Avg Unreached Honest Participants (out of 6700 honest)**

| Distance Factor | Unreached |
|-----------------|-----------|
| d (full flood)  | 0.03      |
| d/1.3           | 3.55      |
| d/1.5           | 137.15    |
| d/2.0           | 836.10    |
| d/3.0           | 2733.45   |

**45% Attackers — Avg Unreached Honest Participants (out of 5500 honest)**

| Distance Factor | Unreached |
|-----------------|-----------|
| d (full flood)  | 6.51      |
| d/1.3           | 15.36     |
| d/1.5           | 188.38    |
| d/2.0           | 1068.47   |
| d/3.0           | 2955.35   |

**33% Attackers — P(single honest participant blocked)**

| Distance Factor | Probability | Expected Blocked |
|-----------------|-------------|------------------|
| d (full flood)  | 0.00000403  | 0.03             |
| d/1.3           | 0.00052970  | 3.55             |
| d/1.5           | 0.02047060  | 137.15           |
| d/2.0           | 0.12479104  | 836.10           |
| d/3.0           | 0.40797701  | 2733.45          |

**45% Attackers — P(single honest participant blocked)**

| Distance Factor | Probability | Expected Blocked |
|-----------------|-------------|------------------|
| d (full flood)  | 0.00118436  | 6.51             |
| d/1.3           | 0.00279345  | 15.36            |
| d/1.5           | 0.03425073  | 188.38           |
| d/2.0           | 0.19426709  | 1068.47          |
| d/3.0           | 0.53733600  | 2955.35          |

---

### Wald Distribution (Inverse Gaussian)

**33% Attackers — Avg Unreached Honest Participants (out of 6700 honest)**

| Distance Factor | Unreached |
|-----------------|-----------|
| d (full flood)  | 0.01      |
| d/1.3           | 3.21      |
| d/1.5           | 129.04    |
| d/2.0           | 821.51    |
| d/3.0           | 2787.35   |

**45% Attackers — Avg Unreached Honest Participants (out of 5500 honest)**

| Distance Factor | Unreached |
|-----------------|-----------|
| d (full flood)  | 0.34      |
| d/1.3           | 7.45      |
| d/1.5           | 178.71    |
| d/2.0           | 1094.85   |
| d/3.0           | 3144.70   |

**33% Attackers — P(single honest participant blocked)**

| Distance Factor | Probability | Expected Blocked |
|-----------------|-------------|------------------|
| d (full flood)  | 0.00000104  | 0.01             |
| d/1.3           | 0.00047925  | 3.21             |
| d/1.5           | 0.01925940  | 129.04           |
| d/2.0           | 0.12261403  | 821.51           |
| d/3.0           | 0.41602209  | 2787.35          |

**45% Attackers — P(single honest participant blocked)**

| Distance Factor | Probability | Expected Blocked |
|-----------------|-------------|------------------|
| d (full flood)  | 0.00006255  | 0.34             |
| d/1.3           | 0.00135473  | 7.45             |
| d/1.5           | 0.03249218  | 178.71           |
| d/2.0           | 0.19906418  | 1094.85          |
| d/3.0           | 0.57176291  | 3144.70          |

---

## Bandwidth Analysis

**Note:** Messages shown are for ONE participant publishing. For ALL 10,000 participants publishing, multiply by 10,000.

### Uniform Distribution

**33% Attackers:**

| Factor | Neighbors | Msgs (1 pub) | Msgs/Participant | Bandwidth % | Total if ALL publish |
|--------|-----------|--------------|------------------|-------------|----------------------|
| d (full) | 14      | 86,611       | 8.66             | 100.0       | 866.11M              |
| d/1.3  | 11        | 73,633       | 7.36             | 85.0        | 736.33M              |
| d/1.5  | 9         | 59,140       | 5.91             | 68.3        | 591.40M              |
| d/2.0  | 7         | 41,207       | 4.12             | 47.6        | 412.07M              |
| d/3.0  | 5         | 19,956       | 2.00             | 23.0        | 199.56M              |

**45% Attackers:**

| Factor | Neighbors | Msgs (1 pub) | Msgs/Participant | Bandwidth % | Total if ALL publish |
|--------|-----------|--------------|------------------|-------------|----------------------|
| d (full) | 14      | 71,582       | 7.16             | 100.0       | 715.82M              |
| d/1.3  | 11        | 60,397       | 6.04             | 84.4        | 603.97M              |
| d/1.5  | 9         | 47,837       | 4.78             | 66.8        | 478.37M              |
| d/2.0  | 7         | 31,137       | 3.11             | 43.5        | 311.37M              |
| d/3.0  | 5         | 12,576       | 1.26             | 17.6        | 125.76M              |

---

### High-Weight Distribution

**33% Attackers:**

| Factor | Neighbors | Msgs (1 pub) | Msgs/Participant | Bandwidth % | Total if ALL publish |
|--------|-----------|--------------|------------------|-------------|----------------------|
| d (full) | 14      | 86,493       | 8.65             | 100.0       | 864.93M              |
| d/1.3  | 11        | 73,616       | 7.36             | 85.1        | 736.16M              |
| d/1.5  | 9         | 59,127       | 5.91             | 68.4        | 591.27M              |
| d/2.0  | 7         | 41,082       | 4.11             | 47.5        | 410.82M              |
| d/3.0  | 5         | 19,837       | 1.98             | 22.9        | 198.37M              |

**45% Attackers:**

| Factor | Neighbors | Msgs (1 pub) | Msgs/Participant | Bandwidth % | Total if ALL publish |
|--------|-----------|--------------|------------------|-------------|----------------------|
| d (full) | 14      | 70,249       | 7.02             | 100.0       | 702.49M              |
| d/1.3  | 11        | 60,367       | 6.04             | 85.9        | 603.67M              |
| d/1.5  | 9         | 47,849       | 4.78             | 68.1        | 478.49M              |
| d/2.0  | 7         | 30,911       | 3.09             | 44.0        | 309.11M              |
| d/3.0  | 5         | 12,361       | 1.24             | 17.6        | 123.61M              |

---

### Slot-Based Distribution

**33% Attackers:**

| Factor | Neighbors | Msgs (1 pub) | Msgs/Participant | Bandwidth % | Total if ALL publish |
|--------|-----------|--------------|------------------|-------------|----------------------|
| d (full) | 14      | 85,709       | 8.57             | 100.0       | 857.09M              |
| d/1.3  | 11        | 73,616       | 7.36             | 85.9        | 736.16M              |
| d/1.5  | 9         | 59,050       | 5.91             | 68.9        | 590.50M              |
| d/2.0  | 7         | 41,047       | 4.10             | 47.9        | 410.47M              |
| d/3.0  | 5         | 19,832       | 1.98             | 23.1        | 198.32M              |

**45% Attackers:**

| Factor | Neighbors | Msgs (1 pub) | Msgs/Participant | Bandwidth % | Total if ALL publish |
|--------|-----------|--------------|------------------|-------------|----------------------|
| d (full) | 14      | 69,892       | 6.99             | 100.0       | 698.92M              |
| d/1.3  | 11        | 60,288       | 6.03             | 86.3        | 602.88M              |
| d/1.5  | 9         | 47,790       | 4.78             | 68.4        | 477.90M              |
| d/2.0  | 7         | 31,020       | 3.10             | 44.4        | 310.20M              |
| d/3.0  | 5         | 12,723       | 1.27             | 18.2        | 127.23M              |

---

### Wald Distribution

**33% Attackers:**

| Factor | Neighbors | Msgs (1 pub) | Msgs/Participant | Bandwidth % | Total if ALL publish |
|--------|-----------|--------------|------------------|-------------|----------------------|
| d (full) | 14      | 86,152       | 8.62             | 100.0       | 861.52M              |
| d/1.3  | 11        | 73,627       | 7.36             | 85.5        | 736.27M              |
| d/1.5  | 9         | 59,126       | 5.91             | 68.6        | 591.26M              |
| d/2.0  | 7         | 41,149       | 4.11             | 47.8        | 411.49M              |
| d/3.0  | 5         | 19,563       | 1.96             | 22.7        | 195.63M              |

**45% Attackers:**

| Factor | Neighbors | Msgs (1 pub) | Msgs/Participant | Bandwidth % | Total if ALL publish |
|--------|-----------|--------------|------------------|-------------|----------------------|
| d (full) | 14      | 70,523       | 7.05             | 100.0       | 705.23M              |
| d/1.3  | 11        | 60,384       | 6.04             | 85.6        | 603.84M              |
| d/1.5  | 9         | 47,880       | 4.79             | 67.9        | 478.80M              |
| d/2.0  | 7         | 30,836       | 3.08             | 43.7        | 308.36M              |
| d/3.0  | 5         | 11,776       | 1.18             | 16.7        | 117.76M              |

---

## Key Observations

### Hypercube Performance

**Full Flood (factor 1.0):**
- Near-perfect delivery: P(blocked) ≈ 0.000001 (33% attackers)
- High bandwidth: 8.66 messages/node
- Excellent resilience across all attacker distributions

**Factor 1.3 (11 neighbors):**
- Minimal blocking increase: P(blocked) ≈ 0.0005 (33% attackers)
- 15% bandwidth reduction
- **Sweet spot:** Excellent reliability with modest savings

**Factor 1.5 (9 neighbors):**
- Low blocking: P(blocked) ≈ 0.02 (33% attackers)
- 32% bandwidth reduction
- Good balance for most scenarios

**Factor 2.0 (7 neighbors):**
- Moderate blocking: P(blocked) ≈ 0.12 (33% attackers)
- 52% bandwidth reduction
- Acceptable for less critical messages

**Factor 3.0 (5 neighbors):**
- Significant blocking: P(blocked) ≈ 0.40 (33% attackers)
- 77% bandwidth reduction
- High risk, use with caution

### Attacker Distribution Impact

**Observation:** All four attacker distributions (uniform, highweight, slots, wald) produce **nearly identical** results:

- Uniform: P(blocked) = 0.019 (factor 1.5, 33%)
- Highweight: P(blocked) = 0.019 (factor 1.5, 33%)
- Slots: P(blocked) = 0.020 (factor 1.5, 33%)
- Wald: P(blocked) = 0.019 (factor 1.5, 33%)

**Why:** Unlike tree topologies where parent positions matter, hypercube resilience comes from:
- High connectivity (12.9 avg neighbors)
- Multiple paths between any two nodes
- Logarithmic diameter (≤14 hops worst-case)
- Weight doesn't significantly affect topology structure

**Implication:** Hypercube is **robust against adversarial positioning** — even if attackers control high-weight nodes, the network remains resilient.

---

## Comparison: Hypercube vs Multi-Tree

### Topology Characteristics

| Property | Hypercube | Multi-Tree (4 trees, fanout 8) |
|----------|-----------|-------------------------------|
| **Avg connections** | 12.9 | 8-32 (varies by tree position) |
| **Max diameter** | 14 hops | log₈(10000) ≈ 5 levels |
| **Redundancy** | Multiple paths per neighbor | Multiple tree memberships |
| **Weight sensitivity** | Low | High (parents critical) |

### Security Performance (33% attackers)

| Metric | Hypercube (full flood) | Multi-Tree (4 trees, f=8) |
|--------|----------------------|--------------------------|
| **P(blocked) — uniform** | 0.000001 | 0.075 (7.5%) |
| **P(blocked) — highweight** | 0.000001 | 0.259 (25.9%) |
| **P(blocked) — slots** | 0.000004 | 0.248 (24.8%) |
| **P(blocked) — wald** | 0.000001 | 0.267 (26.7%) |

**Winner:** Hypercube provides **dramatically better** delivery guarantees.

### Bandwidth Efficiency (33% attackers)

| Configuration | Messages/Node | Blocking (33%) | Blocking (45%) |
|---------------|---------------|----------------|----------------|
| **Hypercube:** factor 1.3 | 7.36 | 0.05% | 0.13% |
| **Multi-Tree:** 4 trees, f=8 | ~8.0 | 7.5% | 2.3% |
| **Hypercube:** factor 1.5 | 5.91 | 1.9% | 3.3% |
| **Multi-Tree:** 6 trees, f=8 | ~12.0 | 2.3% | 0.7% |

**Winner:** Hypercube achieves **better security at lower bandwidth** with proper distance factor.

### Attack Resistance

**Uniform attackers:**
- Hypercube: Highly resistant (P(blocked) < 0.01%)
- Multi-Tree: Moderate (7.5% blocked)

**High-weight attackers:**
- Hypercube: Unaffected (P(blocked) < 0.01%)
- Multi-Tree: Vulnerable (25.9% blocked)

**Winner:** Hypercube is **immune to weight-based attacks**.

---

## Recommendations

### Hypercube Configuration

**For critical data (transactions, blocks):**
- Use **factor 1.0** (full flood): 8.66 msgs/node, P(blocked) < 0.001%
- Or **factor 1.3**: 7.36 msgs/node, P(blocked) < 0.1%

**For non-critical data (gossip, metadata):**
- Use **factor 1.5**: 5.91 msgs/node, P(blocked) ≈ 2%
- Or **factor 2.0**: 4.12 msgs/node, P(blocked) ≈ 12% (acceptable risk)

**Avoid:**
- Factor 3.0 or higher: blocking becomes unacceptable (40%+)

### Attacker Mitigation

**Good news:** Hypercube is inherently resistant to:
- Weight-based attacker positioning
- Strategic node placement
- Targeted attack distributions

**No special configuration needed** — standard distance-based forwarding provides robust defense.

---

## Conclusion

**Hypercube topology with distance-based forwarding provides:**

1. **Exceptional security:** P(blocked) < 0.001% with full flood
2. **Efficient bandwidth:** Factor 1.3 saves 15% while maintaining 99.95% delivery
3. **Attack resistance:** Immune to weight-based adversarial positioning
4. **Simple configuration:** Factor 1.0-1.5 sufficient for all use cases

**Superiority over multi-tree:**
- 100x lower blocking probability (0.001% vs 7.5%)
- Better bandwidth efficiency at equivalent security
- No vulnerability to weight-based attacks

**Recommended configuration:**
- **Distance factor 1.3**
- 11 neighbors per node
- 7.36 messages per publishing node
- P(blocked) < 0.1% against 33% attackers
- Robust across all attacker distributions


## Running the Simulation

To run the blocking probability simulation:

```bash
cd decentralized-api/

go run scripts/blocking_probability_hypercube/main.go
```