package propagation

import (
	"context"
)

type Cache struct {
	storage BundleStorage
}

func NewCache(storage BundleStorage) *Cache {
	return &Cache{
		storage: storage,
	}
}

func (c *Cache) StoreHeader(ctx context.Context, h BundleHeader) error {
	return c.storage.StoreHeader(ctx, h)
}

func (c *Cache) GetHeader(bundleID [32]byte) (BundleHeader, error) {
	return c.storage.GetHeader(context.Background(), bundleID)
}

func (c *Cache) LatestBundle(participant string, pocHeight int64) (BundleHeader, error) {
	return c.storage.LatestBundle(context.Background(), participant, pocHeight)
}

func (c *Cache) AllBundlesForHeight(pocHeight int64) []BundleHeader {
	bundles, _ := c.storage.AllBundlesForHeight(context.Background(), pocHeight)
	return bundles
}
