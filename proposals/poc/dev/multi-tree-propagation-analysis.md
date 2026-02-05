# Propagation Tree Security Analysis

**Parameters:** 10,000 participants, weighted shuffle

## Theoretical Probability (Random Attacker Distribution)

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

---

## Key Formula (Approximate)

```
P(block one participant) ≈ P(parent is attacker)^numTrees
P(any of n blocked) ≈ n × P(block one)
```

**️ Important Limitation:**

This formula is an **approximation** that uses the average P(parent is attacker) across all nodes. In reality:

- **Low-weight participants** have higher P(parent is attacker) (their parents are more likely to be attackers)
- **High-weight participants** have lower P(parent is attacker) (their parents are less likely to be attackers)

The mathematical issue:
```
(average P)^numTrees  ≠  average of (P_i)^numTrees
```

Due to Jensen's inequality, the average of a power is not equal to the power of an average. This means:
- The formula **underestimates** blocking for low-weight participants
- The formula **overestimates** blocking for high-weight participants
- The overall blocking probability is **approximated** but not exact

The simulation directly counts blocked participants, avoiding this averaging issue.


## Methodology: How P(parent is attacker) is Calculated

These values are measured via simulation, not a closed-form formula.

### Simulation Approach

```go
for _, tree := range trees {
    for _, node := range tree.Nodes {
        if node.Parent == nil {
            continue
        }
        totalParentChecks++
        if attackers[node.Parent.Address] {
            parentIsAttacker++
        }
    }
}
P_parent_attacker = parentIsAttacker / totalParentChecks
```

### Why P(parent is attacker) != Attacker Fraction

With weighted shuffle:
1. **Parents are high-weight nodes** (tree positions 0 to ~n/fanout)
2. **Uniform attackers are spread across ALL weight levels**
3. Attacker density among parents != overall attacker fraction

## Attacker Distribution Models

The simulation supports two attacker distribution strategies:

### 1. Uniform Attacker Distribution

Attackers are evenly distributed across **all weight levels**, starting from the lowest-weight participants.

```go
step := numParticipants / numAttackers  // integer division
for i := 0; i < numAttackers; i++ {
    idx := (i * step) % numParticipants
    attackers[formatAddress(idx)] = true
}
```

**Characteristics:**
- Selects every Nth participant starting from index 0
- For 33% attackers: step = 10000/3300 = 3, selects indices 0, 3, 6, ..., 9897
- For 45% attackers: step = 10000/4500 = 2, selects indices 0, 2, 4, ..., 8998
- **Attackers are spread across low-weight and mid-weight nodes**
- High-weight nodes (indices 9000-9999) have **zero or few attackers**

### 2. High-Weight Attacker Distribution

Attackers are evenly distributed among **high-weight participants**, starting from the highest-weight nodes.

```go
step := numParticipants / numAttackers  // integer division
for i := 0; i < numAttackers; i++ {
    idx := (numParticipants - 1) - (i * step)
    attackers[formatAddress(idx)] = true
}
```

**Characteristics:**
- Selects every Nth participant starting from the **end** (index 9999)
- For 33% attackers: step = 3, selects indices 9999, 9996, 9993, ..., 102
- For 45% attackers: step = 2, selects indices 9999, 9997, 9995, ..., 1
- **Attackers are concentrated among high-weight nodes**
- These nodes are **most likely to become parents** in the tree structure
- This represents a **worst-case attack scenario** where attackers control high-weight nodes

## Examples: How Distribution Affects P(parent is attacker)

### Uniform Distribution Example

**Fanout 32, n=10,000, 45% attackers, 30% shuffle**

1. **Attacker selection:**
   - step = 10000 / 4500 = 2 (integer division)
   - Attackers at original indices: 0, 2, 4, 6, ..., 8998
   - Maximum attacker index = 4499 × 2 = 8998
   - **Indices 9000-9999 have ZERO attackers**

2. **Parent positions after weighted shuffle:**
   - Weight of node i = 1000 + i (higher index = higher weight)
   - After sorting by weight, tree positions 0-312 = original indices ~9687-9999
   - These are the highest-weight nodes

3. **Overlap calculation:**
   - Parents come from indices 9687-9999 (313 nodes)
   - Attackers only exist at indices 0-8998
   - Only due to 30% shuffle randomness, ~27 attackers swap into parent positions
   - P(parent is attacker) = 27/313 = **0.026** (very low!)

**Fanout 4, n=10,000, 33% attackers, uniform**

1. **Attacker selection:**
   - step = 10000 / 3300 = 3
   - Attackers at indices: 0, 3, 6, ..., 9897
   - Maximum attacker index = 3299 × 3 = 9897

2. **Parent positions:**
   - ~3,333 nodes are parents (tree positions 0-3332)
   - These correspond to original indices ~6667-9999

3. **Overlap calculation:**
   - Among indices 6667-9999, attackers are at: 6669, 6672, ..., 9897
   - Count: (9897 - 6669) / 3 + 1 = ~1076 attackers
   - P(parent is attacker) = 1076/3333 = **~0.32**

### High-Weight Distribution Example

**Fanout 32, n=10,000, 45% attackers, high-weight**

1. **Attacker selection:**
   - step = 10000 / 4500 = 2
   - Attackers at indices: 9999, 9997, 9995, ..., 1
   - **ALL high-weight nodes are attackers**

2. **Parent positions after weighted shuffle:**
   - Tree positions 0-312 come from original indices ~9687-9999
   - These are the highest-weight nodes

3. **Overlap calculation:**
   - Parents come from indices 9687-9999 (313 nodes)
   - All odd indices in this range are attackers: 9687, 9689, ..., 9999
   - Count: ~157 attackers (approximately 50%)
   - P(parent is attacker) ≈ **0.50**

**Key Insight:**
- **Uniform distribution:** Attackers are spread across all weights → Low P(parent is attacker) with high fanout
- **High-weight distribution:** Attackers control high-weight nodes → High P(parent is attacker) regardless of fanout (~50% for 45% attackers)
- With uniform attackers, the weighted shuffle places high-weight nodes as parents, but **high-index nodes have fewer or zero attackers**, making P(parent is attacker) lower than the overall attacker fraction

---

## Shuffle Percentage Impact

The **shuffle percentage** controls how much randomness is introduced into the weight-based ordering. Higher shuffle % means more position swaps, dispersing high-weight nodes away from guaranteed root positions.

### How Shuffle Percentage Affects Security

| Shuffle % | Effect on Weight Ordering | Security Impact |
|-----------|--------------------------|-----------------|
| 10%       | Mostly weight-ordered    | Less randomization in parent selection |
| 30%       | Significant randomization | More uniform parent distribution |

### Blocked Participants by Shuffle % (10 trees, fanout 32)

| Shuffle % | 33% Attackers | 45% Attackers |
|-----------|---------------|---------------|
| 10%       | 126.61        | 0.00          |
| 15%       | 36.36         | 0.00          |
| 20%       | 12.58         | 0.00          |
| 25%       | 4.79          | 0.00          |
| 30%       | 1.64          | 0.00          |

### Key Correlation

**Higher shuffle % = Better security**:

Higher shuffle disperses the weight ordering, making the tree structure more randomized. This reduces the probability that any single participant has all attacker parents across all trees.

---

## Simulation Results by Shuffle Percentage

### Shuffle 10%

#### P(parent=attacker) by Fanout
| Fanout | 33% Attackers | 45% Attackers |
|--------|---------------|---------------|
| 4      | 0.320         | 0.300         |
| 8      | 0.307         | 0.112         |
| 16     | 0.283         | 0.018         |
| 32     | 0.237         | 0.010         |

#### Blocked Participants - 33% Attackers
| Trees \ Fanout | 4      | 8      | 16     | 32     |
|----------------|--------|--------|--------|--------|
| 4              | 849.00 | 805.68 | 691.88 | 562.44 |
| 6              | 503.30 | 476.87 | 415.61 | 339.88 |
| 8              | 300.30 | 283.42 | 246.14 | 204.94 |
| 10             | 179.81 | 168.32 | 147.97 | 126.61 |

#### Blocked Participants - 45% Attackers
| Trees \ Fanout | 4      | 8      | 16   | 32   |
|----------------|--------|--------|------|------|
| 4              | 650.09 | 179.00 | 0.00 | 0.00 |
| 6              | 431.57 | 106.48 | 0.00 | 0.00 |
| 8              | 286.22 | 63.61  | 0.00 | 0.00 |
| 10             | 191.45 | 39.16  | 0.00 | 0.00 |

---

### Shuffle 20%

#### P(parent=attacker) by Fanout
| Fanout | 33% Attackers | 45% Attackers |
|--------|---------------|---------------|
| 4      | 0.320         | 0.300         |
| 8      | 0.307         | 0.124         |
| 16     | 0.286         | 0.034         |
| 32     | 0.248         | 0.018         |

#### Blocked Participants - 33% Attackers
| Trees \ Fanout | 4      | 8      | 16     | 32     |
|----------------|--------|--------|--------|--------|
| 4              | 338.12 | 317.03 | 275.20 | 227.55 |
| 6              | 126.67 | 117.47 | 102.98 | 87.35  |
| 8              | 47.46  | 43.60  | 38.77  | 32.80  |
| 10             | 17.95  | 16.03  | 14.78  | 12.58  |

#### Blocked Participants - 45% Attackers
| Trees \ Fanout | 4      | 8     | 16   | 32   |
|----------------|--------|-------|------|------|
| 4              | 322.90 | 73.32 | 0.01 | 0.01 |
| 6              | 149.87 | 27.77 | 0.00 | 0.00 |
| 8              | 71.13  | 10.66 | 0.00 | 0.00 |
| 10             | 32.83  | 3.91  | 0.00 | 0.00 |

---

### Shuffle 30%

#### P(parent=attacker) by Fanout
| Fanout | 33% Attackers | 45% Attackers |
|--------|---------------|---------------|
| 4      | 0.320         | 0.300         |
| 8      | 0.308         | 0.135         |
| 16     | 0.290         | 0.048         |
| 32     | 0.257         | 0.026         |

#### Blocked Participants - 33% Attackers
| Trees \ Fanout | 4      | 8      | 16     | 32     |
|----------------|--------|--------|--------|--------|
| 4              | 168.78 | 154.10 | 136.89 | 111.37 |
| 6              | 39.96  | 36.27  | 31.62  | 27.47  |
| 8              | 10.11  | 8.89   | 8.18   | 7.42   |
| 10             | 2.69   | 2.04   | 1.98   | 1.64   |

#### Blocked Participants - 45% Attackers
| Trees \ Fanout | 4      | 8     | 16   | 32   |
|----------------|--------|-------|------|------|
| 4              | 196.38 | 35.07 | 0.04 | 0.00 |
| 6              | 65.86  | 8.82  | 0.00 | 0.00 |
| 8              | 23.11  | 2.33  | 0.00 | 0.00 |
| 10             | 7.92   | 0.52  | 0.00 | 0.00 |

#### P(block one) - 33% Attackers
| Trees \ Fanout | 4            | 8            | 16           | 32           |
|----------------|--------------|--------------|--------------|--------------|
| 4              | 1.05e-02     | 8.94e-03     | 7.10e-03     | 4.36e-03     |
| 6              | 1.08e-03     | 8.43e-04     | 5.95e-04     | 2.91e-04     |
| 8              | 1.10e-04     | 7.94e-05     | 5.02e-05     | 1.95e-05     |
| 10             | 1.12e-05     | 7.49e-06     | 4.21e-06     | 1.29e-06     |

#### P(block one) - 45% Attackers
| Trees \ Fanout | 4            | 8            | 16           | 32           |
|----------------|--------------|--------------|--------------|--------------|
| 4              | 8.14e-03     | 3.32e-04     | 5.25e-06     | 4.59e-07     |
| 6              | 7.33e-04     | 6.05e-06     | 1.22e-08     | 2.96e-10     |
| 8              | 6.62e-05     | 1.09e-07     | 2.74e-11     | 2.00e-13     |
| 10             | 5.98e-06     | 1.96e-09     | 6.09e-14     | 1.33e-16     |

---

## High-Weight Attacker Distribution Results

When attackers control high-weight nodes, the security characteristics change dramatically. Unlike uniform distribution where higher fanout reduces attack effectiveness, **high-weight attackers maintain consistent P(parent is attacker) across all fanout values** because they already occupy parent positions.

### Shuffle 10% - High-Weight Attackers

#### P(parent=attacker) by Fanout
| Fanout | 33% Attackers | 45% Attackers |
|--------|---------------|---------------|
| 4      | 0.334         | 0.500         |
| 8      | 0.334         | 0.500         |
| 16     | 0.334         | 0.500         |
| 32     | 0.334         | 0.500         |

**Observation:** P(parent is attacker) is **constant across all fanouts** and matches the attacker fraction precisely. This is because attackers already control the highest-weight nodes that become parents.

#### Blocked Participants - 33% Attackers
| Trees \ Fanout | 4      | 8      | 16     | 32     |
|----------------|--------|--------|--------|--------|
| 4              | 908.36 | 913.82 | 863.24 | 880.50 |
| 6              | 544.08 | 550.62 | 529.35 | 542.45 |
| 8              | 328.22 | 332.61 | 320.76 | 333.36 |
| 10             | 198.82 | 202.09 | 197.20 | 208.29 |

#### Blocked Participants - 45% Attackers
| Trees \ Fanout | 4       | 8       | 16      | 32      |
|----------------|---------|---------|---------|---------|
| 4              | 1320.79 | 1319.57 | 1352.90 | 1358.51 |
| 6              | 913.47  | 917.36  | 947.75  | 953.11  |
| 8              | 635.34  | 637.65  | 660.70  | 665.74  |
| 10             | 440.23  | 441.16  | 460.95  | 462.02  |

#### P(block one) - 33% Attackers
| Trees \ Fanout | 4            | 8            | 16           | 32           |
|----------------|--------------|--------------|--------------|--------------|
| 4              | 1.36e-01     | 1.36e-01     | 1.29e-01     | 1.31e-01     |
| 6              | 8.12e-02     | 8.22e-02     | 7.90e-02     | 8.10e-02     |
| 8              | 4.90e-02     | 4.96e-02     | 4.79e-02     | 4.98e-02     |
| 10             | 2.97e-02     | 3.02e-02     | 2.94e-02     | 3.11e-02     |

#### P(block one) - 45% Attackers
| Trees \ Fanout | 4            | 8            | 16           | 32           |
|----------------|--------------|--------------|--------------|--------------|
| 4              | 2.40e-01     | 2.40e-01     | 2.46e-01     | 2.47e-01     |
| 6              | 1.66e-01     | 1.67e-01     | 1.72e-01     | 1.73e-01     |
| 8              | 1.16e-01     | 1.16e-01     | 1.20e-01     | 1.21e-01     |
| 10             | 8.00e-02     | 8.02e-02     | 8.38e-02     | 8.40e-02     |

---

### Shuffle 15% - High-Weight Attackers

#### P(parent=attacker) by Fanout
| Fanout | 33% Attackers | 45% Attackers |
|--------|---------------|---------------|
| 4      | 0.334         | 0.500         |
| 8      | 0.334         | 0.500         |
| 16     | 0.334         | 0.501         |
| 32     | 0.334         | 0.501         |

#### Blocked Participants - 33% Attackers
| Trees \ Fanout | 4      | 8      | 16     | 32     |
|----------------|--------|--------|--------|--------|
| 4              | 571.47 | 572.55 | 547.88 | 561.55 |
| 6              | 271.57 | 271.91 | 267.71 | 274.36 |
| 8              | 129.79 | 131.26 | 129.45 | 134.97 |
| 10             | 63.02  | 63.32  | 63.35  | 65.79  |

#### Blocked Participants - 45% Attackers
| Trees \ Fanout | 4       | 8       | 16      | 32      |
|----------------|---------|---------|---------|---------|
| 4              | 963.89  | 963.85  | 993.55  | 1007.64 |
| 6              | 564.60  | 563.79  | 591.08  | 600.10  |
| 8              | 335.84  | 333.46  | 355.77  | 361.25  |
| 10             | 197.50  | 197.25  | 212.65  | 219.17  |

#### P(block one) - 33% Attackers
| Trees \ Fanout | 4            | 8            | 16           | 32           |
|----------------|--------------|--------------|--------------|--------------|
| 4              | 8.53e-02     | 8.55e-02     | 8.18e-02     | 8.38e-02     |
| 6              | 4.05e-02     | 4.06e-02     | 4.00e-02     | 4.09e-02     |
| 8              | 1.94e-02     | 1.96e-02     | 1.93e-02     | 2.01e-02     |
| 10             | 9.41e-03     | 9.45e-03     | 9.46e-03     | 9.82e-03     |

#### P(block one) - 45% Attackers
| Trees \ Fanout | 4            | 8            | 16           | 32           |
|----------------|--------------|--------------|--------------|--------------|
| 4              | 1.75e-01     | 1.75e-01     | 1.81e-01     | 1.83e-01     |
| 6              | 1.03e-01     | 1.03e-01     | 1.07e-01     | 1.09e-01     |
| 8              | 6.11e-02     | 6.06e-02     | 6.47e-02     | 6.57e-02     |
| 10             | 3.59e-02     | 3.59e-02     | 3.87e-02     | 3.98e-02     |

---

### Shuffle 20% - High-Weight Attackers

#### P(parent=attacker) by Fanout
| Fanout | 33% Attackers | 45% Attackers |
|--------|---------------|---------------|
| 4      | 0.333         | 0.500         |
| 8      | 0.334         | 0.500         |
| 16     | 0.334         | 0.500         |
| 32     | 0.334         | 0.501         |

#### Blocked Participants - 33% Attackers
| Trees \ Fanout | 4      | 8      | 16     | 32     |
|----------------|--------|--------|--------|--------|
| 4              | 371.46 | 376.10 | 364.25 | 380.61 |
| 6              | 142.54 | 144.83 | 143.26 | 152.14 |
| 8              | 55.23  | 56.00  | 57.35  | 61.80  |
| 10             | 21.30  | 21.43  | 22.88  | 24.55  |

#### Blocked Participants - 45% Attackers
| Trees \ Fanout | 4      | 8      | 16     | 32     |
|----------------|--------|--------|--------|--------|
| 4              | 735.99 | 735.93 | 764.38 | 772.22 |
| 6              | 368.84 | 369.83 | 392.59 | 399.36 |
| 8              | 189.18 | 190.18 | 206.15 | 212.25 |
| 10             | 97.57  | 96.93  | 108.47 | 110.64 |

#### P(block one) - 33% Attackers
| Trees \ Fanout | 4            | 8            | 16           | 32           |
|----------------|--------------|--------------|--------------|--------------|
| 4              | 5.54e-02     | 5.61e-02     | 5.44e-02     | 5.68e-02     |
| 6              | 2.13e-02     | 2.16e-02     | 2.14e-02     | 2.27e-02     |
| 8              | 8.24e-03     | 8.36e-03     | 8.56e-03     | 9.22e-03     |
| 10             | 3.18e-03     | 3.20e-03     | 3.41e-03     | 3.66e-03     |

#### P(block one) - 45% Attackers
| Trees \ Fanout | 4            | 8            | 16           | 32           |
|----------------|--------------|--------------|--------------|--------------|
| 4              | 1.34e-01     | 1.34e-01     | 1.39e-01     | 1.40e-01     |
| 6              | 6.71e-02     | 6.72e-02     | 7.14e-02     | 7.26e-02     |
| 8              | 3.44e-02     | 3.46e-02     | 3.75e-02     | 3.86e-02     |
| 10             | 1.77e-02     | 1.76e-02     | 1.97e-02     | 2.01e-02     |

---

### Shuffle 25% - High-Weight Attackers

#### P(parent=attacker) by Fanout
| Fanout | 33% Attackers | 45% Attackers |
|--------|---------------|---------------|
| 4      | 0.334         | 0.500         |
| 8      | 0.333         | 0.500         |
| 16     | 0.334         | 0.501         |
| 32     | 0.334         | 0.500         |

#### Blocked Participants - 33% Attackers
| Trees \ Fanout | 4      | 8      | 16     | 32     |
|----------------|--------|--------|--------|--------|
| 4              | 256.66 | 259.16 | 246.25 | 258.14 |
| 6              | 77.17  | 79.43  | 78.56  | 82.53  |
| 8              | 24.30  | 24.63  | 25.32  | 27.13  |
| 10             | 7.68   | 7.88   | 8.35   | 9.40   |

#### Blocked Participants - 45% Attackers
| Trees \ Fanout | 4      | 8      | 16     | 32     |
|----------------|--------|--------|--------|--------|
| 4              | 584.50 | 587.36 | 610.61 | 610.56 |
| 6              | 250.25 | 255.45 | 267.31 | 274.69 |
| 8              | 113.07 | 113.69 | 123.00 | 126.02 |
| 10             | 50.99  | 51.21  | 55.70  | 57.04  |

#### P(block one) - 33% Attackers
| Trees \ Fanout | 4            | 8            | 16           | 32           |
|----------------|--------------|--------------|--------------|--------------|
| 4              | 3.83e-02     | 3.87e-02     | 3.68e-02     | 3.85e-02     |
| 6              | 1.15e-02     | 1.19e-02     | 1.17e-02     | 1.23e-02     |
| 8              | 3.63e-03     | 3.68e-03     | 3.78e-03     | 4.05e-03     |
| 10             | 1.15e-03     | 1.18e-03     | 1.25e-03     | 1.40e-03     |

#### P(block one) - 45% Attackers
| Trees \ Fanout | 4            | 8            | 16           | 32           |
|----------------|--------------|--------------|--------------|--------------|
| 4              | 1.06e-01     | 1.07e-01     | 1.11e-01     | 1.11e-01     |
| 6              | 4.55e-02     | 4.64e-02     | 4.86e-02     | 4.99e-02     |
| 8              | 2.06e-02     | 2.07e-02     | 2.24e-02     | 2.29e-02     |
| 10             | 9.27e-03     | 9.31e-03     | 1.01e-02     | 1.04e-02     |

---

### Shuffle 30% - High-Weight Attackers

#### P(parent=attacker) by Fanout
| Fanout | 33% Attackers | 45% Attackers |
|--------|---------------|---------------|
| 4      | 0.334         | 0.500         |
| 8      | 0.334         | 0.500         |
| 16     | 0.335         | 0.500         |
| 32     | 0.334         | 0.500         |

#### Blocked Participants - 33% Attackers
| Trees \ Fanout | 4      | 8      | 16     | 32     |
|----------------|--------|--------|--------|--------|
| 4              | 189.93 | 189.23 | 186.47 | 190.75 |
| 6              | 46.49  | 47.20  | 46.05  | 50.17  |
| 8              | 12.16  | 12.15  | 12.50  | 14.58  |
| 10             | 3.35   | 3.24   | 3.31   | 3.78   |

#### Blocked Participants - 45% Attackers
| Trees \ Fanout | 4      | 8      | 16     | 32     |
|----------------|--------|--------|--------|--------|
| 4              | 496.21 | 492.22 | 506.58 | 513.27 |
| 6              | 187.70 | 185.53 | 195.75 | 200.91 |
| 8              | 73.92  | 72.73  | 79.51  | 82.86  |
| 10             | 29.21  | 29.59  | 32.22  | 34.57  |

#### P(block one) - 33% Attackers
| Trees \ Fanout | 4            | 8            | 16           | 32           |
|----------------|--------------|--------------|--------------|--------------|
| 4              | 2.83e-02     | 2.82e-02     | 2.78e-02     | 2.85e-02     |
| 6              | 6.94e-03     | 7.04e-03     | 6.87e-03     | 7.49e-03     |
| 8              | 1.81e-03     | 1.81e-03     | 1.87e-03     | 2.18e-03     |
| 10             | 5.00e-04     | 4.84e-04     | 4.94e-04     | 5.64e-04     |

#### P(block one) - 45% Attackers
| Trees \ Fanout | 4            | 8            | 16           | 32           |
|----------------|--------------|--------------|--------------|--------------|
| 4              | 9.02e-02     | 8.95e-02     | 9.21e-02     | 9.33e-02     |
| 6              | 3.41e-02     | 3.37e-02     | 3.56e-02     | 3.65e-02     |
| 8              | 1.34e-02     | 1.32e-02     | 1.45e-02     | 1.51e-02     |
| 10             | 5.31e-03     | 5.38e-03     | 5.86e-03     | 6.29e-03     |

---

## Key Insights

### Uniform Distribution (Low-Weight Attackers)

1. **Higher shuffle % = better security**: Increasing shuffle from 10% to 30% reduces blocked participants by ~77x for 33% attackers.

2. **Higher fanout reduces P(parent=attacker)** because fewer nodes are parents (only top positions), and uniform attackers are spread across all weight levels.

3. **45% attackers with high fanout is surprisingly secure** because parents come from the top ~3% of nodes, and uniform attackers have low density there.

4. **Recommended production config:** 30% shuffle, 8-10 trees, fanout 16-32 provides excellent security against uniform attacks.

### High-Weight Distribution (Worst-Case Attackers)

1. **Fanout has NO impact** on P(parent is attacker) because attackers already control parent positions. P(parent is attacker) remains constant at ~0.33 for 33% attackers and ~0.50 for 45% attackers across all fanout values.

2. **Shuffle percentage has minimal effect** against high-weight attackers: Even increasing shuffle from 10% to 30% only reduces P(parent is attacker) slightly. The attackers maintain control over high-weight nodes regardless of shuffle (P stays at ~0.50 for 45% attackers).

3. **Blocking is severe with high-weight attackers**: At 10% shuffle with 45% attackers and 4 trees, ~1360 participants are blocked (24.7% of honest nodes). Even with 30% shuffle, ~496 participants remain blocked (9.0% of honest nodes).

4. **More trees are essential** against high-weight attackers: Increasing from 4 to 10 trees reduces blocking from ~1360 to ~462 (66% reduction) at 10% shuffle, and from ~496 to ~29 (94% reduction) at 30% shuffle.
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
