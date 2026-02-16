package propagation

import (
	"context"
	"crypto/sha256"
	"decentralized-api/poc/artifacts"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/stretchr/testify/require"
)

type fltqLoggingSender struct {
	from    string
	dst     FLTQSender
	records *[]string
	mu      sync.Mutex
}

func newFLTQLoggingSender(from string, dst FLTQSender, records *[]string) *fltqLoggingSender {
	return &fltqLoggingSender{from: from, dst: dst, records: records}
}

func (l *fltqLoggingSender) SendHeaderFLTQ(to string, h BundleHeader) error {
	l.mu.Lock()
	if l.records != nil {
		*l.records = append(*l.records, fmt.Sprintf("header: %s -> %s bundle=%x", l.from, to, h.BundleID[:8]))
	}
	l.mu.Unlock()
	return l.dst.SendHeaderFLTQ(to, h)
}

func (l *fltqLoggingSender) SendProofsFLTQ(to string, bundleID [32]byte, proofs []ProofItem) error {
	l.mu.Lock()
	if l.records != nil {
		*l.records = append(*l.records, fmt.Sprintf("proofs: %s -> %s bundle=%x count=%d", l.from, to, bundleID[:8], len(proofs)))
	}
	l.mu.Unlock()
	return l.dst.SendProofsFLTQ(to, bundleID, proofs)
}

func testFLTQPropagationDemo(t *testing.T, numParticipants int, storageFactory propagationStorageFactory) {
	weightedParticipants := make([]WeightedParticipant, numParticipants)
	privKeys := make(map[string][]byte)
	pubKeys := make(map[string]string)

	for i := 0; i < numParticipants; i++ {
		addr := fmt.Sprintf("participant%d", i)
		weight := uint64(100 + i)
		weightedParticipants[i] = WeightedParticipant{
			Address: addr,
			Weight:  weight,
		}

		privKey := ed25519.GenPrivKey()
		privKeys[addr] = privKey.Bytes()
		pubKeys[addr] = base64.StdEncoding.EncodeToString(privKey.PubKey().Bytes())
	}

	blockHash := sha256.Sum256([]byte("test-block-fltq"))
	pocHeight := int64(1000)

	cube := BuildFLTQWithWeights(weightedParticipants, blockHash[:])

	participants := make([]string, numParticipants)
	for i, wp := range weightedParticipants {
		participants[i] = wp.Address
	}

	transport := NewFLTQMockTransport()
	pubKeyProvider := NewMockPubKeyProvider()
	for addr, pubKey := range pubKeys {
		pubKeyProvider.RegisterKey(addr, pubKey)
	}

	var sendLogs []string
	tempDir := t.TempDir()

	caches := make(map[string]*Cache)
	receivers := make(map[string]*FLTQReceiver)
	bundlers := make(map[string]*FLTQBundler)
	stores := make(map[string]*artifacts.ArtifactStore)

	for i, addr := range participants {
		storage, err := storageFactory(t, tempDir, addr)
		require.NoError(t, err)
		cache := NewCache(storage)
		caches[addr] = cache

		perParticipantSender := transport.NewSenderFor(addr)
		sender := newFLTQLoggingSender(addr, perParticipantSender, &sendLogs)
		receiver := NewFLTQReceiver(cache, []*FLTQCube{cube}, pubKeyProvider, addr, sender)
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

		signer := &testED25519Signer{key: privKeys[addr]}
		bundler := NewFLTQBundler(signer, cache, []*FLTQCube{cube}, sender, addr)
		bundlers[addr] = bundler
	}

	sender := participants[len(participants)-1]

	senderCount := stores[sender].Count()
	senderRoot := stores[sender].GetRoot()
	if err := bundlers[sender].Publish(pocHeight, sender, pubKeys[sender], senderCount, senderRoot); err != nil {
		t.Fatalf("failed to publish: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	bundleID := MakeBundleID(sender, pocHeight, senderRoot, senderCount)

	for _, receiver := range receivers {
		receiver.Wait()
	}

	receivedCount := 0
	for _, addr := range participants {
		if addr == sender {
			continue
		}

		header, err := caches[addr].GetHeader(bundleID)
		if err != nil {
			continue
		}

		if header.Participant != sender {
			t.Errorf("participant %s: wrong sender in header: got %s, want %s",
				addr, header.Participant, sender)
		}

		if header.Count != senderCount {
			t.Errorf("participant %s: wrong count in header: got %d, want %d",
				addr, header.Count, senderCount)
		}

		receivedCount++
	}

	for _, store := range stores {
		store.Close()
	}

	allNeighbors := make(map[string]map[string]bool)
	for addr, node := range cube.Nodes {
		if allNeighbors[addr] == nil {
			allNeighbors[addr] = make(map[string]bool)
		}
		for _, neighbor := range node.Neighbors {
			allNeighbors[addr][neighbor] = true
		}
	}

	maxConns, totalConns := 0, 0
	for addr := range allNeighbors {
		conns := len(allNeighbors[addr])
		totalConns += conns
		if conns > maxConns {
			maxConns = conns
		}
	}
	avgConns := float64(totalConns) / float64(numParticipants)

	t.Logf("- FLTQ cube dimensions: %d", cube.Dimensions)
	t.Logf("- Expected diameter: %d", (cube.Dimensions+2)/2)
	t.Logf("- Participants: %d", numParticipants)
	t.Logf("- Participants who received: %d out of %d", receivedCount, numParticipants-1)
	t.Logf("- Max connections per participant: %d", maxConns)
	t.Logf("- Avg connections per participant: %.1f", avgConns)
	t.Logf("- Total messages sent: %d", len(sendLogs))
	t.Logf("- Expected max connections per participant: %d (n+1)", cube.Dimensions+1)

	if receivedCount != numParticipants-1 {
		t.Errorf("Not all participants received the bundle: got %d, want %d", receivedCount, numParticipants-1)
	}
}

func TestFLTQPropagationDemoWithFileStorage(t *testing.T) {
	testFLTQPropagationDemo(t, 103, func(t *testing.T, tempDir, addr string) (BundleStorage, error) {
		storageDir := filepath.Join(tempDir, addr, "bundles_fltq")
		return NewFileBundleStorage(storageDir)
	})
}

func TestFLTQPropagationDemoLargeWithFileStorage(t *testing.T) {
	testFLTQPropagationDemo(t, 1000, func(t *testing.T, tempDir, addr string) (BundleStorage, error) {
		storageDir := filepath.Join(tempDir, addr, "bundles_fltq")
		return NewFileBundleStorage(storageDir)
	})
}

func TestFLTQPropagationDemo(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping postgres tests in -short mode")
	}

	pool, cleanup := setupPropagationPostgres(t)
	defer cleanup()

	testFLTQPropagationDemo(t, 1000, func(t *testing.T, tempDir, addr string) (BundleStorage, error) {
		return NewPostgresBundleStorage(context.Background(), pool, addr)
	})
}

func TestFLTQPropagationDemoSmallPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping postgres tests in -short mode")
	}

	pool, cleanup := setupPropagationPostgres(t)
	defer cleanup()

	testFLTQPropagationDemo(t, 100, func(t *testing.T, tempDir, addr string) (BundleStorage, error) {
		return NewPostgresBundleStorage(context.Background(), pool, addr)
	})
}

func TestFLTQTopology(t *testing.T) {
	participants := make([]WeightedParticipant, 100)
	for i := 0; i < 100; i++ {
		participants[i] = WeightedParticipant{
			Address: fmt.Sprintf("participant%d", i),
			Weight:  uint64(100 + i),
		}
	}

	blockHash := sha256.Sum256([]byte("test-block"))
	cube := BuildFLTQWithWeights(participants, blockHash[:])

	require.Equal(t, 7, cube.Dimensions)
	require.Equal(t, 128, cube.Size)
	require.Equal(t, 100, len(cube.Nodes))

	for addr, node := range cube.Nodes {
		require.NotNil(t, node)
		require.Equal(t, addr, node.Address)
		require.LessOrEqual(t, len(node.Neighbors), cube.Dimensions+1)
		require.GreaterOrEqual(t, len(node.Neighbors), 1)

		for _, neighborAddr := range node.Neighbors {
			neighbor := cube.Nodes[neighborAddr]
			require.NotNil(t, neighbor)

			found := false
			for _, backNeighbor := range neighbor.Neighbors {
				if backNeighbor == addr {
					found = true
					break
				}
			}
			require.True(t, found, "neighbor relationship should be bidirectional")
		}
	}

	t.Logf("FLTQ topology verified:")
	t.Logf("- Dimensions: %d", cube.Dimensions)
	t.Logf("- Size: %d", cube.Size)
	t.Logf("- Nodes: %d", len(cube.Nodes))
	t.Logf("- Expected diameter: %d", (cube.Dimensions+2)/2)

	minNeighbors := cube.Dimensions + 1
	maxNeighbors := 0
	totalNeighbors := 0
	for _, node := range cube.Nodes {
		neighborCount := len(node.Neighbors)
		totalNeighbors += neighborCount
		if neighborCount < minNeighbors {
			minNeighbors = neighborCount
		}
		if neighborCount > maxNeighbors {
			maxNeighbors = neighborCount
		}
	}
	avgNeighbors := float64(totalNeighbors) / float64(len(cube.Nodes))

	t.Logf("- Min neighbors: %d", minNeighbors)
	t.Logf("- Max neighbors: %d", maxNeighbors)
	t.Logf("- Avg neighbors: %.2f", avgNeighbors)
	t.Logf("- Expected degree (n+1): %d", cube.Dimensions+1)
}

func TestFLTQSmallTopology(t *testing.T) {
	participants := make([]WeightedParticipant, 10)
	for i := 0; i < 10; i++ {
		participants[i] = WeightedParticipant{
			Address: fmt.Sprintf("participant%d", i),
			Weight:  uint64(100),
		}
	}

	blockHash := sha256.Sum256([]byte("test-block-small"))
	cube := BuildFLTQWithWeights(participants, blockHash[:])

	require.Equal(t, 4, cube.Dimensions)
	require.Equal(t, 16, cube.Size)
	require.Equal(t, 10, len(cube.Nodes))

	for addr, node := range cube.Nodes {
		t.Logf("Node %s: position=%d, neighbors=%v", addr, node.Position, node.Neighbors)
	}
}
