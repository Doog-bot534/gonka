package versioned

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStore_PutAndList(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(filepath.Join(dir, "versions.json"), filepath.Join(dir, "bin"))
	if err != nil {
		t.Fatal(err)
	}

	v := Version{Name: "v1", Binary: "http://example.com/v1.zip", SHA256: "abc", Port: 9001}
	if err := s.Put(v); err != nil {
		t.Fatal(err)
	}

	cfg := s.List()
	if len(cfg.Versions) != 1 || cfg.Versions[0].Name != "v1" {
		t.Errorf("got %+v", cfg)
	}
}

func TestStore_PutUpdate(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(filepath.Join(dir, "versions.json"), filepath.Join(dir, "bin"))
	if err != nil {
		t.Fatal(err)
	}

	s.Put(Version{Name: "v1", Binary: "http://a.com/old.zip", SHA256: "old", Port: 9001})
	s.Put(Version{Name: "v1", Binary: "http://a.com/new.zip", SHA256: "new", Port: 9002})

	cfg := s.List()
	if len(cfg.Versions) != 1 {
		t.Fatalf("expected 1 version, got %d", len(cfg.Versions))
	}
	if cfg.Versions[0].SHA256 != "new" || cfg.Versions[0].Port != 9002 {
		t.Errorf("version not updated: %+v", cfg.Versions[0])
	}
}

func TestStore_Delete(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(filepath.Join(dir, "versions.json"), filepath.Join(dir, "bin"))
	if err != nil {
		t.Fatal(err)
	}

	s.Put(Version{Name: "v1", Binary: "http://a.com/v1.zip", SHA256: "abc", Port: 9001})
	s.Put(Version{Name: "v2", Binary: "http://a.com/v2.zip", SHA256: "def", Port: 9002})

	if err := s.Delete("v1"); err != nil {
		t.Fatal(err)
	}

	cfg := s.List()
	if len(cfg.Versions) != 1 || cfg.Versions[0].Name != "v2" {
		t.Errorf("after delete: %+v", cfg)
	}
}

func TestStore_DeleteNotFound(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(filepath.Join(dir, "versions.json"), filepath.Join(dir, "bin"))
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Delete("nonexistent"); err == nil {
		t.Fatal("expected error deleting nonexistent version")
	}
}

func TestStore_Persistence(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "versions.json")
	binDir := filepath.Join(dir, "bin")

	s1, _ := NewStore(cfgPath, binDir)
	s1.Put(Version{Name: "v1", Binary: "http://a.com/v1.zip", SHA256: "abc", Port: 9001})

	// Reload from disk
	s2, err := NewStore(cfgPath, binDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := s2.List()
	if len(cfg.Versions) != 1 || cfg.Versions[0].Name != "v1" {
		t.Errorf("persistence failed: %+v", cfg)
	}
}

func TestStore_BinaryDir(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "binaries")
	s, err := NewStore(filepath.Join(dir, "versions.json"), binDir)
	if err != nil {
		t.Fatal(err)
	}
	if s.BinaryDir() != binDir {
		t.Errorf("BinaryDir() = %q, want %q", s.BinaryDir(), binDir)
	}
	// Verify directory was created
	if _, err := os.Stat(binDir); err != nil {
		t.Errorf("binary dir not created: %v", err)
	}
}
