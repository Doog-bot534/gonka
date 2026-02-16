package propagation

import (
	"context"
	"fmt"
	"sync"
	"time"

	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

type FLTQSender interface {
	SendHeaderFLTQ(to string, h BundleHeader) error
	SendProofsFLTQ(to string, bundleID [32]byte, proofs []ProofItem) error
}

type FLTQBundler struct {
	signer HeaderSigner
	cache  *Cache
	cubes  []*FLTQCube
	mu     sync.RWMutex
	sender FLTQSender
	myAddr string
}

func NewFLTQBundler(signer HeaderSigner, cache *Cache, cubes []*FLTQCube, sender FLTQSender, myAddr string) *FLTQBundler {
	return &FLTQBundler{
		signer: signer,
		cache:  cache,
		cubes:  cubes,
		sender: sender,
		myAddr: myAddr,
	}
}

func (b *FLTQBundler) Publish(pocHeight int64, participant string, pubKey string, count uint32, rootHash []byte) error {
	if count == 0 || rootHash == nil {
		logging.Debug("FLTQBundler: no artifacts to publish", types.PoC,
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
			logging.Warn("FLTQBundler: failed to cache own header", types.PoC, "error", err)
		}
	}

	logging.Info("FLTQBundler: publishing commit metadata", types.PoC,
		"pocHeight", pocHeight, "participant", participant,
		"count", count, "bundleID", fmt.Sprintf("%x", bundleID[:8]))

	if err := b.sendHeader(header); err != nil {
		logging.Warn("FLTQBundler: failed to send header", types.PoC, "error", err)
		return fmt.Errorf("send header: %w", err)
	}

	logging.Info("FLTQBundler: publish complete", types.PoC,
		"pocHeight", pocHeight, "participant", participant)

	return nil
}

func (b *FLTQBundler) sendHeader(h BundleHeader) error {
	b.mu.RLock()
	cubes := b.cubes
	b.mu.RUnlock()

	var wg sync.WaitGroup
	totalRecipients := 0

	logging.Info("FLTQBundler: checking FLTQ cubes for neighbors", types.PoC,
		"publisher", b.myAddr, "totalCubes", len(cubes),
		"bundleID", fmt.Sprintf("%x", h.BundleID[:8]))

	allNeighbors := make(map[string]bool)

	for _, cube := range cubes {
		node := cube.GetNode(b.myAddr)
		if node == nil {
			logging.Warn("FLTQBundler: node not found in cube", types.PoC,
				"address", b.myAddr, "cubeIndex", cube.Index)
			continue
		}

		for _, neighborAddr := range node.Neighbors {
			if !allNeighbors[neighborAddr] {
				allNeighbors[neighborAddr] = true
				totalRecipients++

				wg.Add(1)
				go func(addr string) {
					defer wg.Done()
					if err := b.sender.SendHeaderFLTQ(addr, h); err != nil {
						logging.Warn("FLTQBundler: failed to send to neighbor", types.PoC,
							"publisher", b.myAddr, "neighbor", addr, "error", err)
					} else {
						logging.Debug("FLTQBundler: sent to neighbor", types.PoC,
							"publisher", b.myAddr, "neighbor", addr)
					}
				}(neighborAddr)
			}
		}
	}

	wg.Wait()

	logging.Info("FLTQBundler: sent header to all neighbors", types.PoC,
		"publisher", b.myAddr, "totalRecipients", totalRecipients,
		"cubes", len(cubes), "bundleID", fmt.Sprintf("%x", h.BundleID[:8]))

	return nil
}

func (b *FLTQBundler) PublishProofs(bundleID [32]byte, proofs []ProofItem) error {
	if len(proofs) == 0 {
		logging.Debug("FLTQBundler: no proofs to publish", types.PoC,
			"bundleID", fmt.Sprintf("%x", bundleID[:8]))
		return nil
	}

	if b.cache != nil {
		if err := b.cache.StoreProofs(context.Background(), bundleID, proofs); err != nil {
			logging.Warn("FLTQBundler: failed to cache own proofs", types.PoC, "error", err)
		}
	}

	logging.Info("FLTQBundler: publishing proofs", types.PoC,
		"bundleID", fmt.Sprintf("%x", bundleID[:8]), "proofCount", len(proofs))

	if err := b.sendProofs(bundleID, proofs); err != nil {
		logging.Warn("FLTQBundler: failed to send proofs", types.PoC, "error", err)
		return fmt.Errorf("send proofs: %w", err)
	}

	logging.Info("FLTQBundler: proof publish complete", types.PoC,
		"bundleID", fmt.Sprintf("%x", bundleID[:8]))

	return nil
}

func (b *FLTQBundler) sendProofs(bundleID [32]byte, proofs []ProofItem) error {
	b.mu.RLock()
	cubes := b.cubes
	b.mu.RUnlock()

	var wg sync.WaitGroup
	totalRecipients := 0

	logging.Info("FLTQBundler: checking FLTQ cubes for proof neighbors", types.PoC,
		"publisher", b.myAddr, "totalCubes", len(cubes),
		"bundleID", fmt.Sprintf("%x", bundleID[:8]))

	allNeighbors := make(map[string]bool)

	for _, cube := range cubes {
		node := cube.GetNode(b.myAddr)
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
					if err := b.sender.SendProofsFLTQ(addr, bundleID, proofs); err != nil {
						logging.Warn("FLTQBundler: failed to send proofs to neighbor", types.PoC,
							"publisher", b.myAddr, "neighbor", addr, "error", err)
					} else {
						logging.Debug("FLTQBundler: sent proofs to neighbor", types.PoC,
							"publisher", b.myAddr, "neighbor", addr)
					}
				}(neighborAddr)
			}
		}
	}

	wg.Wait()

	logging.Info("FLTQBundler: sent proofs to all neighbors", types.PoC,
		"publisher", b.myAddr, "totalRecipients", totalRecipients,
		"cubes", len(cubes), "bundleID", fmt.Sprintf("%x", bundleID[:8]))

	return nil
}

func (b *FLTQBundler) StoreOwnArrival(pocHeight int64, participant string, count uint32) error {
	if b.cache == nil {
		return fmt.Errorf("cache not available")
	}
	return b.cache.StoreFirstArrival(participant, pocHeight, time.Now().UnixMilli(), count)
}

func (b *FLTQBundler) SetFLTQCubes(cubes []*FLTQCube) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cubes = cubes
}
