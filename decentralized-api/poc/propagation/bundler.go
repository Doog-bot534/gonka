package propagation

import (
	"fmt"
	"sync"
	"time"

	"decentralized-api/logging"
	"decentralized-api/poc/artifacts"

	"github.com/productscience/inference/x/inference/types"
)

type Bundler struct {
	trees  []*Tree
	sender Sender
	myAddr string
}

func NewBundler(trees []*Tree, sender Sender, myAddr string) *Bundler {
	return &Bundler{
		trees:  trees,
		sender: sender,
		myAddr: myAddr,
	}
}

func (b *Bundler) Publish(store *artifacts.ArtifactStore, pocHeight int64, blockHash []byte, participant string, privKey []byte) error {
	if store == nil {
		logging.Debug("Bundler: store not available", types.PoC,
			"pocHeight", pocHeight, "participant", participant)
		return nil
	}

	count := store.Count()
	rootHash := store.GetRoot()

	if count == 0 || rootHash == nil {
		logging.Debug("Bundler: no artifacts to publish", types.PoC,
			"pocHeight", pocHeight, "participant", participant)
		return nil
	}

	bundleID := MakeBundleID(participant, pocHeight, rootHash, count, 1)
	header := BundleHeader{
		BundleID:     bundleID,
		Participant:  participant,
		PocHeight:    pocHeight,
		PocBlockHash: blockHash,
		RootHash:     rootHash,
		Count:        count,
		Version:      1,
		CreatedAt:    time.Now().Unix(),
	}

	sig, err := SignHeader(header, privKey)
	if err != nil {
		return fmt.Errorf("sign header: %w", err)
	}
	header.Signature = sig

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
	var wg sync.WaitGroup
	totalRecipients := 0
	sent := make(map[string]bool)

	logging.Info("Bundler: checking trees for recipients", types.PoC,
		"publisher", b.myAddr, "totalTrees", len(b.trees),
		"bundleID", fmt.Sprintf("%x", h.BundleID[:8]))

	for _, tree := range b.trees {
		node := tree.GetNode(b.myAddr)
		if node == nil {
			logging.Debug("Bundler: not in tree", types.PoC,
				"publisher", b.myAddr, "tree", tree.Index)
			continue
		}

		recipients := make([]*Node, 0)

		// Send to children (downward)
		recipients = append(recipients, node.Children...)

		// Send to parent (upward)
		if node.Parent != nil {
			recipients = append(recipients, node.Parent)
		}

		// Send to siblings (sideways)
		recipients = append(recipients, node.Siblings...)

		if len(recipients) > 0 {
			logging.Info("Bundler: sending in tree", types.PoC,
				"publisher", b.myAddr, "tree", tree.Index,
				"children", len(node.Children), "parent", node.Parent != nil,
				"siblings", len(node.Siblings), "total", len(recipients))
		}

		for _, recipient := range recipients {
			if sent[recipient.Address] {
				continue
			}
			sent[recipient.Address] = true
			totalRecipients++

			wg.Add(1)
			go func(treeIndex int, recipientAddr string) {
				defer wg.Done()
				if err := b.sender.SendHeader(treeIndex, recipientAddr, h); err != nil {
					logging.Warn("Bundler: failed to send header", types.PoC,
						"publisher", b.myAddr, "tree", treeIndex, "to", recipientAddr, "error", err)
				} else {
					logging.Debug("Bundler: sent", types.PoC,
						"publisher", b.myAddr, "tree", treeIndex, "to", recipientAddr)
				}
			}(tree.Index, recipient.Address)
		}
	}

	wg.Wait()

	logging.Info("Bundler: sent header to all recipients", types.PoC,
		"publisher", b.myAddr, "totalRecipients", totalRecipients,
		"trees", len(b.trees), "bundleID", fmt.Sprintf("%x", h.BundleID[:8]))

	return nil
}

func (b *Bundler) SetTrees(trees []*Tree) {
	b.trees = trees
}
