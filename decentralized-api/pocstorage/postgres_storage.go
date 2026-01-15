package pocstorage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"decentralized-api/logging"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/productscience/inference/x/inference/types"
)

// NOTE: Schema details will be extended in task 2.2.
// We create minimal tables to support storing PoC runs and per-mlnode records.
const createSchemaSQL = `
CREATE TABLE IF NOT EXISTS poc_runs (
    block_height BIGINT PRIMARY KEY,
    epoch_length BIGINT NOT NULL,
    block_hash TEXT NOT NULL,
    block_time TIMESTAMPTZ NOT NULL,
    duration_seconds BIGINT NOT NULL,
    frequency_seconds BIGINT NOT NULL,
    batch_size INT NOT NULL,
    params_json JSONB NOT NULL,
    interrupted_time TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS poc_batches_generated (
    block_height BIGINT NOT NULL,
    address TEXT NOT NULL,
    public_key TEXT NOT NULL,
    block_hash TEXT NOT NULL,
    node_id TEXT NOT NULL,
    model TEXT NOT NULL,
    amount BIGINT NOT NULL,
    hash TEXT NOT NULL,
    time_since_block BIGINT NOT NULL,
    nonces_json JSONB NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (block_height, address, node_id, received_at)
) PARTITION BY RANGE (block_height);

CREATE TABLE IF NOT EXISTS poc_mlnode_state (
    block_height BIGINT NOT NULL,
    address TEXT NOT NULL,
    node_id TEXT NOT NULL,
    model TEXT NOT NULL,
    amount BIGINT NOT NULL,
    hash TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (block_height, address, node_id, model)
);

-- Secondary indexes to support debug queries by run/participant/node.
-- Note: indexes created on the partitioned table are propagated to partitions.
CREATE INDEX IF NOT EXISTS poc_batches_generated_block_height_address_idx ON poc_batches_generated (block_height, address);
CREATE INDEX IF NOT EXISTS poc_batches_generated_block_height_node_id_idx ON poc_batches_generated (block_height, node_id);
CREATE INDEX IF NOT EXISTS poc_batches_generated_block_height_model_idx ON poc_batches_generated (block_height, model);
`

type PostgresStorage struct {
	pool        *pgxpool.Pool
	knownBlocks sync.Map
}

func jsonMarshal(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal json: %w", err)
	}
	return b, nil
}

func jsonUnmarshal(data []byte, v any) error {
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("unmarshal json: %w", err)
	}
	return nil
}

func (s *PostgresStorage) ensurePartition(ctx context.Context, blockHeight int64) error {
	if _, ok := s.knownBlocks.Load(blockHeight); ok {
		return nil
	}

	// Create one partition per PoC run height, similar to payload storage per epoch.
	tableName := fmt.Sprintf("poc_batches_generated_block_%d", blockHeight)
	query := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s
		PARTITION OF poc_batches_generated
		FOR VALUES FROM (%d) TO (%d)
	`, tableName, blockHeight, blockHeight+1)

	_, err := s.pool.Exec(ctx, query)
	if err != nil {
		// Handle race condition: table already exists (error code 42P07)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P07" {
			s.knownBlocks.Store(blockHeight, true)
			return nil
		}
		return fmt.Errorf("create partition %s: %w", tableName, err)
	}

	s.knownBlocks.Store(blockHeight, true)
	return nil
}

// NewPostgresStorage creates a new PostgreSQL storage using standard libpq env vars.
// Environment variables: PGHOST, PGPORT, PGDATABASE, PGUSER, PGPASSWORD
func NewPostgresStorage(ctx context.Context) (*PostgresStorage, error) {
	pool, err := pgxpool.New(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	s := &PostgresStorage{pool: pool}
	if err := s.ensureSchema(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ensure schema: %w", err)
	}
	logging.Info("PostgreSQL PoC storage initialized", types.PoC)
	return s, nil
}

func (s *PostgresStorage) ensureSchema(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, createSchemaSQL)
	return err
}

func (s *PostgresStorage) UpsertRun(ctx context.Context, run PoCRun) error {
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Now().UTC()
	}
	paramsJSON, err := jsonMarshal(run.Params)
	if err != nil {
		return err
	}

	_, err = s.pool.Exec(ctx, `
INSERT INTO poc_runs (
  block_height, epoch_length, block_hash, block_time,
  duration_seconds, frequency_seconds, batch_size, params_json,
  interrupted_time, created_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
ON CONFLICT (block_height) DO UPDATE SET
  epoch_length = EXCLUDED.epoch_length,
  block_hash = EXCLUDED.block_hash,
  block_time = EXCLUDED.block_time,
  duration_seconds = EXCLUDED.duration_seconds,
  frequency_seconds = EXCLUDED.frequency_seconds,
  batch_size = EXCLUDED.batch_size,
  params_json = EXCLUDED.params_json,
  interrupted_time = EXCLUDED.interrupted_time
`, run.BlockHeight, run.EpochLength, run.BlockHash, run.BlockTime,
		run.DurationSeconds, run.FrequencySeconds, run.BatchSize, paramsJSON,
		run.InterruptedTime, run.CreatedAt)
	if err != nil {
		return fmt.Errorf("upsert poc_runs: %w", err)
	}
	return nil
}

func (s *PostgresStorage) MarkInterrupted(ctx context.Context, blockHeight int64, interruptedAt time.Time) error {
	_, err := s.pool.Exec(ctx, `
UPDATE poc_runs SET interrupted_time = $2 WHERE block_height = $1
`, blockHeight, interruptedAt)
	if err != nil {
		return fmt.Errorf("update interrupted_time: %w", err)
	}
	return nil
}

func (s *PostgresStorage) GetLatestRun(ctx context.Context) (PoCRun, error) {
	return s.GetClosestRunAtOrBefore(ctx, 1<<62) // effectively "max int64"
}

func (s *PostgresStorage) GetClosestRunAtOrBefore(ctx context.Context, blockHeight int64) (PoCRun, error) {
	row := s.pool.QueryRow(ctx, `
SELECT block_height, epoch_length, block_hash, block_time,
       duration_seconds, frequency_seconds, batch_size, params_json,
       interrupted_time, created_at
FROM poc_runs
WHERE block_height <= $1
ORDER BY block_height DESC
LIMIT 1
`, blockHeight)

	var run PoCRun
	var paramsJSON []byte
	var interrupted *time.Time

	err := row.Scan(&run.BlockHeight, &run.EpochLength, &run.BlockHash, &run.BlockTime,
		&run.DurationSeconds, &run.FrequencySeconds, &run.BatchSize, &paramsJSON,
		&interrupted, &run.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PoCRun{}, ErrNotFound
		}
		return PoCRun{}, fmt.Errorf("select poc_runs: %w", err)
	}
	run.InterruptedTime = interrupted

	if err := jsonUnmarshal(paramsJSON, &run.Params); err != nil {
		return PoCRun{}, err
	}
	return run, nil
}

func (s *PostgresStorage) StoreGeneratedRecord(ctx context.Context, rec PoCBatchesGeneratedRecord) (PoCBatchesGeneratedRecord, error) {
	if err := validateRecordKey(rec); err != nil {
		return PoCBatchesGeneratedRecord{}, err
	}
	if rec.ReceivedAt.IsZero() {
		rec.ReceivedAt = time.Now().UTC()
	}
	if err := s.ensurePartition(ctx, rec.BlockHeight); err != nil {
		return PoCBatchesGeneratedRecord{}, err
	}

	// Optimization: Compute batch hash *before* starting the transaction to avoid holding the lock during CPU-bound work.
	var batchHash string
	if len(rec.Artifacts) > 0 {
		batchHash = computeBatchHash(rec.Artifacts)
	}

	// Prepare JSON before tx as well.
	noncesJSON, err := jsonMarshal(rec.Artifacts)
	if err != nil {
		return PoCBatchesGeneratedRecord{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return PoCBatchesGeneratedRecord{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Lock state row (or establish defaults).
	var prevAmount int64
	var prevHash string
	row := tx.QueryRow(ctx, `
SELECT amount, hash
FROM poc_mlnode_state
WHERE block_height = $1 AND address = $2 AND node_id = $3 AND model = $4
FOR UPDATE
`, rec.BlockHeight, rec.Address, rec.NodeID, rec.Model)
	scanErr := row.Scan(&prevAmount, &prevHash)
	if scanErr != nil {
		if !errors.Is(scanErr, pgx.ErrNoRows) {
			return PoCBatchesGeneratedRecord{}, fmt.Errorf("select state: %w", scanErr)
		}
		prevAmount = 0
		prevHash = ""
	}

	var newAmount int64
	var newHash string
	var errState error

	if len(rec.Artifacts) > 0 {
		// Peer (or local) with artifacts: compute incrementally.
		newAmount = prevAmount + int64(len(rec.Artifacts))
		newHash, errState = computeRollingHash(prevHash, batchHash, newAmount)
	} else {
		// Peer without artifacts: trust the provided amount/hash.
		newAmount = rec.Amount
		newHash = rec.Hash
		errState = nil
	}

	if errState != nil {
		return PoCBatchesGeneratedRecord{}, errState
	}

	// Upsert state.
	_, err = tx.Exec(ctx, `
INSERT INTO poc_mlnode_state (
  block_height, address, node_id, model,
  amount, hash, updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7)
ON CONFLICT (block_height, address, node_id, model) DO UPDATE SET
  amount = EXCLUDED.amount,
  hash = EXCLUDED.hash,
  updated_at = EXCLUDED.updated_at
`, rec.BlockHeight, rec.Address, rec.NodeID, rec.Model, newAmount, newHash, rec.ReceivedAt)
	if err != nil {
		return PoCBatchesGeneratedRecord{}, fmt.Errorf("upsert state: %w", err)
	}

	// Insert record with computed snapshot.
	rec.Amount = newAmount
	rec.Hash = newHash

	_, err = tx.Exec(ctx, `
INSERT INTO poc_batches_generated (
  block_height, address, public_key, block_hash, node_id,
  model, amount, hash, time_since_block, nonces_json, received_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
`, rec.BlockHeight, rec.Address, rec.PublicKey, rec.BlockHash, rec.NodeID,
		rec.Model, rec.Amount, rec.Hash, rec.TimeSinceBlock, noncesJSON, rec.ReceivedAt)
	if err != nil {
		return PoCBatchesGeneratedRecord{}, fmt.Errorf("insert poc_batches_generated: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return PoCBatchesGeneratedRecord{}, fmt.Errorf("commit tx: %w", err)
	}
	return rec, nil
}

func (s *PostgresStorage) StoreGeneratedRecordsBatch(ctx context.Context, records []PoCBatchesGeneratedRecord) ([]PoCBatchesGeneratedRecord, error) {
	if len(records) == 0 {
		return nil, nil
	}
	// Sort to avoid deadlocks
	sort.Slice(records, func(i, j int) bool {
		if records[i].BlockHeight != records[j].BlockHeight {
			return records[i].BlockHeight < records[j].BlockHeight
		}
		if records[i].Address != records[j].Address {
			return records[i].Address < records[j].Address
		}
		if records[i].NodeID != records[j].NodeID {
			return records[i].NodeID < records[j].NodeID
		}
		return records[i].Model < records[j].Model
	})

	// Assuming all for same block (if not, we'd need multiple ensurePartition calls)
	// Just call for all distinct heights seen
	seenHeights := make(map[int64]bool)
	for i := range records {
		r := &records[i]
		if !seenHeights[r.BlockHeight] {
			if err := s.ensurePartition(ctx, r.BlockHeight); err != nil {
				return nil, err
			}
			seenHeights[r.BlockHeight] = true
		}
		if r.ReceivedAt.IsZero() {
			r.ReceivedAt = time.Now().UTC()
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Bulk Lock State
	// Use unnest to select multiple rows
	var (
		heights []int64
		addrs   []string
		nodes   []string
		models  []string
	)
	for _, r := range records {
		heights = append(heights, r.BlockHeight)
		addrs = append(addrs, r.Address)
		nodes = append(nodes, r.NodeID)
		models = append(models, r.Model)
	}

	rows, err := tx.Query(ctx, `
SELECT block_height, address, node_id, model, amount, hash 
FROM poc_mlnode_state
WHERE (block_height, address, node_id, model) IN (
	SELECT * FROM unnest($1::bigint[], $2::text[], $3::text[], $4::text[])
)
FOR UPDATE
`, heights, addrs, nodes, models)
	if err != nil {
		return nil, fmt.Errorf("select state batch: %w", err)
	}

	stateMap := make(map[string]PoCMlnodeState)
	for rows.Next() {
		var s PoCMlnodeState
		if err := rows.Scan(&s.BlockHeight, &s.Address, &s.NodeID, &s.Model, &s.Amount, &s.Hash); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan state: %w", err)
		}
		key := fmt.Sprintf("%d:%s:%s:%s", s.BlockHeight, s.Address, s.NodeID, s.Model)
		stateMap[key] = s
	}
	rows.Close()

	// Process Records
	var outRecords []PoCBatchesGeneratedRecord
	batch := &pgx.Batch{}

	for _, rec := range records {
		if err := validateRecordKey(rec); err != nil {
			return nil, err
		}

		key := fmt.Sprintf("%d:%s:%s:%s", rec.BlockHeight, rec.Address, rec.NodeID, rec.Model)
		state, exists := stateMap[key]
		if !exists {
			state = PoCMlnodeState{
				BlockHeight: rec.BlockHeight,
				Address:     rec.Address,
				NodeID:      rec.NodeID,
				Model:       rec.Model,
				Amount:      0,
				Hash:        "",
			}
		}

		var newAmount int64
		var newHash string
		var errState error

		var batchHash string
		if len(rec.Artifacts) > 0 {
			batchHash = computeBatchHash(rec.Artifacts)
			newAmount = state.Amount + int64(len(rec.Artifacts))
			newHash, errState = computeRollingHash(state.Hash, batchHash, newAmount)
		} else {
			newAmount = rec.Amount
			newHash = rec.Hash
			errState = nil
		}
		if errState != nil {
			return nil, errState
		}

		rec.Amount = newAmount
		rec.Hash = newHash

		outRecords = append(outRecords, rec)

		// Queue State Upsert
		batch.Queue(`
INSERT INTO poc_mlnode_state (
  block_height, address, node_id, model,
  amount, hash, updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7)
ON CONFLICT (block_height, address, node_id, model) DO UPDATE SET
  amount = EXCLUDED.amount,
  hash = EXCLUDED.hash,
  updated_at = EXCLUDED.updated_at
`, rec.BlockHeight, rec.Address, rec.NodeID, rec.Model, newAmount, newHash, rec.ReceivedAt)

		// Queue Record Insert
		noncesJSON, err := jsonMarshal(rec.Artifacts)
		if err != nil {
			return nil, err
		}

		batch.Queue(`
INSERT INTO poc_batches_generated (
    block_height, address, public_key, block_hash,
    node_id, model, amount, hash, time_since_block,
    nonces_json, received_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
`, rec.BlockHeight, rec.Address, rec.PublicKey, rec.BlockHash,
			rec.NodeID, rec.Model, rec.Amount, rec.Hash, rec.TimeSinceBlock,
			noncesJSON, rec.ReceivedAt)
	}

	br := tx.SendBatch(ctx, batch)
	if err := br.Close(); err != nil {
		return nil, fmt.Errorf("batch execution failed: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	return outRecords, nil
}

func (s *PostgresStorage) ListGeneratedRecords(ctx context.Context, blockHeight int64) ([]PoCBatchesGeneratedRecord, error) {
	rows, err := s.pool.Query(ctx, `
SELECT block_height, address, public_key, block_hash, node_id,
       model, amount, hash, time_since_block, nonces_json, received_at
FROM poc_batches_generated
WHERE block_height = $1
ORDER BY received_at ASC
`, blockHeight)
	if err != nil {
		return nil, fmt.Errorf("select poc_batches_generated: %w", err)
	}
	defer rows.Close()

	out := make([]PoCBatchesGeneratedRecord, 0)
	for rows.Next() {
		var rec PoCBatchesGeneratedRecord
		var noncesJSON []byte
		if err := rows.Scan(&rec.BlockHeight, &rec.Address, &rec.PublicKey, &rec.BlockHash, &rec.NodeID,
			&rec.Model, &rec.Amount, &rec.Hash, &rec.TimeSinceBlock, &noncesJSON, &rec.ReceivedAt); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		if err := jsonUnmarshal(noncesJSON, &rec.Artifacts); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	return out, nil
}

func (s *PostgresStorage) Close() {
	s.pool.Close()
}

var _ PoCStorage = (*PostgresStorage)(nil)
