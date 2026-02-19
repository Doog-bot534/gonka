package propagation

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

type propagationStorageFactory func(t *testing.T, tempDir, addr string) (BundleStorage, error)

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

type MockPubKeyProvider struct {
	mu   sync.RWMutex
	keys map[string]string
}

func NewMockPubKeyProvider() *MockPubKeyProvider {
	return &MockPubKeyProvider{
		keys: make(map[string]string),
	}
}

func (m *MockPubKeyProvider) RegisterKey(addr, pubKey string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keys[addr] = pubKey
}

func (m *MockPubKeyProvider) GetPubKey(participantAddr string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	pubKey, ok := m.keys[participantAddr]
	if !ok {
		return "", fmt.Errorf("public key not found for %s", participantAddr)
	}
	return pubKey, nil
}

func setupPropagationPostgres(t *testing.T) (*pgxpool.Pool, func()) {
	ctx := context.Background()

	pgContainer, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("postgres"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2),
		),
	)
	require.NoError(t, err)

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	pool, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)

	cleanup := func() {
		pool.Close()
		if err := pgContainer.Terminate(ctx); err != nil {
			t.Logf("failed to terminate postgres container: %v", err)
		}
	}

	return pool, cleanup
}

func formatAddress(i int) string {
	return fmt.Sprintf("participant%d", i)
}
