package propagation

import (
	"context"
	"sync"
)

type MemBundleStorage struct {
	bundles  sync.Map // [4]byte -> BundleHeader
	arrivals sync.Map // participantPocKey -> ArrivalInfo
}

func NewMemBundleStorage() *MemBundleStorage {
	return &MemBundleStorage{}
}

func (s *MemBundleStorage) StoreHeader(_ context.Context, h BundleHeader) error {
	s.bundles.LoadOrStore(h.BundleID, h)
	return nil
}

func (s *MemBundleStorage) StoreHeaderBatch(_ context.Context, headers []BundleHeader) error {
	for _, h := range headers {
		s.bundles.LoadOrStore(h.BundleID, h)
	}
	return nil
}

func (s *MemBundleStorage) GetHeader(_ context.Context, bundleID [4]byte) (BundleHeader, error) {
	val, exists := s.bundles.Load(bundleID)
	if !exists {
		return BundleHeader{}, ErrBundleNotFound
	}
	return val.(BundleHeader), nil
}

func (s *MemBundleStorage) LatestBundle(_ context.Context, participant string, pocHeight int64) (BundleHeader, error) {
	var latest BundleHeader
	var found bool
	s.bundles.Range(func(_, val interface{}) bool {
		h := val.(BundleHeader)
		if h.Participant == participant && h.PocHeight == pocHeight {
			if !found || h.CreatedAt > latest.CreatedAt {
				latest = h
				found = true
			}
		}
		return true
	})
	if !found {
		return BundleHeader{}, ErrBundleNotFound
	}
	return latest, nil
}

func (s *MemBundleStorage) AllBundlesForHeight(_ context.Context, pocHeight int64) ([]BundleHeader, error) {
	result := make([]BundleHeader, 0)
	s.bundles.Range(func(_, val interface{}) bool {
		h := val.(BundleHeader)
		if h.PocHeight == pocHeight {
			result = append(result, h)
		}
		return true
	})
	return result, nil
}

func (s *MemBundleStorage) StoreFirstArrival(_ context.Context, participant string, pocHeight int64, arrivalTime int64, count uint32) error {
	key := participantPocKey{Participant: participant, PocHeight: pocHeight}
	s.arrivals.LoadOrStore(key, ArrivalInfo{Time: arrivalTime, Count: count})
	return nil
}

func (s *MemBundleStorage) StoreFirstArrivalBatch(_ context.Context, arrivals []ArrivalInfo, participants []string, pocHeights []int64) error {
	for i := range arrivals {
		key := participantPocKey{Participant: participants[i], PocHeight: pocHeights[i]}
		s.arrivals.LoadOrStore(key, arrivals[i])
	}
	return nil
}

func (s *MemBundleStorage) GetFirstArrival(_ context.Context, participant string, pocHeight int64) (ArrivalInfo, error) {
	key := participantPocKey{Participant: participant, PocHeight: pocHeight}
	val, exists := s.arrivals.Load(key)
	if !exists {
		return ArrivalInfo{}, ErrArrivalNotFound
	}
	return val.(ArrivalInfo), nil
}

func (s *MemBundleStorage) GetAllFirstArrivals(_ context.Context, pocHeight int64) (map[string]ArrivalInfo, error) {
	result := make(map[string]ArrivalInfo)
	s.arrivals.Range(func(k, val interface{}) bool {
		key := k.(participantPocKey)
		if key.PocHeight == pocHeight {
			result[key.Participant] = val.(ArrivalInfo)
		}
		return true
	})
	return result, nil
}

func (s *MemBundleStorage) CleanupOldHeights(_ context.Context, _ int) error {
	return nil
}

func (s *MemBundleStorage) Close() error {
	return nil
}

var _ BundleStorage = (*MemBundleStorage)(nil)
