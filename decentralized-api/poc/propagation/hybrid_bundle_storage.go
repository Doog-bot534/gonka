package propagation

import (
	"context"
	"sync"
	"time"

	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

const (
	pgConnectTimeout = 2 * time.Second
	pgRetryInterval  = 240 * time.Second
)

type HybridBundleStorage struct {
	pg            *PostgresBundleStorage
	file          *FileBundleStorage
	mu            sync.Mutex
	lastRetry     time.Time
	retryInterval time.Duration
}

func NewHybridBundleStorage(pg *PostgresBundleStorage, file *FileBundleStorage) *HybridBundleStorage {
	return &HybridBundleStorage{
		pg:            pg,
		file:          file,
		retryInterval: pgRetryInterval,
	}
}

func (h *HybridBundleStorage) shouldAttemptConnect() (bool, *PostgresBundleStorage) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.pg != nil {
		return false, h.pg
	}
	if time.Since(h.lastRetry) < h.retryInterval {
		return false, nil
	}
	h.lastRetry = time.Now()
	return true, nil
}

func (h *HybridBundleStorage) saveConnection(pg *PostgresBundleStorage) {
	h.mu.Lock()
	defer h.mu.Unlock()
	logging.Info("PostgreSQL bundle storage connection established", types.PoC)
	h.pg = pg
}

func (h *HybridBundleStorage) currentPg() *PostgresBundleStorage {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pg
}

func (h *HybridBundleStorage) StoreHeader(ctx context.Context, header BundleHeader) error {
	if pg := h.currentPg(); pg != nil {
		err := pg.StoreHeader(ctx, header)
		if err == nil {
			return nil
		}
		logging.Warn("PostgreSQL store header failed, falling back to file", types.PoC,
			"bundleID", header.BundleID, "error", err)
	}
	return h.file.StoreHeader(ctx, header)
}

func (h *HybridBundleStorage) GetHeader(ctx context.Context, bundleID [32]byte) (BundleHeader, error) {
	if pg := h.currentPg(); pg != nil {
		header, err := pg.GetHeader(ctx, bundleID)
		if err == nil {
			return header, nil
		}
		if err != ErrBundleNotFound {
			logging.Debug("PostgreSQL get header failed, checking file", types.PoC,
				"bundleID", bundleID, "error", err)
		}
	}

	return h.file.GetHeader(ctx, bundleID)
}

func (h *HybridBundleStorage) LatestBundle(ctx context.Context, participant string, pocHeight int64) (BundleHeader, error) {
	if pg := h.currentPg(); pg != nil {
		header, err := pg.LatestBundle(ctx, participant, pocHeight)
		if err == nil {
			return header, nil
		}
		if err != ErrBundleNotFound {
			logging.Debug("PostgreSQL latest bundle failed, checking file", types.PoC,
				"participant", participant, "pocHeight", pocHeight, "error", err)
		}
	}

	return h.file.LatestBundle(ctx, participant, pocHeight)
}

func (h *HybridBundleStorage) AllBundlesForHeight(ctx context.Context, pocHeight int64) ([]BundleHeader, error) {
	if pg := h.currentPg(); pg != nil {
		bundles, err := pg.AllBundlesForHeight(ctx, pocHeight)
		if err == nil {
			return bundles, nil
		}
		logging.Debug("PostgreSQL all bundles failed, checking file", types.PoC,
			"pocHeight", pocHeight, "error", err)
	}

	return h.file.AllBundlesForHeight(ctx, pocHeight)
}

func (h *HybridBundleStorage) StoreProofs(ctx context.Context, bundleID [32]byte, proofs []ProofItem) error {
	if pg := h.currentPg(); pg != nil {
		err := pg.StoreProofs(ctx, bundleID, proofs)
		if err == nil {
			return nil
		}
		logging.Warn("PostgreSQL store proofs failed, falling back to file", types.PoC,
			"bundleID", bundleID, "error", err)
	}
	return h.file.StoreProofs(ctx, bundleID, proofs)
}

func (h *HybridBundleStorage) GetProofs(ctx context.Context, bundleID [32]byte) ([][]ProofItem, error) {
	if pg := h.currentPg(); pg != nil {
		proofs, err := pg.GetProofs(ctx, bundleID)
		if err == nil {
			return proofs, nil
		}
		if err != ErrProofsNotFound {
			logging.Debug("PostgreSQL get proofs failed, checking file", types.PoC,
				"bundleID", bundleID, "error", err)
		}
	}

	return h.file.GetProofs(ctx, bundleID)
}

func (h *HybridBundleStorage) StoreFirstArrival(ctx context.Context, participant string, pocHeight int64, arrivalTime int64, count uint32) error {
	if pg := h.currentPg(); pg != nil {
		err := pg.StoreFirstArrival(ctx, participant, pocHeight, arrivalTime, count)
		if err == nil {
			return nil
		}
		logging.Warn("PostgreSQL store first arrival failed, falling back to file", types.PoC,
			"participant", participant, "pocHeight", pocHeight, "error", err)
	}
	return h.file.StoreFirstArrival(ctx, participant, pocHeight, arrivalTime, count)
}

func (h *HybridBundleStorage) GetFirstArrival(ctx context.Context, participant string, pocHeight int64) (ArrivalInfo, error) {
	if pg := h.currentPg(); pg != nil {
		info, err := pg.GetFirstArrival(ctx, participant, pocHeight)
		if err == nil {
			return info, nil
		}
		if err != ErrArrivalNotFound {
			logging.Debug("PostgreSQL get first arrival failed, checking file", types.PoC,
				"participant", participant, "pocHeight", pocHeight, "error", err)
		}
	}

	return h.file.GetFirstArrival(ctx, participant, pocHeight)
}

func (h *HybridBundleStorage) GetAllFirstArrivals(ctx context.Context, pocHeight int64) (map[string]ArrivalInfo, error) {
	if pg := h.currentPg(); pg != nil {
		arrivals, err := pg.GetAllFirstArrivals(ctx, pocHeight)
		if err == nil {
			return arrivals, nil
		}
		logging.Debug("PostgreSQL get all first arrivals failed, checking file", types.PoC,
			"pocHeight", pocHeight, "error", err)
	}

	return h.file.GetAllFirstArrivals(ctx, pocHeight)
}

func (h *HybridBundleStorage) DeleteBeforeHeight(ctx context.Context, maxPocHeight int64) (int, error) {
	totalDeleted := 0

	// Delete from both storages
	if pg := h.currentPg(); pg != nil {
		pgDeleted, err := pg.DeleteBeforeHeight(ctx, maxPocHeight)
		if err != nil {
			logging.Warn("PostgreSQL delete before height failed", types.PoC,
				"maxPocHeight", maxPocHeight, "error", err)
		} else {
			totalDeleted += pgDeleted
		}
	}

	fileDeleted, err := h.file.DeleteBeforeHeight(ctx, maxPocHeight)
	if err != nil {
		return totalDeleted, err
	}
	totalDeleted += fileDeleted

	return totalDeleted, nil
}

func (h *HybridBundleStorage) Close() error {
	var pgErr, fileErr error

	if pg := h.currentPg(); pg != nil {
		pgErr = pg.Close()
	}

	fileErr = h.file.Close()

	if pgErr != nil {
		return pgErr
	}
	return fileErr
}

var _ BundleStorage = (*HybridBundleStorage)(nil)
