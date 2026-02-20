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
}

type FLTQBundler struct {
	signer HeaderSigner
	cache  *Cache
	cube   *FLTQCube
	mu     sync.RWMutex
	sender FLTQSender
	myAddr string
}

func NewFLTQBundler(signer HeaderSigner, cache *Cache, cube *FLTQCube, sender FLTQSender, myAddr string) *FLTQBundler {
	return &FLTQBundler{
		signer: signer,
		cache:  cache,
		cube:   cube,
		sender: sender,
		myAddr: myAddr,
	}
}

func (b *FLTQBundler) Publish(pocHeight int64, participant string, count uint32, rootHash []byte) error {
	if count == 0 || rootHash == nil {
		logging.Debug("FLTQBundler: no artifacts to publish", types.PoC,
			"pocHeight", pocHeight, "participant", participant)
		return nil
	}

	bundleID := MakeBundleID(participant, pocHeight, rootHash, count)
	var header BundleHeader
	header.BundleID = bundleID
	header.Participant = participant
	header.PocHeight = pocHeight
	copy(header.RootHash[:], rootHash)
	header.Count = count
	header.CreatedAt = time.Now().Unix()

	sig, err := SignHeaderWith(header, b.signer)
	if err != nil {
		return fmt.Errorf("sign header: %w", err)
	}
	copy(header.Signature[:], sig)

	if b.cache != nil {
		if err := b.cache.StoreHeaderBatch(context.Background(), []BundleHeader{header}); err != nil {
			logging.Warn("FLTQBundler: failed to cache own header", types.PoC, "error", err)
		}
	}

	logging.Info("FLTQBundler: publishing commit metadata", types.PoC,
		"pocHeight", pocHeight, "participant", participant,
		"count", count, "bundleID", fmt.Sprintf("%x", bundleID[:]))

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
	cube := b.cube
	b.mu.RUnlock()

	if cube == nil {
		logging.Warn("FLTQBundler: no cube configured", types.PoC, "publisher", b.myAddr)
		return nil
	}

	node := cube.GetNode(b.myAddr)
	if node == nil {
		logging.Warn("FLTQBundler: node not found in cube", types.PoC,
			"address", b.myAddr, "cubeIndex", cube.Index)
		return nil
	}

	var wg sync.WaitGroup
	totalRecipients := len(node.Neighbors)

	logging.Info("FLTQBundler: sending header to neighbors", types.PoC,
		"publisher", b.myAddr, "neighbors", totalRecipients,
		"bundleID", fmt.Sprintf("%x", h.BundleID[:]))

	for _, neighborAddr := range node.Neighbors {
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

	wg.Wait()

	logging.Info("FLTQBundler: sent header to all neighbors", types.PoC,
		"publisher", b.myAddr, "totalRecipients", totalRecipients,
		"bundleID", fmt.Sprintf("%x", h.BundleID[:]))

	return nil
}

func (b *FLTQBundler) StoreOwnArrival(pocHeight int64, participant string, count uint32) error {
	if b.cache == nil {
		return fmt.Errorf("cache not available")
	}
	return b.cache.StoreFirstArrival(participant, pocHeight, time.Now().UnixMilli(), count)
}

func (b *FLTQBundler) SetFLTQCube(cube *FLTQCube) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cube = cube
}
