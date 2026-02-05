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

## Key Formula

```
P(block one participant) = P(parent is attacker)^numTrees
P(any of n blocked) ≈ n × P(block one)
```


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

### Uniform Attacker Selection in Simulation

```go
step := numParticipants / numAttackers  // integer division
for i := 0; i < numAttackers; i++ {
    idx := (i * step) % numParticipants
    attackers[formatAddress(idx)] = true
}
```

**Example: Fanout 32, n=10,000, 45% attackers**

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
   - P(parent is attacker) = 27/313 = **0.085**

**Example: Fanout 4, n=10,000, 33% attackers**

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

### Key Insight

The weighted shuffle places high-weight nodes as parents. With uniform attacker selection by stepping through indices, **high-index nodes have fewer or zero attackers**, making P(parent is attacker) lower than the overall attacker fraction.

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

## Key Insights

1. **Higher shuffle % = better security**: Increasing shuffle from 10% to 30% reduces blocked participants by ~77x for 33% attackers.

2. **Higher fanout reduces P(parent=attacker)** because fewer nodes are parents (only top positions), and uniform attackers are spread across all weight levels.

3. **45% attackers with high fanout is surprisingly secure** because parents come from the top ~3% of nodes, and uniform attackers have low density there.

4. **Recommended production config:** 30% shuffle, 8-10 trees, fanout 16-32 provides excellent security against uniform attacks.

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
