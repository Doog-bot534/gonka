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
	const dbg = "POC_V2_DEBUG"

	if s.pocStorage == nil {
		logging.Error(dbg+": start: poc storage nil", types.PoC)
		return echo.NewHTTPError(http.StatusInternalServerError, "poc storage not configured")
	}

	var req pocStartV2Request
	bodyBytes, err := io.ReadAll(c.Request().Body)
	if err != nil {
		logging.Warn(dbg+": start: failed to read body", types.PoC, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, "failed to read body")
	}
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		logging.Warn(dbg+": start: invalid json", types.PoC, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	logging.Info(dbg+": start: received", types.PoC,
		"remote_ip", c.RealIP(),
		"api_version", c.Request().Header.Get(utils.XApiVersionHeader),
		"blockHeight", req.BlockHeight,
		"blockHash", req.BlockHash,
		"blockTime", req.BlockTime,
		"duration", req.Duration,
		"frequency", req.Frequency,
		"batchSize", req.BatchSize,
		"model", req.Params.Model,
		"seqLen", req.Params.SeqLen,
		"kDim", req.Params.KDim)

	// Temporary auth gate: require X-TA-Signature validated against a single pubkey.
	// This is a testing-only constraint and should be removed for production.
	signature := c.Request().Header.Get(utils.XTASignatureHeader)
	if signature == "" {
		logging.Warn(dbg+": start: missing X-TA-Signature", types.PoC)
		return echo.NewHTTPError(http.StatusUnauthorized, "X-TA-Signature header required")
	}
	canonical, err := utils.CanonicalizeJSON(bodyBytes)
	if err != nil {
		logging.Warn(dbg+": start: canonicalize json failed", types.PoC, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, "invalid json")
	}
	payloadHash := pocV2StartPayloadHashFromBody(canonical)
	if err := validatePoCV2StartSignature(signature, payloadHash); err != nil {
		logging.Warn("PoC v2 start signature invalid", types.PoC, "error", err)
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid X-TA-Signature")
	}
	logging.Info(dbg+": start: signature ok", types.PoC, "payload_hash", payloadHash)

	// Basic validation
	if req.BlockHeight <= 0 {
		logging.Warn(dbg+": start: invalid block_height", types.PoC, "blockHeight", req.BlockHeight)
		return echo.NewHTTPError(http.StatusBadRequest, "block_height must be > 0")
	}
	if req.BlockHash == "" {
		logging.Warn(dbg+": start: missing block_hash", types.PoC)
		return echo.NewHTTPError(http.StatusBadRequest, "block_hash is required")
	}
	if req.BlockTime.IsZero() {
		logging.Warn(dbg+": start: missing block_time", types.PoC)
		return echo.NewHTTPError(http.StatusBadRequest, "block_time is required")
	}
	if req.Duration <= 0 {
		logging.Warn(dbg+": start: invalid duration", types.PoC, "duration", req.Duration)
		return echo.NewHTTPError(http.StatusBadRequest, "duration must be > 0")
	}
	if req.Frequency <= 0 {
		logging.Warn(dbg+": start: invalid frequency", types.PoC, "frequency", req.Frequency)
		return echo.NewHTTPError(http.StatusBadRequest, "frequency must be > 0")
	}
	if req.BatchSize <= 0 {
		logging.Warn(dbg+": start: invalid batch_size", types.PoC, "batchSize", req.BatchSize)
		return echo.NewHTTPError(http.StatusBadRequest, "batch_size must be > 0")
	}
	if req.Params.Model == "" {
		logging.Warn(dbg+": start: missing params.model", types.PoC)
		return echo.NewHTTPError(http.StatusBadRequest, "params.model is required")
	}
	if req.Params.SeqLen <= 0 {
		logging.Warn(dbg+": start: invalid params.seq_len", types.PoC, "seqLen", req.Params.SeqLen)
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
	logging.Info(dbg+": start: legacy check complete", types.PoC, "legacyActive", legacyActive, "started", started)

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
	logging.Info(dbg+": start: v2 previous-run check complete", types.PoC,
		"started", started,
		"interruptedPrevious", interruptedPrev)

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
		Params:           pocstorage.PoCParamsV2{Model: req.Params.Model, SeqLen: req.Params.SeqLen},
		InterruptedTime:  interruptedAt,
		CreatedAt:        now,
	}

	if err := s.pocStorage.UpsertRun(c.Request().Context(), run); err != nil {
		logging.Error("Failed to upsert PoC run", types.PoC, "blockHeight", req.BlockHeight, "error", err)
		logging.Error(dbg+": start: upsert failed", types.PoC, "blockHeight", req.BlockHeight, "error", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to store poc run")
	}

	logging.Info("PoC v2 start request stored", types.PoC,
		"blockHeight", run.BlockHeight,
		"started", started,
		"legacyPoCActive", legacyActive,
		"interruptedPrevious", interruptedPrev)
	logging.Info(dbg+": start: stored", types.PoC, "blockHeight", run.BlockHeight, "started", started)

	if started {
		logging.Info(dbg+": start: triggering mlnodes", types.PoC, "blockHeight", run.BlockHeight, "model", run.Params.Model)
		if err := s.triggerPoCV2InitGenerate(c.Request().Context(), run); err != nil {
			logging.Error("Failed to trigger PoC v2 init generation on mlnodes", types.PoC,
				"blockHeight", run.BlockHeight,
				"error", err)
			logging.Error(dbg+": start: trigger mlnodes failed", types.PoC, "blockHeight", run.BlockHeight, "error", err)
			return echo.NewHTTPError(http.StatusBadGateway, err.Error())
		}

		s.schedulePoCV2Stop(run)
		logging.Info(dbg+": start: scheduled stop", types.PoC, "blockHeight", run.BlockHeight, "after_seconds", run.DurationSeconds)
	}

	logging.Info(dbg+": start: responding", types.PoC, "blockHeight", run.BlockHeight, "started", started)
	return c.JSON(http.StatusOK, pocStartV2Response{
		Started:             started,
		Run:                 run,
		InterruptedPrevious: interruptedPrev,
	})
}

const (
	PoCv2ArtifactsBasePath = "/v2/poc-artifacts"
)

// GetPocArtifactsV2GeneratedCallbackUrl returns the base callback URL for v2 artifact generation.
// MLNode will append /generated to this URL when calling back.
func GetPocArtifactsV2GeneratedCallbackUrl(callbackUrl string) string {
	return fmt.Sprintf("%s%s", callbackUrl, PoCv2ArtifactsBasePath)
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

	eligible := make([]broker.NodeResponse, 0, len(nodes))
	for _, nr := range nodes {
		if nr.Node.PoCPort == 0 {
			continue
		}
		if !s.nodeSupportsModelForPoCV2(nr, run.Params.Model) {
			logging.Info("Skipping PoC v2 init generation for node: model not supported", types.PoC,
				"node_id", nr.Node.Id,
				"model", run.Params.Model)
			continue
		}
		eligible = append(eligible, nr)
	}
	if len(eligible) == 0 {
		return fmt.Errorf("no eligible nodes for model %q", run.Params.Model)
	}

	pubKey := s.nodeBroker.GetParticipantPubKey()
	if pubKey == "" {
		return fmt.Errorf("participant pubkey not available")
	}

	var wg sync.WaitGroup

	var mu sync.Mutex
	var failed []string

	for _, nr := range eligible {
		nr := nr
		wg.Add(1)
		go func() {
			defer wg.Done()

			client := s.nodeBroker.NewNodeClient(&nr.Node)

			req := mlnodeclient.PoCInitGenerateRequestV2{
				BlockHash:   run.BlockHash,
				BlockHeight: run.BlockHeight,
				PublicKey:   pubKey,
				NodeID:      int(nr.Node.NodeNum),
				NodeCount:   len(eligible),
				Params: mlnodeclient.PoCParamsV2{
					Model:  run.Params.Model,
					SeqLen: run.Params.SeqLen,
				},
				URL: GetPocArtifactsV2GeneratedCallbackUrl(s.nodeBroker.GetCallbackUrl()),
			}

			// Don't let a single node hang the whole request for minutes.
			callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			_, err := client.InitGenerateV2(callCtx, req)
			if err != nil {
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

func (s *Server) schedulePoCV2Stop(run pocstorage.PoCRun) {
	if s.pocStorage == nil || s.nodeBroker == nil {
		return
	}
	if run.DurationSeconds <= 0 {
		return
	}

	delay := time.Duration(run.DurationSeconds) * time.Second
	go func(blockHeight int64) {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		<-timer.C

		ctx := context.Background()

		// Safety checks: only stop if this is still the latest run and it's not interrupted.
		latest, err := s.pocStorage.GetLatestRun(ctx)
		if err != nil {
			return
		}
		if latest.BlockHeight != blockHeight {
			return
		}
		if latest.InterruptedTime != nil {
			return
		}

		if err := s.stopPoCV2OnAllNodes(ctx, latest); err != nil {
			logging.Warn("PoC v2 stop fanout finished with errors", types.PoC,
				"blockHeight", latest.BlockHeight,
				"error", err)
		} else {
			logging.Info("PoC v2 stop fanout finished", types.PoC, "blockHeight", latest.BlockHeight)
		}
	}(run.BlockHeight)
}

func (s *Server) stopPoCV2OnAllNodes(ctx context.Context, run pocstorage.PoCRun) error {
	nodes, err := s.nodeBroker.GetNodes()
	if err != nil {
		return fmt.Errorf("get nodes: %w", err)
	}
	if len(nodes) == 0 {
		return fmt.Errorf("no nodes configured")
	}

	eligible := make([]broker.NodeResponse, 0, len(nodes))
	for _, nr := range nodes {
		if nr.Node.PoCPort == 0 {
			continue
		}
		if !s.nodeSupportsModelForPoCV2(nr, run.Params.Model) {
			continue
		}
		eligible = append(eligible, nr)
	}
	if len(eligible) == 0 {
		// Nothing to stop for this model.
		return nil
	}

	var wg sync.WaitGroup

	var mu sync.Mutex
	var failed []string

	for _, nr := range eligible {
		nr := nr
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := s.nodeBroker.NewNodeClient(&nr.Node)

			callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			_, err := client.StopPowV2(callCtx)
			if err != nil {
				mu.Lock()
				failed = append(failed, nr.Node.Id)
				mu.Unlock()
				logging.Warn("PoC v2 stop failed for node", types.PoC,
					"blockHeight", run.BlockHeight,
					"node_id", nr.Node.Id,
					"error", err)
			}
		}()
	}

	wg.Wait()

	if len(failed) > 0 {
		return fmt.Errorf("stop failed on %d node(s): %v", len(failed), failed)
	}
	return nil
}

func (s *Server) nodeSupportsModelForPoCV2(nr broker.NodeResponse, modelID string) bool {
	if modelID == "" {
		return false
	}

	// For PoC v2 we only run on nodes currently in INFERENCE, and only if the model matches
	// the same model-selection logic used when the broker switches nodes to INFERENCE.
	if nr.State.CurrentStatus != types.HardwareNodeStatus_INFERENCE {
		return false
	}
	if nr.Node.Id == "" {
		return false
	}
	if s.nodeBroker == nil {
		return false
	}
	// Best-effort: compute selected model ID without mutating broker/node state.
	// If we can't determine it, treat the node as ineligible.
	//
	// Note: `SelectedInferenceModelIDForNode` may query governance models from chain when
	// epoch models are not populated.
	selected, err := s.nodeBroker.SelectedInferenceModelIDForNode(nr)
	if err != nil {
		logging.Warn("PoC v2: failed to determine selected inference model for node", types.PoC,
			"node_id", nr.Node.Id,
			"error", err)
		return false
	}
	return selected == modelID
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
