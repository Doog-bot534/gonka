package propagation

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"decentralized-api/logging"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/productscience/inference/x/inference/types"
)

type PostgresBundleStorage struct {
	pool     *pgxpool.Pool
	instance string
	mu       sync.RWMutex
	bundles  map[[32]byte]BundleHeader
	proofs   map[[32]byte][]ProofItem
}

func NewPostgresBundleStorage(ctx context.Context, pool *pgxpool.Pool, instance string) (*PostgresBundleStorage, error) {
	if pool == nil {
		return nil, fmt.Errorf("pgx pool is nil")
	}
	if instance == "" {
		instance = "default"
	}

	s := &PostgresBundleStorage{
		pool:     pool,
		instance: instance,
		bundles:  make(map[[32]byte]BundleHeader),
		proofs:   make(map[[32]byte][]ProofItem),
	}

	if err := s.ensureSchema(ctx); err != nil {
		return nil, fmt.Errorf("ensure schema: %w", err)
	}

	if err := s.loadBundles(ctx); err != nil {
		return nil, fmt.Errorf("load bundles: %w", err)
	}

	logging.Info("PostgreSQL bundle storage initialized", types.PoC, "instance", instance)
	return s, nil
}

func (s *PostgresBundleStorage) ensureSchema(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS poc_bundle_headers (
			instance TEXT NOT NULL,
			bundle_id BYTEA NOT NULL,
			participant TEXT NOT NULL,
			pub_key TEXT NOT NULL,
			poc_height BIGINT NOT NULL,
			root_hash BYTEA NOT NULL,
			count INTEGER NOT NULL,
			created_at BIGINT NOT NULL,
			signature BYTEA,
			PRIMARY KEY (instance, bundle_id)
		);
		
		CREATE TABLE IF NOT EXISTS poc_bundle_proofs (
			instance TEXT NOT NULL,
			bundle_id BYTEA NOT NULL,
			proofs JSONB NOT NULL,
			PRIMARY KEY (instance, bundle_id)
		);
	`)
	return err
}

func (s *PostgresBundleStorage) loadBundles(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `
		SELECT bundle_id, participant, pub_key, poc_height, root_hash, count, created_at, signature
		FROM poc_bundle_headers
		WHERE instance = $1
	`, s.instance)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var idBytes []byte
		var h BundleHeader
		if err := rows.Scan(&idBytes, &h.Participant, &h.PubKey, &h.PocHeight, &h.RootHash, &h.Count, &h.CreatedAt, &h.Signature); err != nil {
			return err
		}
		if len(idBytes) != len(h.BundleID) {
			continue
		}
		copy(h.BundleID[:], idBytes)
		s.bundles[h.BundleID] = h
	}

	if err := rows.Err(); err != nil {
		return err
	}

	proofsRows, err := s.pool.Query(ctx, `
		SELECT bundle_id, proofs
		FROM poc_bundle_proofs
		WHERE instance = $1
	`, s.instance)
	if err != nil {
		return err
	}
	defer proofsRows.Close()

	for proofsRows.Next() {
		var idBytes []byte
		var proofsJSON []byte
		if err := proofsRows.Scan(&idBytes, &proofsJSON); err != nil {
			return err
		}
		if len(idBytes) != 32 {
			continue
		}
		var bundleID [32]byte
		copy(bundleID[:], idBytes)

		var proofs []ProofItem
		if err := json.Unmarshal(proofsJSON, &proofs); err != nil {
			logging.Warn("Failed to unmarshal proofs from PostgreSQL", types.PoC, "error", err)
			continue
		}
		s.proofs[bundleID] = proofs
	}

	logging.Info("Loaded bundles from PostgreSQL", types.PoC, "count", len(s.bundles), "proofs", len(s.proofs))
	return proofsRows.Err()
}

func (s *PostgresBundleStorage) StoreHeader(ctx context.Context, h BundleHeader) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.pool.Exec(ctx, `
		INSERT INTO poc_bundle_headers (instance, bundle_id, participant, pub_key, poc_height, root_hash, count, created_at, signature)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (instance, bundle_id) DO UPDATE SET
			participant = EXCLUDED.participant,
			pub_key = EXCLUDED.pub_key,
			poc_height = EXCLUDED.poc_height,
			root_hash = EXCLUDED.root_hash,
			count = EXCLUDED.count,
			created_at = EXCLUDED.created_at,
			signature = EXCLUDED.signature
	`, s.instance, h.BundleID[:], h.Participant, h.PubKey, h.PocHeight, h.RootHash, h.Count, h.CreatedAt, h.Signature)
	if err != nil {
		return fmt.Errorf("store header: %w", err)
	}

	s.bundles[h.BundleID] = h
	return nil
}

func (s *PostgresBundleStorage) GetHeader(ctx context.Context, bundleID [32]byte) (BundleHeader, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	header, exists := s.bundles[bundleID]
	if !exists {
		return BundleHeader{}, ErrBundleNotFound
	}
	return header, nil
}

func (s *PostgresBundleStorage) LatestBundle(ctx context.Context, participant string, pocHeight int64) (BundleHeader, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var latest BundleHeader
	var found bool

	for _, header := range s.bundles {
		if header.Participant == participant && header.PocHeight == pocHeight {
			if !found || header.CreatedAt > latest.CreatedAt {
				latest = header
				found = true
			}
		}
	}

	if !found {
		return BundleHeader{}, ErrBundleNotFound
	}
	return latest, nil
}

func (s *PostgresBundleStorage) AllBundlesForHeight(ctx context.Context, pocHeight int64) ([]BundleHeader, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]BundleHeader, 0)
	for _, header := range s.bundles {
		if header.PocHeight == pocHeight {
			result = append(result, header)
		}
	}
	return result, nil
}

func (s *PostgresBundleStorage) StoreProofs(ctx context.Context, bundleID [32]byte, proofs []ProofItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	proofsJSON, err := json.Marshal(proofs)
	if err != nil {
		return fmt.Errorf("marshal proofs: %w", err)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO poc_bundle_proofs (instance, bundle_id, proofs)
		VALUES ($1, $2, $3)
		ON CONFLICT (instance, bundle_id) DO UPDATE SET
			proofs = EXCLUDED.proofs
	`, s.instance, bundleID[:], proofsJSON)
	if err != nil {
		return fmt.Errorf("store proofs: %w", err)
	}

	s.proofs[bundleID] = proofs
	return nil
}

func (s *PostgresBundleStorage) GetProofs(ctx context.Context, bundleID [32]byte) ([]ProofItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	proofs, exists := s.proofs[bundleID]
	if !exists {
		return nil, ErrProofsNotFound
	}
	return proofs, nil
}

func (s *PostgresBundleStorage) Close() error {
	return nil
}

var _ BundleStorage = (*PostgresBundleStorage)(nil)
