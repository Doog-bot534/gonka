package propagation

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/semaphore"
)

type FLTQReceiver struct {
	cache    *Cache
	cube     *FLTQCube
	verifier PubKeyProvider
	myAddr   string
	sender   FLTQSender

	processedHeaders sync.Map
	pendingHeaders   sync.Map
	lastHeaderTime   sync.Map

	mu      sync.RWMutex
	wg      sync.WaitGroup
	diskSem *semaphore.Weighted
}

func NewFLTQReceiver(cache *Cache, cube *FLTQCube, verifier PubKeyProvider, myAddr string, sender FLTQSender) *FLTQReceiver {
	return &FLTQReceiver{
		cache:    cache,
		cube:     cube,
		verifier: verifier,
		myAddr:   myAddr,
		sender:   sender,
		diskSem:  semaphore.NewWeighted(20),
	}
}

func (r *FLTQReceiver) OnHeader(h BundleHeader, from string) error {
	if _, loaded := r.processedHeaders.LoadOrStore(h.BundleID, true); loaded {
		return nil
	}

	if _, err := r.cache.GetHeader(h.BundleID); err == nil {
		return nil
	}

	pubKey, err := r.verifier.GetPubKey(h.Participant)
	if err != nil {
		r.processedHeaders.Delete(h.BundleID)
		return fmt.Errorf("get pubkey: %w", err)
	}

	if err := VerifyHeader(h, pubKey); err != nil {
		r.processedHeaders.Delete(h.BundleID)
		return fmt.Errorf("verify header: %w", err)
	}

	expectedID := MakeBundleID(h.Participant, h.PocHeight, h.RootHash[:], h.Count)
	if !bytes.Equal(expectedID[:], h.BundleID[:]) {
		r.processedHeaders.Delete(h.BundleID)
		return fmt.Errorf("bundle ID mismatch")
	}

	r.pendingHeaders.Store(h.BundleID, &h)
	r.lastHeaderTime.Store(h.BundleID, time.Now())

	r.mu.RLock()
	cube := r.cube
	r.mu.RUnlock()

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		if err := r.diskSem.Acquire(context.Background(), 1); err != nil {
			return
		}
		defer r.diskSem.Release(1)

		_ = r.cache.StoreHeader(context.Background(), h)
		arrivalTime := time.Now().UnixMilli()
		_ = r.cache.StoreFirstArrival(h.Participant, h.PocHeight, arrivalTime, h.Count)
	}()

	r.forwardHeaderToNeighbors(h, cube, from)
	return nil
}

func (r *FLTQReceiver) forwardHeaderToNeighbors(h BundleHeader, cube *FLTQCube, from string) {
	if cube == nil {
		return
	}

	node := cube.GetNode(r.myAddr)
	if node == nil {
		return
	}

	for _, neighborAddr := range node.Neighbors {
		if neighborAddr != from {
			_ = r.sender.SendHeaderFLTQ(neighborAddr, h)
		}
	}
}

func (r *FLTQReceiver) SetFLTQCube(cube *FLTQCube) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cube = cube
}

func (r *FLTQReceiver) ClearProcessedState() {
	r.processedHeaders.Range(func(key, value interface{}) bool {
		r.processedHeaders.Delete(key)
		return true
	})
	r.pendingHeaders.Range(func(key, value interface{}) bool {
		r.pendingHeaders.Delete(key)
		return true
	})
	r.lastHeaderTime.Range(func(key, value interface{}) bool {
		r.lastHeaderTime.Delete(key)
		return true
	})
}

func (r *FLTQReceiver) Wait() {
	r.wg.Wait()
}

func (r *FLTQReceiver) Close() {
	r.wg.Wait()
}
