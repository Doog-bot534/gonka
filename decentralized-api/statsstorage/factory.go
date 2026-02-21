package statsstorage

import (
	"context"
	"os"
	"strconv"

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
	retentionDays := parseRetentionDays()

	fileStorage := NewFileStorage(fileBasePath)

	pgHost := os.Getenv("PGHOST")
	if pgHost == "" {
		logging.Info("PGHOST not set for stats storage, using file storage only", types.System,
			"path", fileBasePath, "retention_days", retentionDays)
		return NewManagedStorage(fileStorage, retentionDays), nil
	}

	pgStorage, err := NewPostgresStorage(ctx)
	if err != nil {
		logging.Warn("PostgreSQL stats storage init failed, using file storage fallback", types.System, "host", pgHost, "error", err)
		return NewManagedStorage(fileStorage, retentionDays), nil
	}
	logging.Info("Using PostgreSQL stats storage", types.System, "host", pgHost, "retention_days", retentionDays)
	return NewManagedStorage(pgStorage, retentionDays), nil
}

func parseRetentionDays() int {
	raw := os.Getenv("DAPI_STATS_RETENTION_DAYS")
	if raw == "" {
		return defaultRetentionDays
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		logging.Warn("Invalid DAPI_STATS_RETENTION_DAYS, using default", types.System, "value", raw, "default", defaultRetentionDays, "error", err)
		return defaultRetentionDays
	}
	return n
}
