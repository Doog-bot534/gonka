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

type hypercubeLoggingSender struct {
	from    string
	dst     HypercubeSender
	records *[]string
	mu      sync.Mutex
}

func newHypercubeLoggingSender(from string, dst HypercubeSender, records *[]string) *hypercubeLoggingSender {
	return &hypercubeLoggingSender{from: from, dst: dst, records: records}
}

func (l *hypercubeLoggingSender) SendHeaderHypercube(to string, h BundleHeader) error {
	l.mu.Lock()
	if l.records != nil {
		*l.records = append(*l.records, fmt.Sprintf("header: %s -> %s bundle=%x", l.from, to, h.BundleID[:8]))
	}
	l.mu.Unlock()
	return l.dst.SendHeaderHypercube(to, h)
}

func (l *hypercubeLoggingSender) SendProofsHypercube(to string, bundleID [32]byte, proofs []ProofItem) error {
	l.mu.Lock()
	if l.records != nil {
		*l.records = append(*l.records, fmt.Sprintf("proofs: %s -> %s bundle=%x count=%d", l.from, to, bundleID[:8], len(proofs)))
	}
	l.mu.Unlock()
	return l.dst.SendProofsHypercube(to, bundleID, proofs)
}

func testHypercubePropagationDemo(t *testing.T, numParticipants int, storageFactory propagationStorageFactory) {
	numHypercubes := 2

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

	blockHash := sha256.Sum256([]byte("test-block-hypercube"))
	pocHeight := int64(1000)

	hypercubes := BuildHypercubesWithWeights(weightedParticipants, blockHash[:], numHypercubes)

	participants := make([]string, numParticipants)
	for i, wp := range weightedParticipants {
		participants[i] = wp.Address
	}

	transport := NewHypercubeMockTransport()
	pubKeyProvider := NewMockPubKeyProvider()
	for addr, pubKey := range pubKeys {
		pubKeyProvider.RegisterKey(addr, pubKey)
	}

	var sendLogs []string
	tempDir := t.TempDir()

	caches := make(map[string]*Cache)
	receivers := make(map[string]*HypercubeReceiver)
	bundlers := make(map[string]*HypercubeBundler)
	stores := make(map[string]*artifacts.ArtifactStore)

	for i, addr := range participants {
		storage, err := storageFactory(t, tempDir, addr)
		require.NoError(t, err)
		cache := NewCache(storage)
		caches[addr] = cache

		perParticipantSender := transport.NewSenderFor(addr)
		sender := newHypercubeLoggingSender(addr, perParticipantSender, &sendLogs)
		receiver := NewHypercubeReceiver(cache, hypercubes, pubKeyProvider, addr, sender)
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
		bundler := NewHypercubeBundler(signer, cache, hypercubes, sender, addr)
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
	for _, hypercube := range hypercubes {
		for addr, node := range hypercube.Nodes {
			if allNeighbors[addr] == nil {
				allNeighbors[addr] = make(map[string]bool)
			}
			for _, neighbor := range node.Neighbors {
				allNeighbors[addr][neighbor] = true
			}
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

	t.Logf("- Hypercubes: %d", numHypercubes)
	t.Logf("- Hypercube dimensions: %d", hypercubes[0].Dimensions)
	t.Logf("- Participants: %d", numParticipants)
	t.Logf("- Participants who received: %d out of %d", receivedCount, numParticipants-1)
	t.Logf("- Max connections per participant: %d", maxConns)
	t.Logf("- Avg connections per participant: %.1f", avgConns)
	t.Logf("- Total messages sent: %d", len(sendLogs))
	t.Logf("- Expected max connections per participant (across %d hypercubes): ~%d", numHypercubes, hypercubes[0].Dimensions*numHypercubes)

	if receivedCount != numParticipants-1 {
		t.Errorf("Not all participants received the bundle: got %d, want %d", receivedCount, numParticipants-1)
	}
}

func TestHypercubePropagationDemoWithFileStorage(t *testing.T) {
	testHypercubePropagationDemo(t, 103, func(t *testing.T, tempDir, addr string) (BundleStorage, error) {
		storageDir := filepath.Join(tempDir, addr, "bundles_hypercube")
		return NewFileBundleStorage(storageDir)
	})
}

func TestHypercubePropagationDemoLargeWithFileStorage(t *testing.T) {
	testHypercubePropagationDemo(t, 1000, func(t *testing.T, tempDir, addr string) (BundleStorage, error) {
		storageDir := filepath.Join(tempDir, addr, "bundles_hypercube")
		return NewFileBundleStorage(storageDir)
	})
}

func TestHypercubePropagationDemo(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping postgres tests in -short mode")
	}

	pool, cleanup := setupPropagationPostgres(t)
	defer cleanup()

	testHypercubePropagationDemo(t, 1000, func(t *testing.T, tempDir, addr string) (BundleStorage, error) {
		return NewPostgresBundleStorage(context.Background(), pool, addr)
	})
}

func TestHypercubePropagationDemoSmallPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping postgres tests in -short mode")
	}

	pool, cleanup := setupPropagationPostgres(t)
	defer cleanup()

	testHypercubePropagationDemo(t, 100, func(t *testing.T, tempDir, addr string) (BundleStorage, error) {
		return NewPostgresBundleStorage(context.Background(), pool, addr)
	})
}

func TestHypercubeTopology(t *testing.T) {
	participants := make([]WeightedParticipant, 100)
	for i := 0; i < 100; i++ {
		participants[i] = WeightedParticipant{
			Address: fmt.Sprintf("participant%d", i),
			Weight:  uint64(100 + i),
		}
	}

	blockHash := sha256.Sum256([]byte("test-block"))
	hypercube := BuildHypercubeWithWeights(participants, blockHash[:])

	require.Equal(t, 7, hypercube.Dimensions)
	require.Equal(t, 128, hypercube.Size)
	require.Equal(t, 100, len(hypercube.Nodes))

	for addr, node := range hypercube.Nodes {
		require.NotNil(t, node)
		require.Equal(t, addr, node.Address)
		require.LessOrEqual(t, len(node.Neighbors), hypercube.Dimensions)
		require.GreaterOrEqual(t, len(node.Neighbors), 1)

		for _, neighborAddr := range node.Neighbors {
			neighbor := hypercube.Nodes[neighborAddr]
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

	t.Logf("Hypercube topology verified:")
	t.Logf("- Dimensions: %d", hypercube.Dimensions)
	t.Logf("- Size: %d", hypercube.Size)
	t.Logf("- Nodes: %d", len(hypercube.Nodes))

	minNeighbors := hypercube.Dimensions
	maxNeighbors := 0
	totalNeighbors := 0
	for _, node := range hypercube.Nodes {
		neighborCount := len(node.Neighbors)
		totalNeighbors += neighborCount
		if neighborCount < minNeighbors {
			minNeighbors = neighborCount
		}
		if neighborCount > maxNeighbors {
			maxNeighbors = neighborCount
		}
	}
	avgNeighbors := float64(totalNeighbors) / float64(len(hypercube.Nodes))

	t.Logf("- Min neighbors: %d", minNeighbors)
	t.Logf("- Max neighbors: %d", maxNeighbors)
	t.Logf("- Avg neighbors: %.2f", avgNeighbors)
}

func TestHypercubeSmallTopology(t *testing.T) {
	participants := make([]WeightedParticipant, 10)
	for i := 0; i < 10; i++ {
		participants[i] = WeightedParticipant{
			Address: fmt.Sprintf("participant%d", i),
			Weight:  uint64(100),
		}
	}

	blockHash := sha256.Sum256([]byte("test-block-small"))
	hypercube := BuildHypercubeWithWeights(participants, blockHash[:])

	require.Equal(t, 4, hypercube.Dimensions)
	require.Equal(t, 16, hypercube.Size)
	require.Equal(t, 10, len(hypercube.Nodes))

	for addr, node := range hypercube.Nodes {
		t.Logf("Node %s: position=%d, neighbors=%v", addr, node.Position, node.Neighbors)
	}
}
