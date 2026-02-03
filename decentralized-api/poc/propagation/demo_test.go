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

	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/stretchr/testify/require"
)

type testED25519Signer struct {
	key []byte
}

func (s *testED25519Signer) Sign(msg []byte) ([]byte, error) {
	if len(s.key) != 64 {
		return nil, fmt.Errorf("invalid ed25519 private key length: %d", len(s.key))
	}
	privKey := ed25519.PrivKey(s.key)
	return privKey.Sign(msg)
}

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

func (l *loggingSender) SendProofs(to string, bundleID [32]byte, proofs []ProofItem) error {
	l.mu.Lock()
	if l.records != nil {
		*l.records = append(*l.records, fmt.Sprintf("proofs: %s -> %s bundle=%x count=%d", l.from, to, bundleID[:8], len(proofs)))
	}
	l.mu.Unlock()
	proofSender, ok := l.dst.(interface {
		SendProofs(to string, bundleID [32]byte, proofs []ProofItem) error
	})
	if !ok {
		return fmt.Errorf("destination sender does not support SendProofs")
	}
	return proofSender.SendProofs(to, bundleID, proofs)
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

func (s *treeTrackingSender) SendProofs(to string, bundleID [32]byte, proofs []ProofItem) error {
	s.mu.Lock()
	if s.records != nil {
		*s.records = append(*s.records, fmt.Sprintf("proofs: %s -> %s bundle=%x count=%d", s.from, to, bundleID[:8], len(proofs)))
	}
	s.mu.Unlock()
	proofSender, ok := s.dst.(interface {
		SendProofs(to string, bundleID [32]byte, proofs []ProofItem) error
	})
	if !ok {
		return fmt.Errorf("destination sender does not support SendProofs")
	}
	return proofSender.SendProofs(to, bundleID, proofs)
}

type propagationStorageFactory func(t *testing.T, tempDir, addr string) (BundleStorage, error)

func testPropagationDemo(t *testing.T, numParticipants int, storageFactory propagationStorageFactory) {
	numTrees := 8
	fanout := 4

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

	blockHash := sha256.Sum256([]byte("test-block"))
	pocHeight := int64(1000)

	trees := BuildTreesWithWeights(weightedParticipants, blockHash[:], numTrees, fanout)

	participants := make([]string, numParticipants)
	for i, wp := range weightedParticipants {
		participants[i] = wp.Address
	}

	transport := NewMockTransport()
	pubKeyProvider := NewMockPubKeyProvider()
	for addr, pubKey := range pubKeys {
		pubKeyProvider.RegisterKey(addr, pubKey)
	}

	var sendLogs []string
	tempDir := t.TempDir()

	caches := make(map[string]*Cache)
	receivers := make(map[string]*Receiver)
	bundlers := make(map[string]*Bundler)
	stores := make(map[string]*artifacts.ArtifactStore)

	for i, addr := range participants {
		storage, err := storageFactory(t, tempDir, addr)
		require.NoError(t, err)
		cache := NewCache(storage)
		caches[addr] = cache

		perParticipantSender := transport.NewSenderFor(addr)
		sender := newLoggingSender(addr, perParticipantSender, &sendLogs)
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

		signer := &testED25519Signer{key: privKeys[addr]}
		bundler := NewBundler(signer, cache, trees, sender, addr)
		bundlers[addr] = bundler
	}

	sender := trees[0].Shuffled[len(trees[0].Shuffled)-1]

	senderCount := stores[sender].Count()
	senderRoot := stores[sender].GetRoot()
	if err := bundlers[sender].Publish(pocHeight, sender, pubKeys[sender], senderCount, senderRoot); err != nil {
		t.Fatalf("failed to publish: %v", err)
	}

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

	connectionsPerParticipant := make(map[string]int)
	for _, tree := range trees {
		for _, node := range tree.Nodes {
			if node.Parent != nil {
				connectionsPerParticipant[node.Address]++
			}
			connectionsPerParticipant[node.Address] += len(node.Children)
		}
	}

	maxConns, totalConns := 0, 0
	for _, conns := range connectionsPerParticipant {
		totalConns += conns
		if conns > maxConns {
			maxConns = conns
		}
	}
	avgConns := float64(totalConns) / float64(len(connectionsPerParticipant))

	t.Logf("- Trees: %d (fanout %d)", numTrees, fanout)
	t.Logf("- Participants: %d", numParticipants)
	t.Logf("- Participants who received: %d out of %d", receivedCount, numParticipants-1)
	t.Logf("- Max connections per participant: %d (expected ~%d)", maxConns, numTrees*(fanout+1))
	t.Logf("- Avg connections per participant: %.1f", avgConns)
	t.Logf("- Total messages sent: %d", len(sendLogs))

	if receivedCount != numParticipants-1 {
		t.Errorf("Not all participants received the bundle: got %d, want %d", receivedCount, numParticipants-1)
	}
}

func TestPropagationDemo(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping postgres tests in -short mode")
	}

	pool, cleanup := setupPropagationPostgres(t)
	defer cleanup()

	testPropagationDemo(t, 1000, func(t *testing.T, tempDir, addr string) (BundleStorage, error) {
		return NewPostgresBundleStorage(context.Background(), pool, addr)
	})
}

func TestPropagationDemoSmallPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping postgres tests in -short mode")
	}

	pool, cleanup := setupPropagationPostgres(t)
	defer cleanup()

	testPropagationDemo(t, 100, func(t *testing.T, tempDir, addr string) (BundleStorage, error) {
		return NewPostgresBundleStorage(context.Background(), pool, addr)
	})
}

func TestPropagationDemoWithFileStorage(t *testing.T) {
	testPropagationDemo(t, 100, func(t *testing.T, tempDir, addr string) (BundleStorage, error) {
		storageDir := filepath.Join(tempDir, addr, "bundles")
		return NewFileBundleStorage(storageDir)
	})
}

func TestPropagationDemoLargeWithFileStorage(t *testing.T) {
	testPropagationDemo(t, 1000, func(t *testing.T, tempDir, addr string) (BundleStorage, error) {
		storageDir := filepath.Join(tempDir, addr, "bundles")
		return NewFileBundleStorage(storageDir)
	})
}

func testProofPropagation(t *testing.T, numParticipants int, storageFactory propagationStorageFactory) {
	numTrees := 8
	fanout := 4

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

	blockHash := sha256.Sum256([]byte("test-block-proofs"))
	pocHeight := int64(2000)

	trees := BuildTreesWithWeights(weightedParticipants, blockHash[:], numTrees, fanout)

	participants := make([]string, numParticipants)
	for i, wp := range weightedParticipants {
		participants[i] = wp.Address
	}

	transport := NewMockTransport()
	pubKeyProvider := NewMockPubKeyProvider()
	for addr, pubKey := range pubKeys {
		pubKeyProvider.RegisterKey(addr, pubKey)
	}

	var sendLogs []string
	var sendLogsMu sync.Mutex
	tempDir := t.TempDir()

	caches := make(map[string]*Cache)
	receivers := make(map[string]*Receiver)
	bundlers := make(map[string]*Bundler)
	stores := make(map[string]*artifacts.ArtifactStore)

	for i, addr := range participants {
		storage, err := storageFactory(t, tempDir, addr)
		require.NoError(t, err)
		cache := NewCache(storage)
		caches[addr] = cache

		perParticipantSender := transport.NewSenderFor(addr)
		sender := &proofLoggingSender{
			from:    addr,
			dst:     perParticipantSender,
			records: &sendLogs,
			mu:      &sendLogsMu,
		}
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

		for j := 0; j < 50; j++ {
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
		bundler := NewBundler(signer, cache, trees, sender, addr)
		bundlers[addr] = bundler
	}

	sender := trees[0].Shuffled[len(trees[0].Shuffled)-1]

	senderCount := stores[sender].Count()
	senderRoot := stores[sender].GetRoot()
	if err := bundlers[sender].Publish(pocHeight, sender, pubKeys[sender], senderCount, senderRoot); err != nil {
		t.Fatalf("failed to publish header: %v", err)
	}

	bundleID := MakeBundleID(sender, pocHeight, senderRoot, senderCount)

	proofs := make([]ProofItem, 10)
	for i := range proofs {
		proofs[i] = ProofItem{
			LeafIndex:   uint32(i),
			NonceValue:  int32(i),
			VectorBytes: fmt.Sprintf("vector-%d", i),
			Proof:       []string{fmt.Sprintf("proof-hash-%d", i)},
		}
	}

	if err := bundlers[sender].PublishProofs(bundleID, proofs); err != nil {
		t.Fatalf("failed to publish proofs: %v", err)
	}

	for _, receiver := range receivers {
		receiver.Wait()
	}

	headerReceivedCount := 0
	proofReceivedCount := 0
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
		headerReceivedCount++

		cachedProofs, err := caches[addr].GetProofs(bundleID)
		if err != nil {
			continue
		}

		if len(cachedProofs) != len(proofs) {
			t.Errorf("participant %s: wrong proof count: got %d, want %d",
				addr, len(cachedProofs), len(proofs))
		}
		proofReceivedCount++
	}

	for _, store := range stores {
		store.Close()
	}

	headerMsgCount := 0
	proofMsgCount := 0
	sendLogsMu.Lock()
	for _, log := range sendLogs {
		if len(log) > 6 && log[:6] == "header" {
			headerMsgCount++
		} else if len(log) > 6 && log[:6] == "proofs" {
			proofMsgCount++
		}
	}
	sendLogsMu.Unlock()

	t.Logf("- Trees: %d (fanout %d)", numTrees, fanout)
	t.Logf("- Participants: %d", numParticipants)
	t.Logf("- Headers received: %d out of %d", headerReceivedCount, numParticipants-1)
	t.Logf("- Proofs received: %d out of %d", proofReceivedCount, numParticipants-1)
	t.Logf("- Header messages sent: %d", headerMsgCount)
	t.Logf("- Proof messages sent: %d", proofMsgCount)

	if headerReceivedCount != numParticipants-1 {
		t.Errorf("Not all participants received the header: got %d, want %d", headerReceivedCount, numParticipants-1)
	}

	if proofReceivedCount != numParticipants-1 {
		t.Errorf("Not all participants received the proofs: got %d, want %d", proofReceivedCount, numParticipants-1)
	}
}

type proofLoggingSender struct {
	from    string
	dst     Sender
	records *[]string
	mu      *sync.Mutex
}

func (p *proofLoggingSender) SendHeader(treeIdx int, to string, h BundleHeader) error {
	p.mu.Lock()
	if p.records != nil {
		*p.records = append(*p.records, fmt.Sprintf("header: %s -> %s tree=%d bundle=%x", p.from, to, treeIdx, h.BundleID[:8]))
	}
	p.mu.Unlock()
	return p.dst.SendHeader(treeIdx, to, h)
}

func (p *proofLoggingSender) SendProofs(to string, bundleID [32]byte, proofs []ProofItem) error {
	p.mu.Lock()
	if p.records != nil {
		*p.records = append(*p.records, fmt.Sprintf("proofs: %s -> %s bundle=%x count=%d", p.from, to, bundleID[:8], len(proofs)))
	}
	p.mu.Unlock()
	proofSender, ok := p.dst.(interface {
		SendProofs(to string, bundleID [32]byte, proofs []ProofItem) error
	})
	if !ok {
		return fmt.Errorf("destination sender does not support SendProofs")
	}
	return proofSender.SendProofs(to, bundleID, proofs)
}

func TestProofPropagationWithFileStorage(t *testing.T) {
	testProofPropagation(t, 50, func(t *testing.T, tempDir, addr string) (BundleStorage, error) {
		storageDir := filepath.Join(tempDir, addr, "bundles")
		return NewFileBundleStorage(storageDir)
	})
}

func TestProofPropagationLargeWithFileStorage(t *testing.T) {
	testProofPropagation(t, 1000, func(t *testing.T, tempDir, addr string) (BundleStorage, error) {
		storageDir := filepath.Join(tempDir, addr, "bundles")
		return NewFileBundleStorage(storageDir)
	})
}

func testMultiPublisherPropagation(t *testing.T, numParticipants int, storageFactory propagationStorageFactory) {
	numTrees := 6
	fanout := 4

	weightedParticipants := make([]WeightedParticipant, numParticipants)
	privKeys := make(map[string][]byte)
	pubKeys := make(map[string]string)

	for i := 0; i < numParticipants; i++ {
		addr := fmt.Sprintf("participant%d", i)
		weight := uint64(50 + i*10)
		weightedParticipants[i] = WeightedParticipant{
			Address: addr,
			Weight:  weight,
		}

		privKey := ed25519.GenPrivKey()
		privKeys[addr] = privKey.Bytes()
		pubKeys[addr] = base64.StdEncoding.EncodeToString(privKey.PubKey().Bytes())
	}

	blockHash := sha256.Sum256([]byte("test-block"))
	pocHeight := int64(1000)

	trees := BuildTreesWithWeights(weightedParticipants, blockHash[:], numTrees, fanout)

	participants := make([]string, numParticipants)
	for i, wp := range weightedParticipants {
		participants[i] = wp.Address
	}

	transport := NewMockTransport()
	pubKeyProvider := NewMockPubKeyProvider()
	for addr, pubKey := range pubKeys {
		pubKeyProvider.RegisterKey(addr, pubKey)
	}

	treeUsage := make(map[int]int)
	var sendLogs []string
	var treeUsageMu sync.Mutex
	tempDir := t.TempDir()

	caches := make(map[string]*Cache)
	receivers := make(map[string]*Receiver)
	bundlers := make(map[string]*Bundler)
	stores := make(map[string]*artifacts.ArtifactStore)

	for i, addr := range participants {
		storage, err := storageFactory(t, tempDir, addr)
		require.NoError(t, err)
		cache := NewCache(storage)
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

		signer := &testED25519Signer{key: privKeys[addr]}
		bundler := NewBundler(signer, cache, trees, sender, addr)
		bundlers[addr] = bundler
	}

	publishers := participants
	bundleIDs := make([][32]byte, len(publishers))

	for i, publisher := range publishers {
		bundler := bundlers[publisher]
		if bundler == nil {
			t.Fatalf("bundler missing for %s", publisher)
		}
		pubCount := stores[publisher].Count()
		pubRoot := stores[publisher].GetRoot()
		if err := bundler.Publish(pocHeight, publisher, pubKeys[publisher], pubCount, pubRoot); err != nil {
			t.Fatalf("failed to publish from %s: %v", publisher, err)
		}
		bundleIDs[i] = MakeBundleID(publisher, pocHeight, pubRoot, pubCount)
	}

	for _, receiver := range receivers {
		receiver.Wait()
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

	t.Logf("\nPropagation Results:")
	t.Logf("- Participants: %d", numParticipants)
	t.Logf("- Publishers: %d", len(publishers))
	t.Logf("- Trees: %d (fanout %d)", numTrees, fanout)
	t.Logf("- Actual receipts: %d", actualReceipts)
}

func TestMultiPublisherPropagation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping postgres tests in -short mode")
	}

	pool, cleanup := setupPropagationPostgres(t)
	defer cleanup()

	testMultiPublisherPropagation(t, 100, func(t *testing.T, tempDir, addr string) (BundleStorage, error) {
		return NewPostgresBundleStorage(context.Background(), pool, addr)
	})
}

func TestMultiPublisherPropagationWithFileStorage(t *testing.T) {
	testMultiPublisherPropagation(t, 50, func(t *testing.T, tempDir, addr string) (BundleStorage, error) {
		storageDir := filepath.Join(tempDir, addr, "bundles")
		return NewFileBundleStorage(storageDir)
	})
}

func TestTreeTopology(t *testing.T) {
	weightedParticipants := []WeightedParticipant{
		{Address: "A", Weight: 100},
		{Address: "B", Weight: 90},
		{Address: "C", Weight: 80},
		{Address: "D", Weight: 70},
		{Address: "E", Weight: 60},
		{Address: "F", Weight: 50},
		{Address: "G", Weight: 40},
		{Address: "H", Weight: 30},
		{Address: "I", Weight: 20},
		{Address: "J", Weight: 10},
	}
	blockHash := sha256.Sum256([]byte("test"))

	trees := BuildTreesWithWeights(weightedParticipants, blockHash[:], 4, 2)

	for _, tree := range trees {
		t.Logf("\nTree %d:", tree.Index)
		t.Logf("Order: %v", tree.Shuffled)

		for _, wp := range weightedParticipants {
			parent, children := tree.Role(wp.Address)
			t.Logf("  %s (weight=%d): parent=%s, children=%v", wp.Address, wp.Weight, parent, children)
		}
	}
}

func TestBundleSigning(t *testing.T) {
	privKey := ed25519.GenPrivKey()
	pubKey := base64.StdEncoding.EncodeToString(privKey.PubKey().Bytes())

	header := BundleHeader{
		BundleID:    [32]byte{1, 2, 3},
		Participant: "test-participant",
		PubKey:      pubKey,
		PocHeight:   1000,
		RootHash:    []byte("root-hash"),
		Count:       100,
		CreatedAt:   1234567890,
	}

	sig, err := SignHeader(header, privKey.Bytes())
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
