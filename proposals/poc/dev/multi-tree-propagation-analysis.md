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

## Key Insights

### Uniform Distribution

1. **Higher fanout dramatically improves security**: With 10 trees and 45% attackers, fanout 4 blocks 0.04% of honest nodes while fanout 16+ blocks essentially 0%.

2. **More trees exponentially reduce blocking**: Each additional tree roughly halves the blocking probability.

### High-Weight Attackers (Worst-Case)

1. **Devastating attack effectiveness**: With 45% high-weight attackers, even with 10 trees and fanout 32, over 22% of honest nodes are blocked.

2. **Fanout still helps significantly**: Increasing fanout from 4 to 32 reduces blocking by ~60% (e.g., 0.87 → 0.22 for 10 trees, 45% attackers).
---

### Connection Counts by Configuration

| Trees (T) | Fanout (F) | Max Connections | Typical Connections |
|-----------|------------|-----------------|---------------------|
| 6         | 4          | 48              | 36-48               |
| 6         | 8          | 96              | 72-96               |
| 6         | 16         | 192             | 144-192             |
| 6         | 32         | 384             | 288-384             |
| 8         | 4          | 64              | 48-64               |
| 8         | 8          | 128             | 96-128              |
| 8         | 16         | 256             | 192-256             |
| 8         | 32         | 512             | 384-512             |
| 10        | 4          | 80              | 60-80               |
| 10        | 8          | 160             | 120-160             |
| 10        | 16         | 320             | 240-320             |
| 10        | 32         | 640             | 480-640             |
| 12        | 4          | 96              | 72-96               |
| 12        | 8          | 192             | 144-192             |
| 12        | 16         | 384             | 288-384             |
| 12        | 32         | 768             | 576-768             |

## Running the Simulation

To run the blocking probability simulation:

```bash
cd decentralized-api/

go run scripts/blocking_probability/main.go
```