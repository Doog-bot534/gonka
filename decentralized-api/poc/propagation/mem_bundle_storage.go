package propagation

import (
	"context"
	"sync"
)

type bundleShard struct {
	mu      sync.RWMutex
	bundles map[[4]byte]int64 // bundleID -> pocHeight
}

type MemBundleStorage struct {
	shards [256]bundleShard
}

func NewMemBundleStorage() *MemBundleStorage {
	return &MemBundleStorage{}
}

func (s *MemBundleStorage) shardFor(id [4]byte) *bundleShard {
	return &s.shards[id[0]]
}

func (s *MemBundleStorage) StoreHeader(_ context.Context, h BundleHeader) error {
	sh := s.shardFor(h.BundleID)
	sh.mu.Lock()
	if sh.bundles == nil {
		sh.bundles = make(map[[4]byte]int64, 64)
	}
	sh.bundles[h.BundleID] = h.PocHeight
	sh.mu.Unlock()
	return nil
}

func (s *MemBundleStorage) StoreHeaderBatch(_ context.Context, headers []BundleHeader) error {
	for _, h := range headers {
		sh := s.shardFor(h.BundleID)
		sh.mu.Lock()
		if sh.bundles == nil {
			sh.bundles = make(map[[4]byte]int64, 64)
		}
		sh.bundles[h.BundleID] = h.PocHeight
		sh.mu.Unlock()
	}
	return nil
}

func (s *MemBundleStorage) GetHeader(_ context.Context, bundleID [4]byte) (BundleHeader, error) {
	sh := s.shardFor(bundleID)
	sh.mu.RLock()
	_, exists := sh.bundles[bundleID]
	sh.mu.RUnlock()
	if !exists {
		return BundleHeader{}, ErrBundleNotFound
	}
	return BundleHeader{BundleID: bundleID}, nil
}

func (s *MemBundleStorage) LatestBundle(_ context.Context, _ string, _ int64) (BundleHeader, error) {
	return BundleHeader{}, ErrBundleNotFound
}

func (s *MemBundleStorage) AllBundlesForHeight(_ context.Context, pocHeight int64) ([]BundleHeader, error) {
	count := 0
	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.RLock()
		for _, h := range sh.bundles {
			if h == pocHeight {
				count++
			}
		}
		sh.mu.RUnlock()
	}
	return make([]BundleHeader, count), nil
}

func (s *MemBundleStorage) StoreFirstArrival(_ context.Context, _ string, _ int64, _ int64, _ uint32) error {
	return nil
}

func (s *MemBundleStorage) StoreFirstArrivalBatch(_ context.Context, _ []ArrivalInfo, _ []string, _ []int64) error {
	return nil
}

func (s *MemBundleStorage) GetFirstArrival(_ context.Context, _ string, _ int64) (ArrivalInfo, error) {
	return ArrivalInfo{}, ErrArrivalNotFound
}

func (s *MemBundleStorage) GetAllFirstArrivals(_ context.Context, _ int64) (map[string]ArrivalInfo, error) {
	return nil, nil
}

func (s *MemBundleStorage) CleanupOldHeights(_ context.Context, _ int) error {
	return nil
}

func (s *MemBundleStorage) Close() error {
	return nil
}

var _ BundleStorage = (*MemBundleStorage)(nil)
