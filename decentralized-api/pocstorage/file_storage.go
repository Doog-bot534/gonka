package pocstorage

import (
	"context"
	"decentralized-api/logging"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/productscience/inference/x/inference/types"
)

// File layout:
// - runs:    {baseDir}/runs/{blockHeight}.json
// - records: {baseDir}/records/{blockHeight}/{address}/{timestamp_nanos}.json
type FileStorage struct {
	baseDir string
	locks   sync.Map // key -> *sync.Mutex
}

func NewFileStorage(baseDir string) *FileStorage {
	return &FileStorage{baseDir: baseDir}
}

func (f *FileStorage) runsDir() string {
	return filepath.Join(f.baseDir, "runs")
}

func safePathSegment(s string) string {
	// Avoid path traversal / separators from model strings etc.
	return strings.ReplaceAll(s, "/", "_")
}

func (f *FileStorage) recordsDir(blockHeight int64, address string) string {
	return filepath.Join(f.baseDir, "records", strconv.FormatInt(blockHeight, 10), safePathSegment(address))
}

func (f *FileStorage) recordsBlockDir(blockHeight int64) string {
	return filepath.Join(f.baseDir, "records", strconv.FormatInt(blockHeight, 10))
}

func (f *FileStorage) stateDir(blockHeight int64) string {
	return filepath.Join(f.baseDir, "state", strconv.FormatInt(blockHeight, 10))
}

func (f *FileStorage) statePath(blockHeight int64, address, nodeID, model string) string {
	return filepath.Join(
		f.stateDir(blockHeight),
		safePathSegment(address),
		safePathSegment(nodeID),
		safePathSegment(model)+".json",
	)
}

func (f *FileStorage) lockFor(key string) *sync.Mutex {
	v, _ := f.locks.LoadOrStore(key, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func atomicWriteJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

func (f *FileStorage) UpsertRun(ctx context.Context, run PoCRun) error {
	_ = ctx
	if err := os.MkdirAll(f.runsDir(), 0755); err != nil {
		return fmt.Errorf("mkdir runs dir: %w", err)
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Now().UTC()
	}
	path := filepath.Join(f.runsDir(), strconv.FormatInt(run.BlockHeight, 10)+".json")
	return atomicWriteJSON(path, run)
}

func (f *FileStorage) MarkInterrupted(ctx context.Context, blockHeight int64, interruptedAt time.Time) error {
	_ = ctx
	run, err := f.GetClosestRunAtOrBefore(context.Background(), blockHeight)
	if err != nil {
		return err
	}
	// Only mark if it matches exactly (best-effort).
	if run.BlockHeight != blockHeight {
		return nil
	}
	run.InterruptedTime = &interruptedAt
	return f.UpsertRun(context.Background(), run)
}

func (f *FileStorage) GetLatestRun(ctx context.Context) (PoCRun, error) {
	_ = ctx
	dir := f.runsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return PoCRun{}, ErrNotFound
		}
		return PoCRun{}, fmt.Errorf("readdir: %w", err)
	}
	var best int64 = -1
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if filepath.Ext(name) != ".json" {
			continue
		}
		h, err := strconv.ParseInt(name[:len(name)-len(".json")], 10, 64)
		if err != nil {
			continue
		}
		if h > best {
			best = h
		}
	}
	if best < 0 {
		return PoCRun{}, ErrNotFound
	}
	return f.GetClosestRunAtOrBefore(context.Background(), best)
}

func (f *FileStorage) GetClosestRunAtOrBefore(ctx context.Context, blockHeight int64) (PoCRun, error) {
	_ = ctx
	dir := f.runsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return PoCRun{}, ErrNotFound
		}
		return PoCRun{}, fmt.Errorf("readdir: %w", err)
	}
	var candidates []int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if filepath.Ext(name) != ".json" {
			continue
		}
		h, err := strconv.ParseInt(name[:len(name)-len(".json")], 10, 64)
		if err != nil {
			continue
		}
		if h <= blockHeight {
			candidates = append(candidates, h)
		}
	}
	if len(candidates) == 0 {
		return PoCRun{}, ErrNotFound
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i] > candidates[j] })
	path := filepath.Join(dir, strconv.FormatInt(candidates[0], 10)+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return PoCRun{}, ErrNotFound
		}
		return PoCRun{}, fmt.Errorf("readfile: %w", err)
	}
	var run PoCRun
	if err := json.Unmarshal(data, &run); err != nil {
		return PoCRun{}, fmt.Errorf("unmarshal run: %w", err)
	}
	return run, nil
}

func (f *FileStorage) StoreGeneratedRecord(ctx context.Context, rec PoCBatchesGeneratedRecord) (PoCBatchesGeneratedRecord, error) {
	_ = ctx
	if err := validateRecordKey(rec); err != nil {
		return PoCBatchesGeneratedRecord{}, err
	}

	lockKey := fmt.Sprintf("%d:%s:%s:%s", rec.BlockHeight, rec.Address, rec.NodeID, rec.Model)
	mu := f.lockFor(lockKey)
	mu.Lock()
	defer mu.Unlock()

	if rec.ReceivedAt.IsZero() {
		rec.ReceivedAt = time.Now().UTC()
	}

	// Update rolling per-mlnode state (amount/hash) without rereading all records.
	statePath := f.statePath(rec.BlockHeight, rec.Address, rec.NodeID, rec.Model)
	var state PoCMlnodeState
	stateBytes, err := os.ReadFile(statePath)
	if err == nil {
		if err := json.Unmarshal(stateBytes, &state); err != nil {
			return PoCBatchesGeneratedRecord{}, fmt.Errorf("unmarshal state: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return PoCBatchesGeneratedRecord{}, fmt.Errorf("read state: %w", err)
	} else {
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

	if len(rec.Artifacts) > 0 {
		// Peer (or local) with artifacts: compute hash from artifacts.
		batchHash := computeBatchHash(rec.Artifacts)
		newAmount = state.Amount + int64(len(rec.Artifacts))
		newHash, errState = computeRollingHash(state.Hash, batchHash, newAmount)
	} else {
		// Peer without artifacts: trust the provided amount/hash.
		// Note: We blindly accept the new state as "current" because we can't verify it incrementally without artifacts.
		// In a real verification scenario, we might want to check if it's strictly increasing, but for now we trust the peer's latest claim.
		newAmount = rec.Amount
		newHash = rec.Hash
		errState = nil
	}

	if errState != nil {
		return PoCBatchesGeneratedRecord{}, errState
	}

	state.Amount = newAmount
	state.Hash = newHash
	state.UpdatedAt = rec.ReceivedAt

	// Ensure state dirs exist and persist state atomically.
	if err := os.MkdirAll(filepath.Dir(statePath), 0755); err != nil {
		return PoCBatchesGeneratedRecord{}, fmt.Errorf("mkdir state dir: %w", err)
	}
	if err := atomicWriteJSON(statePath, state); err != nil {
		return PoCBatchesGeneratedRecord{}, fmt.Errorf("write state: %w", err)
	}

	// Write record with computed snapshot.
	// For peer records without artifacts, Artifacts is empty, which is fine (we store the metadata/proof of receipt).
	rec.Amount = newAmount
	rec.Hash = newHash

	// Separation by address: records/{blockHeight}/{address}/{timestamp}.json
	dir := f.recordsDir(rec.BlockHeight, rec.Address)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return PoCBatchesGeneratedRecord{}, fmt.Errorf("mkdir records dir: %w", err)
	}
	path := filepath.Join(dir, strconv.FormatInt(rec.ReceivedAt.UnixNano(), 10)+".json")
	logging.Debug("Storing PoC generated record", types.PoC, "path", path, "blockHeight", rec.BlockHeight, "nodeId", rec.NodeID)
	if err := atomicWriteJSON(path, rec); err != nil {
		return PoCBatchesGeneratedRecord{}, err
	}
	return rec, nil
}

func (f *FileStorage) StoreGeneratedRecordsBatch(ctx context.Context, records []PoCBatchesGeneratedRecord) ([]PoCBatchesGeneratedRecord, error) {
	// For file storage, locking is granular per file (per address/node/model).
	// So we can simply iterate and call StoreGeneratedRecord.
	// Optimizing this further would require architectural changes to file layout or locking.
	var out []PoCBatchesGeneratedRecord
	for _, rec := range records {
		updated, err := f.StoreGeneratedRecord(ctx, rec)
		if err != nil {
			return nil, err
		}
		out = append(out, updated)
	}
	return out, nil
}

func (f *FileStorage) ListGeneratedRecords(ctx context.Context, blockHeight int64) ([]PoCBatchesGeneratedRecord, error) {
	_ = ctx
	// Root dir for this block: records/{blockHeight}
	rootDir := f.recordsBlockDir(blockHeight)

	// Check if root dir exists
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("readdir block root: %w", err)
	}

	var records []PoCBatchesGeneratedRecord

	// Iterate over address directories
	for _, addressEntry := range entries {
		if !addressEntry.IsDir() {
			continue
		}
		addressDir := filepath.Join(rootDir, addressEntry.Name())

		fileEntries, err := os.ReadDir(addressDir)
		if err != nil {
			continue
		}

		for _, e := range fileEntries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
				continue
			}
			data, err := os.ReadFile(filepath.Join(addressDir, e.Name()))
			if err != nil {
				continue
			}
			var rec PoCBatchesGeneratedRecord
			if err := json.Unmarshal(data, &rec); err != nil {
				continue
			}
			records = append(records, rec)
		}
	}

	if len(records) == 0 {
		return nil, ErrNotFound
	}

	// Sort by received_at to maintain consistent order
	sort.Slice(records, func(i, j int) bool {
		return records[i].ReceivedAt.Before(records[j].ReceivedAt)
	})

	return records, nil
}

var _ PoCStorage = (*FileStorage)(nil)
