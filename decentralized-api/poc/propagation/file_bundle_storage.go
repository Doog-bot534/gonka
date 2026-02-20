package propagation

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

const bundleShardSize = 100

type FileBundleStorage struct {
	baseDir             string
	bundles             sync.Map
	arrivals            sync.Map
	pendingArrivals     []firstArrivalEntry // unflushed arrivals, guarded by arrivalsMu
	arrivalsMu          sync.Mutex
	arrivalShardCounter int // next arrivals shard index, guarded by arrivalsMu
	writeMu             sync.Mutex
	shardCounters       map[int64]int // height -> next bundle shard index, guarded by writeMu
}

func NewFileBundleStorage(baseDir string) (*FileBundleStorage, error) {
	if baseDir == "" {
		return nil, fmt.Errorf("baseDir cannot be empty")
	}

	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("create base directory: %w", err)
	}

	s := &FileBundleStorage{
		baseDir:       baseDir,
		shardCounters: make(map[int64]int),
	}

	if err := s.loadBundles(); err != nil {
		return nil, fmt.Errorf("load bundles: %w", err)
	}

	logging.Info("File bundle storage initialized", types.PoC, "baseDir", baseDir)
	return s, nil
}

func (s *FileBundleStorage) shardFilePath(pocHeight int64, idx int) string {
	return filepath.Join(s.baseDir, fmt.Sprintf("height_%d_%04d.jsonl", pocHeight, idx))
}

func (s *FileBundleStorage) legacyHeightFilePath(pocHeight int64) string {
	return filepath.Join(s.baseDir, fmt.Sprintf("height_%d.jsonl", pocHeight))
}

func (s *FileBundleStorage) arrivalShardFilePath(idx int) string {
	return filepath.Join(s.baseDir, fmt.Sprintf("arrivals_%04d.jsonl", idx))
}

func (s *FileBundleStorage) legacyArrivalsFilePath() string {
	return filepath.Join(s.baseDir, "arrivals.json")
}

type firstArrivalEntry struct {
	Participant string `json:"participant"`
	PocHeight   int64  `json:"poc_height"`
	ArrivalTime int64  `json:"arrival_time"`
	Count       uint32 `json:"count"`
}

func (s *FileBundleStorage) loadBundles() error {
	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read directory: %w", err)
	}

	bundleCount := 0

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		ext := filepath.Ext(name)

		switch ext {
		case ".json":
			if name == "arrivals.json" {
				continue
			}
			filePath := filepath.Join(s.baseDir, name)
			data, err := os.ReadFile(filePath)
			if err != nil {
				logging.Warn("Failed to read bundle file", types.PoC, "file", name, "error", err)
				continue
			}
			var header BundleHeader
			if err := json.Unmarshal(data, &header); err != nil {
				logging.Warn("Failed to unmarshal bundle header", types.PoC, "file", name, "error", err)
				continue
			}
			s.bundles.LoadOrStore(header.BundleID, header)
			bundleCount++

		case ".jsonl":
			if strings.HasPrefix(name, "arrivals_") {
				continue
			}
			filePath := filepath.Join(s.baseDir, name)
			count := s.loadJSONLFile(filePath, name)
			bundleCount += count

			// Update shard counter from sharded filenames: height_<N>_<idx>.jsonl
			base := strings.TrimSuffix(name, ".jsonl")
			parts := strings.SplitN(base, "_", 3)
			// parts[0]="height", parts[1]="<pocHeight>", parts[2]="<idx>" (optional)
			if len(parts) == 3 && parts[0] == "height" {
				if height, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
					if idx, err := strconv.Atoi(parts[2]); err == nil {
						if idx+1 > s.shardCounters[height] {
							s.shardCounters[height] = idx + 1
						}
					}
				}
			}
		}
	}

	arrivalCount := 0
	if err := s.loadArrivals(&arrivalCount); err != nil {
		logging.Warn("Failed to load arrivals", types.PoC, "error", err)
	}

	logging.Info("Loaded bundles from disk", types.PoC, "count", bundleCount, "arrivals", arrivalCount)
	return nil
}

func (s *FileBundleStorage) loadJSONLFile(filePath, name string) int {
	f, err := os.Open(filePath)
	if err != nil {
		logging.Warn("Failed to open JSONL file", types.PoC, "file", name, "error", err)
		return 0
	}
	defer f.Close()

	count := 0
	scanner := bufio.NewScanner(f)
	const maxLineSize = 4 * 1024 * 1024
	buf := make([]byte, maxLineSize)
	scanner.Buffer(buf, maxLineSize)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var header BundleHeader
		if err := json.Unmarshal(line, &header); err != nil {
			logging.Warn("Failed to unmarshal JSONL header", types.PoC, "file", name, "error", err)
			continue
		}
		s.bundles.LoadOrStore(header.BundleID, header)
		count++
	}
	if err := scanner.Err(); err != nil {
		logging.Warn("JSONL scanner error", types.PoC, "file", name, "error", err)
	}
	return count
}

func (s *FileBundleStorage) loadArrivals(count *int) error {
	// Load sharded arrivals_XXXX.jsonl files
	pattern := filepath.Join(s.baseDir, "arrivals_*.jsonl")
	matches, _ := filepath.Glob(pattern)
	for _, filePath := range matches {
		name := filepath.Base(filePath)
		base := strings.TrimSuffix(name, ".jsonl")
		// arrivals_XXXX → extract XXXX
		parts := strings.SplitN(base, "_", 2)
		if len(parts) == 2 {
			if idx, err := strconv.Atoi(parts[1]); err == nil {
				if idx+1 > s.arrivalShardCounter {
					s.arrivalShardCounter = idx + 1
				}
			}
		}
		s.loadArrivalJSONLFile(filePath, name, count)
	}

	// Legacy arrivals.json backward compat
	legacyPath := s.legacyArrivalsFilePath()
	data, err := os.ReadFile(legacyPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read legacy arrivals file: %w", err)
	}

	var entries []firstArrivalEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("unmarshal legacy arrivals: %w", err)
	}

	for _, entry := range entries {
		key := participantPocKey{Participant: entry.Participant, PocHeight: entry.PocHeight}
		s.arrivals.LoadOrStore(key, ArrivalInfo{Time: entry.ArrivalTime, Count: entry.Count})
		*count++
	}

	return nil
}

func (s *FileBundleStorage) loadArrivalJSONLFile(filePath, name string, count *int) {
	f, err := os.Open(filePath)
	if err != nil {
		logging.Warn("Failed to open arrivals JSONL shard", types.PoC, "file", name, "error", err)
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	const maxLineSize = 512 * 1024
	buf := make([]byte, maxLineSize)
	scanner.Buffer(buf, maxLineSize)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry firstArrivalEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			logging.Warn("Failed to unmarshal arrival entry", types.PoC, "file", name, "error", err)
			continue
		}
		key := participantPocKey{Participant: entry.Participant, PocHeight: entry.PocHeight}
		s.arrivals.LoadOrStore(key, ArrivalInfo{Time: entry.ArrivalTime, Count: entry.Count})
		*count++
	}
	if err := scanner.Err(); err != nil {
		logging.Warn("Arrivals JSONL scanner error", types.PoC, "file", name, "error", err)
	}
}

func (s *FileBundleStorage) StoreHeader(ctx context.Context, h BundleHeader) error {
	return s.StoreHeaderBatch(ctx, []BundleHeader{h})
}

func (s *FileBundleStorage) StoreHeaderBatch(ctx context.Context, headers []BundleHeader) error {
	if len(headers) == 0 {
		return nil
	}

	byHeight := make(map[int64][]BundleHeader)
	for _, h := range headers {
		if _, exists := s.bundles.LoadOrStore(h.BundleID, h); !exists {
			byHeight[h.PocHeight] = append(byHeight[h.PocHeight], h)
		}
	}

	if len(byHeight) == 0 {
		return nil
	}

	if err := os.MkdirAll(s.baseDir, 0755); err != nil {
		return fmt.Errorf("ensure directory: %w", err)
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	for height, hdrs := range byHeight {
		for len(hdrs) > 0 {
			chunkSize := len(hdrs)
			if chunkSize > bundleShardSize {
				chunkSize = bundleShardSize
			}
			chunk := hdrs[:chunkSize]
			hdrs = hdrs[chunkSize:]

			idx := s.shardCounters[height]
			s.shardCounters[height]++

			if err := s.writeJSONLShard(s.shardFilePath(height, idx), chunk); err != nil {
				logging.Warn("FileBundleStorage: failed to write shard", types.PoC,
					"height", height, "shard", idx, "error", err)
			}
		}
	}

	return nil
}

func (s *FileBundleStorage) writeJSONLShard(filePath string, headers []BundleHeader) error {
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("open shard file: %w", err)
	}
	defer f.Close()

	w := bufio.NewWriterSize(f, 256*1024)
	for _, h := range headers {
		line, err := json.Marshal(h)
		if err != nil {
			continue
		}
		w.Write(line)
		w.WriteByte('\n')
	}

	if err := w.Flush(); err != nil {
		return fmt.Errorf("flush shard: %w", err)
	}

	if err := f.Sync(); err != nil {
		return fmt.Errorf("fsync shard: %w", err)
	}

	return nil
}

func (s *FileBundleStorage) GetHeader(ctx context.Context, bundleID [4]byte) (BundleHeader, error) {
	val, exists := s.bundles.Load(bundleID)
	if !exists {
		return BundleHeader{}, ErrBundleNotFound
	}
	return val.(BundleHeader), nil
}

func (s *FileBundleStorage) LatestBundle(ctx context.Context, participant string, pocHeight int64) (BundleHeader, error) {
	var latest BundleHeader
	var found bool

	s.bundles.Range(func(key, val interface{}) bool {
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

func (s *FileBundleStorage) AllBundlesForHeight(ctx context.Context, pocHeight int64) ([]BundleHeader, error) {
	result := make([]BundleHeader, 0)
	s.bundles.Range(func(key, val interface{}) bool {
		header := val.(BundleHeader)
		if header.PocHeight == pocHeight {
			result = append(result, header)
		}
		return true
	})
	return result, nil
}

func (s *FileBundleStorage) StoreFirstArrival(ctx context.Context, participant string, pocHeight int64, arrivalTime int64, count uint32) error {
	key := participantPocKey{Participant: participant, PocHeight: pocHeight}
	info := ArrivalInfo{Time: arrivalTime, Count: count}
	if _, exists := s.arrivals.LoadOrStore(key, info); !exists {
		s.arrivalsMu.Lock()
		s.pendingArrivals = append(s.pendingArrivals, firstArrivalEntry{
			Participant: participant,
			PocHeight:   pocHeight,
			ArrivalTime: arrivalTime,
			Count:       count,
		})
		s.arrivalsMu.Unlock()
	}
	return nil
}

func (s *FileBundleStorage) StoreFirstArrivalBatch(ctx context.Context, arrivals []ArrivalInfo, participants []string, pocHeights []int64) error {
	if len(arrivals) == 0 || len(participants) != len(arrivals) || len(pocHeights) != len(arrivals) {
		return nil
	}

	var newEntries []firstArrivalEntry
	for i := range arrivals {
		key := participantPocKey{Participant: participants[i], PocHeight: pocHeights[i]}
		if _, exists := s.arrivals.LoadOrStore(key, arrivals[i]); !exists {
			newEntries = append(newEntries, firstArrivalEntry{
				Participant: participants[i],
				PocHeight:   pocHeights[i],
				ArrivalTime: arrivals[i].Time,
				Count:       arrivals[i].Count,
			})
		}
	}

	if len(newEntries) > 0 {
		s.arrivalsMu.Lock()
		s.pendingArrivals = append(s.pendingArrivals, newEntries...)
		s.arrivalsMu.Unlock()
	}

	return nil
}

func (s *FileBundleStorage) FlushArrivals() error {
	s.arrivalsMu.Lock()
	if len(s.pendingArrivals) == 0 {
		s.arrivalsMu.Unlock()
		return nil
	}
	pending := s.pendingArrivals
	s.pendingArrivals = nil
	s.arrivalsMu.Unlock()

	for len(pending) > 0 {
		chunkSize := len(pending)
		if chunkSize > bundleShardSize {
			chunkSize = bundleShardSize
		}
		chunk := pending[:chunkSize]
		pending = pending[chunkSize:]

		s.arrivalsMu.Lock()
		idx := s.arrivalShardCounter
		s.arrivalShardCounter++
		s.arrivalsMu.Unlock()

		if err := s.writeArrivalJSONLShard(s.arrivalShardFilePath(idx), chunk); err != nil {
			logging.Warn("FileBundleStorage: failed to write arrivals shard", types.PoC,
				"shard", idx, "error", err)
		}
	}

	return nil
}

func (s *FileBundleStorage) writeArrivalJSONLShard(filePath string, entries []firstArrivalEntry) error {
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("open arrivals shard: %w", err)
	}
	defer f.Close()

	w := bufio.NewWriterSize(f, 64*1024)
	for _, e := range entries {
		line, err := json.Marshal(e)
		if err != nil {
			continue
		}
		w.Write(line)
		w.WriteByte('\n')
	}

	if err := w.Flush(); err != nil {
		return fmt.Errorf("flush arrivals shard: %w", err)
	}

	if err := f.Sync(); err != nil {
		return fmt.Errorf("fsync arrivals shard: %w", err)
	}

	return nil
}

func (s *FileBundleStorage) GetFirstArrival(ctx context.Context, participant string, pocHeight int64) (ArrivalInfo, error) {
	key := participantPocKey{Participant: participant, PocHeight: pocHeight}
	val, exists := s.arrivals.Load(key)
	if !exists {
		return ArrivalInfo{}, ErrArrivalNotFound
	}
	return val.(ArrivalInfo), nil
}

func (s *FileBundleStorage) GetAllFirstArrivals(ctx context.Context, pocHeight int64) (map[string]ArrivalInfo, error) {
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

func (s *FileBundleStorage) CleanupOldHeights(ctx context.Context, retainCount int) error {
	heights := make(map[int64]struct{})
	s.bundles.Range(func(key, val interface{}) bool {
		header := val.(BundleHeader)
		heights[header.PocHeight] = struct{}{}
		return true
	})
	s.arrivals.Range(func(key, val interface{}) bool {
		k := key.(participantPocKey)
		heights[k.PocHeight] = struct{}{}
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
		s.bundles.Range(func(key, val interface{}) bool {
			bundleID := key.([4]byte)
			header := val.(BundleHeader)
			if header.PocHeight == height {
				// Remove old single-bundle .json files if any remain
				oldPath := filepath.Join(s.baseDir, fmt.Sprintf("%x.json", bundleID))
				if err := os.Remove(oldPath); err != nil && !os.IsNotExist(err) {
					logging.Warn("Failed to remove bundle file", types.PoC,
						"bundleID", bundleID, "pocHeight", height, "error", err)
				}
				s.bundles.Delete(bundleID)
			}
			return true
		})

		// Remove legacy single-file format
		legacyPath := s.legacyHeightFilePath(height)
		if err := os.Remove(legacyPath); err != nil && !os.IsNotExist(err) {
			logging.Warn("Failed to remove legacy JSONL file", types.PoC,
				"pocHeight", height, "error", err)
		}

		// Remove all shard files for this height
		pattern := filepath.Join(s.baseDir, fmt.Sprintf("height_%d_*.jsonl", height))
		matches, _ := filepath.Glob(pattern)
		for _, match := range matches {
			if err := os.Remove(match); err != nil && !os.IsNotExist(err) {
				logging.Warn("Failed to remove shard file", types.PoC,
					"file", match, "pocHeight", height, "error", err)
			}
		}

		s.arrivals.Range(func(k, val interface{}) bool {
			key := k.(participantPocKey)
			if key.PocHeight == height {
				s.arrivals.Delete(key)
			}
			return true
		})

		// Reset shard counter for this height
		s.writeMu.Lock()
		delete(s.shardCounters, height)
		s.writeMu.Unlock()

		logging.Info("Cleaned up propagation data for PoC height", types.PoC, "pocHeight", height)
	}

	// Remove all existing arrivals shards and rewrite survivors
	pattern := filepath.Join(s.baseDir, "arrivals_*.jsonl")
	oldShards, _ := filepath.Glob(pattern)
	for _, f := range oldShards {
		os.Remove(f)
	}
	// Remove legacy arrivals.json if present
	os.Remove(s.legacyArrivalsFilePath())

	// Collect remaining arrivals and rewrite to fresh shards
	remaining := make([]firstArrivalEntry, 0)
	s.arrivals.Range(func(key, val interface{}) bool {
		k := key.(participantPocKey)
		info := val.(ArrivalInfo)
		remaining = append(remaining, firstArrivalEntry{
			Participant: k.Participant,
			PocHeight:   k.PocHeight,
			ArrivalTime: info.Time,
			Count:       info.Count,
		})
		return true
	})

	s.arrivalsMu.Lock()
	s.arrivalShardCounter = 0
	s.pendingArrivals = nil
	s.arrivalsMu.Unlock()

	for len(remaining) > 0 {
		chunkSize := len(remaining)
		if chunkSize > bundleShardSize {
			chunkSize = bundleShardSize
		}
		chunk := remaining[:chunkSize]
		remaining = remaining[chunkSize:]

		s.arrivalsMu.Lock()
		idx := s.arrivalShardCounter
		s.arrivalShardCounter++
		s.arrivalsMu.Unlock()

		if err := s.writeArrivalJSONLShard(s.arrivalShardFilePath(idx), chunk); err != nil {
			logging.Warn("CleanupOldHeights: failed to write arrivals shard", types.PoC,
				"shard", idx, "error", err)
		}
	}

	return nil
}

func (s *FileBundleStorage) Close() error {
	return nil
}

var _ BundleStorage = (*FileBundleStorage)(nil)
