# Propagation Tree Security Analysis

**Parameters:** 10,000 participants, weighted shuffle, 1000 simulations per scenario

## Theoretical Probability (Random Attacker Distribution without correlation to weight)

If attackers are randomly distributed without correlation to weight:

```
P(parent is attacker) = α  (attacker fraction)
P(block one) = α^numTrees
```

### Theoretical P(block one participant)

| Trees | 33% Attackers | 45% Attackers |
|-------|---------------|---------------|
| 4     | 0.0119 (1.2%) | 0.0410 (4.1%) |
| 6     | 0.00129 (0.13%) | 0.00830 (0.83%) |
| 8     | 0.000141 (0.014%) | 0.00168 (0.17%) |
| 10    | 0.0000153 (0.0015%) | 0.000341 (0.034%) |

### Theoretical Expected Blocked (n=10,000 total participants)

| Trees | 33% Attackers | 45% Attackers |
|-------|---------------|---------------|
| 4     | 79.5          | 225.0         |
| 6     | 8.7           | 45.6          |
| 8     | 0.94          | 9.2           |
| 10    | 0.10          | 1.9           |

**Note:** These theoretical values assume P(parent is attacker) = α, which holds when attacker selection is independent of weight. In practice with weighted shuffle, P(parent is attacker) can be higher or lower depending on how attackers are distributed across weight levels.


## What We Measure

**P(single honest participant blocked)** — the probability that a randomly chosen honest participant will **not** receive a message when a sender broadcasts through all trees and attackers actively block relay.

### How It's Calculated

```
P(single honest participant blocked) = AvgUnreached / HonestNodes
```

**Simulation process:**
1. For each simulation, one sender broadcasts their message to all tree roots
2. Message propagates down each tree via BFS — attacker nodes don't relay to their children
3. A honest participant is "reached" if they receive the message in **at least one tree**
4. `UnreachedHonest` = count of honest participants not reached in any tree
5. Average across all simulations → `AvgUnreached`
6. Divide by total honest nodes → probability

**Example:** A value of 0.10 means any given honest node has a 10% chance of not receiving the data.

---

## Weighted Shuffle Algorithm

The tree structure is built using a **weighted deterministic shuffle** that places higher-weight participants closer to the root (parent positions).

### How It Works

```go
func weightedDeterministicShuffle(participants []WeightedParticipant, seed []byte, shufflePct float64) []string {
    rng := rand.New(rand.NewSource(int64(binary.BigEndian.Uint64(seed[:8]))))

    for i, p := range participants {
        baseScore := float64(p.Weight)
        randomComponent := rng.Float64() * float64(p.Weight) * shufflePct
        items[i].randomScore = baseScore + randomComponent
    }

    sort.Slice(items, func(i, j int) bool {
        return items[i].randomScore > items[j].randomScore
    })
}
```

### Score Calculation

For each participant:
```
finalScore = weight + (random[0,1) × weight × shufflePct)
```

**Example** (shufflePct = 0.20):
- Participant with weight 10000: score in range `[10000, 12000]`
- Participant with weight 5000: score in range `[5000, 6000]`
- Participant with weight 1000: score in range `[1000, 1200]`

### Effect of Shuffle Percentage

| Shuffle % | Behavior |
|-----------|----------|
| 0%        | Pure weight ordering — highest weight always first |
| 10%       | Minimal randomization — weight mostly determines position |
| 30%       | Moderate randomization — some position swaps between adjacent weights |
| 100%      | High randomization — weight ranges overlap significantly |

**Key property:** Higher-weight participants have larger absolute random ranges, but the *relative* randomization is the same percentage for all participants.

### Tree Construction from Shuffled List

After shuffling, a **complete k-ary tree** is built where `k = fanout`:

```go
func buildTree(shuffled []string, fanout int) *Tree {
    // Position 0 = root
    t.Root = t.Nodes[shuffled[0]]

    // For each node at position i > 0, parent is at position (i-1)/fanout
    for i := 1; i < n; i++ {
        parentIndex := (i - 1) / fanout
        node.Parent = t.Nodes[shuffled[parentIndex]]
    }
}
```

**Structure with fanout=4:**
```
Position:     0              <- Root (highest weight)
            / | \ \
           1  2  3  4        <- Level 1 (positions 1-4)
          /|\ ...
         5 6 7 8 ...         <- Level 2 (positions 5-20)
```

**Parent calculation:** Node at position `i` has parent at position `(i-1) / fanout`
- Positions 1-4 → parent at 0
- Positions 5-8 → parent at 1
- Positions 9-12 → parent at 2

**Why this matters:** The first `n/fanout` positions are parents. Since the shuffle places high-weight nodes first, **high-weight nodes become parents** and low-weight nodes become leaves.

---

## Attacker Distribution Models

### 1. Uniform Distribution

Attackers are evenly distributed across **all weight levels**.

```go
step := numParticipants / numAttackers
for i := 0; i < numAttackers; i++ {
    idx := (i * step) % numParticipants
    attackers[formatAddress(idx)] = true
}
```

**Characteristics:**
- Selects every Nth participant starting from index 0
- For 33% attackers: step = 3, selects indices 0, 3, 6, ..., 9897
- For 45% attackers: step = 2, selects indices 0, 2, 4, ..., 8998
- High-weight nodes (tree parents) have **fewer attackers**

### 2. High-Weight Distribution

Attackers are concentrated among **high-weight participants** (worst-case scenario).

```go
step := numParticipants / numAttackers
for i := 0; i < numAttackers; i++ {
    idx := (numParticipants - 1) - (i * step)
    attackers[formatAddress(idx)] = true
}
```

**Characteristics:**
- Selects every Nth participant starting from the **end** (highest weight)
- For 33% attackers: selects indices 9999, 9996, 9993, ..., 102
- For 45% attackers: selects indices 9999, 9997, 9995, ..., 1
- Attackers control nodes **most likely to become parents**

### 3. Wald Distribution (Weight-Based Probabilistic)

Attackers are selected using **Inverse Gaussian (Wald) distribution** where participant weight controls selection probability.

**Mathematical Model:**

```go
const lambda = 1.0  // shape parameter (global noise control)

maxWeight := max(all participant weights)

for each participant p:
    mu := maxWeight / p.Weight               // inverse weight mapping
    score := sampleWald(mu, lambda, rng)     // draw from Wald(μ, λ)

sort participants by score (ascending)
select first N as attackers
```

**Wald Distribution Properties:**

The Wald distribution models "first passage time" — the time it takes for a process to reach a threshold:

```
f(x; μ, λ) = sqrt(λ / (2πx³)) × exp(-λ(x-μ)² / (2μ²x))

E[X] = μ         (expected value)
Var[X] = μ³/λ    (variance increases cubically with μ)
```

**Why This Works:**

1. **Inverse weight mapping:**
   - Participant with weight 10999 (max): μ = 10999/10999 = 1.0, E[score] = 1.0
   - Participant with weight 5000: μ = 10999/5000 = 2.2, E[score] = 2.2
   - Participant with weight 1000: μ = 10999/1000 = 11.0, E[score] = 11.0
   
2. **Lower score = higher priority:**
   - Smaller draw → "recruited faster" → selected as attacker
   - Higher-weight participants have **smaller μ** → **lower expected scores** → higher selection probability

3. **Probabilistic but weighted:**
   - Not deterministic like uniform/highweight
   - Randomness adds variance while preserving weight influence
   - Heavy tail allows occasional low-weight selections

**Selection Probability:**

For a participant with weight `w`, the probability of being selected depends on:
```
P(selected) ∝ P(Wald(maxWeight/w, 1) < threshold)
```

Where the threshold is determined by the N-th order statistic of all Wald draws.

Higher weight → smaller μ → distribution shifted left → higher chance of small score → higher selection probability.

**Sampling Algorithm (Michael–Schucany–Haas):**

```go
func sampleWald(mu, lambda float64, rng *rand.Rand) float64 {
    nu := rng.NormFloat64()           // standard normal
    y := nu²
    x := mu + (mu²y)/(2λ) - (mu/(2λ))×sqrt(4muλy + mu²y²)
    
    u := rng.Float64()                // uniform [0,1)
    if u <= mu/(mu+x):
        return x
    return mu²/x
}
```

**Characteristics:**
- Weight-proportional selection with controlled randomness
- Higher-weight nodes have higher probability of becoming attackers
- Variance **decreases** with weight (μ³/λ, but μ is inverse to weight)
  - Highest weight (10999): μ=1.0, Var=1.0 (tight distribution)
  - Lowest weight (1000): μ=11.0, Var=1331 (wide distribution)
- Heavy tail allows occasional low-weight selections, but attackers concentrate toward high-weight participants
- More realistic than deterministic highweight distribution

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

**Random Value Generation:**

```go
func slotRandomVal(appHash, participantAddress string, slotIdx int, totalWeight int64) int64 {
    seedData := appHash + participantAddress + slotIdx
    hash := SHA256(seedData)
    return hash[:8] as uint64 % totalWeight
}
```

**Selection Probability:**

For participant with weight `w`:
```
P(selected in slot i) = w / totalWeight

P(selected overall) = 1 - (1 - w/totalWeight)^numAttackers

E[selections] = numAttackers × (w / totalWeight)
```

**Example (weights 1000-10999, total = 60,994,500):**

- Highest weight (10999): P(slot) = 10999/60,994,500 ≈ 0.01803%
- Lowest weight (1000): P(slot) = 1000/60,994,500 ≈ 0.01639%
- Weight ratio: 11:1

**Characteristics:**
- Sequential weighted lottery (models PoS slot assignment)
- Probability strictly proportional to weight
- Deterministic given same seed
- Duplicates ignored (sampling without replacement)

---

## Simulation Results

### Uniform Distribution

#### Shuffle 10%

**33% Attackers — Avg Unreached Honest Participants (out of 6699 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 1506.18        | 501.62         | 169.09         | 49.71          |
| 6              | 767.28         | 152.09         | 30.65          | 4.81           |
| 8              | 397.24         | 47.24          | 5.69           | 0.48           |
| 10             | 208.89         | 15.10          | 1.09           | 0.05           |

**45% Attackers — Avg Unreached Honest Participants (out of 5500 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 157.09         | 12.25          | 0.00           | 0.00           |
| 6              | 38.44          | 1.25           | 0.00           | 0.00           |
| 8              | 9.50           | 0.13           | 0.00           | 0.00           |
| 10             | 2.38           | 0.01           | 0.00           | 0.00           |

**33% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.224803       | 0.074868       | 0.025237       | 0.007420       |
| 6              | 0.114520       | 0.022700       | 0.004575       | 0.000719       |
| 8              | 0.059289       | 0.007051       | 0.000849       | 0.000071       |
| 10             | 0.031177       | 0.002253       | 0.000163       | 0.000007       |

**45% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.028561       | 0.002227       | 0.000000       | 0.000000       |
| 6              | 0.006988       | 0.000228       | 0.000000       | 0.000000       |
| 8              | 0.001728       | 0.000024       | 0.000000       | 0.000000       |
| 10             | 0.000433       | 0.000001       | 0.000000       | 0.000000       |

---

#### Shuffle 15%

**33% Attackers — Avg Unreached Honest Participants (out of 6699 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 1733.14        | 602.20         | 209.18         | 73.33          |
| 6              | 934.89         | 196.75         | 40.78          | 8.89           |
| 8              | 513.96         | 66.83          | 7.98           | 1.10           |
| 10             | 286.98         | 23.08          | 1.60           | 0.16           |

**45% Attackers — Avg Unreached Honest Participants (out of 5500 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 176.99         | 9.62           | 0.13           | 0.00           |
| 6              | 48.52          | 0.84           | 0.00           | 0.00           |
| 8              | 13.58          | 0.07           | 0.00           | 0.00           |
| 10             | 4.01           | 0.01           | 0.00           | 0.00           |

**33% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.258677       | 0.089880       | 0.031221       | 0.010944       |
| 6              | 0.139536       | 0.029366       | 0.006086       | 0.001327       |
| 8              | 0.076710       | 0.009974       | 0.001192       | 0.000164       |
| 10             | 0.042833       | 0.003444       | 0.000239       | 0.000024       |

**45% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.032179       | 0.001749       | 0.000024       | 0.000000       |
| 6              | 0.008822       | 0.000152       | 0.000000       | 0.000000       |
| 8              | 0.002470       | 0.000013       | 0.000000       | 0.000000       |
| 10             | 0.000730       | 0.000001       | 0.000000       | 0.000000       |

---

#### Shuffle 20%

**33% Attackers — Avg Unreached Honest Participants (out of 6699 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 1895.37        | 672.48         | 244.61         | 95.85          |
| 6              | 1066.40        | 232.08         | 51.38          | 13.29          |
| 8              | 611.44         | 81.54          | 11.20          | 1.82           |
| 10             | 355.15         | 29.61          | 2.42           | 0.27           |

**45% Attackers — Avg Unreached Honest Participants (out of 5500 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 194.69         | 9.86           | 0.62           | 0.00           |
| 6              | 57.14          | 0.67           | 0.02           | 0.00           |
| 8              | 17.64          | 0.04           | 0.00           | 0.00           |
| 10             | 5.49           | 0.01           | 0.00           | 0.00           |

**33% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.282891       | 0.100371       | 0.036509       | 0.014306       |
| 6              | 0.159165       | 0.034638       | 0.007669       | 0.001984       |
| 8              | 0.091259       | 0.012170       | 0.001672       | 0.000271       |
| 10             | 0.053008       | 0.004420       | 0.000361       | 0.000040       |

**45% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.035399       | 0.001792       | 0.000112       | 0.000000       |
| 6              | 0.010390       | 0.000122       | 0.000003       | 0.000000       |
| 8              | 0.003208       | 0.000008       | 0.000000       | 0.000000       |
| 10             | 0.000998       | 0.000001       | 0.000000       | 0.000000       |

---

#### Shuffle 25%

**33% Attackers — Avg Unreached Honest Participants (out of 6699 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 2020.15        | 732.13         | 275.12         | 114.10         |
| 6              | 1168.75        | 260.01         | 59.89          | 17.01          |
| 8              | 688.18         | 94.20          | 13.66          | 2.61           |
| 10             | 410.56         | 35.00          | 3.27           | 0.39           |

**45% Attackers — Avg Unreached Honest Participants (out of 5500 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 203.22         | 11.87          | 1.49           | 0.01           |
| 6              | 61.28          | 0.78           | 0.05           | 0.00           |
| 8              | 19.40          | 0.06           | 0.00           | 0.00           |
| 10             | 6.37           | 0.01           | 0.00           | 0.00           |

**33% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.301515       | 0.109273       | 0.041063       | 0.017030       |
| 6              | 0.174440       | 0.038808       | 0.008939       | 0.002539       |
| 8              | 0.102714       | 0.014059       | 0.002039       | 0.000390       |
| 10             | 0.061277       | 0.005223       | 0.000487       | 0.000058       |

**45% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.036949       | 0.002158       | 0.000270       | 0.000001       |
| 6              | 0.011142       | 0.000142       | 0.000009       | 0.000000       |
| 8              | 0.003527       | 0.000012       | 0.000000       | 0.000000       |
| 10             | 0.001158       | 0.000001       | 0.000000       | 0.000000       |

---

#### Shuffle 30%

**33% Attackers — Avg Unreached Honest Participants (out of 6699 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 2119.94        | 787.50         | 299.31         | 129.47         |
| 6              | 1251.39        | 287.82         | 68.65          | 20.37          |
| 8              | 752.89         | 108.03         | 16.46          | 3.43           |
| 10             | 457.60         | 41.64          | 4.14           | 0.54           |

**45% Attackers — Avg Unreached Honest Participants (out of 5500 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 205.42         | 15.55          | 2.66           | 0.03           |
| 6              | 61.16          | 1.22           | 0.12           | 0.00           |
| 8              | 19.42          | 0.10           | 0.01           | 0.00           |
| 10             | 6.39           | 0.01           | 0.00           | 0.00           |

**33% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.316409       | 0.117537       | 0.044673       | 0.019323       |
| 6              | 0.186775       | 0.042958       | 0.010246       | 0.003040       |
| 8              | 0.112371       | 0.016124       | 0.002457       | 0.000511       |
| 10             | 0.068298       | 0.006215       | 0.000618       | 0.000081       |

**45% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.037349       | 0.002827       | 0.000484       | 0.000006       |
| 6              | 0.011119       | 0.000221       | 0.000023       | 0.000000       |
| 8              | 0.003532       | 0.000019       | 0.000001       | 0.000000       |
| 10             | 0.001162       | 0.000002       | 0.000000       | 0.000000       |

---

### High-Weight Attackers (Worst-Case)

#### Shuffle 10%

**33% Attackers — Avg Unreached Honest Participants (out of 6699 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 4864.87        | 3374.68        | 2377.50        | 1620.29        |
| 6              | 4159.87        | 2420.17        | 1446.24        | 807.21         |
| 8              | 3594.02        | 1769.09        | 906.32         | 411.28         |
| 10             | 3111.10        | 1304.57        | 572.80         | 209.41         |

**45% Attackers — Avg Unreached Honest Participants (out of 5500 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 5201.87        | 4574.86        | 3878.96        | 3161.21        |
| 6              | 5058.39        | 4180.52        | 3273.18        | 2394.27        |
| 8              | 4930.77        | 3845.27        | 2798.55        | 1844.16        |
| 10             | 4801.07        | 3536.35        | 2385.20        | 1420.29        |

**33% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.726100       | 0.503684       | 0.354851       | 0.241834       |
| 6              | 0.620876       | 0.361219       | 0.215856       | 0.120479       |
| 8              | 0.536421       | 0.264044       | 0.135272       | 0.061385       |
| 10             | 0.464344       | 0.194711       | 0.085493       | 0.031256       |

**45% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.945795       | 0.831793       | 0.705265       | 0.574765       |
| 6              | 0.919708       | 0.760095       | 0.595124       | 0.435322       |
| 8              | 0.896503       | 0.699140       | 0.508828       | 0.335301       |
| 10             | 0.872922       | 0.642973       | 0.433673       | 0.258235       |

---

#### Shuffle 15%

**33% Attackers — Avg Unreached Honest Participants (out of 6699 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 4834.12        | 3353.80        | 2353.48        | 1590.23        |
| 6              | 4141.98        | 2408.10        | 1437.20        | 793.79         |
| 8              | 3577.21        | 1751.01        | 894.87         | 399.38         |
| 10             | 3080.94        | 1277.97        | 559.33         | 202.15         |

**45% Attackers — Avg Unreached Honest Participants (out of 5500 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 5197.41        | 4566.17        | 3866.26        | 3152.11        |
| 6              | 5056.67        | 4165.72        | 3255.10        | 2381.88        |
| 8              | 4921.84        | 3821.09        | 2771.76        | 1820.93        |
| 10             | 4795.93        | 3508.95        | 2360.30        | 1392.31        |

**33% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.721510       | 0.500567       | 0.351266       | 0.237349       |
| 6              | 0.618206       | 0.359417       | 0.214507       | 0.118476       |
| 8              | 0.533913       | 0.261344       | 0.133563       | 0.059609       |
| 10             | 0.459842       | 0.190742       | 0.083482       | 0.030171       |

**45% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.944984       | 0.830213       | 0.702957       | 0.573110       |
| 6              | 0.919394       | 0.757403       | 0.591836       | 0.433068       |
| 8              | 0.894880       | 0.694743       | 0.503956       | 0.331078       |
| 10             | 0.871987       | 0.637991       | 0.429146       | 0.253147       |

---

#### Shuffle 20%

**33% Attackers — Avg Unreached Honest Participants (out of 6699 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 4826.44        | 3342.88        | 2342.23        | 1577.22        |
| 6              | 4134.02        | 2408.65        | 1438.44        | 789.86         |
| 8              | 3567.26        | 1758.21        | 894.36         | 404.33         |
| 10             | 3070.05        | 1276.95        | 553.80         | 205.53         |

**45% Attackers — Avg Unreached Honest Participants (out of 5500 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 5194.47        | 4563.91        | 3856.05        | 3151.74        |
| 6              | 5048.91        | 4163.79        | 3244.02        | 2379.17        |
| 8              | 4912.77        | 3815.65        | 2753.76        | 1813.38        |
| 10             | 4782.85        | 3499.42        | 2341.62        | 1379.69        |

**33% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.720364       | 0.498937       | 0.349586       | 0.235406       |
| 6              | 0.617019       | 0.359500       | 0.214692       | 0.117889       |
| 8              | 0.532426       | 0.262419       | 0.133486       | 0.060347       |
| 10             | 0.458216       | 0.190590       | 0.082657       | 0.030676       |

**45% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.944449       | 0.829802       | 0.701101       | 0.573043       |
| 6              | 0.917983       | 0.757053       | 0.589821       | 0.432576       |
| 8              | 0.893232       | 0.693755       | 0.500684       | 0.329705       |
| 10             | 0.869609       | 0.636259       | 0.425749       | 0.250853       |

---

#### Shuffle 25%

**33% Attackers — Avg Unreached Honest Participants (out of 6699 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 4807.10        | 3313.70        | 2307.20        | 1546.35        |
| 6              | 4105.71        | 2382.21        | 1405.58        | 765.83         |
| 8              | 3532.44        | 1728.42        | 868.63         | 385.16         |
| 10             | 3034.38        | 1252.29        | 537.70         | 191.13         |

**45% Attackers — Avg Unreached Honest Participants (out of 5500 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 5191.53        | 4558.62        | 3838.47        | 3142.00        |
| 6              | 5046.02        | 4156.70        | 3230.43        | 2368.76        |
| 8              | 4906.70        | 3804.46        | 2743.01        | 1801.58        |
| 10             | 4777.97        | 3490.03        | 2328.98        | 1371.47        |

**33% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.717477       | 0.494581       | 0.344358       | 0.230799       |
| 6              | 0.612792       | 0.355553       | 0.209788       | 0.114303       |
| 8              | 0.527229       | 0.257973       | 0.129647       | 0.057486       |
| 10             | 0.452892       | 0.186909       | 0.080254       | 0.028528       |

**45% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.943915       | 0.828840       | 0.697903       | 0.571273       |
| 6              | 0.917458       | 0.755763       | 0.587350       | 0.430683       |
| 8              | 0.892127       | 0.691719       | 0.498729       | 0.327560       |
| 10             | 0.868722       | 0.634551       | 0.423451       | 0.249359       |

---

#### Shuffle 30%

**33% Attackers — Avg Unreached Honest Participants (out of 6699 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 4812.88        | 3326.90        | 2316.17        | 1559.58        |
| 6              | 4111.15        | 2387.64        | 1410.54        | 777.51         |
| 8              | 3530.97        | 1723.93        | 863.68         | 389.84         |
| 10             | 3027.51        | 1244.91        | 532.40         | 190.97         |

**45% Attackers — Avg Unreached Honest Participants (out of 5500 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 5188.56        | 4552.96        | 3836.06        | 3140.85        |
| 6              | 5044.99        | 4155.02        | 3231.89        | 2373.23        |
| 8              | 4908.19        | 3801.46        | 2734.77        | 1806.12        |
| 10             | 4776.90        | 3480.99        | 2317.09        | 1372.28        |

**33% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.718340       | 0.496553       | 0.345697       | 0.232774       |
| 6              | 0.613605       | 0.356364       | 0.210528       | 0.116046       |
| 8              | 0.527010       | 0.257303       | 0.128908       | 0.058185       |
| 10             | 0.451867       | 0.185808       | 0.079462       | 0.028503       |

**45% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.943374       | 0.827811       | 0.697465       | 0.571064       |
| 6              | 0.917271       | 0.755458       | 0.587616       | 0.431497       |
| 8              | 0.892398       | 0.691175       | 0.497231       | 0.328386       |
| 10             | 0.868526       | 0.632908       | 0.421289       | 0.249505       |

---

### Wald Distribution (Weight-Based Probabilistic)

#### Shuffle 5%

**33% Attackers — Avg Unreached Honest Participants (out of 6700 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 6010.39        | 4956.99        | 3997.55        | 3153.29        |
| 6              | 5716.69        | 4330.54        | 3183.93        | 2238.56        |
| 8              | 5441.65        | 3794.12        | 2550.84        | 1598.87        |
| 10             | 5185.85        | 3339.04        | 2057.83        | 1157.39        |

**45% Attackers — Avg Unreached Honest Participants (out of 5500 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 5405.06        | 5103.46        | 4662.19        | 4155.89        |
| 6              | 5362.04        | 4931.55        | 4323.65        | 3637.34        |
| 8              | 5317.38        | 4766.88        | 4017.39        | 3189.16        |
| 10             | 5273.82        | 4614.06        | 3739.56        | 2804.70        |

**33% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.897073       | 0.739849       | 0.596649       | 0.470641       |
| 6              | 0.853238       | 0.646350       | 0.475213       | 0.334113       |
| 8              | 0.812187       | 0.566287       | 0.380722       | 0.238637       |
| 10             | 0.774008       | 0.498364       | 0.307138       | 0.172745       |

**45% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.982739       | 0.927902       | 0.847671       | 0.755616       |
| 6              | 0.974917       | 0.896645       | 0.786119       | 0.661334       |
| 8              | 0.966797       | 0.866705       | 0.730434       | 0.579847       |
| 10             | 0.958876       | 0.838920       | 0.679919       | 0.509946       |

---

#### Shuffle 10%

**33% Attackers — Avg Unreached Honest Participants (out of 6700 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 6002.88        | 4959.82        | 3983.75        | 3121.61        |
| 6              | 5712.69        | 4333.90        | 3164.15        | 2209.83        |
| 8              | 5429.91        | 3780.78        | 2521.16        | 1557.55        |
| 10             | 5174.17        | 3321.59        | 2030.95        | 1115.13        |

**45% Attackers — Avg Unreached Honest Participants (out of 5500 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 5403.40        | 5104.76        | 4665.89        | 4141.62        |
| 6              | 5359.10        | 4930.92        | 4318.13        | 3606.74        |
| 8              | 5313.11        | 4762.63        | 4005.64        | 3148.04        |
| 10             | 5269.44        | 4603.87        | 3721.47        | 2763.09        |

**33% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.895953       | 0.740272       | 0.594590       | 0.465912       |
| 6              | 0.852640       | 0.646850       | 0.472261       | 0.329825       |
| 8              | 0.810435       | 0.564296       | 0.376292       | 0.232471       |
| 10             | 0.772264       | 0.495760       | 0.303127       | 0.166438       |

**45% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.982437       | 0.928138       | 0.848343       | 0.753022       |
| 6              | 0.974383       | 0.896531       | 0.785114       | 0.655771       |
| 8              | 0.966020       | 0.865933       | 0.728298       | 0.572371       |
| 10             | 0.958080       | 0.837067       | 0.676631       | 0.502380       |

---

#### Shuffle 15%

**33% Attackers — Avg Unreached Honest Participants (out of 6700 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 5996.59        | 4961.84        | 4005.26        | 3116.83        |
| 6              | 5694.95        | 4335.57        | 3172.07        | 2190.85        |
| 8              | 5412.48        | 3772.87        | 2510.93        | 1528.94        |
| 10             | 5158.51        | 3316.78        | 2026.63        | 1094.26        |

**45% Attackers — Avg Unreached Honest Participants (out of 5500 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 5401.24        | 5105.10        | 4675.44        | 4147.34        |
| 6              | 5352.89        | 4928.15        | 4322.11        | 3596.43        |
| 8              | 5307.05        | 4756.30        | 4002.82        | 3128.96        |
| 10             | 5263.48        | 4595.60        | 3718.61        | 2742.76        |

**33% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.895013       | 0.740573       | 0.597800       | 0.465198       |
| 6              | 0.849993       | 0.647100       | 0.473444       | 0.326992       |
| 8              | 0.807833       | 0.563115       | 0.374766       | 0.228200       |
| 10             | 0.769926       | 0.495041       | 0.302482       | 0.163322       |

**45% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.982045       | 0.928201       | 0.850081       | 0.754062       |
| 6              | 0.973253       | 0.896027       | 0.785838       | 0.653897       |
| 8              | 0.964917       | 0.864782       | 0.727786       | 0.568902       |
| 10             | 0.956997       | 0.835563       | 0.676111       | 0.498683       |

---

#### Shuffle 20%

**33% Attackers — Avg Unreached Honest Participants (out of 6700 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 5993.39        | 4940.99        | 3974.36        | 3096.50        |
| 6              | 5691.89        | 4298.29        | 3126.65        | 2161.07        |
| 8              | 5415.00        | 3746.57        | 2468.98        | 1504.07        |
| 10             | 5154.80        | 3286.50        | 1988.94        | 1070.47        |

**45% Attackers — Avg Unreached Honest Participants (out of 5500 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 5404.65        | 5100.03        | 4666.13        | 4139.37        |
| 6              | 5357.40        | 4917.88        | 4307.71        | 3589.18        |
| 8              | 5310.83        | 4743.55        | 3983.38        | 3121.22        |
| 10             | 5265.41        | 4581.19        | 3699.52        | 2738.30        |

**33% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.894536       | 0.737461       | 0.593187       | 0.462164       |
| 6              | 0.849536       | 0.641536       | 0.466664       | 0.322548       |
| 8              | 0.808208       | 0.559190       | 0.368505       | 0.224488       |
| 10             | 0.769374       | 0.490523       | 0.296856       | 0.159771       |

**45% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.982664       | 0.927277       | 0.848388       | 0.752613       |
| 6              | 0.974073       | 0.894160       | 0.783220       | 0.652579       |
| 8              | 0.965606       | 0.862464       | 0.724252       | 0.567494       |
| 10             | 0.957348       | 0.832943       | 0.672640       | 0.497872       |

---

#### Shuffle 25%

**33% Attackers — Avg Unreached Honest Participants (out of 6700 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 5997.88        | 4938.15        | 3978.61        | 3089.30        |
| 6              | 5693.03        | 4285.35        | 3118.46        | 2150.06        |
| 8              | 5408.45        | 3723.93        | 2459.38        | 1495.90        |
| 10             | 5146.11        | 3260.58        | 1968.28        | 1058.09        |

**45% Attackers — Avg Unreached Honest Participants (out of 5500 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 5405.96        | 5099.92        | 4666.29        | 4132.50        |
| 6              | 5359.45        | 4910.78        | 4304.11        | 3582.07        |
| 8              | 5311.74        | 4733.68        | 3977.27        | 3110.29        |
| 10             | 5264.29        | 4570.59        | 3683.93        | 2714.70        |

**33% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.895205       | 0.737037       | 0.593823       | 0.461090       |
| 6              | 0.849706       | 0.639605       | 0.465442       | 0.320904       |
| 8              | 0.807231       | 0.555811       | 0.367072       | 0.223269       |
| 10             | 0.768076       | 0.486654       | 0.293773       | 0.157924       |

**45% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.982901       | 0.927259       | 0.848417       | 0.751363       |
| 6              | 0.974446       | 0.892869       | 0.782566       | 0.651286       |
| 8              | 0.965772       | 0.860669       | 0.723139       | 0.565507       |
| 10             | 0.957143       | 0.831017       | 0.669805       | 0.493581       |

---

#### Shuffle 30%

**33% Attackers — Avg Unreached Honest Participants (out of 6700 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 5992.00        | 4938.42        | 3963.11        | 3086.46        |
| 6              | 5685.49        | 4279.54        | 3104.78        | 2145.43        |
| 8              | 5398.93        | 3719.09        | 2443.23        | 1486.33        |
| 10             | 5136.73        | 3256.03        | 1952.43        | 1044.51        |

**45% Attackers — Avg Unreached Honest Participants (out of 5500 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 5403.78        | 5095.32        | 4652.87        | 4124.27        |
| 6              | 5357.33        | 4908.07        | 4290.51        | 3572.08        |
| 8              | 5309.31        | 4727.60        | 3961.89        | 3099.53        |
| 10             | 5263.49        | 4562.21        | 3667.96        | 2705.82        |

**33% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.894328       | 0.737078       | 0.591509       | 0.460666       |
| 6              | 0.848580       | 0.638737       | 0.463400       | 0.320214       |
| 8              | 0.805811       | 0.555087       | 0.364661       | 0.221840       |
| 10             | 0.766676       | 0.485976       | 0.291408       | 0.155895       |

**45% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.982505       | 0.926423       | 0.846067       | 0.749868       |
| 6              | 0.974060       | 0.892376       | 0.780093       | 0.649469       |
| 8              | 0.965330       | 0.859563       | 0.720344       | 0.563551       |
| 10             | 0.956999       | 0.829493       | 0.666902       | 0.491967       |

---

### Slot-Based Distribution (Weighted Random Sampling)

#### Shuffle 5%

**33% Attackers — Avg Unreached Honest Participants (out of 6700 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 6424.48        | 5846.60        | 5127.15        | 4218.91        |
| 6              | 6319.26        | 5461.25        | 4535.49        | 3498.32        |
| 8              | 6194.43        | 5095.05        | 3895.42        | 2833.38        |
| 10             | 6065.13        | 4782.99        | 3502.05        | 2340.71        |

**45% Attackers — Avg Unreached Honest Participants (out of 5500 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 5477.14        | 5372.80        | 5142.29        | 4808.14        |
| 6              | 5464.24        | 5306.77        | 4990.34        | 4493.33        |
| 8              | 5454.31        | 5241.68        | 4807.94        | 4203.22        |
| 10             | 5444.99        | 5177.44        | 4729.32        | 3938.61        |

**33% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.958878       | 0.872627       | 0.765246       | 0.629688       |
| 6              | 0.943173       | 0.815112       | 0.676939       | 0.522138       |
| 8              | 0.924541       | 0.760455       | 0.581406       | 0.422892       |
| 10             | 0.905243       | 0.713880       | 0.522693       | 0.349360       |

**45% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.995843       | 0.976873       | 0.934962       | 0.874207       |
| 6              | 0.993499       | 0.964867       | 0.907335       | 0.816969       |
| 8              | 0.991694       | 0.953033       | 0.874171       | 0.764221       |
| 10             | 0.989999       | 0.941353       | 0.859876       | 0.716110       |

---

#### Shuffle 10%

**33% Attackers — Avg Unreached Honest Participants (out of 6700 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 6421.38        | 5799.35        | 5086.91        | 4244.39        |
| 6              | 6321.00        | 5421.42        | 4415.51        | 3420.78        |
| 8              | 6193.56        | 5135.39        | 3903.86        | 2790.75        |
| 10             | 6084.88        | 4747.09        | 3463.66        | 2194.30        |

**45% Attackers — Avg Unreached Honest Participants (out of 5500 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 5476.04        | 5360.23        | 5131.74        | 4808.17        |
| 6              | 5463.26        | 5286.28        | 4981.41        | 4477.71        |
| 8              | 5449.00        | 5243.51        | 4852.50        | 4198.72        |
| 10             | 5441.05        | 5187.17        | 4654.89        | 3955.02        |

**33% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.958416       | 0.865575       | 0.759241       | 0.633491       |
| 6              | 0.943433       | 0.809167       | 0.659031       | 0.510564       |
| 8              | 0.924413       | 0.766477       | 0.582666       | 0.416529       |
| 10             | 0.908191       | 0.708521       | 0.516964       | 0.327508       |

**45% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.995644       | 0.974587       | 0.933043       | 0.874212       |
| 6              | 0.993319       | 0.961142       | 0.905711       | 0.814129       |
| 8              | 0.990728       | 0.953365       | 0.882273       | 0.763404       |
| 10             | 0.989283       | 0.943123       | 0.846344       | 0.719094       |

---

#### Shuffle 15%

**33% Attackers — Avg Unreached Honest Participants (out of 6700 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 6425.91        | 5790.72        | 5088.90        | 4250.04        |
| 6              | 6320.31        | 5425.84        | 4396.91        | 3458.87        |
| 8              | 6175.54        | 5086.18        | 3918.57        | 2774.91        |
| 10             | 6063.12        | 4761.28        | 3454.26        | 2196.04        |

**45% Attackers — Avg Unreached Honest Participants (out of 5500 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 5474.50        | 5360.66        | 5132.86        | 4810.75        |
| 6              | 5465.56        | 5306.45        | 4951.46        | 4477.89        |
| 8              | 5451.11        | 5230.67        | 4815.64        | 4151.21        |
| 10             | 5438.84        | 5179.79        | 4689.34        | 3948.72        |

**33% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.959092       | 0.864286       | 0.759538       | 0.634334       |
| 6              | 0.943330       | 0.809827       | 0.656255       | 0.516249       |
| 8              | 0.921723       | 0.759132       | 0.584862       | 0.414165       |
| 10             | 0.904943       | 0.710639       | 0.515562       | 0.327767       |

**45% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.995364       | 0.974666       | 0.933248       | 0.874682       |
| 6              | 0.993739       | 0.964810       | 0.900266       | 0.814161       |
| 8              | 0.991111       | 0.951032       | 0.875571       | 0.754766       |
| 10             | 0.988880       | 0.941780       | 0.852608       | 0.717950       |

---

#### Shuffle 20%

**33% Attackers — Avg Unreached Honest Participants (out of 6700 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 6421.01        | 5818.24        | 5044.86        | 4236.26        |
| 6              | 6298.00        | 5436.52        | 4441.56        | 3362.28        |
| 8              | 6167.06        | 5104.36        | 3846.01        | 2764.11        |
| 10             | 6082.51        | 4803.30        | 3474.03        | 2179.74        |

**45% Attackers — Avg Unreached Honest Participants (out of 5500 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 5476.16        | 5364.93        | 5140.55        | 4796.90        |
| 6              | 5462.21        | 5296.97        | 4953.27        | 4495.51        |
| 8              | 5449.36        | 5233.76        | 4790.83        | 4179.02        |
| 10             | 5434.28        | 5169.26        | 4654.05        | 3906.71        |

**33% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.958359       | 0.868394       | 0.752964       | 0.632277       |
| 6              | 0.940000       | 0.811420       | 0.662919       | 0.501833       |
| 8              | 0.920457       | 0.761845       | 0.574031       | 0.412553       |
| 10             | 0.907837       | 0.716910       | 0.518512       | 0.325334       |

**45% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.995666       | 0.975442       | 0.934645       | 0.872164       |
| 6              | 0.993129       | 0.963085       | 0.900594       | 0.817365       |
| 8              | 0.990793       | 0.951593       | 0.871059       | 0.759823       |
| 10             | 0.988051       | 0.939866       | 0.846191       | 0.710311       |

---

#### Shuffle 25%

**33% Attackers — Avg Unreached Honest Participants (out of 6700 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 6419.61        | 5828.53        | 5037.22        | 4175.68        |
| 6              | 6288.62        | 5396.60        | 4433.90        | 3346.93        |
| 8              | 6173.97        | 5035.93        | 3832.61        | 2686.86        |
| 10             | 6050.28        | 4744.10        | 3446.03        | 2194.09        |

**45% Attackers — Avg Unreached Honest Participants (out of 5500 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 5475.00        | 5365.32        | 5132.29        | 4757.37        |
| 6              | 5460.84        | 5287.46        | 4953.63        | 4420.89        |
| 8              | 5447.87        | 5234.58        | 4838.38        | 4173.01        |
| 10             | 5438.34        | 5165.11        | 4649.71        | 3895.47        |

**33% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.958150       | 0.869929       | 0.751823       | 0.623236       |
| 6              | 0.938600       | 0.805462       | 0.661776       | 0.499541       |
| 8              | 0.921488       | 0.751631       | 0.572031       | 0.401024       |
| 10             | 0.903027       | 0.708075       | 0.514333       | 0.327476       |

**45% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.995454       | 0.975512       | 0.933144       | 0.864976       |
| 6              | 0.992879       | 0.961356       | 0.900661       | 0.803799       |
| 8              | 0.990522       | 0.951743       | 0.879706       | 0.758730       |
| 10             | 0.988789       | 0.939110       | 0.845402       | 0.708267       |

---

#### Shuffle 30%

**33% Attackers — Avg Unreached Honest Participants (out of 6700 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 6428.48        | 5788.93        | 5055.99        | 4096.18        |
| 6              | 6282.83        | 5419.12        | 4356.57        | 3285.22        |
| 8              | 6157.84        | 5020.57        | 3851.66        | 2666.38        |
| 10             | 6043.92        | 4748.93        | 3409.81        | 2143.70        |

**45% Attackers — Avg Unreached Honest Participants (out of 5500 honest)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 5472.75        | 5357.83        | 5109.01        | 4738.42        |
| 6              | 5458.72        | 5276.22        | 4955.39        | 4473.86        |
| 8              | 5448.57        | 5223.32        | 4786.18        | 4158.69        |
| 10             | 5435.27        | 5152.15        | 4670.98        | 3846.27        |

**33% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.959474       | 0.864019       | 0.754625       | 0.611370       |
| 6              | 0.937735       | 0.808824       | 0.650234       | 0.490332       |
| 8              | 0.919080       | 0.749339       | 0.574877       | 0.397966       |
| 10             | 0.902078       | 0.708795       | 0.508936       | 0.319950       |

**45% Attackers — P(single honest participant blocked)**

| Trees \ Fanout | 4              | 8              | 16             | 32             |
|----------------|----------------|----------------|----------------|----------------|
| 4              | 0.995050       | 0.974151       | 0.929275       | 0.861530       |
| 6              | 0.992495       | 0.959313       | 0.901162       | 0.813429       |
| 8              | 0.990649       | 0.949694       | 0.870215       | 0.756126       |
| 10             | 0.988232       | 0.936757       | 0.849269       | 0.699321       |

---

## Key Insights

### Uniform Distribution

1. **Higher fanout dramatically improves security**: With 10 trees and 45% attackers, fanout 4 blocks 0.04% of honest nodes while fanout 16+ blocks essentially 0%.

2. **More trees exponentially reduce blocking**: Each additional tree roughly halves the blocking probability.

### High-Weight Attackers (Worst-Case)

1. **Devastating attack effectiveness**: With 45% high-weight attackers, even with 10 trees and fanout 32, over 22% of honest nodes are blocked.

2. **Fanout still helps significantly**: Increasing fanout from 4 to 32 reduces blocking by ~60% (e.g., 0.87 → 0.22 for 10 trees, 45% attackers).
---

## Message Counts and Network Load

**Note:** Messages shown are for ONE participant publishing through tree roots. For ALL 10,000 participants publishing, multiply 'Total Messages' by 10,000.

### Uniform Distribution - Message Counts

#### 33% Attackers, Shuffle 10%

| Trees | Fanout | Msgs (1 pub) | Msgs/Participant | Total if ALL publish |
|-------|--------|--------------|------------------|-----------------------|
| 4     | 4      | 13199        | 1.32             | 131.99M               |
| 4     | 8      | 20008        | 2.00             | 200.08M               |
| 4     | 16     | 24978        | 2.50             | 249.78M               |
| 4     | 32     | 29003        | 2.90             | 290.03M               |
| 6     | 4      | 19781        | 1.98             | 197.81M               |
| 6     | 8      | 30020        | 3.00             | 300.20M               |
| 6     | 16     | 37461        | 3.75             | 374.61M               |
| 6     | 32     | 43480        | 4.35             | 434.80M               |
| 8     | 4      | 26368        | 2.64             | 263.68M               |
| 8     | 8      | 40018        | 4.00             | 400.18M               |
| 8     | 16     | 49927        | 4.99             | 499.27M               |
| 8     | 32     | 57966        | 5.80             | 579.66M               |
| 10    | 4      | 32944        | 3.29             | 329.44M               |
| 10    | 8      | 49992        | 5.00             | 499.92M               |
| 10    | 16     | 62413        | 6.24             | 624.13M               |
| 10    | 32     | 72427        | 7.24             | 724.27M               |

#### 45% Attackers, Shuffle 10%

| Trees | Fanout | Msgs (1 pub) | Msgs/Participant | Total if ALL publish |
|-------|--------|--------------|------------------|-----------------------|
| 4     | 4      | 27908        | 2.79             | 279.08M               |
| 4     | 8      | 35578        | 3.56             | 355.78M               |
| 4     | 16     | 39806        | 3.98             | 398.06M               |
| 4     | 32     | 39996        | 4.00             | 399.96M               |
| 6     | 4      | 41860        | 4.19             | 418.60M               |
| 6     | 8      | 53368        | 5.34             | 533.68M               |
| 6     | 16     | 59707        | 5.97             | 597.07M               |
| 6     | 32     | 59994        | 6.00             | 599.94M               |
| 8     | 4      | 55812        | 5.58             | 558.12M               |
| 8     | 8      | 71158        | 7.12             | 711.58M               |
| 8     | 16     | 79610        | 7.96             | 796.10M               |
| 8     | 32     | 79992        | 8.00             | 799.92M               |
| 10    | 4      | 69767        | 6.98             | 697.67M               |
| 10    | 8      | 88953        | 8.90             | 889.53M               |
| 10    | 16     | 99514        | 9.95             | 995.14M               |
| 10    | 32     | 99990        | 10.00            | 999.90M               |

---

### High-Weight Distribution - Message Counts

#### 33% Attackers, Shuffle 10%

| Trees | Fanout | Msgs (1 pub) | Msgs/Participant | Total if ALL publish |
|-------|--------|--------------|------------------|-----------------------|
| 4     | 4      | 3046         | 0.30             | 30.46M                |
| 4     | 8      | 6289         | 0.63             | 62.89M                |
| 4     | 16     | 9157         | 0.92             | 91.57M                |
| 4     | 32     | 11880        | 1.19             | 118.80M               |
| 6     | 4      | 4554         | 0.46             | 45.54M                |
| 6     | 8      | 9415         | 0.94             | 94.15M                |
| 6     | 16     | 13784        | 1.38             | 137.84M               |
| 6     | 32     | 17830        | 1.78             | 178.30M               |
| 8     | 4      | 6077         | 0.61             | 60.77M                |
| 8     | 8      | 12563        | 1.26             | 125.63M               |
| 8     | 16     | 18357        | 1.84             | 183.57M               |
| 8     | 32     | 23675        | 2.37             | 236.75M               |
| 10    | 4      | 7569         | 0.76             | 75.69M                |
| 10    | 8      | 15631        | 1.56             | 156.31M               |
| 10    | 16     | 22922        | 2.29             | 229.22M               |
| 10    | 32     | 29608        | 2.96             | 296.08M               |

#### 45% Attackers, Shuffle 10%

| Trees | Fanout | Msgs (1 pub) | Msgs/Participant | Total if ALL publish |
|-------|--------|--------------|------------------|-----------------------|
| 4     | 4      | 70           | 0.01             | 700.00K               |
| 4     | 8      | 344          | 0.03             | 3.44M                 |
| 4     | 16     | 991          | 0.10             | 9.91M                 |
| 4     | 32     | 1817         | 0.18             | 18.17M                |
| 6     | 4      | 102          | 0.01             | 1.02M                 |
| 6     | 8      | 524          | 0.05             | 5.24M                 |
| 6     | 16     | 1479         | 0.15             | 14.79M                |
| 6     | 32     | 2721         | 0.27             | 27.21M                |
| 8     | 4      | 134          | 0.01             | 1.34M                 |
| 8     | 8      | 698          | 0.07             | 6.98M                 |
| 8     | 16     | 1984         | 0.20             | 19.84M                |
| 8     | 32     | 3665         | 0.37             | 36.65M                |
| 10    | 4      | 169          | 0.02             | 1.69M                 |
| 10    | 8      | 873          | 0.09             | 8.73M                 |
| 10    | 16     | 2464         | 0.25             | 24.64M                |
| 10    | 32     | 4541         | 0.45             | 45.41M                |

---

### Slot-Based Distribution - Message Counts

#### 33% Attackers, Shuffle 10%

| Trees | Fanout | Msgs (1 pub) | Msgs/Participant | Total if ALL publish |
|-------|--------|--------------|------------------|-----------------------|
| 4     | 4      | 483          | 0.05             | 4.83M                 |
| 4     | 8      | 1589         | 0.16             | 15.89M                |
| 4     | 16     | 2958         | 0.30             | 29.58M                |
| 4     | 32     | 4568         | 0.46             | 45.68M                |
| 6     | 4      | 674          | 0.07             | 6.74M                 |
| 6     | 8      | 2357         | 0.24             | 23.57M                |
| 6     | 16     | 4612         | 0.46             | 46.12M                |
| 6     | 32     | 6807         | 0.68             | 68.07M                |
| 8     | 4      | 914          | 0.09             | 9.14M                 |
| 8     | 8      | 3014         | 0.30             | 30.14M                |
| 8     | 16     | 6037         | 0.60             | 60.37M                |
| 8     | 32     | 8967         | 0.90             | 89.67M                |
| 10    | 4      | 1136         | 0.11             | 11.36M                |
| 10    | 8      | 3950         | 0.40             | 39.50M                |
| 10    | 16     | 7545         | 0.75             | 75.45M                |
| 10    | 32     | 11563        | 1.16             | 115.63M               |

#### 45% Attackers, Shuffle 10%

| Trees | Fanout | Msgs (1 pub) | Msgs/Participant | Total if ALL publish |
|-------|--------|--------------|------------------|-----------------------|
| 4     | 4      | 59           | 0.01             | 590.00K               |
| 4     | 8      | 321          | 0.03             | 3.21M                 |
| 4     | 16     | 839          | 0.08             | 8.39M                 |
| 4     | 32     | 1467         | 0.15             | 14.67M                |
| 6     | 4      | 92           | 0.01             | 920.00K               |
| 6     | 8      | 504          | 0.05             | 5.04M                 |
| 6     | 16     | 1220         | 0.12             | 12.20M                |
| 6     | 32     | 2265         | 0.23             | 22.65M                |
| 8     | 4      | 129          | 0.01             | 1.29M                 |
| 8     | 8      | 618          | 0.06             | 6.18M                 |
| 8     | 16     | 1565         | 0.16             | 15.65M                |
| 8     | 32     | 2977         | 0.30             | 29.77M                |
| 10    | 4      | 151          | 0.02             | 1.51M                 |
| 10    | 8      | 757          | 0.08             | 7.57M                 |
| 10    | 16     | 2110         | 0.21             | 21.10M                |
| 10    | 32     | 3718         | 0.37             | 37.18M                |

---

### Wald Distribution - Message Counts

#### 33% Attackers, Shuffle 10%

| Trees | Fanout | Msgs (1 pub) | Msgs/Participant | Total if ALL publish |
|-------|--------|--------------|------------------|-----------------------|
| 4     | 4      | 1164         | 0.12             | 11.64M                |
| 4     | 8      | 3097         | 0.31             | 30.97M                |
| 4     | 16     | 5230         | 0.52             | 52.30M                |
| 4     | 32     | 7286         | 0.73             | 72.86M                |
| 6     | 4      | 1717         | 0.17             | 17.17M                |
| 6     | 8      | 4594         | 0.46             | 45.94M                |
| 6     | 16     | 7795         | 0.78             | 77.95M                |
| 6     | 32     | 10892        | 1.09             | 108.92M               |
| 8     | 4      | 2291         | 0.23             | 22.91M                |
| 8     | 8      | 6106         | 0.61             | 61.06M                |
| 8     | 16     | 10340        | 1.03             | 103.40M               |
| 8     | 32     | 14500        | 1.45             | 145.00M               |
| 10    | 4      | 2854         | 0.29             | 28.54M                |
| 10    | 8      | 7608         | 0.76             | 76.08M                |
| 10    | 16     | 12895        | 1.29             | 128.95M               |
| 10    | 32     | 18072        | 1.81             | 180.72M               |

#### 45% Attackers, Shuffle 10%

| Trees | Fanout | Msgs (1 pub) | Msgs/Participant | Total if ALL publish |
|-------|--------|--------------|------------------|-----------------------|
| 4     | 4      | 202          | 0.02             | 2.02M                 |
| 4     | 8      | 822          | 0.08             | 8.22M                 |
| 4     | 16     | 1787         | 0.18             | 17.87M                |
| 4     | 32     | 2911         | 0.29             | 29.11M                |
| 6     | 4      | 300          | 0.03             | 3.00M                 |
| 6     | 8      | 1222         | 0.12             | 12.22M                |
| 6     | 16     | 2678         | 0.27             | 26.78M                |
| 6     | 32     | 4386         | 0.44             | 43.86M                |
| 8     | 4      | 403          | 0.04             | 4.03M                 |
| 8     | 8      | 1630         | 0.16             | 16.30M                |
| 8     | 16     | 3567         | 0.36             | 35.67M                |
| 8     | 32     | 5878         | 0.59             | 58.78M                |
| 10    | 4      | 505          | 0.05             | 5.05M                 |
| 10    | 8      | 2036         | 0.20             | 20.36M                |
| 10    | 16     | 4461         | 0.45             | 44.61M                |
| 10    | 32     | 7335         | 0.73             | 73.35M                |

## Running the Simulation

To run the blocking probability simulation:

```bash
cd decentralized-api/

go run scripts/blocking_probability/main.go
```