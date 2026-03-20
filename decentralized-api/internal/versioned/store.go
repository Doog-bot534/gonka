package versioned

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type VersionConfig struct {
	Versions []Version `json:"versions"`
}

type Version struct {
	Name   string `json:"name"`
	Binary string `json:"binary"`
	SHA256 string `json:"sha256,omitempty"`
	Port   int    `json:"port"`
}

type Store struct {
	configPath string
	binaryDir  string
	mu         sync.RWMutex
	config     VersionConfig
}

func NewStore(configPath, binaryDir string) (*Store, error) {
	s := &Store{
		configPath: configPath,
		binaryDir:  binaryDir,
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		return nil, fmt.Errorf("create config dir: %w", err)
	}
	if err := os.MkdirAll(binaryDir, 0755); err != nil {
		return nil, fmt.Errorf("create binary dir: %w", err)
	}
	if err := s.load(); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return s, nil
}

func (s *Store) List() VersionConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := make([]Version, len(s.config.Versions))
	copy(cp, s.config.Versions)
	return VersionConfig{Versions: cp}
}

func (s *Store) Put(v Version) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	updated := make([]Version, len(s.config.Versions))
	copy(updated, s.config.Versions)

	found := false
	for i, existing := range updated {
		if existing.Name == v.Name {
			updated[i] = v
			found = true
			break
		}
	}
	if !found {
		updated = append(updated, v)
	}

	candidate := VersionConfig{Versions: updated}
	if err := s.saveConfig(candidate); err != nil {
		return err
	}
	s.config = candidate
	return nil
}

func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := -1
	for i, v := range s.config.Versions {
		if v.Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("version %q not found", name)
	}

	updated := make([]Version, 0, len(s.config.Versions)-1)
	updated = append(updated, s.config.Versions[:idx]...)
	updated = append(updated, s.config.Versions[idx+1:]...)

	candidate := VersionConfig{Versions: updated}
	if err := s.saveConfig(candidate); err != nil {
		return err
	}
	s.config = candidate
	return nil
}

func (s *Store) BinaryDir() string {
	return s.binaryDir
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.configPath)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &s.config)
}

// saveConfig writes the given config atomically via temp file + rename.
func (s *Store) saveConfig(cfg VersionConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	tmp := s.configPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("write temp: %w", err)
	}
	if err := os.Rename(tmp, s.configPath); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
