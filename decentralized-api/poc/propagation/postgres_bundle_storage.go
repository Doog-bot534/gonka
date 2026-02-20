package propagation

import (
	"context"
	"fmt"
	"sync"

	"decentralized-api/logging"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/productscience/inference/x/inference/types"
)

type PostgresBundleStorage struct {
	pool     *pgxpool.Pool
	instance string
	bundles  sync.Map // [4]byte -> BundleHeader
	arrivals sync.Map // participantPocKey -> ArrivalInfo
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

	bundleCount := 0
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
		s.bundles.Store(h.BundleID, h)
		bundleCount++
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

	arrivalCount := 0
	for arrivalsRows.Next() {
		var participant string
		var pocHeight, arrivalTime int64
		var count uint32
		if err := arrivalsRows.Scan(&participant, &pocHeight, &arrivalTime, &count); err != nil {
			return err
		}
		key := participantPocKey{Participant: participant, PocHeight: pocHeight}
		s.arrivals.Store(key, ArrivalInfo{Time: arrivalTime, Count: count})
		arrivalCount++
	}

	if err := arrivalsRows.Err(); err != nil {
		return err
	}

	logging.Info("Loaded bundles from PostgreSQL", types.PoC, "count", bundleCount, "arrivals", arrivalCount)
	return nil
}

func (s *PostgresBundleStorage) StoreHeader(ctx context.Context, h BundleHeader) error {
	if _, loaded := s.bundles.LoadOrStore(h.BundleID, h); loaded {
		return nil
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO poc_bundle_headers (instance, bundle_id, participant, poc_height, root_hash, count, created_at, signature)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (instance, bundle_id) DO NOTHING
	`, s.instance, h.BundleID[:], h.Participant, h.PocHeight, h.RootHash[:], h.Count, h.CreatedAt, h.Signature[:])
	if err != nil {
		s.bundles.Delete(h.BundleID)
		return fmt.Errorf("store header: %w", err)
	}

	return nil
}

func (s *PostgresBundleStorage) StoreHeaderBatch(ctx context.Context, headers []BundleHeader) error {
	if len(headers) == 0 {
		return nil
	}

	toInsert := make([]BundleHeader, 0, len(headers))
	for _, h := range headers {
		if _, loaded := s.bundles.LoadOrStore(h.BundleID, h); !loaded {
			toInsert = append(toInsert, h)
		}
	}

	if len(toInsert) == 0 {
		return nil
	}

	batch := &pgx.Batch{}
	for _, h := range toInsert {
		batch.Queue(`
			INSERT INTO poc_bundle_headers (instance, bundle_id, participant, poc_height, root_hash, count, created_at, signature)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (instance, bundle_id) DO NOTHING
		`, s.instance, h.BundleID[:], h.Participant, h.PocHeight, h.RootHash[:], h.Count, h.CreatedAt, h.Signature[:])
	}

	results := s.pool.SendBatch(ctx, batch)
	defer results.Close()

	for _, h := range toInsert {
		if _, err := results.Exec(); err != nil {
			logging.Warn("Failed to store header in batch", types.PoC, "error", err, "bundleID", h.BundleID)
			s.bundles.Delete(h.BundleID)
		}
	}

	return nil
}

func (s *PostgresBundleStorage) GetHeader(ctx context.Context, bundleID [4]byte) (BundleHeader, error) {
	val, exists := s.bundles.Load(bundleID)
	if !exists {
		return BundleHeader{}, ErrBundleNotFound
	}
	return val.(BundleHeader), nil
}

func (s *PostgresBundleStorage) LatestBundle(ctx context.Context, participant string, pocHeight int64) (BundleHeader, error) {
	var latest BundleHeader
	var found bool

	s.bundles.Range(func(_, val interface{}) bool {
		header := val.(BundleHeader)
		if header.Participant == participant && header.PocHeight == pocHeight {
			if !found || header.CreatedAt > latest.CreatedAt {
				latest = header
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

func (s *PostgresBundleStorage) AllBundlesForHeight(ctx context.Context, pocHeight int64) ([]BundleHeader, error) {
	result := make([]BundleHeader, 0)
	s.bundles.Range(func(_, val interface{}) bool {
		header := val.(BundleHeader)
		if header.PocHeight == pocHeight {
			result = append(result, header)
		}
		return true
	})
	return result, nil
}

func (s *PostgresBundleStorage) StoreFirstArrival(ctx context.Context, participant string, pocHeight int64, arrivalTime int64, count uint32) error {
	key := participantPocKey{Participant: participant, PocHeight: pocHeight}
	if _, loaded := s.arrivals.LoadOrStore(key, ArrivalInfo{Time: arrivalTime, Count: count}); loaded {
		return nil
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO poc_first_arrivals (instance, participant, poc_height, arrival_time, arrival_count)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (instance, participant, poc_height) DO NOTHING
	`, s.instance, participant, pocHeight, arrivalTime, count)
	if err != nil {
		s.arrivals.Delete(key)
		return fmt.Errorf("store first arrival: %w", err)
	}

	return nil
}

func (s *PostgresBundleStorage) StoreFirstArrivalBatch(ctx context.Context, arrivals []ArrivalInfo, participants []string, pocHeights []int64) error {
	if len(arrivals) == 0 || len(participants) != len(arrivals) || len(pocHeights) != len(arrivals) {
		return nil
	}

	type indexedArrival struct {
		idx     int
		key     participantPocKey
		arrival ArrivalInfo
	}

	toInsert := make([]indexedArrival, 0, len(arrivals))
	for i := range arrivals {
		key := participantPocKey{Participant: participants[i], PocHeight: pocHeights[i]}
		if _, loaded := s.arrivals.LoadOrStore(key, arrivals[i]); !loaded {
			toInsert = append(toInsert, indexedArrival{idx: i, key: key, arrival: arrivals[i]})
		}
	}

	if len(toInsert) == 0 {
		return nil
	}

	batch := &pgx.Batch{}
	for _, ia := range toInsert {
		batch.Queue(`
			INSERT INTO poc_first_arrivals (instance, participant, poc_height, arrival_time, arrival_count)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (instance, participant, poc_height) DO NOTHING
		`, s.instance, ia.key.Participant, ia.key.PocHeight, ia.arrival.Time, ia.arrival.Count)
	}

	results := s.pool.SendBatch(ctx, batch)
	defer results.Close()

	for _, ia := range toInsert {
		if _, err := results.Exec(); err != nil {
			logging.Warn("Failed to store first arrival in batch", types.PoC, "error", err)
			s.arrivals.Delete(ia.key)
		}
	}

	return nil
}

func (s *PostgresBundleStorage) GetFirstArrival(ctx context.Context, participant string, pocHeight int64) (ArrivalInfo, error) {
	key := participantPocKey{Participant: participant, PocHeight: pocHeight}
	val, exists := s.arrivals.Load(key)
	if !exists {
		return ArrivalInfo{}, ErrArrivalNotFound
	}
	return val.(ArrivalInfo), nil
}

func (s *PostgresBundleStorage) GetAllFirstArrivals(ctx context.Context, pocHeight int64) (map[string]ArrivalInfo, error) {
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

func (s *PostgresBundleStorage) CleanupOldHeights(ctx context.Context, retainCount int) error {
	heights := make(map[int64]struct{})
	s.bundles.Range(func(_, val interface{}) bool {
		heights[val.(BundleHeader).PocHeight] = struct{}{}
		return true
	})
	s.arrivals.Range(func(k, _ interface{}) bool {
		heights[k.(participantPocKey).PocHeight] = struct{}{}
		return true
	})

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

		s.bundles.Range(func(key, val interface{}) bool {
			if val.(BundleHeader).PocHeight == height {
				s.bundles.Delete(key)
			}
			return true
		})

		s.arrivals.Range(func(key, _ interface{}) bool {
			if key.(participantPocKey).PocHeight == height {
				s.arrivals.Delete(key)
			}
			return true
		})

		logging.Info("Cleaned up propagation data for PoC height", types.PoC, "pocHeight", height)
	}

	return nil
}

func (s *PostgresBundleStorage) Close() error {
	return nil
}

var _ BundleStorage = (*PostgresBundleStorage)(nil)
