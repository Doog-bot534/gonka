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

	mu                    sync.RWMutex
	processedHeaders      map[[32]byte]bool
	processedProofs       map[[32]byte]bool
	processedObservations map[[32]byte]bool
	forwardedProofs       map[[32]byte]map[string]bool
	pendingHeaders        map[[32]byte]*BundleHeader
	lastHeaderTime        map[[32]byte]time.Time

	wg sync.WaitGroup
}

type PubKeyProvider interface {
	GetPubKey(participantAddr string) (string, error)
}

func NewReceiver(cache *Cache, trees []*Tree, verifier PubKeyProvider, myAddr string, sender Sender) *Receiver {
	return &Receiver{
		cache:                 cache,
		trees:                 trees,
		verifier:              verifier,
		myAddr:                myAddr,
		sender:                sender,
		processedHeaders:      make(map[[32]byte]bool),
		processedProofs:       make(map[[32]byte]bool),
		processedObservations: make(map[[32]byte]bool),
		forwardedProofs:       make(map[[32]byte]map[string]bool),
		pendingHeaders:        make(map[[32]byte]*BundleHeader),
		lastHeaderTime:        make(map[[32]byte]time.Time),
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

	expectedID := MakeBundleID(h.Participant, h.PocHeight, h.RootHash, h.Count)
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

	arrivalTime := time.Now().UnixMilli()
	arrivalCount := h.Count
	go func() {
		if err := r.cache.StoreFirstArrival(h.Participant, h.PocHeight, arrivalTime, arrivalCount); err != nil {
			logging.Debug("Receiver: first arrival already recorded or error", types.PoC,
				"participant", h.Participant, "pocHeight", h.PocHeight, "error", err)
		} else {
			logging.Info("Receiver: recorded first arrival time", types.PoC,
				"participant", h.Participant, "pocHeight", h.PocHeight, "arrivalTime", arrivalTime, "count", arrivalCount)
		}
	}()

	r.processedHeaders[h.BundleID] = true
	r.pendingHeaders[h.BundleID] = &h
	r.lastHeaderTime[h.BundleID] = time.Now()
	trees := r.trees
	r.mu.Unlock()

	logging.Info("Receiver: commit metadata verified and stored", types.PoC,
		"receiver", r.myAddr, "from", from, "publisher", h.Participant, "pocHeight", h.PocHeight,
		"bundleID", fmt.Sprintf("%x", h.BundleID[:8]), "tree", treeIdx)

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.forwardHeaderAllTrees(h, trees)
	}()

	return nil
}

func (r *Receiver) forwardHeaderAllTrees(h BundleHeader, trees []*Tree) {
	var wg sync.WaitGroup

	for _, tree := range trees {
		node := tree.GetNode(r.myAddr)
		if node == nil || len(node.Children) == 0 {
			continue
		}

		logging.Info("Receiver: broadcasting to children", types.PoC,
			"forwarder", r.myAddr, "publisher", h.Participant,
			"tree", tree.Index, "children", len(node.Children))

		for _, child := range node.Children {
			wg.Add(1)
			go func(treeIdx int, childAddr string) {
				defer wg.Done()
				if err := r.sender.SendHeader(treeIdx, childAddr, h); err != nil {
					logging.Warn("Receiver: failed to forward to child", types.PoC,
						"forwarder", r.myAddr, "tree", treeIdx, "child", childAddr, "error", err)
				} else {
					logging.Debug("Receiver: forwarded to child", types.PoC,
						"forwarder", r.myAddr, "tree", treeIdx, "child", childAddr)
				}
			}(tree.Index, child.Address)
		}
	}

	wg.Wait()
}

func (r *Receiver) OnProofs(bundleID [32]byte, proofs []ProofItem, from string) error {
	logging.Info("Receiver: received proofs", types.PoC,
		"receiver", r.myAddr, "from", from, "bundleID", fmt.Sprintf("%x", bundleID[:8]),
		"proofCount", len(proofs))

	r.mu.Lock()
	if r.processedProofs[bundleID] {
		r.mu.Unlock()
		logging.Debug("Receiver: duplicate proofs ignored (already processed)", types.PoC,
			"receiver", r.myAddr, "bundleID", fmt.Sprintf("%x", bundleID[:8]))
		return nil
	}
	r.processedProofs[bundleID] = true
	trees := r.trees
	r.mu.Unlock()

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		if err := r.cache.StoreProofs(context.Background(), bundleID, proofs); err != nil {
			logging.Warn("Receiver: failed to store proofs", types.PoC,
				"bundleID", fmt.Sprintf("%x", bundleID[:8]), "error", err)
			return
		}

		logging.Info("Receiver: proofs stored", types.PoC,
			"receiver", r.myAddr, "bundleID", fmt.Sprintf("%x", bundleID[:8]))

		r.forwardProofsAllTrees(bundleID, proofs, trees, from)
	}()

	return nil
}

func (r *Receiver) forwardProofsAllTrees(bundleID [32]byte, proofs []ProofItem, trees []*Tree, from string) {
	proofSender, ok := r.sender.(interface {
		SendProofs(to string, bundleID [32]byte, proofs []ProofItem) error
	})
	if !ok {
		return
	}

	var wg sync.WaitGroup

	for _, tree := range trees {
		node := tree.GetNode(r.myAddr)
		if node == nil || len(node.Children) == 0 {
			continue
		}

		logging.Debug("Receiver: checking children for proof forwarding", types.PoC,
			"forwarder", r.myAddr, "bundleID", fmt.Sprintf("%x", bundleID[:8]),
			"tree", tree.Index, "children", len(node.Children))

		for _, child := range node.Children {
			if child.Address == from {
				continue
			}

			r.mu.Lock()
			if r.forwardedProofs[bundleID] == nil {
				r.forwardedProofs[bundleID] = make(map[string]bool)
			}
			if r.forwardedProofs[bundleID][child.Address] {
				r.mu.Unlock()
				continue
			}
			r.forwardedProofs[bundleID][child.Address] = true
			r.mu.Unlock()

			wg.Add(1)
			go func(childAddr string) {
				defer wg.Done()
				if err := proofSender.SendProofs(childAddr, bundleID, proofs); err != nil {
					logging.Warn("Receiver: failed to forward proofs to child", types.PoC,
						"forwarder", r.myAddr, "child", childAddr, "error", err)
				} else {
					logging.Debug("Receiver: forwarded proofs to child", types.PoC,
						"forwarder", r.myAddr, "child", childAddr)
				}
			}(child.Address)
		}
	}

	wg.Wait()
}

func (r *Receiver) SetTrees(trees []*Tree) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.trees = trees
}

func (r *Receiver) ClearProcessedState() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.processedHeaders = make(map[[32]byte]bool)
	r.processedProofs = make(map[[32]byte]bool)
	r.processedObservations = make(map[[32]byte]bool)
	r.forwardedProofs = make(map[[32]byte]map[string]bool)
	r.pendingHeaders = make(map[[32]byte]*BundleHeader)
	r.lastHeaderTime = make(map[[32]byte]time.Time)
}

func (r *Receiver) OnObservation(obs FirstArrivalObservation, from string) error {
	logging.Info("Receiver: received observation", types.PoC,
		"receiver", r.myAddr, "from", from, "validator", obs.ValidatorAddress,
		"pocHeight", obs.PocHeight, "arrivals", len(obs.Arrivals))

	obsID := MakeObservationID(obs.ValidatorAddress, obs.PocHeight)

	r.mu.Lock()
	if r.processedObservations[obsID] {
		r.mu.Unlock()
		logging.Debug("Receiver: duplicate observation ignored (already processed)", types.PoC,
			"receiver", r.myAddr, "validator", obs.ValidatorAddress, "pocHeight", obs.PocHeight)
		return nil
	}
	r.processedObservations[obsID] = true
	trees := r.trees
	r.mu.Unlock()

	pubKey, err := r.verifier.GetPubKey(obs.ValidatorAddress)
	if err != nil {
		logging.Warn("Receiver: failed to get public key for observation", types.PoC,
			"validatorAddress", obs.ValidatorAddress, "error", err)
		return fmt.Errorf("get pubkey: %w", err)
	}

	if err := VerifyObservation(obs, pubKey); err != nil {
		logging.Warn("Receiver: observation signature verification failed", types.PoC,
			"validatorAddress", obs.ValidatorAddress, "error", err)
		return fmt.Errorf("verify observation: %w", err)
	}

	if err := r.cache.StoreObservation(obs); err != nil {
		logging.Warn("Receiver: failed to store observation", types.PoC,
			"validatorAddress", obs.ValidatorAddress, "error", err)
		return fmt.Errorf("store observation: %w", err)
	}

	logging.Info("Receiver: observation stored", types.PoC,
		"receiver", r.myAddr, "validator", obs.ValidatorAddress, "pocHeight", obs.PocHeight)

	go r.forwardObservationAllTrees(obs, trees)

	return nil
}

func (r *Receiver) forwardObservationAllTrees(obs FirstArrivalObservation, trees []*Tree) {
	obsSender, ok := r.sender.(ObservationSender)
	if !ok {
		return
	}

	var wg sync.WaitGroup

	for _, tree := range trees {
		node := tree.GetNode(r.myAddr)
		if node == nil || len(node.Children) == 0 {
			continue
		}

		logging.Info("Receiver: forwarding observation to children", types.PoC,
			"forwarder", r.myAddr, "validator", obs.ValidatorAddress,
			"tree", tree.Index, "children", len(node.Children))

		for _, child := range node.Children {
			wg.Add(1)
			go func(childAddr string) {
				defer wg.Done()
				if err := obsSender.SendObservation(childAddr, obs); err != nil {
					logging.Warn("Receiver: failed to forward observation to child", types.PoC,
						"forwarder", r.myAddr, "child", childAddr, "error", err)
				} else {
					logging.Debug("Receiver: forwarded observation to child", types.PoC,
						"forwarder", r.myAddr, "child", childAddr)
				}
			}(child.Address)
		}
	}

	wg.Wait()
}

func (r *Receiver) Wait() {
	r.wg.Wait()
}

func (r *Receiver) Close() {
	r.wg.Wait()
}
