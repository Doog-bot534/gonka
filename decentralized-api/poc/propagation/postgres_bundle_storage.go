package propagation

import (
	"context"
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
	bundles  map[[4]byte]BundleHeader
	arrivals map[participantPocKey]ArrivalInfo
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
		bundles:  make(map[[4]byte]BundleHeader),
		arrivals: make(map[participantPocKey]ArrivalInfo),
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
			poc_height BIGINT NOT NULL,
			root_hash BYTEA NOT NULL,
			count INTEGER NOT NULL,
			created_at BIGINT NOT NULL,
			signature BYTEA,
			PRIMARY KEY (instance, bundle_id)
		);

		CREATE TABLE IF NOT EXISTS poc_first_arrivals (
			instance TEXT NOT NULL,
			participant TEXT NOT NULL,
			poc_height BIGINT NOT NULL,
			arrival_time BIGINT NOT NULL,
			arrival_count INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (instance, participant, poc_height)
		);

	`)
	return err
}

func (s *PostgresBundleStorage) loadBundles(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `
		SELECT bundle_id, participant, poc_height, root_hash, count, created_at, signature
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
		if err := rows.Scan(&idBytes, &h.Participant, &h.PocHeight, &h.RootHash, &h.Count, &h.CreatedAt, &h.Signature); err != nil {
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

	arrivalsRows, err := s.pool.Query(ctx, `
		SELECT participant, poc_height, arrival_time, COALESCE(arrival_count, 0)
		FROM poc_first_arrivals
		WHERE instance = $1
	`, s.instance)
	if err != nil {
		return err
	}
	defer arrivalsRows.Close()

	for arrivalsRows.Next() {
		var participant string
		var pocHeight, arrivalTime int64
		var count uint32
		if err := arrivalsRows.Scan(&participant, &pocHeight, &arrivalTime, &count); err != nil {
			return err
		}
		key := participantPocKey{Participant: participant, PocHeight: pocHeight}
		s.arrivals[key] = ArrivalInfo{Time: arrivalTime, Count: count}
	}

	if err := arrivalsRows.Err(); err != nil {
		return err
	}

	logging.Info("Loaded bundles from PostgreSQL", types.PoC, "count", len(s.bundles), "arrivals", len(s.arrivals))
	return nil
}

func (s *PostgresBundleStorage) StoreHeader(ctx context.Context, h BundleHeader) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.pool.Exec(ctx, `
		INSERT INTO poc_bundle_headers (instance, bundle_id, participant, poc_height, root_hash, count, created_at, signature)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (instance, bundle_id) DO UPDATE SET
			participant = EXCLUDED.participant,
			poc_height = EXCLUDED.poc_height,
			root_hash = EXCLUDED.root_hash,
			count = EXCLUDED.count,
			created_at = EXCLUDED.created_at,
			signature = EXCLUDED.signature
	`, s.instance, h.BundleID[:], h.Participant, h.PocHeight, h.RootHash[:], h.Count, h.CreatedAt, h.Signature[:])
	if err != nil {
		return fmt.Errorf("store header: %w", err)
	}

	s.bundles[h.BundleID] = h
	return nil
}

func (s *PostgresBundleStorage) GetHeader(ctx context.Context, bundleID [4]byte) (BundleHeader, error) {
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

func (s *PostgresBundleStorage) StoreFirstArrival(ctx context.Context, participant string, pocHeight int64, arrivalTime int64, count uint32) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := participantPocKey{Participant: participant, PocHeight: pocHeight}
	if _, exists := s.arrivals[key]; exists {
		return nil
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO poc_first_arrivals (instance, participant, poc_height, arrival_time, arrival_count)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (instance, participant, poc_height) DO NOTHING
	`, s.instance, participant, pocHeight, arrivalTime, count)
	if err != nil {
		return fmt.Errorf("store first arrival: %w", err)
	}

	s.arrivals[key] = ArrivalInfo{Time: arrivalTime, Count: count}
	return nil
}

func (s *PostgresBundleStorage) GetFirstArrival(ctx context.Context, participant string, pocHeight int64) (ArrivalInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := participantPocKey{Participant: participant, PocHeight: pocHeight}
	info, exists := s.arrivals[key]
	if !exists {
		return ArrivalInfo{}, ErrArrivalNotFound
	}
	return info, nil
}

func (s *PostgresBundleStorage) GetAllFirstArrivals(ctx context.Context, pocHeight int64) (map[string]ArrivalInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]ArrivalInfo)
	for key, info := range s.arrivals {
		if key.PocHeight == pocHeight {
			result[key.Participant] = info
		}
	}
	return result, nil
}

func (s *PostgresBundleStorage) CleanupOldHeights(ctx context.Context, retainCount int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	heights := make(map[int64]struct{})
	for _, header := range s.bundles {
		heights[header.PocHeight] = struct{}{}
	}
	for key := range s.arrivals {
		heights[key.PocHeight] = struct{}{}
	}

	heightList := make([]int64, 0, len(heights))
	for h := range heights {
		heightList = append(heightList, h)
	}

	if len(heightList) <= retainCount {
		return nil
	}

	for i := 0; i < len(heightList)-1; i++ {
		for j := i + 1; j < len(heightList); j++ {
			if heightList[i] > heightList[j] {
				heightList[i], heightList[j] = heightList[j], heightList[i]
			}
		}
	}

	toPrune := heightList[:len(heightList)-retainCount]

	for _, height := range toPrune {
		_, err := s.pool.Exec(ctx, `
			DELETE FROM poc_bundle_headers
			WHERE instance = $1 AND poc_height = $2
		`, s.instance, height)
		if err != nil {
			logging.Warn("Failed to delete bundle headers from PostgreSQL", types.PoC,
				"pocHeight", height, "error", err)
		}

		_, err = s.pool.Exec(ctx, `
			DELETE FROM poc_first_arrivals
			WHERE instance = $1 AND poc_height = $2
		`, s.instance, height)
		if err != nil {
			logging.Warn("Failed to delete first arrivals from PostgreSQL", types.PoC,
				"pocHeight", height, "error", err)
		}

		for bundleID, header := range s.bundles {
			if header.PocHeight == height {
				delete(s.bundles, bundleID)
			}
		}

		for key := range s.arrivals {
			if key.PocHeight == height {
				delete(s.arrivals, key)
			}
		}

		logging.Info("Cleaned up propagation data for PoC height", types.PoC, "pocHeight", height)
	}

	return nil
}

func (s *PostgresBundleStorage) Close() error {
	return nil
}

var _ BundleStorage = (*PostgresBundleStorage)(nil)
