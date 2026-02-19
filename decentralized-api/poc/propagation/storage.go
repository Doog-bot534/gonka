package propagation

import (
	"context"
	"errors"
)

var (
	ErrBundleNotFound  = errors.New("bundle not found")
	ErrArrivalNotFound = errors.New("first arrival not found")
)

type BundleStorage interface {
	StoreHeader(ctx context.Context, h BundleHeader) error
	GetHeader(ctx context.Context, bundleID [4]byte) (BundleHeader, error)
	LatestBundle(ctx context.Context, participant string, pocHeight int64) (BundleHeader, error)
	AllBundlesForHeight(ctx context.Context, pocHeight int64) ([]BundleHeader, error)

	StoreFirstArrival(ctx context.Context, participant string, pocHeight int64, arrivalTime int64, count uint32) error
	GetFirstArrival(ctx context.Context, participant string, pocHeight int64) (ArrivalInfo, error)
	GetAllFirstArrivals(ctx context.Context, pocHeight int64) (map[string]ArrivalInfo, error)

	CleanupOldHeights(ctx context.Context, retainCount int) error

	Close() error
}

type participantPocKey struct {
	Participant string
	PocHeight   int64
}
