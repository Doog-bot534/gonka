package propagation

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"decentralized-api/poc/artifacts"

	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func setupPropagationPostgres(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping postgres tests in -short mode")
	}
	ctx := context.Background()
	container, err := postgres.Run(ctx,
		"postgres:18.1-bookworm",
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("testuser"),
		postgres.WithPassword("testpass"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	require.NoError(t, err)
	connString, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	pool, err := pgxpool.New(ctx, connString)
	require.NoError(t, err)
	cleanup := func() {
		pool.Close()
		container.Terminate(ctx)
	}
	return pool, cleanup
}

type bundleStorageFactory func(t *testing.T, tempDir, addr string) (BundleStorage, error)

func testSmallPropagation(t *testing.T, storageFactory bundleStorageFactory) {
	numParticipants := 5
	numTrees := 1
	fanout := 2

	participants := make([]string, numParticipants)
	privKeys := make(map[string][]byte)
	pubKeys := make(map[string]string)

	for i := 0; i < numParticipants; i++ {
		addr := fmt.Sprintf("participant%d", i)
		participants[i] = addr

		privKey := ed25519.GenPrivKey()
		privKeys[addr] = privKey.Bytes()
		pubKeys[addr] = base64.StdEncoding.EncodeToString(privKey.PubKey().Bytes())
	}

	blockHash := sha256.Sum256([]byte("test-block"))
	pocHeight := int64(1000)

	trees := BuildTrees(participants, blockHash[:], numTrees, fanout)

	transport := NewMockTransport()
	pubKeyProvider := NewMockPubKeyProvider()
	for addr, pubKey := range pubKeys {
		pubKeyProvider.RegisterKey(addr, pubKey)
	}

	tempDir := t.TempDir()

	caches := make(map[string]*Cache)
	receivers := make(map[string]*Receiver)
	bundlers := make(map[string]*Bundler)
	stores := make(map[string]*artifacts.ArtifactStore)

	signers := make(map[string]HeaderSigner)

	for i, addr := range participants {
		storage, err := storageFactory(t, tempDir, addr)
		require.NoError(t, err)
		cache := NewCache(storage)
		caches[addr] = cache

		perParticipantSender := transport.NewSenderFor(addr)
		receiver := NewReceiver(cache, trees, pubKeyProvider, addr, perParticipantSender)
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

		for j := 0; j < 10; j++ {
			nonce := int32(i*1000 + j)
			vector := []byte(fmt.Sprintf("vector-%d-%d", i, j))
			if err := store.Add(nonce, vector); err != nil {
				t.Fatalf("failed to add artifact: %v", err)
			}
		}

		if err := store.Flush(); err != nil {
			t.Fatalf("failed to flush store for %s: %v", addr, err)
		}

		signers[addr] = &testKeySigner{key: privKeys[addr]}
		bundler := NewBundler(signers[addr], cache, trees, transport, addr)
		bundlers[addr] = bundler
	}

	sender := trees[0].Shuffled[0]

	senderCount := stores[sender].Count()
	senderRoot := stores[sender].GetRoot()
	if err := bundlers[sender].Publish(pocHeight, sender, pubKeys[sender], senderCount, senderRoot); err != nil {
		t.Fatalf("failed to publish: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	bundleID := MakeBundleID(sender, pocHeight, stores[sender].GetRoot(), stores[sender].Count())

	receivedCount := 0
	for _, addr := range participants {
		if addr == sender {
			continue
		}

		header, err := caches[addr].GetHeader(bundleID)
		if err != nil {
			continue
		}

		receivedCount++

		if header.Participant != sender {
			t.Errorf("participant %s: wrong sender in header: got %s, want %s",
				addr, header.Participant, sender)
		}
	}

	if receivedCount != numParticipants-1 {
		t.Errorf("Not all participants received the bundle: got %d, want %d", receivedCount, numParticipants-1)
	}

	for _, store := range stores {
		store.Close()
	}
}

func TestSmallPropagation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping postgres tests in -short mode")
	}

	pool, cleanup := setupPropagationPostgres(t)
	defer cleanup()

	testSmallPropagation(t, func(t *testing.T, tempDir, addr string) (BundleStorage, error) {
		return NewPostgresBundleStorage(context.Background(), pool, addr)
	})
}

func TestSmallPropagationWithFileStorage(t *testing.T) {
	testSmallPropagation(t, func(t *testing.T, tempDir, addr string) (BundleStorage, error) {
		storageDir := filepath.Join(tempDir, addr, "bundles")
		return NewFileBundleStorage(storageDir)
	})
}

func testAllBundlesForHeightAfterPropagation(t *testing.T, storageFactory bundleStorageFactory) {
	numParticipants := 5
	fanout := 2

	participants := make([]string, numParticipants)
	privKeys := make(map[string][]byte)
	pubKeys := make(map[string]string)

	for i := 0; i < numParticipants; i++ {
		addr := fmt.Sprintf("participant%d", i)
		participants[i] = addr

		privKey := ed25519.GenPrivKey()
		privKeys[addr] = privKey.Bytes()
		pubKeys[addr] = base64.StdEncoding.EncodeToString(privKey.PubKey().Bytes())
	}

	pocHeight := int64(2000)

	trees := make([]*Tree, numParticipants)
	for i := 0; i < numParticipants; i++ {
		rotated := make([]string, numParticipants)
		for j := 0; j < numParticipants; j++ {
			rotated[j] = participants[(i+j)%numParticipants]
		}
		trees[i] = buildTree(i, rotated, fanout)
	}

	transport := NewMockTransport()
	pubKeyProvider := NewMockPubKeyProvider()
	for addr, pubKey := range pubKeys {
		pubKeyProvider.RegisterKey(addr, pubKey)
	}

	tempDir := t.TempDir()

	caches := make(map[string]*Cache)
	bundlers := make(map[string]*Bundler)
	stores := make(map[string]*artifacts.ArtifactStore)

	for i, addr := range participants {
		storage, err := storageFactory(t, tempDir, addr)
		require.NoError(t, err)
		cache := NewCache(storage)
		caches[addr] = cache

		receiver := NewReceiver(cache, trees, pubKeyProvider, addr, transport)
		transport.RegisterReceiver(addr, receiver)

		storeDir := filepath.Join(tempDir, addr, "store")
		require.NoError(t, os.MkdirAll(storeDir, 0755))
		store, err := artifacts.Open(storeDir)
		require.NoError(t, err)
		stores[addr] = store

		for j := 0; j < 10; j++ {
			nonce := int32(i*1000 + j)
			vector := []byte(fmt.Sprintf("vector-%d-%d", i, j))
			require.NoError(t, store.Add(nonce, vector))
		}
		require.NoError(t, store.Flush())

		bundler := NewBundler(&testKeySigner{key: privKeys[addr]}, cache, trees, transport, addr)
		bundlers[addr] = bundler
	}

	type publishedMeta struct {
		participant string
		count       uint32
		rootHash    []byte
	}
	published := make(map[string]publishedMeta, numParticipants)

	for _, addr := range participants {
		count := stores[addr].Count()
		root := stores[addr].GetRoot()
		require.NoError(t, bundlers[addr].Publish(pocHeight, addr, pubKeys[addr], count, root))
		published[addr] = publishedMeta{
			participant: addr,
			count:       count,
			rootHash:    root,
		}
	}

	for _, addr := range participants {
		bundles := caches[addr].AllBundlesForHeight(pocHeight)

		ownFound := false
		for _, h := range bundles {
			if h.Participant == addr {
				ownFound = true
			}
		}
		require.True(t, ownFound,
			"participant %s: own header missing from AllBundlesForHeight", addr)

		seen := make(map[string]bool)
		for _, h := range bundles {
			require.False(t, seen[h.Participant],
				"participant %s: duplicate header for %s", addr, h.Participant)
			seen[h.Participant] = true

			require.Equal(t, pocHeight, h.PocHeight)

			meta, ok := published[h.Participant]
			require.True(t, ok,
				"participant %s: header from unknown participant %s", addr, h.Participant)
			require.Equal(t, meta.count, h.Count,
				"participant %s: wrong count for %s", addr, h.Participant)
			require.Equal(t, meta.rootHash, h.RootHash,
				"participant %s: wrong root hash for %s", addr, h.Participant)

			expectedID := MakeBundleID(h.Participant, pocHeight, meta.rootHash, meta.count)
			require.Equal(t, expectedID, h.BundleID,
				"participant %s: wrong bundle ID for %s", addr, h.Participant)
		}
	}

	for _, addr := range participants {
		bundles := caches[addr].AllBundlesForHeight(pocHeight)
		require.Len(t, bundles, numParticipants,
			"participant %s: expected %d bundles, got %d", addr, numParticipants, len(bundles))
	}

	for _, store := range stores {
		store.Close()
	}
}

func TestAllBundlesForHeightAfterPropagation(t *testing.T) {
	testAllBundlesForHeightAfterPropagation(t, func(t *testing.T, tempDir, addr string) (BundleStorage, error) {
		storageDir := filepath.Join(tempDir, addr, "bundles")
		return NewFileBundleStorage(storageDir)
	})
}

type testKeySigner struct {
	key []byte
}

func (s *testKeySigner) Sign(msg []byte) ([]byte, error) {
	if len(s.key) != 64 {
		return nil, fmt.Errorf("invalid ed25519 private key length: %d", len(s.key))
	}
	privKey := ed25519.PrivKey(s.key)
	return privKey.Sign(msg)
}

func TestFirstArrivalTimeRecordedOnce(t *testing.T) {
	tempDir := t.TempDir()
	storageDir := filepath.Join(tempDir, "bundles")
	storage, err := NewFileBundleStorage(storageDir)
	require.NoError(t, err)

	ctx := context.Background()
	participant := "participant1"
	pocHeight := int64(1000)

	firstTime := int64(1000000)
	firstCount := uint32(100)
	err = storage.StoreFirstArrival(ctx, participant, pocHeight, firstTime, firstCount)
	require.NoError(t, err)

	retrieved, err := storage.GetFirstArrival(ctx, participant, pocHeight)
	require.NoError(t, err)
	require.Equal(t, firstTime, retrieved.Time)
	require.Equal(t, firstCount, retrieved.Count)

	secondTime := int64(2000000)
	secondCount := uint32(200)
	err = storage.StoreFirstArrival(ctx, participant, pocHeight, secondTime, secondCount)
	require.NoError(t, err)

	retrieved, err = storage.GetFirstArrival(ctx, participant, pocHeight)
	require.NoError(t, err)
	require.Equal(t, firstTime, retrieved.Time, "first arrival time should not change")
	require.Equal(t, firstCount, retrieved.Count, "first arrival count should not change")
}

func TestFirstArrivalTimePersisted(t *testing.T) {
	tempDir := t.TempDir()
	storageDir := filepath.Join(tempDir, "bundles")

	storage, err := NewFileBundleStorage(storageDir)
	require.NoError(t, err)

	ctx := context.Background()
	participant := "participant1"
	pocHeight := int64(1000)
	arrivalTime := int64(1234567890)
	arrivalCount := uint32(50)

	err = storage.StoreFirstArrival(ctx, participant, pocHeight, arrivalTime, arrivalCount)
	require.NoError(t, err)

	storage2, err := NewFileBundleStorage(storageDir)
	require.NoError(t, err)

	retrieved, err := storage2.GetFirstArrival(ctx, participant, pocHeight)
	require.NoError(t, err)
	require.Equal(t, arrivalTime, retrieved.Time, "arrival time should persist across storage reload")
	require.Equal(t, arrivalCount, retrieved.Count, "arrival count should persist across storage reload")
}

func TestFirstArrivalTimeMultipleParticipants(t *testing.T) {
	tempDir := t.TempDir()
	storageDir := filepath.Join(tempDir, "bundles")
	storage, err := NewFileBundleStorage(storageDir)
	require.NoError(t, err)

	ctx := context.Background()
	pocHeight := int64(1000)

	arrivals := map[string]ArrivalInfo{
		"participant1": {Time: 1000000, Count: 10},
		"participant2": {Time: 2000000, Count: 20},
		"participant3": {Time: 3000000, Count: 30},
	}

	for participant, info := range arrivals {
		err = storage.StoreFirstArrival(ctx, participant, pocHeight, info.Time, info.Count)
		require.NoError(t, err)
	}

	for participant, expected := range arrivals {
		retrieved, err := storage.GetFirstArrival(ctx, participant, pocHeight)
		require.NoError(t, err)
		require.Equal(t, expected.Time, retrieved.Time)
		require.Equal(t, expected.Count, retrieved.Count)
	}

	diffHeight := int64(2000)
	err = storage.StoreFirstArrival(ctx, "participant1", diffHeight, 9999999, 999)
	require.NoError(t, err)

	retrieved, err := storage.GetFirstArrival(ctx, "participant1", pocHeight)
	require.NoError(t, err)
	require.Equal(t, arrivals["participant1"].Time, retrieved.Time, "different pocHeight should not affect original")
}

func TestGetAllFirstArrivals(t *testing.T) {
	tempDir := t.TempDir()
	storageDir := filepath.Join(tempDir, "bundles")
	storage, err := NewFileBundleStorage(storageDir)
	require.NoError(t, err)

	ctx := context.Background()
	pocHeight1 := int64(1000)
	pocHeight2 := int64(2000)

	err = storage.StoreFirstArrival(ctx, "participant1", pocHeight1, 1000000, 10)
	require.NoError(t, err)
	err = storage.StoreFirstArrival(ctx, "participant2", pocHeight1, 2000000, 20)
	require.NoError(t, err)
	err = storage.StoreFirstArrival(ctx, "participant3", pocHeight1, 3000000, 30)
	require.NoError(t, err)

	err = storage.StoreFirstArrival(ctx, "participant1", pocHeight2, 5000000, 50)
	require.NoError(t, err)

	arrivals, err := storage.GetAllFirstArrivals(ctx, pocHeight1)
	require.NoError(t, err)
	require.Len(t, arrivals, 3)
	require.Equal(t, int64(1000000), arrivals["participant1"].Time)
	require.Equal(t, uint32(10), arrivals["participant1"].Count)
	require.Equal(t, int64(2000000), arrivals["participant2"].Time)
	require.Equal(t, uint32(20), arrivals["participant2"].Count)
	require.Equal(t, int64(3000000), arrivals["participant3"].Time)
	require.Equal(t, uint32(30), arrivals["participant3"].Count)

	arrivals2, err := storage.GetAllFirstArrivals(ctx, pocHeight2)
	require.NoError(t, err)
	require.Len(t, arrivals2, 1)
	require.Equal(t, int64(5000000), arrivals2["participant1"].Time)
	require.Equal(t, uint32(50), arrivals2["participant1"].Count)

	arrivals3, err := storage.GetAllFirstArrivals(ctx, int64(9999))
	require.NoError(t, err)
	require.Len(t, arrivals3, 0)
}

func TestFirstArrivalTimeNotFound(t *testing.T) {
	tempDir := t.TempDir()
	storageDir := filepath.Join(tempDir, "bundles")
	storage, err := NewFileBundleStorage(storageDir)
	require.NoError(t, err)

	ctx := context.Background()
	_, err = storage.GetFirstArrival(ctx, "nonexistent", 1000)
	require.ErrorIs(t, err, ErrArrivalNotFound)
}

func TestFirstArrivalTimeRecordedOnHeader(t *testing.T) {
	numParticipants := 3
	fanout := 2

	participants := make([]string, numParticipants)
	privKeys := make(map[string][]byte)
	pubKeys := make(map[string]string)

	for i := 0; i < numParticipants; i++ {
		addr := fmt.Sprintf("participant%d", i)
		participants[i] = addr

		privKey := ed25519.GenPrivKey()
		privKeys[addr] = privKey.Bytes()
		pubKeys[addr] = base64.StdEncoding.EncodeToString(privKey.PubKey().Bytes())
	}

	blockHash := sha256.Sum256([]byte("test-block"))
	pocHeight := int64(1000)

	trees := BuildTrees(participants, blockHash[:], 1, fanout)

	transport := NewMockTransport()
	pubKeyProvider := NewMockPubKeyProvider()
	for addr, pubKey := range pubKeys {
		pubKeyProvider.RegisterKey(addr, pubKey)
	}

	tempDir := t.TempDir()

	caches := make(map[string]*Cache)
	bundlers := make(map[string]*Bundler)
	stores := make(map[string]*artifacts.ArtifactStore)

	for i, addr := range participants {
		storageDir := filepath.Join(tempDir, addr, "bundles")
		storage, err := NewFileBundleStorage(storageDir)
		require.NoError(t, err)
		cache := NewCache(storage)
		caches[addr] = cache

		perParticipantSender := transport.NewSenderFor(addr)
		receiver := NewReceiver(cache, trees, pubKeyProvider, addr, perParticipantSender)
		transport.RegisterReceiver(addr, receiver)

		storeDir := filepath.Join(tempDir, addr, "store")
		require.NoError(t, os.MkdirAll(storeDir, 0755))
		store, err := artifacts.Open(storeDir)
		require.NoError(t, err)
		stores[addr] = store

		for j := 0; j < 10; j++ {
			nonce := int32(i*1000 + j)
			vector := []byte(fmt.Sprintf("vector-%d-%d", i, j))
			require.NoError(t, store.Add(nonce, vector))
		}
		require.NoError(t, store.Flush())

		bundler := NewBundler(&testKeySigner{key: privKeys[addr]}, cache, trees, transport, addr)
		bundlers[addr] = bundler
	}

	sender := participants[0]
	senderCount := stores[sender].Count()
	senderRoot := stores[sender].GetRoot()
	err := bundlers[sender].Publish(pocHeight, sender, pubKeys[sender], senderCount, senderRoot)
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	for _, addr := range participants {
		if addr == sender {
			continue
		}

		arrival, err := caches[addr].GetFirstArrival(sender, pocHeight)
		require.NoError(t, err, "participant %s should have first arrival for sender", addr)
		require.Greater(t, arrival.Time, int64(0), "arrival time should be positive")
		require.Equal(t, senderCount, arrival.Count, "arrival count should match sender count")
	}

	for _, store := range stores {
		store.Close()
	}
}

func TestReceiverClearProcessedState(t *testing.T) {
	tempDir := t.TempDir()
	storageDir := filepath.Join(tempDir, "bundles")
	storage, err := NewFileBundleStorage(storageDir)
	require.NoError(t, err)
	cache := NewCache(storage)

	transport := NewMockTransport()
	pubKeyProvider := NewMockPubKeyProvider()

	receiver := NewReceiver(cache, nil, pubKeyProvider, "test", transport.NewSenderFor("test"))

	receiver.mu.Lock()
	receiver.processedHeaders[[32]byte{1}] = true
	receiver.processedProofs[[32]byte{2}] = true
	receiver.mu.Unlock()

	receiver.ClearProcessedState()

	receiver.mu.RLock()
	require.Empty(t, receiver.processedHeaders)
	require.Empty(t, receiver.processedProofs)
	receiver.mu.RUnlock()
}
