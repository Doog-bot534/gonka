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

func (h *HybridBundleStorage) StoreHeaderBatch(ctx context.Context, headers []BundleHeader) error {
	if pg := h.currentPg(); pg != nil {
		err := pg.StoreHeaderBatch(ctx, headers)
		if err == nil {
			return nil
		}
		logging.Warn("PostgreSQL store header batch failed, falling back to file", types.PoC, "error", err)
	}
	return h.file.StoreHeaderBatch(ctx, headers)
}

func (h *HybridBundleStorage) GetHeader(ctx context.Context, bundleID [4]byte) (BundleHeader, error) {
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

func (h *HybridBundleStorage) StoreFirstArrivalBatch(ctx context.Context, arrivals []ArrivalInfo, participants []string, pocHeights []int64) error {
	if pg := h.currentPg(); pg != nil {
		err := pg.StoreFirstArrivalBatch(ctx, arrivals, participants, pocHeights)
		if err == nil {
			return nil
		}
		logging.Warn("PostgreSQL store first arrival batch failed, falling back to file", types.PoC, "error", err)
	}
	return h.file.StoreFirstArrivalBatch(ctx, arrivals, participants, pocHeights)
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

func (h *HybridBundleStorage) CleanupOldHeights(ctx context.Context, retainCount int) error {
	var pgErr, fileErr error

	if pg := h.currentPg(); pg != nil {
		pgErr = pg.CleanupOldHeights(ctx, retainCount)
		if pgErr != nil {
			logging.Warn("PostgreSQL cleanup failed", types.PoC, "error", pgErr)
		}
	}

	fileErr = h.file.CleanupOldHeights(ctx, retainCount)
	if fileErr != nil {
		logging.Warn("File cleanup failed", types.PoC, "error", fileErr)
	}

	if pgErr != nil {
		return pgErr
	}
	return fileErr
}

func (h *HybridBundleStorage) DeleteBeforeHeight(ctx context.Context, maxPocHeight int64) (int, error) {
	totalDeleted := 0

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
