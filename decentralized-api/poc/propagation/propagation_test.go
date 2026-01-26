package propagation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"decentralized-api/poc/artifacts"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
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

func TestSmallPropagation(t *testing.T) {
	numParticipants := 5
	numTrees := 1
	fanout := 2

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

	t.Logf("Tree topology:")
	for _, tree := range trees {
		t.Logf("Tree %d order: %v", tree.Index, tree.Shuffled)
		for _, addr := range participants {
			parent, children := tree.Role(addr)
			t.Logf("  %s: parent=%s, children=%v", addr, parent, children)
		}
	}

	transport := NewMockTransport()
	pubKeyProvider := NewMockPubKeyProvider()
	for addr, pubKey := range pubKeys {
		pubKeyProvider.RegisterKey(addr, pubKey)
	}

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

		receiver := NewReceiver(cache, trees, pubKeyProvider, addr, transport)
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

		bundler := NewBundler(store, trees, transport, addr)
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

		receivedCount++
		t.Logf("Participant %s successfully received bundle", addr)

		if header.Participant != sender {
			t.Errorf("participant %s: wrong sender in header: got %s, want %s",
				addr, header.Participant, sender)
		}
	}

	t.Logf("Total participants who received: %d out of %d", receivedCount, numParticipants-1)

	if receivedCount != numParticipants-1 {
		t.Errorf("Not all participants received the bundle: got %d, want %d", receivedCount, numParticipants-1)
	}

	for _, store := range stores {
		store.Close()
	}
}
