package pocartifacts

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenEmpty(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	if store.Count() != 0 {
		t.Errorf("expected count 0, got %d", store.Count())
	}

	if store.GetRoot() != nil {
		t.Errorf("expected nil root for empty store")
	}
}

func TestAddAndCount(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	// Add some artifacts
	for i := int32(0); i < 10; i++ {
		vector := []byte{byte(i), byte(i + 1), byte(i + 2)}
		if err := store.Add(i, vector); err != nil {
			t.Fatalf("Add(%d) failed: %v", i, err)
		}
	}

	if store.Count() != 10 {
		t.Errorf("expected count 10, got %d", store.Count())
	}
}

func TestDuplicateNonceRejection(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	vector := []byte{1, 2, 3}
	if err := store.Add(42, vector); err != nil {
		t.Fatalf("First Add failed: %v", err)
	}

	// Try to add duplicate
	if err := store.Add(42, vector); err != ErrDuplicateNonce {
		t.Errorf("expected ErrDuplicateNonce, got %v", err)
	}

	// Count should still be 1
	if store.Count() != 1 {
		t.Errorf("expected count 1, got %d", store.Count())
	}
}

func TestFlushAndGetArtifact(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	// Add artifacts
	artifacts := []struct {
		nonce  int32
		vector []byte
	}{
		{100, []byte{1, 2, 3, 4}},
		{200, []byte{5, 6, 7, 8, 9}},
		{-50, []byte{10, 11}}, // Negative nonce
	}

	for _, a := range artifacts {
		if err := store.Add(a.nonce, a.vector); err != nil {
			t.Fatalf("Add(%d) failed: %v", a.nonce, err)
		}
	}

	// Get from buffer (before flush)
	for i, a := range artifacts {
		nonce, vector, err := store.GetArtifact(uint32(i))
		if err != nil {
			t.Fatalf("GetArtifact(%d) failed: %v", i, err)
		}
		if nonce != a.nonce {
			t.Errorf("artifact %d: expected nonce %d, got %d", i, a.nonce, nonce)
		}
		if !bytes.Equal(vector, a.vector) {
			t.Errorf("artifact %d: vector mismatch", i)
		}
	}

	// Flush to disk
	if err := store.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Get from disk (after flush)
	for i, a := range artifacts {
		nonce, vector, err := store.GetArtifact(uint32(i))
		if err != nil {
			t.Fatalf("GetArtifact(%d) after flush failed: %v", i, err)
		}
		if nonce != a.nonce {
			t.Errorf("artifact %d after flush: expected nonce %d, got %d", i, a.nonce, nonce)
		}
		if !bytes.Equal(vector, a.vector) {
			t.Errorf("artifact %d after flush: vector mismatch", i)
		}
	}
}

func TestGetArtifactOutOfRange(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	store.Add(1, []byte{1})

	_, _, err = store.GetArtifact(5)
	if err != ErrLeafIndexOutOfRange {
		t.Errorf("expected ErrLeafIndexOutOfRange, got %v", err)
	}
}

func TestRecovery(t *testing.T) {
	dir := t.TempDir()

	// Create store and add data
	store1, err := Open(dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	artifacts := []struct {
		nonce  int32
		vector []byte
	}{
		{10, []byte{1, 2, 3}},
		{20, []byte{4, 5, 6}},
		{30, []byte{7, 8, 9}},
	}

	for _, a := range artifacts {
		if err := store1.Add(a.nonce, a.vector); err != nil {
			t.Fatalf("Add failed: %v", err)
		}
	}

	if err := store1.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	root1 := store1.GetRoot()
	count1 := store1.Count()

	store1.Close()

	// Reopen and verify recovery
	store2, err := Open(dir)
	if err != nil {
		t.Fatalf("Reopen failed: %v", err)
	}
	defer store2.Close()

	if store2.Count() != count1 {
		t.Errorf("recovered count: expected %d, got %d", count1, store2.Count())
	}

	root2 := store2.GetRoot()
	if !bytes.Equal(root1, root2) {
		t.Errorf("recovered root mismatch")
	}

	// Verify artifacts
	for i, a := range artifacts {
		nonce, vector, err := store2.GetArtifact(uint32(i))
		if err != nil {
			t.Fatalf("GetArtifact(%d) after recovery failed: %v", i, err)
		}
		if nonce != a.nonce {
			t.Errorf("artifact %d: expected nonce %d, got %d", i, a.nonce, nonce)
		}
		if !bytes.Equal(vector, a.vector) {
			t.Errorf("artifact %d: vector mismatch", i)
		}
	}

	// Verify duplicate rejection still works after recovery
	if err := store2.Add(10, []byte{1}); err != ErrDuplicateNonce {
		t.Errorf("expected duplicate rejection after recovery, got %v", err)
	}
}

func TestRecoveryWithTruncatedRecord(t *testing.T) {
	dir := t.TempDir()

	// Create store and add data
	store1, err := Open(dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	store1.Add(10, []byte{1, 2, 3})
	store1.Add(20, []byte{4, 5, 6})
	store1.Flush()
	root1 := store1.GetRoot()
	store1.Close()

	// Append garbage (partial record) to data file
	dataPath := filepath.Join(dir, "artifacts.data")
	f, err := os.OpenFile(dataPath, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatalf("open data file: %v", err)
	}
	f.Write([]byte{0x10, 0x00, 0x00, 0x00}) // partial header (length only, no nonce/vector)
	f.Close()

	// Reopen - should recover by truncating partial record
	store2, err := Open(dir)
	if err != nil {
		t.Fatalf("Reopen with truncated record failed: %v", err)
	}
	defer store2.Close()

	if store2.Count() != 2 {
		t.Errorf("expected count 2 after truncation recovery, got %d", store2.Count())
	}

	root2 := store2.GetRoot()
	if !bytes.Equal(root1, root2) {
		t.Errorf("root mismatch after truncation recovery")
	}
}

func TestRootDeterminism(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	store1, _ := Open(dir1)
	store2, _ := Open(dir2)
	defer store1.Close()
	defer store2.Close()

	// Add same artifacts in same order
	for i := int32(0); i < 100; i++ {
		vector := []byte{byte(i), byte(i * 2), byte(i * 3)}
		store1.Add(i, vector)
		store2.Add(i, vector)
	}

	root1 := store1.GetRoot()
	root2 := store2.GetRoot()

	if !bytes.Equal(root1, root2) {
		t.Errorf("roots should be identical for same data")
	}
}

func TestFilesCreated(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir)

	store.Add(1, []byte{1, 2, 3})
	store.Flush()
	store.Close()

	// Check files exist
	if _, err := os.Stat(filepath.Join(dir, "artifacts.data")); os.IsNotExist(err) {
		t.Error("artifacts.data not created")
	}
	if _, err := os.Stat(filepath.Join(dir, "artifacts.index")); os.IsNotExist(err) {
		t.Error("artifacts.index not created")
	}
}

func TestMMRHashLeaf(t *testing.T) {
	data := []byte{0x01, 0x02, 0x03}
	hash := hashLeaf(data)

	if len(hash) != 32 {
		t.Errorf("expected 32 byte hash, got %d", len(hash))
	}

	// Same data should produce same hash
	hash2 := hashLeaf(data)
	if !bytes.Equal(hash, hash2) {
		t.Error("hashLeaf not deterministic")
	}

	// Different data should produce different hash
	hash3 := hashLeaf([]byte{0x04, 0x05})
	if bytes.Equal(hash, hash3) {
		t.Error("different data produced same hash")
	}
}

func TestMMRHashNode(t *testing.T) {
	left := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}
	right := []byte{32, 31, 30, 29, 28, 27, 26, 25, 24, 23, 22, 21, 20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}

	hash := hashNode(left, right)

	if len(hash) != 32 {
		t.Errorf("expected 32 byte hash, got %d", len(hash))
	}

	// Order matters
	hash2 := hashNode(right, left)
	if bytes.Equal(hash, hash2) {
		t.Error("hashNode should be order-dependent")
	}
}

func TestMMRSizeForLeaves(t *testing.T) {
	tests := []struct {
		leaves   uint32
		expected int
	}{
		{0, 0},
		{1, 1},  // [L0]
		{2, 3},  // [L0, L1, N01]
		{3, 4},  // [L0, L1, N01, L2]
		{4, 7},  // [L0, L1, N01, L2, L3, N23, N0123]
		{5, 8},  // + L4
		{6, 10}, // + L5, N45
		{7, 11}, // + L6
		{8, 15}, // + L7, N67, N4567, N01234567
	}

	for _, tc := range tests {
		got := mmrSizeForLeaves(tc.leaves)
		if got != tc.expected {
			t.Errorf("mmrSizeForLeaves(%d) = %d, expected %d", tc.leaves, got, tc.expected)
		}
	}
}

func TestLeafPositionInMMR(t *testing.T) {
	tests := []struct {
		leafIndex uint32
		expected  int
	}{
		{0, 0},
		{1, 1},
		{2, 3},
		{3, 4},
		{4, 7},
		{5, 8},
		{6, 10},
		{7, 11},
	}

	for _, tc := range tests {
		got := leafPositionInMMR(tc.leafIndex)
		if got != tc.expected {
			t.Errorf("leafPositionInMMR(%d) = %d, expected %d", tc.leafIndex, got, tc.expected)
		}
	}
}

func TestGetPeakPositions(t *testing.T) {
	tests := []struct {
		leafCount uint32
		expected  []int
	}{
		{1, []int{0}},
		{2, []int{2}},
		{3, []int{2, 3}},
		{4, []int{6}},
		{5, []int{6, 7}},
		{6, []int{6, 9}},
		{7, []int{6, 9, 10}},
		{8, []int{14}},
	}

	for _, tc := range tests {
		got := getPeakPositions(tc.leafCount)
		if len(got) != len(tc.expected) {
			t.Errorf("getPeakPositions(%d): expected %v, got %v", tc.leafCount, tc.expected, got)
			continue
		}
		for i := range got {
			if got[i] != tc.expected[i] {
				t.Errorf("getPeakPositions(%d): expected %v, got %v", tc.leafCount, tc.expected, got)
				break
			}
		}
	}
}

func TestProofGeneration(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir)
	defer store.Close()

	// Add 8 artifacts to get a single-peak tree
	for i := int32(0); i < 8; i++ {
		store.Add(i, []byte{byte(i)})
	}

	root := store.GetRoot()
	if root == nil {
		t.Fatal("root should not be nil")
	}

	// Generate and verify proof for each leaf
	for i := uint32(0); i < 8; i++ {
		proof, err := store.GetProof(i, 8)
		if err != nil {
			t.Fatalf("GetProof(%d, 8) failed: %v", i, err)
		}

		// Proof should have 3 siblings for a perfect 8-leaf tree (log2(8) = 3)
		// Plus 0 other peaks (single peak tree)
		if len(proof) != 3 {
			t.Errorf("proof for leaf %d: expected 3 elements, got %d", i, len(proof))
		}

		// Verify the proof
		leafData := encodeLeaf(int32(i), []byte{byte(i)})
		if !VerifyProof(root, 8, i, leafData, proof) {
			t.Errorf("proof verification failed for leaf %d", i)
		}
	}
}

func TestProofForMultiPeakTree(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir)
	defer store.Close()

	// Add 5 artifacts: creates peaks at positions for trees of size 4 and 1
	for i := int32(0); i < 5; i++ {
		store.Add(i, []byte{byte(i)})
	}

	root := store.GetRoot()

	// Verify proofs for all leaves
	for i := uint32(0); i < 5; i++ {
		proof, err := store.GetProof(i, 5)
		if err != nil {
			t.Fatalf("GetProof(%d, 5) failed: %v", i, err)
		}

		leafData := encodeLeaf(int32(i), []byte{byte(i)})
		if !VerifyProof(root, 5, i, leafData, proof) {
			t.Errorf("proof verification failed for leaf %d in 5-leaf tree", i)
		}
	}
}

func TestSnapshotProof(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir)
	defer store.Close()

	// Add 4 artifacts
	for i := int32(0); i < 4; i++ {
		store.Add(i, []byte{byte(i)})
	}
	root4 := store.GetRoot()

	// Add 4 more artifacts
	for i := int32(4); i < 8; i++ {
		store.Add(i, []byte{byte(i)})
	}
	root8 := store.GetRoot()

	// Verify we can still generate proofs for the old snapshot
	for i := uint32(0); i < 4; i++ {
		proof, err := store.GetProof(i, 4)
		if err != nil {
			t.Fatalf("GetProof(%d, 4) failed: %v", i, err)
		}

		leafData := encodeLeaf(int32(i), []byte{byte(i)})
		if !VerifyProof(root4, 4, i, leafData, proof) {
			t.Errorf("snapshot proof verification failed for leaf %d at count 4", i)
		}
	}

	// Verify current snapshot too
	for i := uint32(0); i < 8; i++ {
		proof, err := store.GetProof(i, 8)
		if err != nil {
			t.Fatalf("GetProof(%d, 8) failed: %v", i, err)
		}

		leafData := encodeLeaf(int32(i), []byte{byte(i)})
		if !VerifyProof(root8, 8, i, leafData, proof) {
			t.Errorf("current proof verification failed for leaf %d", i)
		}
	}
}

func TestSnapshotProofAfterRecovery(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	for i := int32(0); i < 6; i++ {
		if err := store.Add(i, []byte{byte(i)}); err != nil {
			t.Fatalf("Add(%d) failed: %v", i, err)
		}
		if i == 2 {
			if err := store.Flush(); err != nil {
				t.Fatalf("Flush failed: %v", err)
			}
		}
	}

	if err := store.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Capture roots before closing
	rootAt3Before := bagPeaks(store.mmrNodes, 3)
	rootAt6Before := store.GetRoot()
	store.Close()

	store2, err := Open(dir)
	if err != nil {
		t.Fatalf("Reopen failed: %v", err)
	}
	defer store2.Close()

	// Verify roots match after recovery
	rootAt3After := bagPeaks(store2.mmrNodes, 3)
	rootAt6After := store2.GetRoot()

	if !bytes.Equal(rootAt3Before, rootAt3After) {
		t.Errorf("root at count 3 changed after recovery")
	}
	if !bytes.Equal(rootAt6Before, rootAt6After) {
		t.Errorf("root at count 6 changed after recovery")
	}

	// Verify snapshot proofs at count 3
	for i := uint32(0); i < 3; i++ {
		proof, err := store2.GetProof(i, 3)
		if err != nil {
			t.Fatalf("GetProof(%d, 3) failed: %v", i, err)
		}
		leafData := encodeLeaf(int32(i), []byte{byte(i)})
		if !VerifyProof(rootAt3After, 3, i, leafData, proof) {
			t.Errorf("snapshot proof verification failed for leaf %d at count 3", i)
		}
	}

	// Verify proofs at count 6
	for i := uint32(0); i < 6; i++ {
		proof, err := store2.GetProof(i, 6)
		if err != nil {
			t.Fatalf("GetProof(%d, 6) failed: %v", i, err)
		}
		leafData := encodeLeaf(int32(i), []byte{byte(i)})
		if !VerifyProof(rootAt6After, 6, i, leafData, proof) {
			t.Errorf("proof verification failed for leaf %d at count 6", i)
		}
	}
}

func TestRecoveryPreservesMMRStructure(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	// Add 100 artifacts with multiple flushes
	for i := int32(0); i < 100; i++ {
		vector := []byte{byte(i), byte(i >> 8), byte(i >> 16)}
		if err := store.Add(i, vector); err != nil {
			t.Fatalf("Add(%d) failed: %v", i, err)
		}
		if i%20 == 19 {
			store.Flush()
		}
	}
	store.Flush()

	// Capture all roots at various snapshots
	snapshots := []uint32{10, 25, 50, 75, 100}
	rootsBefore := make(map[uint32][]byte)
	for _, count := range snapshots {
		rootsBefore[count] = bagPeaks(store.mmrNodes, count)
	}
	store.Close()

	// Reopen and verify
	store2, err := Open(dir)
	if err != nil {
		t.Fatalf("Reopen failed: %v", err)
	}
	defer store2.Close()

	for _, count := range snapshots {
		rootAfter := bagPeaks(store2.mmrNodes, count)
		if !bytes.Equal(rootsBefore[count], rootAfter) {
			t.Errorf("root at count %d changed after recovery", count)
		}
	}
}

func TestProofForVariousTreeSizes(t *testing.T) {
	sizes := []int{1, 2, 3, 7, 8, 9, 15, 16, 17, 31, 32, 33, 63, 64, 65, 100, 127, 128, 255, 256}

	for _, size := range sizes {
		t.Run(fmt.Sprintf("size_%d", size), func(t *testing.T) {
			dir := t.TempDir()
			store, _ := Open(dir)
			defer store.Close()

			for i := 0; i < size; i++ {
				store.Add(int32(i), []byte{byte(i)})
			}

			root := store.GetRoot()
			for i := uint32(0); i < uint32(size); i++ {
				proof, err := store.GetProof(i, uint32(size))
				if err != nil {
					t.Fatalf("GetProof(%d, %d) failed: %v", i, size, err)
				}
				leafData := encodeLeaf(int32(i), []byte{byte(i)})
				if !VerifyProof(root, uint32(size), i, leafData, proof) {
					t.Errorf("proof failed for leaf %d in tree of size %d", i, size)
				}
			}
		})
	}
}

func TestAddAfterClose(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir)
	store.Add(1, []byte{1})
	store.Close()

	err := store.Add(2, []byte{2})
	if err != ErrStoreClosed {
		t.Errorf("expected ErrStoreClosed, got %v", err)
	}
}

func TestFlushAfterClose(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir)
	store.Add(1, []byte{1})
	store.Close()

	err := store.Flush()
	if err != ErrStoreClosed {
		t.Errorf("expected ErrStoreClosed, got %v", err)
	}
}

func TestGetArtifactAfterClose(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir)
	store.Add(1, []byte{1})
	store.Flush()
	store.Close()

	_, _, err := store.GetArtifact(0)
	if err != ErrStoreClosed {
		t.Errorf("expected ErrStoreClosed, got %v", err)
	}
}

func TestGetProofAfterClose(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir)
	store.Add(1, []byte{1})
	store.Flush()
	store.Close()

	_, err := store.GetProof(0, 1)
	if err != ErrStoreClosed {
		t.Errorf("expected ErrStoreClosed, got %v", err)
	}
}

func TestEmptyVectorArtifact(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir)
	defer store.Close()

	// Empty vector should be allowed
	if err := store.Add(1, []byte{}); err != nil {
		t.Fatalf("Add with empty vector failed: %v", err)
	}

	store.Flush()

	nonce, vector, err := store.GetArtifact(0)
	if err != nil {
		t.Fatalf("GetArtifact failed: %v", err)
	}
	if nonce != 1 {
		t.Errorf("expected nonce 1, got %d", nonce)
	}
	if len(vector) != 0 {
		t.Errorf("expected empty vector, got %v", vector)
	}
}

func TestLargeVector(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir)
	defer store.Close()

	largeVector := make([]byte, 1024*1024) // 1 MB
	for i := range largeVector {
		largeVector[i] = byte(i)
	}

	if err := store.Add(42, largeVector); err != nil {
		t.Fatalf("Add with large vector failed: %v", err)
	}

	store.Flush()

	nonce, vector, err := store.GetArtifact(0)
	if err != nil {
		t.Fatalf("GetArtifact failed: %v", err)
	}
	if nonce != 42 {
		t.Errorf("expected nonce 42, got %d", nonce)
	}
	if !bytes.Equal(vector, largeVector) {
		t.Errorf("large vector mismatch")
	}
}

func TestVerifyProofRejectsTamperedLeafData(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir)
	defer store.Close()

	for i := int32(0); i < 8; i++ {
		store.Add(i, []byte{byte(i), byte(i + 1)})
	}

	root := store.GetRoot()
	proof, _ := store.GetProof(3, 8)

	// Valid proof should pass
	validLeafData := encodeLeaf(3, []byte{3, 4})
	if !VerifyProof(root, 8, 3, validLeafData, proof) {
		t.Fatal("valid proof should pass")
	}

	// Tampered nonce should fail
	tamperedNonce := encodeLeaf(4, []byte{3, 4})
	if VerifyProof(root, 8, 3, tamperedNonce, proof) {
		t.Error("tampered nonce should fail verification")
	}

	// Tampered vector (flip one bit) should fail
	tamperedVector := encodeLeaf(3, []byte{3, 5}) // changed 4 to 5
	if VerifyProof(root, 8, 3, tamperedVector, proof) {
		t.Error("tampered vector should fail verification")
	}

	// Single bit flip in vector should fail
	tamperedBit := encodeLeaf(3, []byte{3, 4 ^ 0x01}) // flip lowest bit
	if VerifyProof(root, 8, 3, tamperedBit, proof) {
		t.Error("single bit flip should fail verification")
	}
}

func TestVerifyProofRejectsTamperedProof(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir)
	defer store.Close()

	for i := int32(0); i < 8; i++ {
		store.Add(i, []byte{byte(i)})
	}

	root := store.GetRoot()
	proof, _ := store.GetProof(0, 8)
	leafData := encodeLeaf(0, []byte{0})

	// Valid proof should pass
	if !VerifyProof(root, 8, 0, leafData, proof) {
		t.Fatal("valid proof should pass")
	}

	// Tamper with first sibling hash
	if len(proof) > 0 {
		tamperedProof := make([][]byte, len(proof))
		copy(tamperedProof, proof)
		tamperedProof[0] = make([]byte, 32)
		copy(tamperedProof[0], proof[0])
		tamperedProof[0][0] ^= 0x01 // flip one bit

		if VerifyProof(root, 8, 0, leafData, tamperedProof) {
			t.Error("tampered proof sibling should fail verification")
		}
	}

	// Tamper with last sibling hash
	if len(proof) > 1 {
		tamperedProof := make([][]byte, len(proof))
		copy(tamperedProof, proof)
		lastIdx := len(proof) - 1
		tamperedProof[lastIdx] = make([]byte, 32)
		copy(tamperedProof[lastIdx], proof[lastIdx])
		tamperedProof[lastIdx][31] ^= 0x80 // flip high bit of last byte

		if VerifyProof(root, 8, 0, leafData, tamperedProof) {
			t.Error("tampered proof (last sibling) should fail verification")
		}
	}
}

func TestVerifyProofRejectsWrongLeafIndex(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir)
	defer store.Close()

	for i := int32(0); i < 8; i++ {
		store.Add(i, []byte{byte(i)})
	}

	root := store.GetRoot()
	proof, _ := store.GetProof(3, 8)
	leafData := encodeLeaf(3, []byte{3})

	// Correct index should pass
	if !VerifyProof(root, 8, 3, leafData, proof) {
		t.Fatal("valid proof should pass")
	}

	// Wrong leaf index should fail
	if VerifyProof(root, 8, 4, leafData, proof) {
		t.Error("wrong leaf index should fail verification")
	}
}

func TestVerifyProofRejectsWrongRoot(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir)
	defer store.Close()

	for i := int32(0); i < 8; i++ {
		store.Add(i, []byte{byte(i)})
	}

	root := store.GetRoot()
	proof, _ := store.GetProof(0, 8)
	leafData := encodeLeaf(0, []byte{0})

	// Correct root should pass
	if !VerifyProof(root, 8, 0, leafData, proof) {
		t.Fatal("valid proof should pass")
	}

	// Tampered root should fail
	tamperedRoot := make([]byte, 32)
	copy(tamperedRoot, root)
	tamperedRoot[0] ^= 0x01

	if VerifyProof(tamperedRoot, 8, 0, leafData, proof) {
		t.Error("wrong root should fail verification")
	}
}

func TestEncodeLeaf(t *testing.T) {
	nonce := int32(0x12345678)
	vector := []byte{0xAA, 0xBB, 0xCC}

	encoded := encodeLeaf(nonce, vector)

	// Should be 4 bytes nonce (LE) + vector
	if len(encoded) != 7 {
		t.Errorf("expected 7 bytes, got %d", len(encoded))
	}

	// Check little-endian encoding
	if encoded[0] != 0x78 || encoded[1] != 0x56 || encoded[2] != 0x34 || encoded[3] != 0x12 {
		t.Errorf("nonce not correctly encoded as little-endian")
	}

	if encoded[4] != 0xAA || encoded[5] != 0xBB || encoded[6] != 0xCC {
		t.Errorf("vector not correctly appended")
	}
}

func TestNegativeNonce(t *testing.T) {
	dir := t.TempDir()
	store, _ := Open(dir)
	defer store.Close()

	// Add negative nonces
	store.Add(-1, []byte{1})
	store.Add(-100, []byte{2})
	store.Add(-2147483648, []byte{3}) // INT32_MIN

	if store.Count() != 3 {
		t.Errorf("expected 3, got %d", store.Count())
	}

	// Verify retrieval
	nonce, _, err := store.GetArtifact(0)
	if err != nil || nonce != -1 {
		t.Errorf("expected nonce -1, got %d, err: %v", nonce, err)
	}
}

func BenchmarkAdd(b *testing.B) {
	dir := b.TempDir()
	store, _ := Open(dir)
	defer store.Close()

	vector := make([]byte, 100)
	for i := range vector {
		vector[i] = byte(i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store.Add(int32(i), vector)
	}
}

func BenchmarkAddAndFlush(b *testing.B) {
	dir := b.TempDir()
	store, _ := Open(dir)
	defer store.Close()

	vector := make([]byte, 100)
	batchSize := 1000

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store.Add(int32(i), vector)
		if i%batchSize == batchSize-1 {
			store.Flush()
		}
	}
}
