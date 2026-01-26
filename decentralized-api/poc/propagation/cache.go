package propagation

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrBundleNotFound = errors.New("bundle not found")
)

type Cache struct {
	mu       sync.RWMutex
	pool     *pgxpool.Pool
	instance string
	bundles  map[[32]byte]*bundleMetadata
}

type bundleMetadata struct {
	header BundleHeader
}

func NewCache(ctx context.Context, pool *pgxpool.Pool, instance string) (*Cache, error) {
	if pool == nil {
		return nil, errors.New("pgx pool is nil")
	}
	if instance == "" {
		instance = "default"
	}
	c := &Cache{
		pool:     pool,
		instance: instance,
		bundles:  make(map[[32]byte]*bundleMetadata),
	}
	if err := c.ensureSchema(ctx); err != nil {
		return nil, fmt.Errorf("ensure schema: %w", err)
	}
	if err := c.recover(ctx); err != nil {
		return nil, fmt.Errorf("recover cache: %w", err)
	}
	return c, nil
}

func (c *Cache) ensureSchema(ctx context.Context) error {
	_, err := c.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS poc_bundle_headers (
			instance TEXT NOT NULL,
			bundle_id BYTEA NOT NULL,
			participant TEXT NOT NULL,
			poc_height BIGINT NOT NULL,
			poc_block_hash BYTEA NOT NULL,
			root_hash BYTEA NOT NULL,
			count INTEGER NOT NULL,
			version INTEGER NOT NULL,
			created_at BIGINT NOT NULL,
			signature BYTEA,
			PRIMARY KEY (instance, bundle_id)
		)
	`)
	return err
}

func (c *Cache) recover(ctx context.Context) error {
	rows, err := c.pool.Query(ctx, `
		SELECT bundle_id, participant, poc_height, poc_block_hash, root_hash, count, version, created_at, signature
		FROM poc_bundle_headers
		WHERE instance = $1
	`, c.instance)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var idBytes []byte
		var h BundleHeader
		if err := rows.Scan(&idBytes, &h.Participant, &h.PocHeight, &h.PocBlockHash, &h.RootHash, &h.Count, &h.Version, &h.CreatedAt, &h.Signature); err != nil {
			return err
		}
		if len(idBytes) != len(h.BundleID) {
			continue
		}
		copy(h.BundleID[:], idBytes)
		c.bundles[h.BundleID] = &bundleMetadata{
			header: h,
		}
	}
	return rows.Err()
}

func (c *Cache) StoreHeader(ctx context.Context, h BundleHeader) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.pool.Exec(ctx, `
		INSERT INTO poc_bundle_headers (instance, bundle_id, participant, poc_height, poc_block_hash, root_hash, count, version, created_at, signature)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (instance, bundle_id) DO UPDATE SET
			participant = EXCLUDED.participant,
			poc_height = EXCLUDED.poc_height,
			poc_block_hash = EXCLUDED.poc_block_hash,
			root_hash = EXCLUDED.root_hash,
			count = EXCLUDED.count,
			version = EXCLUDED.version,
			created_at = EXCLUDED.created_at,
			signature = EXCLUDED.signature
	`, c.instance, h.BundleID[:], h.Participant, h.PocHeight, h.PocBlockHash, h.RootHash, h.Count, h.Version, h.CreatedAt, h.Signature)
	if err != nil {
		return fmt.Errorf("store header: %w", err)
	}
	meta := c.bundles[h.BundleID]
	if meta == nil {
		meta = &bundleMetadata{}
		c.bundles[h.BundleID] = meta
	}
	meta.header = h
	return nil
}

func (c *Cache) GetHeader(bundleID [32]byte) (BundleHeader, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	meta := c.bundles[bundleID]
	if meta == nil {
		return BundleHeader{}, ErrBundleNotFound
	}
	return meta.header, nil
}

func (c *Cache) LatestBundle(participant string, pocHeight int64) (BundleHeader, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var latest BundleHeader
	var found bool
	for _, meta := range c.bundles {
		if meta.header.Participant == participant && meta.header.PocHeight == pocHeight {
			if !found || meta.header.Version > latest.Version {
				latest = meta.header
				found = true
			}
		}
	}
	if !found {
		return BundleHeader{}, ErrBundleNotFound
	}
	return latest, nil
}

func (c *Cache) AllBundlesForHeight(pocHeight int64) []BundleHeader {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make([]BundleHeader, 0)
	for _, meta := range c.bundles {
		if meta.header.PocHeight == pocHeight {
			result = append(result, meta.header)
		}
	}
	return result
}
