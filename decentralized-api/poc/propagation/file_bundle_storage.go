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
	baseDir string
	mu      sync.RWMutex
	bundles map[[32]byte]BundleHeader
	proofs  map[[32]byte][]ProofItem
}

func NewFileBundleStorage(baseDir string) (*FileBundleStorage, error) {
	if baseDir == "" {
		return nil, fmt.Errorf("baseDir cannot be empty")
	}

	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("create base directory: %w", err)
	}

	s := &FileBundleStorage{
		baseDir: baseDir,
		bundles: make(map[[32]byte]BundleHeader),
		proofs:  make(map[[32]byte][]ProofItem),
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

func (s *FileBundleStorage) proofsFilePath(bundleID [32]byte) string {
	filename := hex.EncodeToString(bundleID[:]) + "_proofs.json"
	return filepath.Join(s.baseDir, filename)
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

		if filepath.Ext(name) == ".json" && len(name) > 11 && name[len(name)-11:] == "_proofs.json" {
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

			bundleIDHex := name[:len(name)-11]
			bundleIDBytes, err := hex.DecodeString(bundleIDHex)
			if err != nil || len(bundleIDBytes) != 32 {
				logging.Warn("Invalid bundle ID in proofs filename", types.PoC, "file", name)
				continue
			}
			var bundleID [32]byte
			copy(bundleID[:], bundleIDBytes)
			s.proofs[bundleID] = proofs
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

	logging.Info("Loaded bundles from disk", types.PoC, "count", len(s.bundles), "proofs", len(s.proofs))
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
	if _, exists := s.proofs[bundleID]; exists {
		s.mu.Unlock()
		return nil
	}
	s.proofs[bundleID] = proofs
	s.mu.Unlock()

	data, err := json.Marshal(proofs)
	if err != nil {
		s.mu.Lock()
		delete(s.proofs, bundleID)
		s.mu.Unlock()
		return fmt.Errorf("marshal proofs: %w", err)
	}

	if err := os.MkdirAll(s.baseDir, 0755); err != nil {
		s.mu.Lock()
		delete(s.proofs, bundleID)
		s.mu.Unlock()
		return fmt.Errorf("ensure directory exists: %w", err)
	}

	filePath := s.proofsFilePath(bundleID)
	tempPath := filePath + ".tmp"

	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		s.mu.Lock()
		delete(s.proofs, bundleID)
		s.mu.Unlock()
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := os.Rename(tempPath, filePath); err != nil {
		os.Remove(tempPath)
		s.mu.Lock()
		delete(s.proofs, bundleID)
		s.mu.Unlock()
		return fmt.Errorf("rename to target: %w", err)
	}

	return nil
}

func (s *FileBundleStorage) GetProofs(ctx context.Context, bundleID [32]byte) ([]ProofItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	proofs, exists := s.proofs[bundleID]
	if !exists {
		return nil, ErrProofsNotFound
	}
	return proofs, nil
}

func (s *FileBundleStorage) Close() error {
	return nil
}

var _ BundleStorage = (*FileBundleStorage)(nil)
