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
// - records: {baseDir}/records/{blockHeight}/{timestamp_nanos}.json
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

func (f *FileStorage) recordsDir(blockHeight int64) string {
	return filepath.Join(f.baseDir, "records", strconv.FormatInt(blockHeight, 10))
}

func (f *FileStorage) stateDir(blockHeight int64) string {
	return filepath.Join(f.baseDir, "state", strconv.FormatInt(blockHeight, 10))
}

func safePathSegment(s string) string {
	// Avoid path traversal / separators from model strings etc.
	return strings.ReplaceAll(s, "/", "_")
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

	batchHash := computeBatchHash(rec.Artifacts)
	newAmount := state.Amount + int64(len(rec.Artifacts))
	newHash, err := computeRollingHash(state.Hash, batchHash, newAmount)
	if err != nil {
		return PoCBatchesGeneratedRecord{}, err
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
	rec.Amount = newAmount
	rec.Hash = newHash
	dir := f.recordsDir(rec.BlockHeight)
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

func (f *FileStorage) ListGeneratedRecords(ctx context.Context, blockHeight int64) ([]PoCBatchesGeneratedRecord, error) {
	_ = ctx
	dir := f.recordsDir(blockHeight)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("readdir: %w", err)
	}
	records := make([]PoCBatchesGeneratedRecord, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var rec PoCBatchesGeneratedRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			continue
		}
		records = append(records, rec)
	}
	if len(records) == 0 {
		return nil, ErrNotFound
	}
	return records, nil
}

var _ PoCStorage = (*FileStorage)(nil)
