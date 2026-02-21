package statsstorage

import (
	"context"
	"os"

	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

// NewStatsStorage creates a stats storage backend.
// Uses PostgreSQL when configured and reachable, otherwise falls back to file storage.
func NewStatsStorage(ctx context.Context) (StatsStorage, error) {
	fileBasePath := os.Getenv("DAPI_STATS_STORAGE_PATH")
	if fileBasePath == "" {
		fileBasePath = "/root/.dapi/data/stats"
	}

	fileStorage := NewFileStorage(fileBasePath)

	pgHost := os.Getenv("PGHOST")
	if pgHost == "" {
		logging.Info("PGHOST not set for stats storage, using file storage only", types.System, "path", fileBasePath)
		return fileStorage, nil
	}

	pgStorage, err := NewPostgresStorage(ctx)
	if err != nil {
		logging.Warn("PostgreSQL stats storage init failed, using file storage fallback", types.System, "host", pgHost, "error", err)
		return fileStorage, nil
	}
	logging.Info("Using PostgreSQL stats storage", types.System, "host", pgHost)
	return pgStorage, nil
}
