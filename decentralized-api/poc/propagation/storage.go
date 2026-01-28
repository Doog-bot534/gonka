package propagation

import (
	"context"
	"errors"
)

var (
	ErrBundleNotFound = errors.New("bundle not found")
)

type BundleStorage interface {
	StoreHeader(ctx context.Context, h BundleHeader) error
	GetHeader(ctx context.Context, bundleID [32]byte) (BundleHeader, error)
	LatestBundle(ctx context.Context, participant string, pocHeight int64) (BundleHeader, error)
	AllBundlesForHeight(ctx context.Context, pocHeight int64) ([]BundleHeader, error)
	Close() error
}
