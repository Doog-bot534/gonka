# Multi-Tree Propagation for Off-Chain PoC Artifacts

## Overview

Multi-tree propagation is a gossip protocol that uses multiple independent propagation trees to distribute off-chain PoC artifact metadata (bundle headers) between participants. By using multiple trees with different shuffled orderings, the protocol ensures that even with 33-49% of the network compromised, individual participants cannot be selectively censored.

**Goal**: Provide censorship-resistant propagation of PoC artifact bundle headers to enable validators to locate and fetch artifacts from participant APIs.

---

## Problem Statement

In a single propagation tree, if an attacker controls your parent node, they can drop messages meant for you. With 33% of the network compromised, there's a 33% chance any specific victim gets blocked.

Multiple independent trees reduce this probability exponentially: with T trees and attacker controlling fraction p of the network, the probability of blocking a specific victim is p^T.

**Attack Success Rates:**

| Attacker % | 1 Tree | 4 Trees | 8 Trees | 12 Trees |
|------------|--------|---------|---------|----------|
| 33%        | 33%    | 1.2%    | 0.015%  | 0.00002% |
| 40%        | 40%    | 2.6%    | 0.065%  | 0.00017% |
| 49%        | 49%    | 5.8%    | 0.33%   | 0.019%   |

Formula: p^T where p = attacker fraction, T = number of trees

---

## Architecture

### Single Tree Structure

A tree with fanout F:

```
                         [Root]
                            |
            +-------+-------+-------+-------+
            |       |       |       |       |
           [A]     [B]     [C]     [D]
            |       |       |       |
         +--+--+ +--+--+ +--+--+ +--+--+
         |  |  | |  |  | |  |  | |  |  |
        [E][F][G][H][I][J][K][L][M][N][O][P]
```

- **Layer 0**: 1 node (root)
- **Layer 1**: F nodes (fanout)
- **Layer 2**: F² nodes
- Each node has 1 parent and up to F children

### Multiple Independent Trees

Use T trees, each with a different deterministic shuffle:

```
Tree 0: seed = SHA256(block_hash || 0)
Tree 1: seed = SHA256(block_hash || 1)
Tree 2: seed = SHA256(block_hash || 2)
...
```

Same participants, different positions in each tree:

```
TREE 0              TREE 1              TREE 2              TREE 3

    [A]                 [D]                 [B]                 [C]
     |                   |                   |                   |
  +--+--+             +--+--+             +--+--+             +--+--+
  |     |             |     |             |     |             |     |
 [B]   [C]           [A]   [B]           [D]   [A]           [A]   [D]
  |                   |                         |                   |
 [X]                 [X]                       [X]                 [X]

X's parent: B        X's parent: A        X's parent: A        X's parent: D
```

Node X has a **different parent** in each tree. To block victim X, attacker must control X's parent in **ALL** trees.

---

## Deterministic Tree Construction

Every node computes the same tree structure independently based on:
- **Participant list**: All participants committed to PoC generation (from chain state)
- **Block hash**: Deterministic source of randomness for the PoC stage
- **Tree index**: 0, 1, 2, ..., T-1

### Tree Building Algorithm

```go
func BuildTrees(participants []string, blockHash []byte, numTrees int, fanout int) []*Tree {
    trees := make([]*Tree, numTrees)
    for i := 0; i < numTrees; i++ {
        seed := sha256(append(blockHash, byte(i)))
        trees[i] = &Tree{
            Index:    i,
            Shuffled: deterministicShuffle(participants, seed),
            Fanout:   fanout,
        }
    }
    return trees
}

func deterministicShuffle(list []string, seed []byte) []string {
    result := make([]string, len(list))
    copy(result, list)
    rng := rand.New(rand.NewSource(int64(binary.BigEndian.Uint64(seed[:8]))))
    
    for i := len(result) - 1; i > 0; i-- {
        j := rng.Intn(i + 1)
        result[i], result[j] = result[j], result[i]
    }
    return result
}
```

### Node Role Calculation

Each node determines its parent and children in a tree:

```go
func (t *Tree) GetRole(myAddr string) (parent string, children []string) {
    myIndex := indexOf(t.Shuffled, myAddr)
    
    // Parent: node at position (myIndex-1)/fanout
    if myIndex == 0 {
        parent = "" // Root has no parent
    } else {
        parent = t.Shuffled[(myIndex-1)/t.Fanout]
    }
    
    // Children: fanout consecutive nodes starting at myIndex*fanout + 1
    childStart := myIndex*t.Fanout + 1
    for i := 0; i < t.Fanout && childStart+i < len(t.Shuffled); i++ {
        children = append(children, t.Shuffled[childStart+i])
    }
    
    return parent, children
}
```

---

## Message Flow

### Bundle Header Propagation

When a participant generates artifacts, they:
1. Create a bundle header containing metadata (root hash, count, signatures)
2. Broadcast the header to their children in each tree
3. Relay nodes forward headers to their children upon receipt

### Originator Broadcast

```go
func (n *Node) Broadcast(header *BundleHeader) {
    for _, tree := range n.Trees {
        _, children := tree.GetRole(n.Address)
        for _, child := range children {
            go n.SendHeader(child, header, tree.Index)
        }
    }
}
```

### Relay Node Forwarding

```go
func (n *Node) OnReceiveHeader(header *BundleHeader, treeIndex int) {
    // Deduplication
    if n.AlreadySeen(header.Hash()) {
        return
    }
    n.MarkSeen(header.Hash())
    
    // Process header (store metadata for validation phase)
    n.ProcessHeader(header)
    
    // Forward to children in this tree
    _, children := n.Trees[treeIndex].GetRole(n.Address)
    for _, child := range children {
        go n.SendHeader(child, header, treeIndex)
    }
}
```

---

## Bandwidth Analysis

### Parameters

- N = 1000 participants
- T = 8 trees
- F = 32 fanout
- H = 1 KB header size (metadata only, not full artifacts)

### Per-Message Cost

**Originator outbound**: T × F × H = 8 × 32 × 1KB = 256 KB per bundle header

**Per PoC round** (all 1000 participants broadcast):
- **Inbound**: N × H = 1000 × 1KB = 1 MB (receive all headers, deduplicated)
- **Outbound**: Varies by tree position; average node relays to ~F nodes per tree

### Topology Comparison

| Topology | Connections | Outbound per header | Censorship resistance |
|----------|-------------|---------------------|----------------------|
| Full mesh | 999 | 999 KB | 33-49% (single point) |
| Single tree (F=32) | 33 | 32 KB | 33-49% (single parent) |
| 8 trees (F=32) | 264 | 256 KB | 0.015-0.33% |

Multi-tree uses more bandwidth than single tree but provides **exponentially better attack resistance**.

---

## Connection Management

Each node maintains:

```
Per tree:
- 1 inbound connection (from parent)
- F outbound connections (to children)

Total: T × (F + 1) = 8 × 33 = 264 connections
```

Connection establishment at PoC generation stage start:

```go
func (n *Node) SetupConnections(trees []*Tree) {
    for _, tree := range trees {
        parent, children := tree.GetRole(n.Address)
        
        // Expect inbound from parent
        if parent != "" {
            n.ExpectInbound(parent, tree.Index)
        }
        
        // Establish outbound to children
        for _, child := range children {
            n.EstablishOutbound(child, tree.Index)
        }
    }
}
```

---

## Security Properties

### Censorship Resistance

**Threat Model**: Byzantine attacker controlling p fraction of participants attempts to prevent specific victim from receiving bundle headers.

**Defense**: Victim receives header if at least one parent across all trees is honest.

**Probability of successful censorship**: p^T

With T=8 trees and p=33%, attack succeeds only 0.015% of the time.

### Deduplication

Each node maintains a seen-header cache (keyed by bundle_id) to:
- Prevent processing duplicate headers
- Avoid infinite relay loops
- Reduce bandwidth waste

### Message Authentication

Bundle headers contain:
- Participant address (creator)
- Signature over header contents
- PoC stage block height and hash

Receivers verify:
- Signature matches participant's public key
- PoC stage parameters match current epoch
- Root hash and count are consistent with on-chain commit

---

## What This Solves

Multi-tree propagation ensures that:
1. **No single point of censorship**: Attacker must control victim's parent in ALL trees
2. **Deterministic and verifiable**: All nodes compute same tree structure
3. **Scalable**: Connection count grows as T × F, not N
4. **Practical bandwidth**: Headers are small metadata (~1KB), not full artifacts

With 8 trees and 49% Byzantine attacker, censorship succeeds only **0.33%** of the time against any specific target.

---

## Implementation Phases

- **Phase 1**: Core tree construction, HTTP transport, basic bundler/receiver (WIP)

---

## Related Documents

- `offchain.md` — Off-chain PoC artifacts architecture
- `offchain-phase2.md` — Proof API for artifact retrieval
- `offchain-phase3.md` — On-chain commit messages
- `multi-tree-propagation-phase1.md` — Phase 1 implementation details
