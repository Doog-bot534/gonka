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
	cubes    []*FLTQCube
	verifier PubKeyProvider
	myAddr   string
	sender   FLTQSender

	mu               sync.RWMutex
	processedHeaders map[[32]byte]bool
	processedProofs  map[[32]byte]bool
	forwardedProofs  map[[32]byte]map[string]bool
	pendingHeaders   map[[32]byte]*BundleHeader
	lastHeaderTime   map[[32]byte]time.Time

	wg sync.WaitGroup
}

func NewFLTQReceiver(cache *Cache, cubes []*FLTQCube, verifier PubKeyProvider, myAddr string, sender FLTQSender) *FLTQReceiver {
	return &FLTQReceiver{
		cache:            cache,
		cubes:            cubes,
		verifier:         verifier,
		myAddr:           myAddr,
		sender:           sender,
		processedHeaders: make(map[[32]byte]bool),
		processedProofs:  make(map[[32]byte]bool),
		forwardedProofs:  make(map[[32]byte]map[string]bool),
		pendingHeaders:   make(map[[32]byte]*BundleHeader),
		lastHeaderTime:   make(map[[32]byte]time.Time),
	}
}

func (r *FLTQReceiver) OnHeaderFLTQ(h BundleHeader, from string) error {
	logging.Info("FLTQReceiver: received header", types.PoC,
		"receiver", r.myAddr, "from", from, "publisher", h.Participant, "pocHeight", h.PocHeight,
		"count", h.Count, "bundleID", fmt.Sprintf("%x", h.BundleID[:8]))

	r.mu.RLock()
	processed := r.processedHeaders[h.BundleID]
	r.mu.RUnlock()

	if processed {
		logging.Debug("FLTQReceiver: duplicate header ignored (already processed)", types.PoC,
			"receiver", r.myAddr, "bundleID", fmt.Sprintf("%x", h.BundleID[:8]))
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

	expectedID := MakeBundleID(h.Participant, h.PocHeight, h.RootHash, h.Count)
	if !bytes.Equal(expectedID[:], h.BundleID[:]) {
		logging.Warn("FLTQReceiver: bundle ID mismatch", types.PoC,
			"expected", fmt.Sprintf("%x", expectedID[:8]),
			"got", fmt.Sprintf("%x", h.BundleID[:8]))
		return fmt.Errorf("bundle ID mismatch")
	}

	r.mu.Lock()
	if r.processedHeaders[h.BundleID] {
		r.mu.Unlock()
		logging.Info("FLTQReceiver: duplicate header ignored (race detected)", types.PoC,
			"receiver", r.myAddr, "bundleID", fmt.Sprintf("%x", h.BundleID[:8]))
		return nil
	}

	if err := r.cache.StoreHeader(context.Background(), h); err != nil {
		r.mu.Unlock()
		logging.Warn("FLTQReceiver: failed to store header", types.PoC,
			"bundleID", fmt.Sprintf("%x", h.BundleID[:8]), "error", err)
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
	cubes := r.cubes
	r.mu.Unlock()

	logging.Info("FLTQReceiver: commit metadata verified and stored", types.PoC,
		"receiver", r.myAddr, "from", from, "publisher", h.Participant, "pocHeight", h.PocHeight,
		"bundleID", fmt.Sprintf("%x", h.BundleID[:8]))

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.forwardHeaderToNeighbors(h, cubes, from)
	}()

	return nil
}

func (r *FLTQReceiver) forwardHeaderToNeighbors(h BundleHeader, cubes []*FLTQCube, from string) {
	var wg sync.WaitGroup
	allNeighbors := make(map[string]bool)

	for _, cube := range cubes {
		node := cube.GetNode(r.myAddr)
		if node == nil {
			continue
		}

		for _, neighborAddr := range node.Neighbors {
			if neighborAddr != from && !allNeighbors[neighborAddr] {
				allNeighbors[neighborAddr] = true
			}
		}
	}

	logging.Info("FLTQReceiver: forwarding to neighbors", types.PoC,
		"forwarder", r.myAddr, "publisher", h.Participant,
		"neighbors", len(allNeighbors), "cubes", len(cubes))

	for neighborAddr := range allNeighbors {
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

func (r *FLTQReceiver) OnProofsFLTQ(bundleID [32]byte, proofs []ProofItem, from string) error {
	logging.Info("FLTQReceiver: received proofs", types.PoC,
		"receiver", r.myAddr, "from", from, "bundleID", fmt.Sprintf("%x", bundleID[:8]),
		"proofCount", len(proofs))

	r.mu.Lock()
	if r.processedProofs[bundleID] {
		logging.Debug("FLTQReceiver: duplicate proofs", types.PoC,
			"receiver", r.myAddr, "bundleID", fmt.Sprintf("%x", bundleID[:8]))
	}

	r.processedProofs[bundleID] = true
	cubes := r.cubes
	r.mu.Unlock()

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		if err := r.cache.StoreProofs(context.Background(), bundleID, proofs); err != nil {
			logging.Warn("FLTQReceiver: failed to store proofs", types.PoC,
				"bundleID", fmt.Sprintf("%x", bundleID[:8]), "error", err)
			return
		}

		logging.Info("FLTQReceiver: proofs stored", types.PoC,
			"receiver", r.myAddr, "bundleID", fmt.Sprintf("%x", bundleID[:8]))

		r.forwardProofsToNeighbors(bundleID, proofs, cubes, from)
	}()

	return nil
}

func (r *FLTQReceiver) forwardProofsToNeighbors(bundleID [32]byte, proofs []ProofItem, cubes []*FLTQCube, from string) {
	var wg sync.WaitGroup
	allNeighbors := make(map[string]bool)

	for _, cube := range cubes {
		node := cube.GetNode(r.myAddr)
		if node == nil {
			continue
		}

		for _, neighborAddr := range node.Neighbors {
			if neighborAddr != from {
				r.mu.Lock()
				if r.forwardedProofs[bundleID] == nil {
					r.forwardedProofs[bundleID] = make(map[string]bool)
				}
				if !r.forwardedProofs[bundleID][neighborAddr] {
					r.forwardedProofs[bundleID][neighborAddr] = true
					allNeighbors[neighborAddr] = true
				}
				r.mu.Unlock()
			}
		}
	}

	logging.Debug("FLTQReceiver: checking neighbors for proof forwarding", types.PoC,
		"forwarder", r.myAddr, "bundleID", fmt.Sprintf("%x", bundleID[:8]),
		"neighbors", len(allNeighbors), "cubes", len(cubes))

	for neighborAddr := range allNeighbors {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			if err := r.sender.SendProofsFLTQ(addr, bundleID, proofs); err != nil {
				logging.Warn("FLTQReceiver: failed to forward proofs to neighbor", types.PoC,
					"forwarder", r.myAddr, "neighbor", addr, "error", err)
			} else {
				logging.Debug("FLTQReceiver: forwarded proofs to neighbor", types.PoC,
					"forwarder", r.myAddr, "neighbor", addr)
			}
		}(neighborAddr)
	}

	wg.Wait()
}

func (r *FLTQReceiver) SetFLTQCubes(cubes []*FLTQCube) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cubes = cubes
}

func (r *FLTQReceiver) ClearProcessedState() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.processedHeaders = make(map[[32]byte]bool)
	r.processedProofs = make(map[[32]byte]bool)
	r.forwardedProofs = make(map[[32]byte]map[string]bool)
	r.pendingHeaders = make(map[[32]byte]*BundleHeader)
	r.lastHeaderTime = make(map[[32]byte]time.Time)
}

func (r *FLTQReceiver) Wait() {
	r.wg.Wait()
}

func (r *FLTQReceiver) Close() {
	r.wg.Wait()
}
