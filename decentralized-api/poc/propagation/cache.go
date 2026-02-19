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

func (c *Cache) GetHeader(bundleID [4]byte) (BundleHeader, error) {
	return c.storage.GetHeader(context.Background(), bundleID)
}

func (c *Cache) LatestBundle(participant string, pocHeight int64) (BundleHeader, error) {
	return c.storage.LatestBundle(context.Background(), participant, pocHeight)
}

func (c *Cache) AllBundlesForHeight(pocHeight int64) []BundleHeader {
	bundles, _ := c.storage.AllBundlesForHeight(context.Background(), pocHeight)
	return bundles
}

func (c *Cache) StoreFirstArrival(participant string, pocHeight int64, arrivalTime int64, count uint32) error {
	return c.storage.StoreFirstArrival(context.Background(), participant, pocHeight, arrivalTime, count)
}

func (c *Cache) GetFirstArrival(participant string, pocHeight int64) (ArrivalInfo, error) {
	return c.storage.GetFirstArrival(context.Background(), participant, pocHeight)
}

func (c *Cache) GetAllFirstArrivals(pocHeight int64) (map[string]ArrivalInfo, error) {
	return c.storage.GetAllFirstArrivals(context.Background(), pocHeight)
}
