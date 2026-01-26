package propagation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"decentralized-api/poc/artifacts"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/stretchr/testify/require"
)

type loggingSender struct {
	from    string
	dst     Sender
	records *[]string
	mu      sync.Mutex
}

func newLoggingSender(from string, dst Sender, records *[]string) *loggingSender {
	return &loggingSender{from: from, dst: dst, records: records}
}

func (l *loggingSender) SendHeader(treeIdx int, to string, h BundleHeader) error {
	l.mu.Lock()
	if l.records != nil {
		*l.records = append(*l.records, fmt.Sprintf("header: %s -> %s tree=%d bundle=%x", l.from, to, treeIdx, h.BundleID[:8]))
	}
	l.mu.Unlock()
	return l.dst.SendHeader(treeIdx, to, h)
}

type treeTrackingSender struct {
	from      string
	dst       Sender
	records   *[]string
	treeUsage map[int]int
	mu        *sync.Mutex
}

func newTreeTrackingSender(from string, dst Sender, records *[]string, treeUsage map[int]int, mu *sync.Mutex) *treeTrackingSender {
	return &treeTrackingSender{
		from:      from,
		dst:       dst,
		records:   records,
		treeUsage: treeUsage,
		mu:        mu,
	}
}

func (s *treeTrackingSender) SendHeader(treeIdx int, to string, h BundleHeader) error {
	s.mu.Lock()
	if s.records != nil {
		*s.records = append(*s.records, fmt.Sprintf("header: %s -> %s tree=%d bundle=%x", s.from, to, treeIdx, h.BundleID[:8]))
	}
	s.treeUsage[treeIdx]++
	s.mu.Unlock()
	return s.dst.SendHeader(treeIdx, to, h)
}

func TestPropagationDemo(t *testing.T) {
	numParticipants := 1000
	numTrees := 6
	fanout := 4

	participants := make([]string, numParticipants)
	privKeys := make(map[string][]byte)
	pubKeys := make(map[string]string)

	for i := 0; i < numParticipants; i++ {
		addr := fmt.Sprintf("participant%d", i)
		participants[i] = addr

		privKey := secp256k1.GenPrivKey()
		privKeys[addr] = privKey.Key
		pubKeys[addr] = hex.EncodeToString(privKey.PubKey().Bytes())
	}

	blockHash := sha256.Sum256([]byte("test-block"))
	pocHeight := int64(1000)

	trees := BuildTrees(participants, blockHash[:], numTrees, fanout)

	transport := NewMockTransport()
	pubKeyProvider := NewMockPubKeyProvider()
	for addr, pubKey := range pubKeys {
		pubKeyProvider.RegisterKey(addr, pubKey)
	}

	var sendLogs []string

	pool, cleanup := setupPropagationPostgres(t)
	defer cleanup()
	ctx := context.Background()

	tempDir := t.TempDir()

	caches := make(map[string]*Cache)
	receivers := make(map[string]*Receiver)
	bundlers := make(map[string]*Bundler)
	stores := make(map[string]*artifacts.ArtifactStore)

	for i, addr := range participants {
		cache, err := NewCache(ctx, pool, addr)
		require.NoError(t, err)
		caches[addr] = cache

		sender := newLoggingSender(addr, transport, &sendLogs)
		receiver := NewReceiver(cache, trees, pubKeyProvider, addr, sender)
		receivers[addr] = receiver
		transport.RegisterReceiver(addr, receiver)

		storeDir := filepath.Join(tempDir, addr, "store")
		if err := os.MkdirAll(storeDir, 0755); err != nil {
			t.Fatalf("failed to create store dir for %s: %v", addr, err)
		}
		store, err := artifacts.Open(storeDir)
		if err != nil {
			t.Fatalf("failed to create store for %s: %v", addr, err)
		}
		stores[addr] = store

		for j := 0; j < 100; j++ {
			nonce := int32(i*1000 + j)
			vector := []byte(fmt.Sprintf("vector-%d-%d", i, j))
			if err := store.Add(nonce, vector); err != nil {
				t.Fatalf("failed to add artifact: %v", err)
			}
		}

		if err := store.Flush(); err != nil {
			t.Fatalf("failed to flush store for %s: %v", addr, err)
		}

		bundler := NewBundler(store, trees, sender, addr)
		bundlers[addr] = bundler
	}

	sender := trees[0].Shuffled[0]
	t.Logf("Sender (tree root): %s", sender)

	if err := bundlers[sender].Publish(pocHeight, blockHash[:], sender, privKeys[sender]); err != nil {
		t.Fatalf("failed to publish: %v", err)
	}

	bundleID := MakeBundleID(sender, pocHeight, stores[sender].GetRoot(), stores[sender].Count(), 1)
	t.Logf("BundleID: %x", bundleID[:8])

	receivedCount := 0
	for _, addr := range participants {
		if addr == sender {
			continue
		}

		header, err := caches[addr].GetHeader(bundleID)
		if err != nil {
			t.Logf("Participant %s did not receive header", addr)
			continue
		}

		if header.Participant != sender {
			t.Errorf("participant %s: wrong sender in header: got %s, want %s",
				addr, header.Participant, sender)
		}

		if header.Count != stores[sender].Count() {
			t.Errorf("participant %s: wrong count in header: got %d, want %d",
				addr, header.Count, stores[sender].Count())
		}

		receivedCount++
		t.Logf("Participant %s successfully received and verified commit metadata", addr)
	}

	for _, store := range stores {
		store.Close()
	}

	for _, line := range sendLogs {
		t.Logf("Send trace: %s", line)
	}
	t.Logf("Total sended traces: %d", len(sendLogs))

	stats := pool.Stat()
	t.Logf("Pool connections: total=%d acquired=%d idle=%d acquire_count=%d canceled_acquire=%d empty_acquire=%d max_lifetime_destroy=%d",
		stats.TotalConns(), stats.AcquiredConns(), stats.IdleConns(), stats.AcquireCount(), stats.CanceledAcquireCount(), stats.EmptyAcquireCount(), stats.MaxLifetimeDestroyCount())

	t.Logf("Demo completed!")
	t.Logf("- Trees: %d (fanout %d)", numTrees, fanout)
	t.Logf("- Participants: %d", numParticipants)
	t.Logf("- Participants who received: %d out of %d", receivedCount, numParticipants-1)

	if receivedCount != numParticipants-1 {
		t.Errorf("Not all participants received the bundle: got %d, want %d", receivedCount, numParticipants-1)
	}
}

func TestMultiPublisherPropagation(t *testing.T) {
	numParticipants := 100
	numTrees := 6
	fanout := 4

	participants := make([]string, numParticipants)
	privKeys := make(map[string][]byte)
	pubKeys := make(map[string]string)

	for i := 0; i < numParticipants; i++ {
		addr := fmt.Sprintf("participant%d", i)
		participants[i] = addr

		privKey := secp256k1.GenPrivKey()
		privKeys[addr] = privKey.Key
		pubKeys[addr] = hex.EncodeToString(privKey.PubKey().Bytes())
	}

	blockHash := sha256.Sum256([]byte("test-block"))
	pocHeight := int64(1000)

	trees := BuildTrees(participants, blockHash[:], numTrees, fanout)

	transport := NewMockTransport()
	pubKeyProvider := NewMockPubKeyProvider()
	for addr, pubKey := range pubKeys {
		pubKeyProvider.RegisterKey(addr, pubKey)
	}

	treeUsage := make(map[int]int)
	var sendLogs []string
	var treeUsageMu sync.Mutex

	pool, cleanup := setupPropagationPostgres(t)
	defer cleanup()
	ctx := context.Background()

	tempDir := t.TempDir()

	caches := make(map[string]*Cache)
	receivers := make(map[string]*Receiver)
	bundlers := make(map[string]*Bundler)
	stores := make(map[string]*artifacts.ArtifactStore)

	for i, addr := range participants {
		cache, err := NewCache(ctx, pool, addr)
		require.NoError(t, err)
		caches[addr] = cache

		sender := newTreeTrackingSender(addr, transport, &sendLogs, treeUsage, &treeUsageMu)
		receiver := NewReceiver(cache, trees, pubKeyProvider, addr, sender)
		receivers[addr] = receiver
		transport.RegisterReceiver(addr, receiver)

		storeDir := filepath.Join(tempDir, addr, "store")
		if err := os.MkdirAll(storeDir, 0755); err != nil {
			t.Fatalf("failed to create store dir for %s: %v", addr, err)
		}
		store, err := artifacts.Open(storeDir)
		if err != nil {
			t.Fatalf("failed to create store for %s: %v", addr, err)
		}
		stores[addr] = store

		for j := 0; j < 100; j++ {
			nonce := int32(i*1000 + j)
			vector := []byte(fmt.Sprintf("vector-%d-%d", i, j))
			if err := store.Add(nonce, vector); err != nil {
				t.Fatalf("failed to add artifact: %v", err)
			}
		}

		if err := store.Flush(); err != nil {
			t.Fatalf("failed to flush store for %s: %v", addr, err)
		}

		bundler := NewBundler(store, trees, sender, addr)
		bundlers[addr] = bundler
	}

	publishers := participants
	bundleIDs := make([][32]byte, len(publishers))

	t.Logf("Publishers (all participants): %d", len(publishers))

	for i, publisher := range publishers {
		bundler := bundlers[publisher]
		if bundler == nil {
			t.Fatalf("bundler missing for %s", publisher)
		}
		if err := bundler.Publish(pocHeight, blockHash[:], publisher, privKeys[publisher]); err != nil {
			t.Fatalf("failed to publish from %s: %v", publisher, err)
		}
		bundleIDs[i] = MakeBundleID(publisher, pocHeight, stores[publisher].GetRoot(), stores[publisher].Count(), 1)
	}

	actualReceipts := 0

	for _, addr := range participants {
		for i, bundleID := range bundleIDs {
			publisher := publishers[i]
			if addr == publisher {
				continue
			}

			header, err := caches[addr].GetHeader(bundleID)
			if err != nil {
				continue
			}

			if header.Participant != publisher {
				t.Errorf("participant %s: wrong sender in header: got %s, want %s",
					addr, header.Participant, publisher)
			}

			actualReceipts++
		}
	}

	for _, store := range stores {
		store.Close()
	}

	t.Logf("\nTree Usage Statistics:")
	totalTreeSends := 0
	for i := 0; i < numTrees; i++ {
		count := treeUsage[i]
		totalTreeSends += count
		t.Logf("  Tree %d: %d sends", i, count)
	}
	t.Logf("Total sends: %d", len(sendLogs))

	t.Logf("\nPropagation Results:")
	t.Logf("- Participants: %d", numParticipants)
	t.Logf("- Publishers: %d", len(publishers))
	t.Logf("- Trees: %d (fanout %d)", numTrees, fanout)
	t.Logf("- Actual receipts: %d", actualReceipts)
}

func TestTreeTopology(t *testing.T) {
	participants := []string{"A", "B", "C", "D", "E", "F", "G", "H", "I", "J"}
	blockHash := sha256.Sum256([]byte("test"))

	trees := BuildTrees(participants, blockHash[:], 4, 2)

	for _, tree := range trees {
		t.Logf("\nTree %d:", tree.Index)
		t.Logf("Order: %v", tree.Shuffled)

		for _, addr := range participants {
			parent, children := tree.Role(addr)
			t.Logf("  %s: parent=%s, children=%v", addr, parent, children)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestBundleSigning(t *testing.T) {
	privKey := secp256k1.GenPrivKey()
	pubKey := hex.EncodeToString(privKey.PubKey().Bytes())

	header := BundleHeader{
		BundleID:     [32]byte{1, 2, 3},
		Participant:  "test-participant",
		PocHeight:    1000,
		PocBlockHash: []byte("block-hash"),
		RootHash:     []byte("root-hash"),
		Count:        100,
		Version:      1,
		CreatedAt:    1234567890,
	}

	sig, err := SignHeader(header, privKey.Key)
	if err != nil {
		t.Fatalf("failed to sign header: %v", err)
	}

	header.Signature = sig

	if err := VerifyHeader(header, pubKey); err != nil {
		t.Fatalf("failed to verify header: %v", err)
	}

	header.Count = 999
	if err := VerifyHeader(header, pubKey); err == nil {
		t.Fatal("expected verification to fail after tampering")
	}

	t.Log("Bundle signing and verification works correctly")
}
