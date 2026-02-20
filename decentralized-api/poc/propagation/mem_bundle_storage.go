package propagation

import (
	"context"
	"sync"
)

type bundleShard struct {
	mu      sync.RWMutex
	bundles map[[4]byte]BundleHeader
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
		sh.bundles = make(map[[4]byte]BundleHeader, 64)
	}
	if _, exists := sh.bundles[h.BundleID]; !exists {
		sh.bundles[h.BundleID] = h
	}
	sh.mu.Unlock()
	return nil
}

func (s *MemBundleStorage) StoreHeaderBatch(_ context.Context, headers []BundleHeader) error {
	for _, h := range headers {
		sh := s.shardFor(h.BundleID)
		sh.mu.Lock()
		if sh.bundles == nil {
			sh.bundles = make(map[[4]byte]BundleHeader, 64)
		}
		if _, exists := sh.bundles[h.BundleID]; !exists {
			sh.bundles[h.BundleID] = h
		}
		sh.mu.Unlock()
	}
	return nil
}

func (s *MemBundleStorage) GetHeader(_ context.Context, bundleID [4]byte) (BundleHeader, error) {
	sh := s.shardFor(bundleID)
	sh.mu.RLock()
	h, exists := sh.bundles[bundleID]
	sh.mu.RUnlock()
	if !exists {
		return BundleHeader{}, ErrBundleNotFound
	}
	return h, nil
}

func (s *MemBundleStorage) LatestBundle(_ context.Context, participant string, pocHeight int64) (BundleHeader, error) {
	var latest BundleHeader
	var found bool
	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.RLock()
		for _, h := range sh.bundles {
			if h.Participant == participant && h.PocHeight == pocHeight {
				if !found || h.CreatedAt > latest.CreatedAt {
					latest = h
					found = true
				}
			}
		}
		sh.mu.RUnlock()
	}
	if !found {
		return BundleHeader{}, ErrBundleNotFound
	}
	return latest, nil
}

func (s *MemBundleStorage) AllBundlesForHeight(_ context.Context, pocHeight int64) ([]BundleHeader, error) {
	result := make([]BundleHeader, 0, 64)
	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.RLock()
		for _, h := range sh.bundles {
			if h.PocHeight == pocHeight {
				result = append(result, h)
			}
		}
		sh.mu.RUnlock()
	}
	return result, nil
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
