package public

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"decentralized-api/broker"
	"decentralized-api/logging"
	"decentralized-api/mlnodeclient"
	"decentralized-api/pocstorage"
	"decentralized-api/utils"

	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
)

type pocStartV2Request struct {
	BlockHeight int64     `json:"block_height"`
	EpochLength int64     `json:"epoch_length"`
	BlockHash   string    `json:"block_hash"`
	BlockTime   time.Time `json:"block_time"`

	Duration  int64 `json:"duration"`
	Frequency int64 `json:"frequency"`

	BatchSize int `json:"batch_size"`
	Params    struct {
		Model  string `json:"model"`
		SeqLen int    `json:"seq_len"`
		KDim   int    `json:"k_dim"`
	} `json:"params"`
}

type pocStartV2Response struct {
	Started             bool              `json:"started"`
	Run                 pocstorage.PoCRun `json:"run"`
	InterruptedPrevious *int64            `json:"interrupted_previous_block_height,omitempty"`
}

func (s *Server) postPoCStartV2(c echo.Context) error {
	if s.pocStorage == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "poc storage not configured")
	}

	var req pocStartV2Request
	bodyBytes, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to read body")
	}
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	// Temporary auth gate: require X-TA-Signature validated against a single pubkey.
	// This is a testing-only constraint and should be removed for production.
	signature := c.Request().Header.Get(utils.XTASignatureHeader)
	if signature == "" {
		return echo.NewHTTPError(http.StatusUnauthorized, "X-TA-Signature header required")
	}
	canonical, err := utils.CanonicalizeJSON(bodyBytes)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid json")
	}
	payloadHash := pocV2StartPayloadHashFromBody(canonical)
	if err := validatePoCV2StartSignature(signature, payloadHash); err != nil {
		logging.Warn("PoC v2 start signature invalid", types.PoC, "error", err)
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid X-TA-Signature")
	}

	// Basic validation
	if req.BlockHeight <= 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "block_height must be > 0")
	}
	if req.BlockHash == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "block_hash is required")
	}
	if req.BlockTime.IsZero() {
		return echo.NewHTTPError(http.StatusBadRequest, "block_time is required")
	}
	if req.Duration <= 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "duration must be > 0")
	}
	if req.Frequency <= 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "frequency must be > 0")
	}
	if req.BatchSize <= 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "batch_size must be > 0")
	}
	if req.Params.Model == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "params.model is required")
	}
	if req.Params.SeqLen <= 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "params.seq_len must be > 0")
	}
	if req.Params.KDim == 0 {
		req.Params.KDim = 12
	}

	now := time.Now().UTC()

	// Determine if:
	// 1) the legacy/v1 PoC process (broker-controlled) is active; and/or
	// 2) a previous PoC v2 run is still active (storage-controlled).
	//
	// If either is true, this v2 run is considered interrupted immediately (per spec).
	started := true
	var interruptedPrev *int64

	legacyActive, err := s.isLegacyPoCActive(c.Request().Context())
	if err != nil {
		logging.Warn("Failed to check legacy PoC status; continuing with v2-only interruption check", types.PoC, "error", err)
	} else if legacyActive {
		started = false
	}

	prev, err := s.pocStorage.GetLatestRun(c.Request().Context())
	if err == nil {
		prevEnd := prev.BlockTime.Add(time.Duration(prev.DurationSeconds) * time.Second)
		prevActive := prev.InterruptedTime == nil && now.Before(prevEnd)
		if prevActive {
			interruptedPrev = &prev.BlockHeight
			_ = s.pocStorage.MarkInterrupted(c.Request().Context(), prev.BlockHeight, now)
			started = false
		}
	}

	var interruptedAt *time.Time
	if !started {
		interruptedAt = &now
	}

	run := pocstorage.PoCRun{
		BlockHeight:      req.BlockHeight,
		EpochLength:      req.EpochLength,
		BlockHash:        req.BlockHash,
		BlockTime:        req.BlockTime.UTC(),
		DurationSeconds:  req.Duration,
		FrequencySeconds: req.Frequency,
		BatchSize:        req.BatchSize,
		Params:           pocstorage.PoCParamsModel{Model: req.Params.Model, SeqLen: req.Params.SeqLen, KDim: req.Params.KDim},
		InterruptedTime:  interruptedAt,
		CreatedAt:        now,
	}

	if err := s.pocStorage.UpsertRun(c.Request().Context(), run); err != nil {
		logging.Error("Failed to upsert PoC run", types.PoC, "blockHeight", req.BlockHeight, "error", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to store poc run")
	}

	logging.Info("PoC v2 start request stored", types.PoC,
		"blockHeight", run.BlockHeight,
		"started", started,
		"legacyPoCActive", legacyActive,
		"interruptedPrevious", interruptedPrev)

	if started {
		if err := s.triggerPoCV2InitGenerate(c.Request().Context(), run); err != nil {
			logging.Error("Failed to trigger PoC v2 init generation on mlnodes", types.PoC,
				"blockHeight", run.BlockHeight,
				"error", err)
			return echo.NewHTTPError(http.StatusBadGateway, err.Error())
		}
	}

	return c.JSON(http.StatusOK, pocStartV2Response{
		Started:             started,
		Run:                 run,
		InterruptedPrevious: interruptedPrev,
	})
}

func (s *Server) triggerPoCV2InitGenerate(ctx context.Context, run pocstorage.PoCRun) error {
	if s.nodeBroker == nil {
		return fmt.Errorf("node broker not configured")
	}

	nodes, err := s.nodeBroker.GetNodes()
	if err != nil {
		return fmt.Errorf("get nodes: %w", err)
	}
	if len(nodes) == 0 {
		return fmt.Errorf("no nodes configured")
	}

	pubKey := s.nodeBroker.GetParticipantPubKey()
	if pubKey == "" {
		return fmt.Errorf("participant pubkey not available")
	}

	var wg sync.WaitGroup

	var mu sync.Mutex
	var failed []string

	for _, nr := range nodes {
		nr := nr
		if nr.Node.PoCPort == 0 {
			continue
		}

		wg.Add(1)
		go func() {
			defer wg.Done()

			client := s.nodeBroker.NewNodeClient(&nr.Node)

			req := mlnodeclient.PoCInitGenerateRequestV2{
				BlockHash:   run.BlockHash,
				BlockHeight: run.BlockHeight,
				PublicKey:   pubKey,
				NodeID:      nr.Node.NodeNum,
				NodeCount:   len(nodes),
				GroupID:     0,
				NGroups:     1,
				BatchSize:   run.BatchSize,
				Params: mlnodeclient.PoCParamsModelV2{
					Model:  run.Params.Model,
					SeqLen: run.Params.SeqLen,
					KDim:   run.Params.KDim,
				},
				URL: "",
			}

			// Don't let a single node hang the whole request for minutes.
			callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if err := client.InitGenerateV2(callCtx, req); err != nil {
				mu.Lock()
				failed = append(failed, nr.Node.Id)
				mu.Unlock()
				logging.Warn("PoC v2 init generation failed for node", types.PoC,
					"node_id", nr.Node.Id,
					"error", err)
			}
		}()
	}

	wg.Wait()

	if len(failed) > 0 {
		return fmt.Errorf("init generation failed on %d node(s): %v", len(failed), failed)
	}
	return nil
}

func (s *Server) isLegacyPoCActive(ctx context.Context) (bool, error) {
	if s.nodeBroker == nil {
		return false, nil
	}

	nodes, err := s.nodeBroker.GetNodes()
	if err != nil {
		return false, err
	}

	for _, n := range nodes {
		st := n.State

		// This aims to detect the *existing* PoC (v1) flow, which is managed by the broker.
		// We treat both "intended" and "current" states as signals to be conservative.
		if st.CurrentStatus == types.HardwareNodeStatus_POC || st.IntendedStatus == types.HardwareNodeStatus_POC {
			return true, nil
		}
		if st.PocCurrentStatus == broker.PocStatusGenerating || st.PocCurrentStatus == broker.PocStatusValidating {
			return true, nil
		}
		if st.PocIntendedStatus == broker.PocStatusGenerating || st.PocIntendedStatus == broker.PocStatusValidating {
			return true, nil
		}
	}

	return false, nil
}
