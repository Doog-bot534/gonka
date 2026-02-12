# Hypercube Propagation Topology Design

## Current System Analysis

Your current system uses independent k-ary trees with weighted shuffle. The key vulnerability:

**Single point of failure per path**: In a tree, each node has exactly 1 parent. If that parent is an attacker, the entire subtree is cut off in that tree. A node is blocked only if its parent is an attacker in all trees simultaneously:

```
P(blocked) = ∏ᵢ P(parent is attacker in tree i)
```

With independent trees and attacker fraction α: `P(blocked) ≈ α^numTrees`

But trees have structural weaknesses:

- **Depth amplifies blocking** — nodes deep in the tree have more ancestors that can block them
- **Root is a single point of failure** per tree (mitigated by multiple trees)
- **No lateral paths** — if your parent blocks you, there's no alternative path within that tree
- **Weight correlation across trees** — weighted shuffle means the same high-weight attackers tend to be parents in ALL trees, breaking independence

## Hypercube Topology Design

### Core Idea

A d-dimensional hypercube on N = 2^d nodes gives each node exactly d neighbors, each differing in exactly one bit position. The critical property: between any two nodes there are d node-disjoint paths (Menger's theorem). An attacker must control at least one node on every one of those d paths to block a participant.

For 10,000 participants we need `d = ⌈log₂(10000)⌉ = 14` dimensions → `2^14 = 16,384` virtual positions (we map real participants into these slots).

### Why Hypercube Beats Trees

| Property | k-ary Trees (current) | Hypercube |
|----------|----------------------|-----------|
| Paths to root | 1 per tree, numTrees total | d node-disjoint paths inherent |
| Blocking requirement | Parent in ALL trees must be attacker | d independent neighbors must ALL be attackers |
| P(blocked) theoretical | α^numTrees | α^d (but with stronger independence) |
| Lateral redundancy | None within a tree | Any neighbor can relay |
| Depth sensitivity | Deep nodes more vulnerable | All nodes have exactly d neighbors |
| Weight correlation | Same weighted shuffle → correlated parents | Different dimension → different neighbor selection |

With d=14: `P(blocked) = α^14`

- **33% attackers**: `0.33^14 ≈ 5.5 × 10⁻⁷` (vs `0.33^10 ≈ 1.5 × 10⁻⁵` with 10 trees)
- **45% attackers**: `0.45^14 ≈ 1.2 × 10⁻⁵` (vs `0.45^10 ≈ 3.4 × 10⁻⁴` with 10 trees)

### Architecture

```
┌─────────────────────────────────────────────────┐
│                Hypercube Overlay                 │
│                                                  │
│  Node 0000 ──dim0── Node 0001                   │
│    │                   │                         │
│   dim1                dim1                       │
│    │                   │                         │
│  Node 0010 ──dim0── Node 0011                   │
│    │                   │                         │
│   dim2                dim2                       │
│    ...                 ...                       │
│                                                  │
│  Each node has d=14 neighbors                    │
│  Each dimension uses INDEPENDENT weighted shuffle│
│  Message floods through all dimensions           │
└─────────────────────────────────────────────────┘
```

**Propagation flow for a message from sender S:**

1. S sends to all its d neighbors
2. Each honest neighbor forwards to ITS neighbors (except the one it received from)
3. BFS/epidemic spread across the hypercube
4. Attacker nodes don't relay → but d-1 other paths exist

### Weighted Dimension Shuffling

The key innovation: each dimension uses an independently seeded weighted shuffle to assign which participant maps to which hypercube position. This ensures:

- Your neighbor in dimension 0 is a different random participant than your neighbor in dimension 1
- Weight still matters — high-weight nodes get "better" positions (more central)
- Independence across dimensions breaks the correlation that weakens multi-tree

**Dimension seed**: `SHA256(blockHash || "hypercube" || dimensionIndex)`

For dimension d:

1. Weighted shuffle all participants using dimension seed
2. Assign `shuffled[i]` to hypercube position `i`
3. Neighbor in dim d = `position XOR (1 << d)`

### Proposed Implementation

Here's the concrete structure I'll implement in the propagation package:

```go
type HypercubeNode struct {
    Address    string
    Position   uint16          // hypercube position (0..2^d-1)
    Neighbors  []*HypercubeNode // one per dimension
    Dimensions int             // d
}

type Hypercube struct {
    Dimensions int
    Size       int             // 2^d
    Nodes      map[string]*HypercubeNode
    Positions  []*HypercubeNode // indexed by position
}
```

### Propagation Protocol

```
SEND(message, sender):
    for each dimension d:
        neighbor = myNeighbors[d]
        send(message, neighbor, fromDimension=d)

RECEIVE(message, fromDimension):
    if already_seen(message): return
    mark_seen(message)
    
    for each dimension d where d != fromDimension:
        neighbor = myNeighbors[d]  
        send(message, neighbor, fromDimension=d)
```

This gives epidemic-style flooding through the hypercube with `O(d × N)` total messages (vs `O(numTrees × N)` in current system, but each node sends to only d peers instead of `fanout × numTrees`).

## Blocking Probability Analysis

For a node to be completely blocked, ALL d neighbors must be attackers (since each is an independent relay path):

```
P(blocked in 1 dimension) = P(neighbor is attacker in that dimension)
```

With independent shuffles per dimension:

```
P(all d neighbors are attackers) = ∏ P(neighbor_i is attacker)
```

But it's actually better than `α^d` because the hypercube has multi-hop redundancy too. Even if your direct neighbor in dimension 3 is an attacker, you can still receive the message via a 2-hop path through dimensions 1→3 (neighbor in dim 1 forwards to their dim-3 neighbor, who connects to you). The hypercube's d! shortest paths between antipodal nodes create massive redundancy.

**Effective blocking probability** (simulation needed for exact values, but theoretical lower bound):

```
P(blocked) ≤ α^d                          (direct neighbor blocking)
P(blocked) ≈ α^d × correction_factor      (correction < 1 due to multi-hop)
```

| Topology | d/Trees | 33% Attackers P(block) | 45% Attackers P(block) |
|----------|---------|------------------------|------------------------|
| Current 10 trees, fanout 4 | 10 | 0.031 (3.1%) | 0.00043 (0.04%) |
| Current 10 trees, fanout 32 | 10 | 0.000007 | 0.000000 |
| Hypercube d=10 | 10 | ≤ 1.7×10⁻⁵ | ≤ 3.4×10⁻⁴ |
| Hypercube d=14 | 14 | ≤ 5.5×10⁻⁷ | ≤ 1.2×10⁻⁵ |

The hypercube with d=14 at fanout-equivalent cost of 14 connections per node dramatically outperforms 10 trees with fanout 4 (which requires 4×10=40 connections per parent node) while using fewer connections per node.

## Handling N ≠ 2^d

Since 10,000 ≠ 2^d, we use virtual nodes:

```go
realSize := len(participants)
d := ceilLog2(realSize)    // 14 for 10000
cubeSize := 1 << d         // 16384

// Map participants to positions via weighted shuffle
shuffled := weightedDeterministicShuffle(participants, seed)

// Positions 0..9999 → real participants
// Positions 10000..16383 → "virtual" (empty, act as pass-through)
```

Virtual nodes are treated as always honest pass-through — they just relay. In practice, messages to virtual positions are simply skipped (the sender knows the position is empty and doesn't need to contact anyone). This means some hypercube edges are "free" — they don't add attack surface.

## Connection Analysis

### Connections Per Participant

In a d-dimensional hypercube, each participant maintains exactly **d bidirectional connections** (one neighbor per dimension).

For 10,000 participants with d=14:
- **Connections per participant: 14**

### Total Network Connections

The total number of unique connections in the network:

```
Total connections = (N × d) / 2
```

Where:
- N = number of participants
- d = dimensions
- Divide by 2 because each connection is bidirectional (counted once, not twice)

For 10,000 participants with d=14:
- **Total connections: (10,000 × 14) / 2 = 70,000**

This is significantly more efficient than the tree topology where each parent node with fanout=4 in 10 trees would maintain up to 40 child connections plus 10 parent connections.

## Cost Comparison

| Metric | 10 Trees, Fanout 4 | Hypercube d=14 |
|--------|-------------------|----------------|
| Connections per node | Up to 4 children × 10 trees + 10 parents = 50 | 14 (one per dimension) |
| Total network connections | ~(10,000 × 10 × 4) / 2 ≈ 200,000 | 70,000 |
| Total messages per broadcast | ~10 × 10,000 = 100,000 | ~14 × 10,000 = 140,000 |
| Latency (hops) | log₄(10000) ≈ 7 | 14 (diameter of hypercube) |
| Fault tolerance | Depends on tree independence | d node-disjoint paths guaranteed |

The hypercube has slightly higher latency (14 hops vs ~7 for fanout-4 tree) but far fewer connections per node and provably stronger fault tolerance.