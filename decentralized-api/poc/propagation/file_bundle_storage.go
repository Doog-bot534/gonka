package propagation

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

type FileBundleStorage struct {
	baseDir  string
	mu       sync.RWMutex
	bundles  map[[4]byte]BundleHeader
	arrivals map[participantPocKey]ArrivalInfo
}

func NewFileBundleStorage(baseDir string) (*FileBundleStorage, error) {
	if baseDir == "" {
		return nil, fmt.Errorf("baseDir cannot be empty")
	}

	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("create base directory: %w", err)
	}

	s := &FileBundleStorage{
		baseDir:  baseDir,
		bundles:  make(map[[4]byte]BundleHeader),
		arrivals: make(map[participantPocKey]ArrivalInfo),
	}

	if err := s.loadBundles(); err != nil {
		return nil, fmt.Errorf("load bundles: %w", err)
	}

	logging.Info("File bundle storage initialized", types.PoC, "baseDir", baseDir)
	return s, nil
}

func (s *FileBundleStorage) bundleFilePath(bundleID [4]byte) string {
	filename := hex.EncodeToString(bundleID[:]) + ".json"
	return filepath.Join(s.baseDir, filename)
}

func (s *FileBundleStorage) arrivalsFilePath() string {
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

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		name := entry.Name()
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

		s.bundles[header.BundleID] = header
	}

	if err := s.loadArrivals(); err != nil {
		logging.Warn("Failed to load arrivals", types.PoC, "error", err)
	}

	logging.Info("Loaded bundles from disk", types.PoC, "count", len(s.bundles), "arrivals", len(s.arrivals))
	return nil
}

func (s *FileBundleStorage) loadArrivals() error {
	filePath := s.arrivalsFilePath()
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read arrivals file: %w", err)
	}

	var entries []firstArrivalEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("unmarshal arrivals: %w", err)
	}

	for _, entry := range entries {
		key := participantPocKey{Participant: entry.Participant, PocHeight: entry.PocHeight}
		s.arrivals[key] = ArrivalInfo{Time: entry.ArrivalTime, Count: entry.Count}
	}

	return nil
}

func (s *FileBundleStorage) StoreHeader(ctx context.Context, h BundleHeader) error {
	s.mu.Lock()
	if _, exists := s.bundles[h.BundleID]; exists {
		s.mu.Unlock()
		return nil
	}
	s.bundles[h.BundleID] = h
	s.mu.Unlock()

	data, err := json.Marshal(h)
	if err != nil {
		s.mu.Lock()
		delete(s.bundles, h.BundleID)
		s.mu.Unlock()
		return fmt.Errorf("marshal header: %w", err)
	}

	if err := os.MkdirAll(s.baseDir, 0755); err != nil {
		s.mu.Lock()
		delete(s.bundles, h.BundleID)
		s.mu.Unlock()
		return fmt.Errorf("ensure directory exists: %w", err)
	}

	filePath := s.bundleFilePath(h.BundleID)
	tempPath := filePath + ".tmp"

	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		s.mu.Lock()
		delete(s.bundles, h.BundleID)
		s.mu.Unlock()
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := os.Rename(tempPath, filePath); err != nil {
		os.Remove(tempPath)
		s.mu.Lock()
		delete(s.bundles, h.BundleID)
		s.mu.Unlock()
		return fmt.Errorf("rename to target: %w", err)
	}

	return nil
}

func (s *FileBundleStorage) GetHeader(ctx context.Context, bundleID [4]byte) (BundleHeader, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	header, exists := s.bundles[bundleID]
	if !exists {
		return BundleHeader{}, ErrBundleNotFound
	}
	return header, nil
}

func (s *FileBundleStorage) LatestBundle(ctx context.Context, participant string, pocHeight int64) (BundleHeader, error) {
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

func (s *FileBundleStorage) AllBundlesForHeight(ctx context.Context, pocHeight int64) ([]BundleHeader, error) {
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

func (s *FileBundleStorage) StoreFirstArrival(ctx context.Context, participant string, pocHeight int64, arrivalTime int64, count uint32) error {
	s.mu.Lock()
	key := participantPocKey{Participant: participant, PocHeight: pocHeight}
	if _, exists := s.arrivals[key]; exists {
		s.mu.Unlock()
		return nil
	}
	s.arrivals[key] = ArrivalInfo{Time: arrivalTime, Count: count}
	entries := make([]firstArrivalEntry, 0, len(s.arrivals))
	for k, info := range s.arrivals {
		entries = append(entries, firstArrivalEntry{
			Participant: k.Participant,
			PocHeight:   k.PocHeight,
			ArrivalTime: info.Time,
			Count:       info.Count,
		})
	}
	s.mu.Unlock()

	data, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("marshal arrivals: %w", err)
	}

	filePath := s.arrivalsFilePath()
	tempPath := filePath + ".tmp"

	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := os.Rename(tempPath, filePath); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("rename to target: %w", err)
	}

	return nil
}

func (s *FileBundleStorage) GetFirstArrival(ctx context.Context, participant string, pocHeight int64) (ArrivalInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := participantPocKey{Participant: participant, PocHeight: pocHeight}
	info, exists := s.arrivals[key]
	if !exists {
		return ArrivalInfo{}, ErrArrivalNotFound
	}
	return info, nil
}

func (s *FileBundleStorage) GetAllFirstArrivals(ctx context.Context, pocHeight int64) (map[string]ArrivalInfo, error) {
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

func (s *FileBundleStorage) CleanupOldHeights(ctx context.Context, retainCount int) error {
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
		for bundleID, header := range s.bundles {
			if header.PocHeight == height {
				filePath := s.bundleFilePath(bundleID)
				if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
					logging.Warn("Failed to remove bundle file", types.PoC,
						"bundleID", bundleID, "pocHeight", height, "error", err)
				}
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

	entries := make([]firstArrivalEntry, 0, len(s.arrivals))
	for k, info := range s.arrivals {
		entries = append(entries, firstArrivalEntry{
			Participant: k.Participant,
			PocHeight:   k.PocHeight,
			ArrivalTime: info.Time,
			Count:       info.Count,
		})
	}

	data, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("marshal arrivals: %w", err)
	}

	filePath := s.arrivalsFilePath()
	tempPath := filePath + ".tmp"

	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := os.Rename(tempPath, filePath); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("rename to target: %w", err)
	}

	return nil
}

func (s *FileBundleStorage) Close() error {
	return nil
}

var _ BundleStorage = (*FileBundleStorage)(nil)
