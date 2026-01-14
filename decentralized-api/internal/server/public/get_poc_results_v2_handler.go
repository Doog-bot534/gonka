package public

import (
	"net/http"
	"sort"
	"strconv"

	"decentralized-api/logging"
	"decentralized-api/pocstorage"
	"decentralized-api/utils"

	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
)

type pocResultsV2Request struct {
	BlockHeight *int64
}

type pocResultsV2Response struct {
	PoC pocResultsV2PoC `json:"poc"`
}

type pocResultsV2PoC struct {
	Run          pocstorage.PoCRun              `json:"run"`
	Participants []pocResultsV2ParticipantEntry `json:"participants"`
}

type pocResultsV2ParticipantEntry struct {
	Address string                   `json:"address"`
	Nodes   []pocResultsV2NodeResult `json:"nodes"`
}

type pocResultsV2NodeResult struct {
	NodeID  string                                 `json:"node_id"`
	Model   string                                 `json:"model"`
	Results []pocstorage.PoCBatchesGeneratedRecord `json:"results"`
}

func (s *Server) getPoCResultsV2(c echo.Context) error {
	const dbg = "POC_V2_DEBUG"

	if s.pocStorage == nil {
		logging.Error(dbg+": results: poc storage nil", types.PoC)
		return echo.NewHTTPError(http.StatusInternalServerError, "poc storage not configured")
	}

	// Temporary auth gate: require X-TA-Signature validated against a single pubkey.
	// For GET requests we sign a stable payload derived from the selected block height.
	signature := c.Request().Header.Get(utils.XTASignatureHeader)
	if signature == "" {
		logging.Warn(dbg+": results: missing X-TA-Signature", types.PoC, "remote_ip", c.RealIP())
		return echo.NewHTTPError(http.StatusUnauthorized, "X-TA-Signature header required")
	}

	req, err := parsePoCResultsV2Request(c)
	if err != nil {
		logging.Warn(dbg+": results: bad request", types.PoC, "error", err)
		return err
	}

	logging.Info(dbg+": results: received", types.PoC,
		"remote_ip", c.RealIP(),
		"api_version", c.Request().Header.Get(utils.XApiVersionHeader),
		"block_height_param", req.BlockHeight)

	payloadHash := pocV2ResultsPayloadHash(req)
	if err := validatePoCV2StartSignature(signature, payloadHash); err != nil {
		logging.Warn("PoC v2 results signature invalid", types.PoC, "error", err)
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid X-TA-Signature")
	}
	logging.Info(dbg+": results: signature ok", types.PoC, "payload_hash", payloadHash)

	// Resolve run
	var run pocstorage.PoCRun
	if req.BlockHeight == nil {
		run, err = s.pocStorage.GetLatestRun(c.Request().Context())
	} else {
		run, err = s.pocStorage.GetClosestRunAtOrBefore(c.Request().Context(), *req.BlockHeight)
	}
	if err != nil {
		if err == pocstorage.ErrNotFound {
			logging.Info(dbg+": results: run not found", types.PoC, "block_height_param", req.BlockHeight)
			return echo.NewHTTPError(http.StatusNotFound, "poc run not found")
		}
		logging.Error(dbg+": results: failed to load run", types.PoC, "error", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to load poc run")
	}
	logging.Info(dbg+": results: resolved run", types.PoC,
		"run_block_height", run.BlockHeight,
		"run_model", run.Params.Model,
		"run_created_at", run.CreatedAt,
		"run_interrupted", run.InterruptedTime != nil)

	recs, err := s.pocStorage.ListGeneratedRecords(c.Request().Context(), run.BlockHeight)
	if err != nil {
		logging.Error(dbg+": results: failed to list records", types.PoC, "blockHeight", run.BlockHeight, "error", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list poc records")
	}
	logging.Info(dbg+": results: loaded records", types.PoC, "blockHeight", run.BlockHeight, "count", len(recs))

	resp := pocResultsV2Response{
		PoC: pocResultsV2PoC{
			Run:          run,
			Participants: aggregatePoCResultsV2Participants(recs),
		},
	}

	logging.Info(dbg+": results: responding", types.PoC, "blockHeight", run.BlockHeight, "participants", len(resp.PoC.Participants))
	return c.JSON(http.StatusOK, resp)
}

func parsePoCResultsV2Request(c echo.Context) (pocResultsV2Request, error) {
	q := c.QueryParam("block_height")
	if q == "" {
		return pocResultsV2Request{BlockHeight: nil}, nil
	}
	v, err := strconv.ParseInt(q, 10, 64)
	if err != nil || v <= 0 {
		return pocResultsV2Request{}, echo.NewHTTPError(http.StatusBadRequest, "invalid block_height")
	}
	return pocResultsV2Request{BlockHeight: &v}, nil
}

func pocV2ResultsPayloadHash(req pocResultsV2Request) string {
	// Must be stable and easy for the caller to reproduce.
	// If block_height is omitted, we sign a sentinel value that means "latest".
	val := "latest"
	if req.BlockHeight != nil {
		val = strconv.FormatInt(*req.BlockHeight, 10)
	}
	// Include a fixed prefix to avoid cross-endpoint replay.
	return utils.GenerateSHA256Hash("GET:/v2/poc/results:block_height=" + val)
}

func aggregatePoCResultsV2Participants(recs []pocstorage.PoCBatchesGeneratedRecord) []pocResultsV2ParticipantEntry {
	type nodeKey struct {
		nodeID string
		model  string
	}

	participants := map[string]map[nodeKey][]pocstorage.PoCBatchesGeneratedRecord{}
	for _, r := range recs {
		if _, ok := participants[r.Address]; !ok {
			participants[r.Address] = map[nodeKey][]pocstorage.PoCBatchesGeneratedRecord{}
		}
		k := nodeKey{nodeID: r.NodeID, model: r.Model}
		participants[r.Address][k] = append(participants[r.Address][k], r)
	}

	// Deterministic ordering for debug output.
	addrs := make([]string, 0, len(participants))
	for addr := range participants {
		addrs = append(addrs, addr)
	}
	sort.Strings(addrs)

	out := make([]pocResultsV2ParticipantEntry, 0, len(addrs))
	for _, addr := range addrs {
		nodesMap := participants[addr]

		keys := make([]nodeKey, 0, len(nodesMap))
		for k := range nodesMap {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i].nodeID != keys[j].nodeID {
				return keys[i].nodeID < keys[j].nodeID
			}
			return keys[i].model < keys[j].model
		})

		nodes := make([]pocResultsV2NodeResult, 0, len(keys))
		for _, k := range keys {
			rr := nodesMap[k]
			// Sort by ReceivedAt for stable playback.
			sort.Slice(rr, func(i, j int) bool { return rr[i].ReceivedAt.Before(rr[j].ReceivedAt) })
			nodes = append(nodes, pocResultsV2NodeResult{
				NodeID:  k.nodeID,
				Model:   k.model,
				Results: rr,
			})
		}

		out = append(out, pocResultsV2ParticipantEntry{
			Address: addr,
			Nodes:   nodes,
		})
	}
	return out
}
