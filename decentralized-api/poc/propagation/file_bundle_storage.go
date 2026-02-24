package propagation

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"

	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

var proofsFileRegex = regexp.MustCompile(`^[0-9a-fA-F]{64}_proofs(_\d+)?\.json$`)

type FileBundleStorage struct {
	baseDir  string
	mu       sync.RWMutex
	bundles  map[[32]byte]BundleHeader
	proofs   map[[32]byte][][]ProofItem // Multiple proof sets per bundleID
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
		bundles:  make(map[[32]byte]BundleHeader),
		proofs:   make(map[[32]byte][][]ProofItem),
		arrivals: make(map[participantPocKey]ArrivalInfo),
	}

	if err := s.loadBundles(); err != nil {
		return nil, fmt.Errorf("load bundles: %w", err)
	}

	logging.Info("File bundle storage initialized", types.PoC, "baseDir", baseDir)
	return s, nil
}

func (s *FileBundleStorage) bundleFilePath(bundleID [32]byte) string {
	filename := hex.EncodeToString(bundleID[:]) + ".json"
	return filepath.Join(s.baseDir, filename)
}

func (s *FileBundleStorage) proofsFilePath(bundleID [32]byte, index int) string {
	if index == 0 {
		// Backward compatible: first proof set uses original filename
		filename := hex.EncodeToString(bundleID[:]) + "_proofs.json"
		return filepath.Join(s.baseDir, filename)
	}
	filename := fmt.Sprintf("%s_proofs_%d.json", hex.EncodeToString(bundleID[:]), index)
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

		// Match proof files: {bundleID}_proofs.json or {bundleID}_proofs_{index}.json
		if proofsFileRegex.MatchString(name) {
			filePath := filepath.Join(s.baseDir, name)
			data, err := os.ReadFile(filePath)
			if err != nil {
				logging.Warn("Failed to read proofs file", types.PoC, "file", name, "error", err)
				continue
			}

			var proofs []ProofItem
			if err := json.Unmarshal(data, &proofs); err != nil {
				logging.Warn("Failed to unmarshal proofs", types.PoC, "file", name, "error", err)
				continue
			}

			bundleIDBytes, err := hex.DecodeString(name[:64])
			if err != nil || len(bundleIDBytes) != 32 {
				logging.Warn("Invalid bundle ID in proofs filename", types.PoC, "file", name)
				continue
			}

			var bundleID [32]byte
			copy(bundleID[:], bundleIDBytes)
			s.proofs[bundleID] = append(s.proofs[bundleID], proofs)
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

		s.bundles[header.BundleID] = header
	}

	if err := s.loadArrivals(); err != nil {
		logging.Warn("Failed to load arrivals", types.PoC, "error", err)
	}

	logging.Info("Loaded bundles from disk", types.PoC, "count", len(s.bundles), "proofs", len(s.proofs), "arrivals", len(s.arrivals))
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

func (s *FileBundleStorage) GetHeader(ctx context.Context, bundleID [32]byte) (BundleHeader, error) {
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

func (s *FileBundleStorage) StoreProofs(ctx context.Context, bundleID [32]byte, proofs []ProofItem) error {
	s.mu.Lock()
	proofIndex := len(s.proofs[bundleID])
	s.proofs[bundleID] = append(s.proofs[bundleID], proofs)
	s.mu.Unlock()

	data, err := json.Marshal(proofs)
	if err != nil {
		s.mu.Lock()
		// Remove the last added proof set on error
		if len(s.proofs[bundleID]) > 0 {
			s.proofs[bundleID] = s.proofs[bundleID][:len(s.proofs[bundleID])-1]
		}
		s.mu.Unlock()
		return fmt.Errorf("marshal proofs: %w", err)
	}

	if err := os.MkdirAll(s.baseDir, 0755); err != nil {
		s.mu.Lock()
		if len(s.proofs[bundleID]) > 0 {
			s.proofs[bundleID] = s.proofs[bundleID][:len(s.proofs[bundleID])-1]
		}
		s.mu.Unlock()
		return fmt.Errorf("ensure directory exists: %w", err)
	}

	filePath := s.proofsFilePath(bundleID, proofIndex)
	tempPath := filePath + ".tmp"

	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		s.mu.Lock()
		if len(s.proofs[bundleID]) > 0 {
			s.proofs[bundleID] = s.proofs[bundleID][:len(s.proofs[bundleID])-1]
		}
		s.mu.Unlock()
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := os.Rename(tempPath, filePath); err != nil {
		os.Remove(tempPath)
		s.mu.Lock()
		if len(s.proofs[bundleID]) > 0 {
			s.proofs[bundleID] = s.proofs[bundleID][:len(s.proofs[bundleID])-1]
		}
		s.mu.Unlock()
		return fmt.Errorf("rename to target: %w", err)
	}

	return nil
}

func (s *FileBundleStorage) GetProofs(ctx context.Context, bundleID [32]byte) ([][]ProofItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	proofSets, exists := s.proofs[bundleID]
	if !exists || len(proofSets) == 0 {
		return nil, ErrProofsNotFound
	}
	return proofSets, nil
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

func (s *FileBundleStorage) DeleteBeforeHeight(ctx context.Context, maxPocHeight int64) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	deletedCount := 0

	// Find and delete bundles for heights <= maxPocHeight
	bundlesToDelete := make([][32]byte, 0)
	for bundleID, header := range s.bundles {
		if header.PocHeight <= maxPocHeight {
			bundlesToDelete = append(bundlesToDelete, bundleID)
		}
	}

	for _, bundleID := range bundlesToDelete {
		// Delete all proof files for this bundle
		proofSets := s.proofs[bundleID]
		allProofsCleaned := true
		for i := range proofSets {
			proofPath := s.proofsFilePath(bundleID, i)
			if err := os.Remove(proofPath); err != nil && !os.IsNotExist(err) {
				allProofsCleaned = false
				logging.Warn("Failed to delete proofs file", types.PoC,
					"bundleID", fmt.Sprintf("%x", bundleID[:8]),
					"index", i,
					"error", err)
			} else if err == nil {
				deletedCount++
			}
		}

		// Continue cleaning only if all proofs were deleted
		if !allProofsCleaned {
			logging.Warn("Failed to delete all proofs for bundle", types.PoC,
				"bundleID", fmt.Sprintf("%x", bundleID[:8]),
				"error", "not all proofs deleted")
			continue
		}

		// Delete bundle header file
		bundlePath := s.bundleFilePath(bundleID)
		if err := os.Remove(bundlePath); err != nil && !os.IsNotExist(err) {
			logging.Warn("Failed to delete bundle file", types.PoC,
				"bundleID", fmt.Sprintf("%x", bundleID[:8]),
				"error", err)
		} else if err == nil {
			deletedCount++
		}

		// Remove from in-memory maps
		delete(s.bundles, bundleID)
		delete(s.proofs, bundleID)
	}

	logging.Info("Deleted data for PoC heights", types.PoC,
		"maxPocHeight", maxPocHeight,
		"bundlesDeleted", len(bundlesToDelete),
		"totalItemsDeleted", deletedCount)

	return deletedCount, nil
}

func (s *FileBundleStorage) Close() error {
	return nil
}

var _ BundleStorage = (*FileBundleStorage)(nil)
