package mlnode

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"time"

	cosmos_client "decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"decentralized-api/mlnodeclient"
	"decentralized-api/pocstorage"

	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
)

func (s *Server) postGeneratedArtifactsV2(ctx echo.Context) error {
	if s.pocStore == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "poc storage not configured")
	}

	var body mlnodeclient.GeneratedArtifactBatchV2
	if err := ctx.Bind(&body); err != nil {
		logging.Error("PoC v2 artifacts-generated callback. Failed to decode request body", types.PoC, "error", err)
		return echo.NewHTTPError(http.StatusBadRequest, err)
	}

	if body.PublicKey == "" || body.BlockHash == "" || body.BlockHeight <= 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "missing required fields")
	}
	if body.NodeId < 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "node_id must be >= 0")
	}

	address, err := cosmos_client.PubKeyToAddress(body.PublicKey)
	if err != nil {
		logging.Error("PoC v2 artifacts-generated callback. Failed to convert public key to address", types.PoC,
			"publicKey", body.PublicKey,
			"error", err)
		return echo.NewHTTPError(http.StatusBadRequest, "invalid public_key")
	}

	// Resolve run and validate this callback belongs to the exact run height.
	run, err := s.pocStore.GetClosestRunAtOrBefore(ctx.Request().Context(), body.BlockHeight)
	if err != nil {
		if err == pocstorage.ErrNotFound {
			return echo.NewHTTPError(http.StatusNotFound, "poc run not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to load poc run")
	}
	if run.BlockHeight != body.BlockHeight {
		return echo.NewHTTPError(http.StatusBadRequest, "unknown block_height for poc run")
	}
	if run.BlockHash != body.BlockHash {
		return echo.NewHTTPError(http.StatusBadRequest, "block_hash does not match stored poc run")
	}
	model := run.Params.Model

	now := time.Now().UTC()
	timeSince := int64(now.Sub(run.BlockTime).Seconds())
	if timeSince < 0 {
		timeSince = 0
	}

	nodeNum := uint64(body.NodeId)
	nodeID := ""
	if s.broker != nil {
		if n, found := s.broker.GetNodeByNodeNum(nodeNum); found && n != nil && n.Id != "" {
			nodeID = n.Id
		} else {
			logging.Warn("PoC v2 artifacts-generated callback. Unknown node_id; storing empty node_id", types.PoC,
				"node_num", body.NodeId)
		}
	}

	artifacts := make([]pocstorage.ArtifactV2, 0, len(body.Artifacts))
	for i, a := range body.Artifacts {
		if _, err := base64.StdEncoding.DecodeString(a.VectorB64); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("invalid artifacts[%d].vector_b64", i))
		}
		artifacts = append(artifacts, pocstorage.ArtifactV2{
			Nonce:     a.Nonce,
			VectorB64: a.VectorB64,
		})
	}

	rec := pocstorage.PoCBatchesGeneratedRecord{
		BlockHeight:    body.BlockHeight,
		Address:        address,
		PublicKey:      body.PublicKey,
		BlockHash:      body.BlockHash,
		NodeID:         nodeID,
		Model:          model,
		TimeSinceBlock: timeSince,
		Artifacts:      artifacts,
		ReceivedAt:     now,
	}

	stored, err := s.pocStore.StoreGeneratedRecord(ctx.Request().Context(), rec)
	if err != nil {
		logging.Error("PoC v2 artifacts-generated callback. Failed to store record", types.PoC,
			"blockHeight", body.BlockHeight,
			"address", address,
			"node_id", nodeID,
			"error", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to store record")
	}

	logging.Info("PoC v2 artifacts-generated callback. Stored record", types.PoC,
		"blockHeight", stored.BlockHeight,
		"address", stored.Address,
		"node_id", stored.NodeID,
		"model", stored.Model,
		"amount", stored.Amount,
		"hash", stored.Hash,
		"time_since_block", stored.TimeSinceBlock,
		"artifacts_count", len(stored.Artifacts))

	if s.broadcaster != nil {
		go s.broadcaster.Broadcast(context.Background(), stored)
	}

	return ctx.NoContent(http.StatusOK)
}
