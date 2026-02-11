package propagation

import (
	"context"
	"fmt"
	"sync"
	"time"

	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

type Bundler struct {
	signer HeaderSigner
	cache  *Cache
	trees  []*Tree
	mu     sync.RWMutex
	sender Sender
	myAddr string
}

func NewBundler(signer HeaderSigner, cache *Cache, trees []*Tree, sender Sender, myAddr string) *Bundler {
	return &Bundler{
		signer: signer,
		cache:  cache,
		trees:  trees,
		sender: sender,
		myAddr: myAddr,
	}
}

func (b *Bundler) Publish(pocHeight int64, participant string, pubKey string, count uint32, rootHash []byte) error {
	if count == 0 || rootHash == nil {
		logging.Debug("Bundler: no artifacts to publish", types.PoC,
			"pocHeight", pocHeight, "participant", participant)
		return nil
	}

	bundleID := MakeBundleID(participant, pocHeight, rootHash, count)
	header := BundleHeader{
		BundleID:    bundleID,
		Participant: participant,
		PubKey:      pubKey,
		PocHeight:   pocHeight,
		RootHash:    rootHash,
		Count:       count,
		CreatedAt:   time.Now().Unix(),
	}

	sig, err := SignHeaderWith(header, b.signer)
	if err != nil {
		return fmt.Errorf("sign header: %w", err)
	}
	header.Signature = sig

	if b.cache != nil {
		if err := b.cache.StoreHeader(context.Background(), header); err != nil {
			logging.Warn("Bundler: failed to cache own header", types.PoC, "error", err)
		}
	}

	logging.Info("Bundler: publishing commit metadata", types.PoC,
		"pocHeight", pocHeight, "participant", participant,
		"count", count, "bundleID", fmt.Sprintf("%x", bundleID[:8]))

	if err := b.sendHeader(header); err != nil {
		logging.Warn("Bundler: failed to send header", types.PoC, "error", err)
		return fmt.Errorf("send header: %w", err)
	}

	logging.Info("Bundler: publish complete", types.PoC,
		"pocHeight", pocHeight, "participant", participant)

	return nil
}

func (b *Bundler) sendHeader(h BundleHeader) error {
	b.mu.RLock()
	trees := b.trees
	b.mu.RUnlock()

	var wg sync.WaitGroup
	totalRecipients := 0

	logging.Info("Bundler: checking trees for recipients", types.PoC,
		"publisher", b.myAddr, "totalTrees", len(b.trees),
		"bundleID", fmt.Sprintf("%x", h.BundleID[:8]))

	// Publisher sends to all roots across all trees
	// Each tree root must receive with its own tree index for proper propagation
	for _, tree := range trees {
		if tree.Root == nil {
			continue
		}

		// Skip if we're already the root (will broadcast via receiver)
		if tree.Root.Address == b.myAddr {
			continue
		}

		totalRecipients++

		wg.Add(1)
		go func(treeIndex int, rootAddr string) {
			defer wg.Done()
			if err := b.sender.SendHeader(treeIndex, rootAddr, h); err != nil {
				logging.Warn("Bundler: failed to send to root", types.PoC,
					"publisher", b.myAddr, "tree", treeIndex, "root", rootAddr, "error", err)
			} else {
				logging.Debug("Bundler: sent to root", types.PoC,
					"publisher", b.myAddr, "tree", treeIndex, "root", rootAddr)
			}
		}(tree.Index, tree.Root.Address)
	}

	// If we're root in any tree, also broadcast to our children in those trees
	// Each child must receive with the correct tree index for proper propagation
	for _, tree := range trees {
		node := tree.GetNode(b.myAddr)
		if node == nil || node.Parent != nil {
			continue // Not root in this tree
		}

		// We're root - broadcast to children
		if len(node.Children) > 0 {
			logging.Info("Bundler: broadcasting as root", types.PoC,
				"publisher", b.myAddr, "tree", tree.Index,
				"children", len(node.Children))
		}

		for _, child := range node.Children {
			totalRecipients++

			wg.Add(1)
			go func(treeIndex int, childAddr string) {
				defer wg.Done()
				if err := b.sender.SendHeader(treeIndex, childAddr, h); err != nil {
					logging.Warn("Bundler: failed to send to child", types.PoC,
						"publisher", b.myAddr, "tree", treeIndex, "child", childAddr, "error", err)
				} else {
					logging.Debug("Bundler: sent to child", types.PoC,
						"publisher", b.myAddr, "tree", treeIndex, "child", childAddr)
				}
			}(tree.Index, child.Address)
		}
	}

	wg.Wait()

	logging.Info("Bundler: sent header to all recipients", types.PoC,
		"publisher", b.myAddr, "totalRecipients", totalRecipients,
		"trees", len(b.trees), "bundleID", fmt.Sprintf("%x", h.BundleID[:8]))

	return nil
}

func (b *Bundler) PublishProofs(bundleID [32]byte, proofs []ProofItem) error {
	if len(proofs) == 0 {
		logging.Debug("Bundler: no proofs to publish", types.PoC,
			"bundleID", fmt.Sprintf("%x", bundleID[:8]))
		return nil
	}

	if b.cache != nil {
		if err := b.cache.StoreProofs(context.Background(), bundleID, proofs); err != nil {
			logging.Warn("Bundler: failed to cache own proofs", types.PoC, "error", err)
		}
	}

	logging.Info("Bundler: publishing proofs", types.PoC,
		"bundleID", fmt.Sprintf("%x", bundleID[:8]), "proofCount", len(proofs))

	if err := b.sendProofs(bundleID, proofs); err != nil {
		logging.Warn("Bundler: failed to send proofs", types.PoC, "error", err)
		return fmt.Errorf("send proofs: %w", err)
	}

	logging.Info("Bundler: proof publish complete", types.PoC,
		"bundleID", fmt.Sprintf("%x", bundleID[:8]))

	return nil
}

func (b *Bundler) sendProofs(bundleID [32]byte, proofs []ProofItem) error {
	b.mu.RLock()
	trees := b.trees
	b.mu.RUnlock()

	var wg sync.WaitGroup
	totalRecipients := 0

	logging.Info("Bundler: checking trees for proof recipients", types.PoC,
		"publisher", b.myAddr, "totalTrees", len(b.trees),
		"bundleID", fmt.Sprintf("%x", bundleID[:8]))

	proofSender, ok := b.sender.(interface {
		SendProofs(to string, bundleID [32]byte, proofs []ProofItem) error
	})
	if !ok {
		return fmt.Errorf("sender does not support SendProofs")
	}

	for _, tree := range trees {
		if tree.Root == nil {
			continue
		}

		if tree.Root.Address == b.myAddr {
			continue
		}

		totalRecipients++

		wg.Add(1)
		go func(rootAddr string) {
			defer wg.Done()
			if err := proofSender.SendProofs(rootAddr, bundleID, proofs); err != nil {
				logging.Warn("Bundler: failed to send proofs to root", types.PoC,
					"publisher", b.myAddr, "root", rootAddr, "error", err)
			} else {
				logging.Debug("Bundler: sent proofs to root", types.PoC,
					"publisher", b.myAddr, "root", rootAddr)
			}
		}(tree.Root.Address)
	}

	for _, tree := range trees {
		node := tree.GetNode(b.myAddr)
		if node == nil || node.Parent != nil {
			continue
		}

		if len(node.Children) > 0 {
			logging.Info("Bundler: broadcasting proofs as root", types.PoC,
				"publisher", b.myAddr, "tree", tree.Index,
				"children", len(node.Children))
		}

		for _, child := range node.Children {
			totalRecipients++

			wg.Add(1)
			go func(childAddr string) {
				defer wg.Done()
				if err := proofSender.SendProofs(childAddr, bundleID, proofs); err != nil {
					logging.Warn("Bundler: failed to send proofs to child", types.PoC,
						"publisher", b.myAddr, "child", childAddr, "error", err)
				} else {
					logging.Debug("Bundler: sent proofs to child", types.PoC,
						"publisher", b.myAddr, "child", childAddr)
				}
			}(child.Address)
		}
	}

	wg.Wait()

	logging.Info("Bundler: sent proofs to all recipients", types.PoC,
		"publisher", b.myAddr, "totalRecipients", totalRecipients,
		"trees", len(b.trees), "bundleID", fmt.Sprintf("%x", bundleID[:8]))

	return nil
}

func (b *Bundler) StoreOwnArrival(pocHeight int64, participant string, count uint32) error {
	if b.cache == nil {
		return fmt.Errorf("cache not available")
	}
	return b.cache.StoreFirstArrival(participant, pocHeight, time.Now().UnixMilli(), count)
}

func (b *Bundler) SetTrees(trees []*Tree) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.trees = trees
}

