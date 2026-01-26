package propagation

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"time"

	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

type Receiver struct {
	cache    *Cache
	trees    []*Tree
	verifier PubKeyProvider
	myAddr   string
	sender   Sender

	mu             sync.RWMutex
	pendingHeaders map[[32]byte]*BundleHeader
	lastHeaderTime map[[32]byte]time.Time
}

type PubKeyProvider interface {
	GetPubKey(participantAddr string) (string, error)
}

func NewReceiver(cache *Cache, trees []*Tree, verifier PubKeyProvider, myAddr string, sender Sender) *Receiver {
	return &Receiver{
		cache:          cache,
		trees:          trees,
		verifier:       verifier,
		myAddr:         myAddr,
		sender:         sender,
		pendingHeaders: make(map[[32]byte]*BundleHeader),
		lastHeaderTime: make(map[[32]byte]time.Time),
	}
}

func (r *Receiver) OnHeader(h BundleHeader, treeIdx int) error {
	logging.Debug("Receiver: received header", types.PoC,
		"participant", h.Participant, "pocHeight", h.PocHeight,
		"count", h.Count, "bundleID", fmt.Sprintf("%x", h.BundleID[:8]),
		"tree", treeIdx)

	r.mu.RLock()
	existing := r.pendingHeaders[h.BundleID]
	r.mu.RUnlock()

	if existing != nil {
		logging.Debug("Receiver: duplicate header ignored", types.PoC,
			"bundleID", fmt.Sprintf("%x", h.BundleID[:8]))
		return nil
	}

	pubKey, err := r.verifier.GetPubKey(h.Participant)
	if err != nil {
		logging.Warn("Receiver: failed to get public key", types.PoC,
			"participant", h.Participant, "error", err)
		return fmt.Errorf("get pubkey: %w", err)
	}

	if err := VerifyHeader(h, pubKey); err != nil {
		logging.Warn("Receiver: header signature verification failed", types.PoC,
			"participant", h.Participant, "error", err)
		return fmt.Errorf("verify header: %w", err)
	}

	expectedID := MakeBundleID(h.Participant, h.PocHeight, h.RootHash, h.Count, h.Version)
	if !bytes.Equal(expectedID[:], h.BundleID[:]) {
		logging.Warn("Receiver: bundle ID mismatch", types.PoC,
			"expected", fmt.Sprintf("%x", expectedID[:8]),
			"got", fmt.Sprintf("%x", h.BundleID[:8]))
		return fmt.Errorf("bundle ID mismatch")
	}

	if err := r.cache.StoreHeader(context.Background(), h); err != nil {
		logging.Warn("Receiver: failed to store header", types.PoC,
			"bundleID", fmt.Sprintf("%x", h.BundleID[:8]), "error", err)
		return fmt.Errorf("store header: %w", err)
	}

	r.mu.Lock()
	r.pendingHeaders[h.BundleID] = &h
	r.lastHeaderTime[h.BundleID] = time.Now()
	r.mu.Unlock()

	logging.Info("Receiver: commit metadata verified and stored", types.PoC,
		"participant", h.Participant, "pocHeight", h.PocHeight,
		"bundleID", fmt.Sprintf("%x", h.BundleID[:8]))

	r.forwardHeader(h, treeIdx)

	return nil
}

func (r *Receiver) forwardHeader(h BundleHeader, treeIdx int) {
	if treeIdx >= len(r.trees) {
		return
	}

	tree := r.trees[treeIdx]
	node := tree.GetNode(r.myAddr)
	if node == nil {
		return
	}

	for _, child := range node.Children {
		if err := r.sender.SendHeader(treeIdx, child.Address, h); err != nil {
			logging.Debug("Receiver: failed to forward header", types.PoC,
				"tree", treeIdx, "child", child.Address, "error", err)
		}
	}
}

func (r *Receiver) SetTrees(trees []*Tree) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.trees = trees
}
