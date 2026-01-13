package pocstorage

import (
	"context"
	"os"

	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

// NewPoCStorage creates a PoCStorage based on environment configuration.
// If PGHOST is set and PostgreSQL is accessible at startup, uses PostgresStorage.
// Otherwise uses FileStorage.
//
// Note: unlike payload storage, we intentionally do NOT do lazy reconnect fallback
// to avoid mixing rolling hash state across backends (file vs postgres) mid-run.
func NewPoCStorage(ctx context.Context, fileBasePath string) PoCStorage {
	fileStorage := NewFileStorage(fileBasePath)

	pgHost := os.Getenv("PGHOST")
	if pgHost == "" {
		logging.Info("PGHOST not set, using file PoC storage only", types.PoC)
		return fileStorage
	}

	pgStorage, err := NewPostgresStorage(ctx)
	if err != nil {
		logging.Warn("PostgreSQL connection failed, using file PoC storage only", types.PoC,
			"host", pgHost, "error", err)
		return fileStorage
	}

	logging.Info("Using PostgreSQL PoC storage", types.PoC, "host", pgHost)
	return pgStorage
}
