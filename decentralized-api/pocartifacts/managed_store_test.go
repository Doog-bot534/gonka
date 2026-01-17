package pocartifacts

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestManagedArtifactStore_GetOrCreateStore(t *testing.T) {
	dir := t.TempDir()
	m := NewManagedArtifactStore(dir, 3)
	defer m.Close()

	store1, err := m.GetOrCreateStore(100)
	if err != nil {
		t.Fatalf("GetOrCreateStore(100) failed: %v", err)
	}
	if store1 == nil {
		t.Fatal("store1 is nil")
	}

	// Same stage should return same store
	store1Again, err := m.GetOrCreateStore(100)
	if err != nil {
		t.Fatalf("GetOrCreateStore(100) again failed: %v", err)
	}
	if store1 != store1Again {
		t.Error("expected same store instance for same stage")
	}

	// Different stage should create new store
	store2, err := m.GetOrCreateStore(200)
	if err != nil {
		t.Fatalf("GetOrCreateStore(200) failed: %v", err)
	}
	if store1 == store2 {
		t.Error("expected different store instance for different stage")
	}

	// Verify directories created
	if _, err := os.Stat(filepath.Join(dir, "100")); os.IsNotExist(err) {
		t.Error("stage 100 directory not created")
	}
	if _, err := os.Stat(filepath.Join(dir, "200")); os.IsNotExist(err) {
		t.Error("stage 200 directory not created")
	}
}

func TestManagedArtifactStore_GetStore_NotFound(t *testing.T) {
	dir := t.TempDir()
	m := NewManagedArtifactStore(dir, 3)
	defer m.Close()

	_, err := m.GetStore(999)
	if err == nil {
		t.Error("expected error for non-existent epoch")
	}
}

func TestManagedArtifactStore_GetStore_ExistingDir(t *testing.T) {
	dir := t.TempDir()

	// Create epoch 100 with first manager
	m1 := NewManagedArtifactStore(dir, 3)
	store, err := m1.GetOrCreateStore(100)
	if err != nil {
		t.Fatalf("GetOrCreateStore failed: %v", err)
	}
	if err := store.Add(1, []byte("test")); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	// Flush to ensure data is persisted before reopening
	if err := m1.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}
	m1.Close()

	// New manager should find existing epoch via GetStore
	m2 := NewManagedArtifactStore(dir, 3)
	defer m2.Close()

	store2, err := m2.GetStore(100)
	if err != nil {
		t.Fatalf("GetStore(100) failed: %v", err)
	}
	if store2.Count() != 1 {
		t.Errorf("expected count 1, got %d", store2.Count())
	}
}

func TestManagedArtifactStore_PruneStore(t *testing.T) {
	dir := t.TempDir()
	m := NewManagedArtifactStore(dir, 3)
	defer m.Close()

	// Create stores at different heights
	for _, height := range []int64{100, 200, 300} {
		if _, err := m.GetOrCreateStore(height); err != nil {
			t.Fatalf("GetOrCreateStore(%d) failed: %v", height, err)
		}
	}

	// Prune store at height 100
	if err := m.PruneStore(100); err != nil {
		t.Fatalf("PruneStore(100) failed: %v", err)
	}

	// Verify directory removed
	if _, err := os.Stat(filepath.Join(dir, "100")); !os.IsNotExist(err) {
		t.Error("height 100 directory should be removed")
	}

	// Other heights should still exist
	if _, err := os.Stat(filepath.Join(dir, "200")); os.IsNotExist(err) {
		t.Error("height 200 directory should still exist")
	}
	if _, err := os.Stat(filepath.Join(dir, "300")); os.IsNotExist(err) {
		t.Error("height 300 directory should still exist")
	}

	// GetStore should fail for pruned height
	if _, err := m.GetStore(100); err == nil {
		t.Error("expected error for pruned height")
	}
}

func TestManagedArtifactStore_ListStores(t *testing.T) {
	dir := t.TempDir()
	m := NewManagedArtifactStore(dir, 3)
	defer m.Close()

	// Initially empty
	heights, err := m.ListStores()
	if err != nil {
		t.Fatalf("ListStores failed: %v", err)
	}
	if len(heights) != 0 {
		t.Errorf("expected 0 stores, got %d", len(heights))
	}

	// Create stores (out of order)
	for _, height := range []int64{300, 100, 200} {
		if _, err := m.GetOrCreateStore(height); err != nil {
			t.Fatalf("GetOrCreateStore(%d) failed: %v", height, err)
		}
	}

	// Should return sorted
	heights, err = m.ListStores()
	if err != nil {
		t.Fatalf("ListStores failed: %v", err)
	}
	if len(heights) != 3 {
		t.Fatalf("expected 3 stores, got %d", len(heights))
	}
	if heights[0] != 100 || heights[1] != 200 || heights[2] != 300 {
		t.Errorf("expected [100, 200, 300], got %v", heights)
	}
}

func TestManagedArtifactStore_AutoPrune(t *testing.T) {
	dir := t.TempDir()
	m := NewManagedArtifactStore(dir, 3) // retainCount=3
	defer m.Close()

	// Create stores at heights 100, 200, 300, 400, 500
	for _, height := range []int64{100, 200, 300, 400, 500} {
		if _, err := m.GetOrCreateStore(height); err != nil {
			t.Fatalf("GetOrCreateStore(%d) failed: %v", height, err)
		}
	}

	// Trigger cleanup manually
	m.cleanup()

	// Wait for async prune goroutines
	time.Sleep(100 * time.Millisecond)

	heights, err := m.ListStores()
	if err != nil {
		t.Fatalf("ListStores failed: %v", err)
	}

	// With retainCount=3, should keep newest 3: 300, 400, 500
	// Heights 100, 200 should be pruned
	if len(heights) != 3 {
		t.Errorf("expected 3 stores after prune, got %d: %v", len(heights), heights)
	}
	if len(heights) == 3 && (heights[0] != 300 || heights[1] != 400 || heights[2] != 500) {
		t.Errorf("expected [300, 400, 500], got %v", heights)
	}
}

func TestManagedArtifactStore_NoPruneWhenBelowRetainCount(t *testing.T) {
	dir := t.TempDir()
	m := NewManagedArtifactStore(dir, 5)
	defer m.Close()

	// Create only 3 stores (below retainCount=5)
	for _, height := range []int64{100, 200, 300} {
		if _, err := m.GetOrCreateStore(height); err != nil {
			t.Fatalf("GetOrCreateStore(%d) failed: %v", height, err)
		}
	}

	m.cleanup()
	time.Sleep(50 * time.Millisecond)

	// Nothing should be pruned since we have fewer stores than retainCount
	heights, err := m.ListStores()
	if err != nil {
		t.Fatalf("ListStores failed: %v", err)
	}
	if len(heights) != 3 {
		t.Errorf("expected 3 stores (no pruning), got %d: %v", len(heights), heights)
	}
}

func TestManagedArtifactStore_Flush(t *testing.T) {
	dir := t.TempDir()
	m := NewManagedArtifactStore(dir, 3)
	defer m.Close()

	store, err := m.GetOrCreateStore(100)
	if err != nil {
		t.Fatalf("GetOrCreateStore failed: %v", err)
	}

	if err := store.Add(1, []byte("test")); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	if err := m.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Verify data file has content
	dataFile := filepath.Join(dir, "100", "artifacts.data")
	info, err := os.Stat(dataFile)
	if err != nil {
		t.Fatalf("stat data file: %v", err)
	}
	if info.Size() == 0 {
		t.Error("data file should not be empty after flush")
	}
}

func TestManagedArtifactStore_AcquireStore(t *testing.T) {
	dir := t.TempDir()
	m := NewManagedArtifactStore(dir, 3)
	defer m.Close()

	// Create a store first
	_, err := m.GetOrCreateStore(100)
	if err != nil {
		t.Fatalf("GetOrCreateStore failed: %v", err)
	}

	// Acquire the store
	store, release, err := m.AcquireStore(100)
	if err != nil {
		t.Fatalf("AcquireStore failed: %v", err)
	}
	if store == nil {
		t.Fatal("acquired store is nil")
	}

	// Verify we can use the store
	if err := store.Add(1, []byte("test")); err != nil {
		t.Fatalf("Add to acquired store failed: %v", err)
	}

	// Release the store
	release()
}

func TestManagedArtifactStore_AcquireStore_NotFound(t *testing.T) {
	dir := t.TempDir()
	m := NewManagedArtifactStore(dir, 3)
	defer m.Close()

	_, _, err := m.AcquireStore(999)
	if err == nil {
		t.Error("expected error for non-existent stage")
	}
}

func TestManagedArtifactStore_SafePruning(t *testing.T) {
	dir := t.TempDir()
	m := NewManagedArtifactStore(dir, 2) // retainCount=2
	defer m.Close()

	// Create stores at heights 100, 200, 300
	for _, height := range []int64{100, 200, 300} {
		if _, err := m.GetOrCreateStore(height); err != nil {
			t.Fatalf("GetOrCreateStore(%d) failed: %v", height, err)
		}
	}

	// Acquire store at height 100 (should prevent pruning)
	_, release, err := m.AcquireStore(100)
	if err != nil {
		t.Fatalf("AcquireStore(100) failed: %v", err)
	}

	// Trigger cleanup - should skip height 100 because it's acquired
	m.cleanup()
	time.Sleep(50 * time.Millisecond)

	// Height 100 should still exist (has active reference)
	heights, err := m.ListStores()
	if err != nil {
		t.Fatalf("ListStores failed: %v", err)
	}

	// With retainCount=2, would normally keep only 200 and 300
	// But 100 is acquired, so it should still exist
	found100 := false
	for _, h := range heights {
		if h == 100 {
			found100 = true
			break
		}
	}
	if !found100 {
		t.Error("height 100 should not be pruned while acquired")
	}

	// Release the store
	release()

	// Now cleanup should be able to prune height 100
	m.cleanup()
	time.Sleep(50 * time.Millisecond)

	heights, err = m.ListStores()
	if err != nil {
		t.Fatalf("ListStores failed: %v", err)
	}

	// Now should have only 200 and 300
	if len(heights) != 2 {
		t.Errorf("expected 2 stores after release and prune, got %d: %v", len(heights), heights)
	}
}
