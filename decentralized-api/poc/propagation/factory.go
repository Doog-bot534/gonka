package propagation

import (
	"context"
	"fmt"
	"os"

	"decentralized-api/logging"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/productscience/inference/x/inference/types"
)

func NewBundleStorage(ctx context.Context, storageDir, participantAddress string) (BundleStorage, *pgxpool.Pool) {
	if storageDir == "" {
		storageDir = "./data/propagation/bundles"
	}

	propagationConnString := os.Getenv("PROPAGATION_DATABASE_URL")
	if propagationConnString == "" {
		logging.Info("No PostgreSQL connection string, using file-based storage", types.PoC, "storageDir", storageDir)
		fileStorage, err := NewFileBundleStorage(storageDir)
		if err != nil {
			panic(fmt.Sprintf("file bundle storage init failed: %v", err))
		}
		return fileStorage, nil
	}

	logging.Info("PostgreSQL connection string found, using hybrid storage", types.PoC)

	propagationPool, err := pgxpool.New(ctx, propagationConnString)
	if err != nil {
		logging.Warn("Failed to create propagation pool, falling back to file storage", types.PoC, "error", err)
		fileStorage, fileErr := NewFileBundleStorage(storageDir)
		if fileErr != nil {
			panic(fmt.Sprintf("file bundle storage init failed: %v", fileErr))
		}
		return fileStorage, nil
	}

	if err := propagationPool.Ping(ctx); err != nil {
		logging.Warn("Failed to ping propagation pool, falling back to file storage", types.PoC, "error", err)
		propagationPool.Close()
		fileStorage, fileErr := NewFileBundleStorage(storageDir)
		if fileErr != nil {
			panic(fmt.Sprintf("file bundle storage init failed: %v", fileErr))
		}
		return fileStorage, nil
	}

	pgStorage, pgErr := NewPostgresBundleStorage(ctx, propagationPool, participantAddress)
	if pgErr != nil {
		logging.Warn("Failed to create PostgreSQL bundle storage, falling back to file storage", types.PoC, "error", pgErr)
		propagationPool.Close()
		fileStorage, fileErr := NewFileBundleStorage(storageDir)
		if fileErr != nil {
			panic(fmt.Sprintf("file bundle storage init failed: %v", fileErr))
		}
		return fileStorage, nil
	}

	fileStorage, fileErr := NewFileBundleStorage(storageDir)
	if fileErr != nil {
		logging.Warn("Failed to create file bundle storage, using PostgreSQL only", types.PoC, "error", fileErr)
		return pgStorage, propagationPool
	}

	bundleStorage := NewHybridBundleStorage(pgStorage, fileStorage)
	logging.Info("Using hybrid bundle storage (PostgreSQL + file fallback)", types.PoC)
	return bundleStorage, propagationPool
}
