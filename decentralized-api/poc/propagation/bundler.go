package propagation

import (
	"fmt"
	"time"

	"decentralized-api/logging"
	"decentralized-api/poc/artifacts"

	"github.com/productscience/inference/x/inference/types"
)

type Bundler struct {
	store  *artifacts.ArtifactStore
	trees  []*Tree
	sender Sender
	myAddr string
}

func NewBundler(store *artifacts.ArtifactStore, trees []*Tree, sender Sender, myAddr string) *Bundler {
	return &Bundler{
		store:  store,
		trees:  trees,
		sender: sender,
		myAddr: myAddr,
	}
}

func (b *Bundler) Publish(pocHeight int64, blockHash []byte, participant string, privKey []byte) error {
	count := b.store.Count()
	rootHash := b.store.GetRoot()
	
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
	for _, tree := range b.trees {
		node := tree.GetNode(b.myAddr)
		if node == nil {
			continue
		}
		for _, child := range node.Children {
			if err := b.sender.SendHeader(tree.Index, child.Address, h); err != nil {
				logging.Debug("Bundler: failed to send header to child", types.PoC,
					"tree", tree.Index, "child", child.Address, "error", err)
			}
		}
	}
	return nil
}

func (b *Bundler) SetTrees(trees []*Tree) {
	b.trees = trees
}
