package apiconfig_test

import (
	"context"
	"database/sql"
	"decentralized-api/apiconfig"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestMigrationSafeSwap(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "gonka-test.db")

	// 1. Setup pre-migration state (simulate existing DB)
	setupPreMigrationDB(t, dbPath)

	// 2. Connect using apiconfig helpers which should trigger migration via EnsureSchema
	cfg := apiconfig.SqliteConfig{Path: dbPath}
	dbWrap := apiconfig.NewSQLiteDb(cfg)
	ctx := context.Background()

	// This calls EnsureSchema internally
	err := dbWrap.BootstrapLocal(ctx)
	require.NoError(t, err)

	db := dbWrap.GetDb()
	require.NotNil(t, db)

	// 3. Verify Schema: Index should exist
	var indexCount int
	err = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name='idx_seed_info_epoch_index'`).Scan(&indexCount)
	require.NoError(t, err)
	require.Equal(t, 1, indexCount, "unique index should be created")

	// 4. Verify Data: Duplicates should be resolved, keeping the latest one
	// We inserted 2 seeds for epoch 100. The later one (id=2) should remain.
	var count int
	err = db.QueryRow(`SELECT count(*) FROM seed_info WHERE epoch_index = 100`).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 1, count, "duplicates for epoch 100 should be resolved to 1")

	var seedVal int64
	err = db.QueryRow(`SELECT seed FROM seed_info WHERE epoch_index = 100`).Scan(&seedVal)
	require.NoError(t, err)
	require.Equal(t, int64(999), seedVal, "should keep the latest seed value (id 2)")

	// Epoch 101 was unique, should be preserved
	err = db.QueryRow(`SELECT count(*) FROM seed_info WHERE epoch_index = 101`).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 1, count)

	// 5. Verify Empty Seeds were filtered out
	// Epoch 200 had an empty seed (seed=0, signature="")
	err = db.QueryRow(`SELECT count(*) FROM seed_info WHERE epoch_index = 200`).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 0, count, "empty seed for epoch 200 should not be migrated")

	// 6. Verify Backup Table exists
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name LIKE 'seed_info_backup_%'`)
	require.NoError(t, err)
	defer rows.Close()
	var backupTable string
	if rows.Next() {
		err = rows.Scan(&backupTable)
		require.NoError(t, err)
	}
	require.NotEmpty(t, backupTable, "backup table should exist")
	t.Logf("Found backup table: %s", backupTable)
}

func setupPreMigrationDB(t *testing.T, dbPath string) {
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer db.Close()

	// Old schema without unique index
	_, err = db.Exec(`
	CREATE TABLE seed_info (
	  id INTEGER PRIMARY KEY AUTOINCREMENT,
	  type TEXT NOT NULL,
	  seed INTEGER NOT NULL,
	  epoch_index INTEGER NOT NULL,
	  signature TEXT NOT NULL,
	  claimed BOOLEAN NOT NULL DEFAULT 0,
	  is_active BOOLEAN NOT NULL DEFAULT 1,
	  created_at DATETIME NOT NULL DEFAULT (STRFTIME('%Y-%m-%d %H:%M:%f','now'))
	);
	`)
	require.NoError(t, err)

	// Insert duplicate data for epoch 100
	// 1st entry (older)
	_, err = db.Exec(`INSERT INTO seed_info (type, seed, epoch_index, signature, claimed, is_active) VALUES ('current', 111, 100, 'sig1', 0, 0)`)
	require.NoError(t, err)

	// 2nd entry (newer, higher ID) - this one should survive
	// Sleep briefly to ensure implicit ordering if reliable on ID
	time.Sleep(10 * time.Millisecond)
	_, err = db.Exec(`INSERT INTO seed_info (type, seed, epoch_index, signature, claimed, is_active) VALUES ('current', 999, 100, 'sig2', 0, 1)`)
	require.NoError(t, err)

	// Unique entry for epoch 101
	_, err = db.Exec(`INSERT INTO seed_info (type, seed, epoch_index, signature, claimed, is_active) VALUES ('upcoming', 222, 101, 'sig3', 0, 1)`)
	require.NoError(t, err)

	// Empty seed for epoch 200 (should be filtered out)
	_, err = db.Exec(`INSERT INTO seed_info (type, seed, epoch_index, signature, claimed, is_active) VALUES ('upcoming', 0, 200, '', 0, 1)`)
	require.NoError(t, err)
}
