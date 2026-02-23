package propagation

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"testing"
	"time"

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

func TestFileBundleStorageDeleteBeforeHeight(t *testing.T) {
	tempDir := t.TempDir()
	storageDir := filepath.Join(tempDir, "bundles")
	storage, err := NewFileBundleStorage(storageDir)
	require.NoError(t, err)

	ctx := context.Background()

	heights := []int64{100, 200, 300, 400, 500}
	for i, height := range heights {
		var bundleID [4]byte
		bundleID[0] = byte(i)
		rootHash := sha256.Sum256([]byte(fmt.Sprintf("root-%d", i)))
		header := BundleHeader{
			BundleID:    bundleID,
			Participant: fmt.Sprintf("participant%d", i),
			PocHeight:   height,
			RootHash:    rootHash,
			Count:       10,
			CreatedAt:   time.Now().UnixMilli(),
		}
		require.NoError(t, storage.StoreHeader(ctx, header))
	}

	deleted, err := storage.DeleteBeforeHeight(ctx, 300)
	require.NoError(t, err)
	require.Greater(t, deleted, 0)

	for i := 0; i < 3; i++ {
		var bundleID [4]byte
		bundleID[0] = byte(i)
		_, err := storage.GetHeader(ctx, bundleID)
		require.ErrorIs(t, err, ErrBundleNotFound)
	}

	for i := 3; i < 5; i++ {
		var bundleID [4]byte
		bundleID[0] = byte(i)
		_, err := storage.GetHeader(ctx, bundleID)
		require.NoError(t, err)
	}
}

func TestFileBundleStorageDeleteBeforeHeightEmpty(t *testing.T) {
	tempDir := t.TempDir()
	storageDir := filepath.Join(tempDir, "bundles")
	storage, err := NewFileBundleStorage(storageDir)
	require.NoError(t, err)

	ctx := context.Background()

	deleted, err := storage.DeleteBeforeHeight(ctx, 1000)
	require.NoError(t, err)
	require.Equal(t, 0, deleted)
}

func TestPostgresBundleStorageDeleteBeforeHeight(t *testing.T) {
	pool, cleanup := setupPropagationPostgres(t)
	defer cleanup()

	ctx := context.Background()
	storage, err := NewPostgresBundleStorage(ctx, pool, "test-cleanup")
	require.NoError(t, err)

	heights := []int64{100, 200, 300, 400, 500}
	for i, height := range heights {
		var bundleID [4]byte
		bundleID[0] = byte(i)
		rootHash := sha256.Sum256([]byte(fmt.Sprintf("root-%d", i)))
		header := BundleHeader{
			BundleID:    bundleID,
			Participant: fmt.Sprintf("participant%d", i),
			PocHeight:   height,
			RootHash:    rootHash,
			Count:       10,
			CreatedAt:   time.Now().UnixMilli(),
		}
		require.NoError(t, storage.StoreHeader(ctx, header))
	}

	deleted, err := storage.DeleteBeforeHeight(ctx, 300)
	require.NoError(t, err)
	require.Greater(t, deleted, 0)

	for i := 0; i < 3; i++ {
		var bundleID [4]byte
		bundleID[0] = byte(i)
		_, err := storage.GetHeader(ctx, bundleID)
		require.ErrorIs(t, err, ErrBundleNotFound)
	}

	for i := 3; i < 5; i++ {
		var bundleID [4]byte
		bundleID[0] = byte(i)
		_, err := storage.GetHeader(ctx, bundleID)
		require.NoError(t, err)
	}

	var headerCount int
	err = pool.QueryRow(ctx, "SELECT COUNT(*) FROM poc_bundle_headers WHERE instance = $1", "test-cleanup").Scan(&headerCount)
	require.NoError(t, err)
	require.Equal(t, 2, headerCount)
}
