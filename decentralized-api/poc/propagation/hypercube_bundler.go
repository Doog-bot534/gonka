package propagation

import (
	"context"
	"fmt"
	"sync"
	"time"

	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

type HypercubeSender interface {
	SendHeaderHypercube(to string, h BundleHeader) error
	SendProofsHypercube(to string, bundleID [32]byte, proofs []ProofItem) error
}

type HypercubeBundler struct {
	signer      HeaderSigner
	cache       *Cache
	hypercubes  []*Hypercube
	mu          sync.RWMutex
	sender      HypercubeSender
	myAddr      string
}

func NewHypercubeBundler(signer HeaderSigner, cache *Cache, hypercubes []*Hypercube, sender HypercubeSender, myAddr string) *HypercubeBundler {
	return &HypercubeBundler{
		signer:     signer,
		cache:      cache,
		hypercubes: hypercubes,
		sender:     sender,
		myAddr:     myAddr,
	}
}

func (b *HypercubeBundler) Publish(pocHeight int64, participant string, pubKey string, count uint32, rootHash []byte) error {
	if count == 0 || rootHash == nil {
		logging.Debug("HypercubeBundler: no artifacts to publish", types.PoC,
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
			logging.Warn("HypercubeBundler: failed to cache own header", types.PoC, "error", err)
		}
	}

	logging.Info("HypercubeBundler: publishing commit metadata", types.PoC,
		"pocHeight", pocHeight, "participant", participant,
		"count", count, "bundleID", fmt.Sprintf("%x", bundleID[:8]))

	if err := b.sendHeader(header); err != nil {
		logging.Warn("HypercubeBundler: failed to send header", types.PoC, "error", err)
		return fmt.Errorf("send header: %w", err)
	}

	logging.Info("HypercubeBundler: publish complete", types.PoC,
		"pocHeight", pocHeight, "participant", participant)

	return nil
}

func (b *HypercubeBundler) sendHeader(h BundleHeader) error {
	b.mu.RLock()
	hypercubes := b.hypercubes
	b.mu.RUnlock()

	var wg sync.WaitGroup
	totalRecipients := 0

	logging.Info("HypercubeBundler: checking hypercubes for neighbors", types.PoC,
		"publisher", b.myAddr, "totalHypercubes", len(hypercubes),
		"bundleID", fmt.Sprintf("%x", h.BundleID[:8]))

	allNeighbors := make(map[string]bool)

	for _, hypercube := range hypercubes {
		node := hypercube.GetNode(b.myAddr)
		if node == nil {
			logging.Warn("HypercubeBundler: node not found in hypercube", types.PoC,
				"address", b.myAddr, "hypercubeIndex", hypercube.Index)
			continue
		}

		for _, neighborAddr := range node.Neighbors {
			if !allNeighbors[neighborAddr] {
				allNeighbors[neighborAddr] = true
				totalRecipients++

				wg.Add(1)
				go func(addr string) {
					defer wg.Done()
					if err := b.sender.SendHeaderHypercube(addr, h); err != nil {
						logging.Warn("HypercubeBundler: failed to send to neighbor", types.PoC,
							"publisher", b.myAddr, "neighbor", addr, "error", err)
					} else {
						logging.Debug("HypercubeBundler: sent to neighbor", types.PoC,
							"publisher", b.myAddr, "neighbor", addr)
					}
				}(neighborAddr)
			}
		}
	}

	wg.Wait()

	logging.Info("HypercubeBundler: sent header to all neighbors", types.PoC,
		"publisher", b.myAddr, "totalRecipients", totalRecipients,
		"hypercubes", len(hypercubes), "bundleID", fmt.Sprintf("%x", h.BundleID[:8]))

	return nil
}

func (b *HypercubeBundler) PublishProofs(bundleID [32]byte, proofs []ProofItem) error {
	if len(proofs) == 0 {
		logging.Debug("HypercubeBundler: no proofs to publish", types.PoC,
			"bundleID", fmt.Sprintf("%x", bundleID[:8]))
		return nil
	}

	if b.cache != nil {
		if err := b.cache.StoreProofs(context.Background(), bundleID, proofs); err != nil {
			logging.Warn("HypercubeBundler: failed to cache own proofs", types.PoC, "error", err)
		}
	}

	logging.Info("HypercubeBundler: publishing proofs", types.PoC,
		"bundleID", fmt.Sprintf("%x", bundleID[:8]), "proofCount", len(proofs))

	if err := b.sendProofs(bundleID, proofs); err != nil {
		logging.Warn("HypercubeBundler: failed to send proofs", types.PoC, "error", err)
		return fmt.Errorf("send proofs: %w", err)
	}

	logging.Info("HypercubeBundler: proof publish complete", types.PoC,
		"bundleID", fmt.Sprintf("%x", bundleID[:8]))

	return nil
}

func (b *HypercubeBundler) sendProofs(bundleID [32]byte, proofs []ProofItem) error {
	b.mu.RLock()
	hypercubes := b.hypercubes
	b.mu.RUnlock()

	var wg sync.WaitGroup
	totalRecipients := 0

	logging.Info("HypercubeBundler: checking hypercubes for proof neighbors", types.PoC,
		"publisher", b.myAddr, "totalHypercubes", len(hypercubes),
		"bundleID", fmt.Sprintf("%x", bundleID[:8]))

	allNeighbors := make(map[string]bool)

	for _, hypercube := range hypercubes {
		node := hypercube.GetNode(b.myAddr)
		if node == nil {
			continue
		}

		for _, neighborAddr := range node.Neighbors {
			if !allNeighbors[neighborAddr] {
				allNeighbors[neighborAddr] = true
				totalRecipients++

				wg.Add(1)
				go func(addr string) {
					defer wg.Done()
					if err := b.sender.SendProofsHypercube(addr, bundleID, proofs); err != nil {
						logging.Warn("HypercubeBundler: failed to send proofs to neighbor", types.PoC,
							"publisher", b.myAddr, "neighbor", addr, "error", err)
					} else {
						logging.Debug("HypercubeBundler: sent proofs to neighbor", types.PoC,
							"publisher", b.myAddr, "neighbor", addr)
					}
				}(neighborAddr)
			}
		}
	}

	wg.Wait()

	logging.Info("HypercubeBundler: sent proofs to all neighbors", types.PoC,
		"publisher", b.myAddr, "totalRecipients", totalRecipients,
		"hypercubes", len(hypercubes), "bundleID", fmt.Sprintf("%x", bundleID[:8]))

	return nil
}

func (b *HypercubeBundler) StoreOwnArrival(pocHeight int64, participant string, count uint32) error {
	if b.cache == nil {
		return fmt.Errorf("cache not available")
	}
	return b.cache.StoreFirstArrival(participant, pocHeight, time.Now().UnixMilli(), count)
}

func (b *HypercubeBundler) SetHypercubes(hypercubes []*Hypercube) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.hypercubes = hypercubes
}
