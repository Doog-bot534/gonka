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

	mu               sync.RWMutex
	processedHeaders map[[32]byte]bool
	pendingHeaders   map[[32]byte]*BundleHeader
	lastHeaderTime   map[[32]byte]time.Time
}

type PubKeyProvider interface {
	GetPubKey(participantAddr string) (string, error)
}

func NewReceiver(cache *Cache, trees []*Tree, verifier PubKeyProvider, myAddr string, sender Sender) *Receiver {
	return &Receiver{
		cache:            cache,
		trees:            trees,
		verifier:         verifier,
		myAddr:           myAddr,
		sender:           sender,
		processedHeaders: make(map[[32]byte]bool),
		pendingHeaders:   make(map[[32]byte]*BundleHeader),
		lastHeaderTime:   make(map[[32]byte]time.Time),
	}
}

func (r *Receiver) OnHeader(h BundleHeader, treeIdx int, from string) error {
	logging.Info("Receiver: received header", types.PoC,
		"receiver", r.myAddr, "from", from, "publisher", h.Participant, "pocHeight", h.PocHeight,
		"count", h.Count, "bundleID", fmt.Sprintf("%x", h.BundleID[:8]),
		"tree", treeIdx)

	r.mu.RLock()
	processed := r.processedHeaders[h.BundleID]
	r.mu.RUnlock()

	if processed {
		logging.Debug("Receiver: duplicate header ignored (already processed)", types.PoC,
			"receiver", r.myAddr, "bundleID", fmt.Sprintf("%x", h.BundleID[:8]))
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

	r.mu.Lock()
	if r.processedHeaders[h.BundleID] {
		r.mu.Unlock()
		logging.Info("Receiver: duplicate header ignored (race detected)", types.PoC,
			"receiver", r.myAddr, "bundleID", fmt.Sprintf("%x", h.BundleID[:8]))
		return nil
	}

	if err := r.cache.StoreHeader(context.Background(), h); err != nil {
		r.mu.Unlock()
		logging.Warn("Receiver: failed to store header", types.PoC,
			"bundleID", fmt.Sprintf("%x", h.BundleID[:8]), "error", err)
		return fmt.Errorf("store header: %w", err)
	}

	r.processedHeaders[h.BundleID] = true
	r.pendingHeaders[h.BundleID] = &h
	r.lastHeaderTime[h.BundleID] = time.Now()
	trees := r.trees
	r.mu.Unlock()

	logging.Info("Receiver: commit metadata verified and stored", types.PoC,
		"receiver", r.myAddr, "from", from, "publisher", h.Participant, "pocHeight", h.PocHeight,
		"bundleID", fmt.Sprintf("%x", h.BundleID[:8]), "tree", treeIdx)

	r.forwardHeader(h, treeIdx, trees)

	return nil
}

func (r *Receiver) forwardHeader(h BundleHeader, treeIdx int, trees []*Tree) {
	var wg sync.WaitGroup
	totalRecipients := 0
	sent := make(map[string]bool)

	for _, tree := range trees {
		node := tree.GetNode(r.myAddr)
		if node == nil {
			continue
		}

		recipients := make([]*Node, 0)

		// Forward to children (downward)
		recipients = append(recipients, node.Children...)

		// Forward to parent (upward)
		if node.Parent != nil {
			recipients = append(recipients, node.Parent)
		}

		// Forward to siblings (sideways)
		recipients = append(recipients, node.Siblings...)

		for _, recipient := range recipients {
			if sent[recipient.Address] {
				continue
			}
			sent[recipient.Address] = true
			totalRecipients++

			wg.Add(1)
			go func(treeIndex int, recipientAddr string) {
				defer wg.Done()
				if err := r.sender.SendHeader(treeIndex, recipientAddr, h); err != nil {
					logging.Warn("Receiver: failed to forward header", types.PoC,
						"forwarder", r.myAddr, "tree", treeIndex, "to", recipientAddr, "error", err)
				} else {
					logging.Debug("Receiver: forwarded", types.PoC,
						"forwarder", r.myAddr, "tree", treeIndex, "to", recipientAddr)
				}
			}(tree.Index, recipient.Address)
		}
	}

	if totalRecipients > 0 {
		logging.Info("Receiver: forwarding across all trees", types.PoC,
			"forwarder", r.myAddr, "publisher", h.Participant,
			"totalRecipients", totalRecipients, "trees", len(trees))
	}

	wg.Wait()
}

func (r *Receiver) SetTrees(trees []*Tree) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.trees = trees
}
