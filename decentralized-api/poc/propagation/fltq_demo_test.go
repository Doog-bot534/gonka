package propagation

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
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
		*l.records = append(*l.records, fmt.Sprintf("header: %s -> %s bundle=%x", l.from, to, h.BundleID[:]))
	}
	l.mu.Unlock()
	return l.dst.SendHeaderFLTQ(to, h)
}

type bandwidthTracker struct {
	sentBytes        sync.Map // string -> *atomic.Int64
	receivedBytes    sync.Map // string -> *atomic.Int64
	sentMessages     sync.Map // string -> *atomic.Int64
	receivedMessages sync.Map // string -> *atomic.Int64
}

func newBandwidthTracker() *bandwidthTracker {
	return &bandwidthTracker{}
}

func (b *bandwidthTracker) counter(m *sync.Map, key string) *atomic.Int64 {
	v, _ := m.LoadOrStore(key, &atomic.Int64{})
	return v.(*atomic.Int64)
}

func (b *bandwidthTracker) record(from, to string, size int) {
	b.counter(&b.sentBytes, from).Add(int64(size))
	b.counter(&b.receivedBytes, to).Add(int64(size))
	b.counter(&b.sentMessages, from).Add(1)
	b.counter(&b.receivedMessages, to).Add(1)
}

func (b *bandwidthTracker) totals() (int, int) {
	var totalSent, totalReceived int64
	b.sentBytes.Range(func(_, v any) bool {
		totalSent += v.(*atomic.Int64).Load()
		return true
	})
	b.receivedBytes.Range(func(_, v any) bool {
		totalReceived += v.(*atomic.Int64).Load()
		return true
	})
	return int(totalSent), int(totalReceived)
}

func (b *bandwidthTracker) messageTotals() (int, int) {
	var sent, received int64
	b.sentMessages.Range(func(_, v any) bool {
		sent += v.(*atomic.Int64).Load()
		return true
	})
	b.receivedMessages.Range(func(_, v any) bool {
		received += v.(*atomic.Int64).Load()
		return true
	})
	return int(sent), int(received)
}

func (b *bandwidthTracker) received(addr string) int {
	v, ok := b.receivedBytes.Load(addr)
	if !ok {
		return 0
	}
	return int(v.(*atomic.Int64).Load())
}

func (b *bandwidthTracker) receivedMsgs(addr string) int {
	v, ok := b.receivedMessages.Load(addr)
	if !ok {
		return 0
	}
	return int(v.(*atomic.Int64).Load())
}

type bandwidthTrackingSender struct {
	from    string
	dst     FLTQSender
	tracker *bandwidthTracker
}

func newBandwidthTrackingSender(from string, dst FLTQSender, tracker *bandwidthTracker) *bandwidthTrackingSender {
	return &bandwidthTrackingSender{from: from, dst: dst, tracker: tracker}
}

func (s *bandwidthTrackingSender) SendHeaderFLTQ(to string, h BundleHeader) error {
	size, err := json.Marshal(h)
	if err != nil {
		return err
	}
	s.tracker.record(s.from, to, len(size))
	return s.dst.SendHeaderFLTQ(to, h)
}

func testParticipantAddr(i int) string {
	return fmt.Sprintf("gonka1participant%04d", i)
}

func testFLTQPropagationDemo(t *testing.T, numParticipants int, storageFactory propagationStorageFactory) {
	weightedParticipants := make([]WeightedParticipant, numParticipants)
	privKeys := make(map[string][]byte)
	pubKeys := make(map[string]string)

	for i := 0; i < numParticipants; i++ {
		addr := testParticipantAddr(i)
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

	for _, addr := range participants {
		storage, err := storageFactory(t, tempDir, addr)
		require.NoError(t, err)
		cache := NewCache(storage)
		caches[addr] = cache

		perParticipantSender := transport.NewSenderFor(addr)
		sender := newFLTQLoggingSender(addr, perParticipantSender, &sendLogs)
		receiver := NewFLTQReceiver(cache, cube, pubKeyProvider, addr, sender)
		receivers[addr] = receiver
		transport.RegisterReceiver(addr, receiver)

		signer := &testED25519Signer{key: privKeys[addr]}
		bundler := NewFLTQBundler(signer, cache, cube, sender, addr)
		bundlers[addr] = bundler
	}

	sender := participants[len(participants)-1]

	senderCount := uint32(1)
	senderRoot := sha256.Sum256([]byte(fmt.Sprintf("root-%s", sender)))
	if err := bundlers[sender].Publish(pocHeight, sender, senderCount, senderRoot[:]); err != nil {
		t.Fatalf("failed to publish: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	bundleID := MakeBundleID(sender, pocHeight, senderRoot[:], senderCount)

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

func TestFLTQPropagationBandwidth(t *testing.T) {
	numParticipants := 100
	weightedParticipants := make([]WeightedParticipant, numParticipants)
	privKeys := make(map[string][]byte)
	pubKeys := make(map[string]string)

	for i := 0; i < numParticipants; i++ {
		addr := testParticipantAddr(i)
		weight := uint64(100 + i)
		weightedParticipants[i] = WeightedParticipant{
			Address: addr,
			Weight:  weight,
		}
		privKey := ed25519.GenPrivKey()
		privKeys[addr] = privKey.Bytes()
		pubKeys[addr] = base64.StdEncoding.EncodeToString(privKey.PubKey().Bytes())
	}

	blockHash := sha256.Sum256([]byte("bandwidth-block"))
	pocHeight := int64(1500)
	cube := BuildFLTQWithWeights(weightedParticipants, blockHash[:])

	participants := make([]string, numParticipants)
	for i, wp := range weightedParticipants {
		participants[i] = wp.Address
	}

	transport := NewFLTQMockTransport()
	tracker := newBandwidthTracker()
	pubKeyProvider := NewMockPubKeyProvider()
	for addr, pubKey := range pubKeys {
		pubKeyProvider.RegisterKey(addr, pubKey)
	}

	caches := make(map[string]*Cache)
	receivers := make(map[string]*FLTQReceiver)
	bundlers := make(map[string]*FLTQBundler)

	for _, addr := range participants {
		storage := NewMemBundleStorage()
		cache := NewCache(storage)
		caches[addr] = cache

		perParticipantSender := transport.NewSenderFor(addr)
		sender := newBandwidthTrackingSender(addr, perParticipantSender, tracker)
		receiver := NewFLTQReceiver(cache, cube, pubKeyProvider, addr, sender)
		receivers[addr] = receiver
		transport.RegisterReceiver(addr, receiver)

		signer := &testED25519Signer{key: privKeys[addr]}
		bundler := NewFLTQBundler(signer, cache, cube, sender, addr)
		bundlers[addr] = bundler
	}

	publishCh := make(chan string, len(participants))
	for _, addr := range participants {
		publishCh <- addr
	}
	close(publishCh)

	var publishWg sync.WaitGroup
	for i := 0; i < 50; i++ {
		publishWg.Add(1)
		go func() {
			defer publishWg.Done()
			for a := range publishCh {
				senderCount := uint32(1)
				senderRoot := sha256.Sum256([]byte(fmt.Sprintf("root-%s", a)))
				if err := bundlers[a].Publish(pocHeight, a, senderCount, senderRoot[:]); err != nil {
					t.Errorf("publish %s: %v", a, err)
				}
			}
		}()
	}
	publishWg.Wait()

	var waitWg sync.WaitGroup
	for _, r := range receivers {
		waitWg.Add(1)
		go func(recv *FLTQReceiver) {
			defer waitWg.Done()
			recv.Wait()
		}(r)
	}
	waitWg.Wait()

	for addr := range caches {
		bundles := caches[addr].AllBundlesForHeight(pocHeight)
		require.Len(t, bundles, numParticipants)
	}

	totalSent, totalReceived := tracker.totals()
	require.Equal(t, totalSent, totalReceived)

	totalSentMsgs, totalReceivedMsgs := tracker.messageTotals()
	require.Equal(t, totalSentMsgs, totalReceivedMsgs)

	for addr := range caches {
		require.Greater(t, tracker.received(addr), 0)
		require.Greater(t, tracker.receivedMsgs(addr), 0)
	}

	avgPerParticipant := float64(totalSent) / float64(numParticipants)
	avgMsgsPerParticipant := float64(totalReceivedMsgs) / float64(numParticipants)
	t.Logf("Total bytes sent: %d", totalSent)
	t.Logf("Total bytes received: %d", totalReceived)
	t.Logf("Average bytes per participant: %.1f", avgPerParticipant)
	t.Logf("Total messages sent: %d", totalSentMsgs)
	t.Logf("Total messages received: %d", totalReceivedMsgs)
	t.Logf("Average messages per participant: %.1f", avgMsgsPerParticipant)
}

func TestFLTQTopology(t *testing.T) {
	participants := make([]WeightedParticipant, 100)
	for i := 0; i < 100; i++ {
		participants[i] = WeightedParticipant{
			Address: testParticipantAddr(i),
			Weight:  uint64(100 + i),
		}
	}

	blockHash := sha256.Sum256([]byte("test-block"))
	config := FLTQConfig{
		NumToSplitPastryDigit: 2,
		PastryEntriesPerLevel: 0,
	}
	cube := BuildFLTQWithConfig(participants, blockHash[:], config)

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
}

func TestFLTQSmallTopology(t *testing.T) {
	participants := make([]WeightedParticipant, 10)
	for i := 0; i < 10; i++ {
		participants[i] = WeightedParticipant{
			Address: testParticipantAddr(i),
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
