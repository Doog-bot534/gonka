package public

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"decentralized-api/logging"
	"decentralized-api/pocstorage"
	"decentralized-api/utils"

	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
)

type poCResultV2NodeRequest struct {
	NodeID         string `json:"node_id"`
	Model          string `json:"model"`
	Amount         int64  `json:"amount"`
	Hash           string `json:"hash"`
	TimeSinceBlock int64  `json:"time_since_block"` // Spec requires time_since
}

type poCResultV2Request struct {
	BlockHeight int64                    `json:"block_height"`
	Address     string                   `json:"address"`
	Nodes       []poCResultV2NodeRequest `json:"nodes"`
}

func (s *Server) postPoCResultsV2(c echo.Context) error {
	ctx := c.Request().Context()

	bodyBytes, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to read body")
	}

	var req poCResultV2Request
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid json")
	}
	receivedAt := time.Now().UTC()

	// 0. Active Validator Check (Receiver)
	// We must be an active validator to accept/validate results
	myAddress := s.recorder.GetAddress()

	// Get Current Epoch from phase tracker (avoid chain query on every request)
	if s.phaseTracker == nil {
		logging.Error("Phase tracker is nil for PoC results active check", types.PoC)
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	epochState := s.phaseTracker.GetCurrentEpochState()
	if epochState == nil || !epochState.IsSynced {
		logging.Warn("Phase tracker not ready for PoC results active check", types.PoC,
			"is_nil", epochState == nil,
			"is_synced", epochState != nil && epochState.IsSynced)
		return echo.NewHTTPError(http.StatusServiceUnavailable, "node not synced")
	}
	currentEpoch := epochState.LatestEpoch.EpochIndex

	isActive, err := s.epochGroupDataCache.IsActiveParticipant(ctx, currentEpoch, myAddress)
	if err != nil {
		logging.Warn("Failed to check active validator status", types.PoC, "error", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	if !isActive {
		logging.Warn("Rejecting PoC results: node is not an active validator", types.PoC, "my_address", myAddress)
		return echo.NewHTTPError(http.StatusForbidden, "node is not an active validator")
	}

	// 1. Signature Verification preparation
	signature := c.Request().Header.Get(utils.XTASignatureHeader)
	if signature == "" {
		return echo.NewHTTPError(http.StatusUnauthorized, "X-TA-Signature header required")
	}
	canonical, err := utils.CanonicalizeJSON(bodyBytes)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "canonicalization failed")
	}
	payloadHash := utils.GenerateSHA256Hash(canonical)

	// 2. Active Validator Verification & PubKey retrieval
	// Use shared cache to avoid RPC spam
	pCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	pubKeys, err := s.participantsCache.GetParticipantPubKeys(pCtx, currentEpoch, req.Address)
	if err != nil {
		logging.Warn("Failed to query participant pubkey for PoC results", types.PoC, "address", req.Address, "error", err)
		return echo.NewHTTPError(http.StatusForbidden, "participant lookup failed")
	}

	// Verify availability/weight - implicitly checked by GetParticipantPubKeys success (it queries chain if missing)
	granterPubKey := ""
	if len(pubKeys) > 0 {
		granterPubKey = pubKeys[len(pubKeys)-1]
	}

	// Verify Signature
	components := calculations.SignatureComponents{
		Payload:         payloadHash,
		Timestamp:       0, // Timestamp check usually part of header if enforced, keeping 0 for now as per `poc_v2_auth.go`
		TransferAddress: "",
		ExecutorAddress: "",
	}

	if err := calculations.ValidateSignatureWithGrantees(components, calculations.Developer, pubKeys, signature); err != nil {
		logging.Warn("Invalid signature for peer results", types.PoC, "address", req.Address, "error", err)
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid signature")
	}
	// 3. Verify Block Height
	latestRun, err := s.pocStorage.GetLatestRun(ctx)
	if err != nil {
		logging.Warn("Failed to get latest PoC run", types.PoC, "error", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to check poc run")
	}
	// Spec: "allow only for the latest known block_height poc"
	if req.BlockHeight != latestRun.BlockHeight {
		return echo.NewHTTPError(http.StatusConflict, fmt.Sprintf("invalid block_height: expected %d, got %d", latestRun.BlockHeight, req.BlockHeight))
	}
	if latestRun.InterruptedTime != nil {
		return echo.NewHTTPError(http.StatusConflict, "poc run interrupted")
	}

	// 4. Store Results
	// 4. Store Results - Batch Optimization
	timeSinceBlock := int64(0)
	if !latestRun.BlockTime.IsZero() {
		timeSinceBlock = int64(receivedAt.Sub(latestRun.BlockTime).Seconds())
	}
	var records []pocstorage.PoCBatchesGeneratedRecord

	for _, node := range req.Nodes {
		rec := pocstorage.PoCBatchesGeneratedRecord{
			BlockHeight:    req.BlockHeight,
			Address:        req.Address,
			NodeID:         node.NodeID,
			Model:          node.Model,
			Amount:         node.Amount,
			Hash:           node.Hash,
			TimeSinceBlock: timeSinceBlock,
			ReceivedAt:     receivedAt,
			Artifacts:      nil, // Peer summary
			PublicKey:      granterPubKey,
		}
		records = append(records, rec)
	}

	if len(records) > 0 {
		_, err := s.pocStorage.StoreGeneratedRecordsBatch(ctx, records)
		if err != nil {
			logging.Error("Failed to store peer PoC record batch", types.PoC, "error", err, "count", len(records))
			return echo.NewHTTPError(http.StatusInternalServerError, "storage failed")
		}
	}

	return c.NoContent(http.StatusOK)
}
