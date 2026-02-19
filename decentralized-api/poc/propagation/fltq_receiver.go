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

type FLTQReceiver struct {
	cache    *Cache
	cube     *FLTQCube
	verifier PubKeyProvider
	myAddr   string
	sender   FLTQSender

	mu               sync.RWMutex
	processedHeaders map[[4]byte]bool
	pendingHeaders   map[[4]byte]*BundleHeader
	lastHeaderTime   map[[4]byte]time.Time

	wg sync.WaitGroup
}

func NewFLTQReceiver(cache *Cache, cube *FLTQCube, verifier PubKeyProvider, myAddr string, sender FLTQSender) *FLTQReceiver {
	return &FLTQReceiver{
		cache:            cache,
		cube:             cube,
		verifier:         verifier,
		myAddr:           myAddr,
		sender:           sender,
		processedHeaders: make(map[[4]byte]bool),
		pendingHeaders:   make(map[[4]byte]*BundleHeader),
		lastHeaderTime:   make(map[[4]byte]time.Time),
	}
}

func (r *FLTQReceiver) OnHeader(h BundleHeader, from string) error {
	logging.Info("FLTQReceiver: received header", types.PoC,
		"receiver", r.myAddr, "from", from, "publisher", h.Participant, "pocHeight", h.PocHeight,
		"count", h.Count, "bundleID", fmt.Sprintf("%x", h.BundleID[:]))

	r.mu.RLock()
	processed := r.processedHeaders[h.BundleID]
	r.mu.RUnlock()

	if processed {
		logging.Debug("FLTQReceiver: duplicate header ignored (already processed)", types.PoC,
			"receiver", r.myAddr, "bundleID", fmt.Sprintf("%x", h.BundleID[:]))
		return nil
	}

	if _, err := r.cache.GetHeader(h.BundleID); err == nil {
		r.mu.Lock()
		r.processedHeaders[h.BundleID] = true
		r.mu.Unlock()
		logging.Debug("FLTQReceiver: duplicate header ignored (already in storage)", types.PoC,
			"receiver", r.myAddr, "bundleID", fmt.Sprintf("%x", h.BundleID[:]))
		return nil
	}

	pubKey, err := r.verifier.GetPubKey(h.Participant)
	if err != nil {
		logging.Warn("FLTQReceiver: failed to get public key", types.PoC,
			"participant", h.Participant, "error", err)
		return fmt.Errorf("get pubkey: %w", err)
	}

	if err := VerifyHeader(h, pubKey); err != nil {
		logging.Warn("FLTQReceiver: header signature verification failed", types.PoC,
			"participant", h.Participant, "error", err)
		return fmt.Errorf("verify header: %w", err)
	}

	expectedID := MakeBundleID(h.Participant, h.PocHeight, h.RootHash[:], h.Count)
	if !bytes.Equal(expectedID[:], h.BundleID[:]) {
		logging.Warn("FLTQReceiver: bundle ID mismatch", types.PoC,
			"expected", fmt.Sprintf("%x", expectedID[:]),
			"got", fmt.Sprintf("%x", h.BundleID[:]))
		return fmt.Errorf("bundle ID mismatch")
	}

	r.mu.Lock()
	if r.processedHeaders[h.BundleID] {
		r.mu.Unlock()
		logging.Info("FLTQReceiver: duplicate header ignored (race detected)", types.PoC,
			"receiver", r.myAddr, "bundleID", fmt.Sprintf("%x", h.BundleID[:]))
		return nil
	}

	if err := r.cache.StoreHeader(context.Background(), h); err != nil {
		r.mu.Unlock()
		logging.Warn("FLTQReceiver: failed to store header", types.PoC,
			"bundleID", fmt.Sprintf("%x", h.BundleID[:]), "error", err)
		return fmt.Errorf("store header: %w", err)
	}

	arrivalTime := time.Now().UnixMilli()
	arrivalCount := h.Count
	go func() {
		if err := r.cache.StoreFirstArrival(h.Participant, h.PocHeight, arrivalTime, arrivalCount); err != nil {
			logging.Debug("FLTQReceiver: first arrival already recorded or error", types.PoC,
				"participant", h.Participant, "pocHeight", h.PocHeight, "error", err)
		} else {
			logging.Info("FLTQReceiver: recorded first arrival time", types.PoC,
				"participant", h.Participant, "pocHeight", h.PocHeight, "arrivalTime", arrivalTime, "count", arrivalCount)
		}
	}()

	r.processedHeaders[h.BundleID] = true
	r.pendingHeaders[h.BundleID] = &h
	r.lastHeaderTime[h.BundleID] = time.Now()
	cube := r.cube
	r.mu.Unlock()

	logging.Info("FLTQReceiver: commit metadata verified and stored", types.PoC,
		"receiver", r.myAddr, "from", from, "publisher", h.Participant, "pocHeight", h.PocHeight,
		"bundleID", fmt.Sprintf("%x", h.BundleID[:]))

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.forwardHeaderToNeighbors(h, cube, from)
	}()

	return nil
}

func (r *FLTQReceiver) forwardHeaderToNeighbors(h BundleHeader, cube *FLTQCube, from string) {
	if cube == nil {
		logging.Warn("FLTQReceiver: no cube configured", types.PoC, "forwarder", r.myAddr)
		return
	}

	node := cube.GetNode(r.myAddr)
	if node == nil {
		logging.Warn("FLTQReceiver: node not found in cube", types.PoC, "forwarder", r.myAddr)
		return
	}

	var wg sync.WaitGroup
	neighbors := make([]string, 0, len(node.Neighbors))
	for _, neighborAddr := range node.Neighbors {
		if neighborAddr != from {
			neighbors = append(neighbors, neighborAddr)
		}
	}

	logging.Info("FLTQReceiver: forwarding to neighbors", types.PoC,
		"forwarder", r.myAddr, "publisher", h.Participant,
		"neighbors", len(neighbors))

	for _, neighborAddr := range neighbors {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			if err := r.sender.SendHeaderFLTQ(addr, h); err != nil {
				logging.Warn("FLTQReceiver: failed to forward to neighbor", types.PoC,
					"forwarder", r.myAddr, "neighbor", addr, "error", err)
			} else {
				logging.Debug("FLTQReceiver: forwarded to neighbor", types.PoC,
					"forwarder", r.myAddr, "neighbor", addr)
			}
		}(neighborAddr)
	}

	wg.Wait()
}

func (r *FLTQReceiver) SetFLTQCube(cube *FLTQCube) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cube = cube
}

func (r *FLTQReceiver) ClearProcessedState() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.processedHeaders = make(map[[4]byte]bool)
	r.pendingHeaders = make(map[[4]byte]*BundleHeader)
	r.lastHeaderTime = make(map[[4]byte]time.Time)
}

func (r *FLTQReceiver) Wait() {
	r.wg.Wait()
}

func (r *FLTQReceiver) Close() {
	r.wg.Wait()
}
