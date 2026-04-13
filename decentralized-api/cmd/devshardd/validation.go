package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strconv"

	internaldevshard "decentralized-api/internal/devshard"

	devshardpkg "devshard"
	"devshard/bridge"
	mlnodeclient "devshard/mlnode"
)

// devshardValidator implements devshard.ValidationEngine for the standalone
// devshardd binary. Same shape as dapi's in-process ValidationAdapter; the
// only structural differences are:
//   - node acquisition uses NodeManager gRPC (no broker)
//   - the payload-store epoch is fixed to 0 (devshardd has no phase tracker)
type devshardValidator struct {
	mlClient   *mlnodeclient.Client
	httpClient *http.Client
	bridge     bridge.MainnetBridge
	recorder   internaldevshard.PayloadAuthClient
	engine     *devshardEngine // reused for doWithLockedNode retry loop
}

func newDevshardValidator(
	mlClient *mlnodeclient.Client,
	httpClient *http.Client,
	br bridge.MainnetBridge,
	recorder internaldevshard.PayloadAuthClient,
	engine *devshardEngine,
) *devshardValidator {
	return &devshardValidator{
		mlClient:   mlClient,
		httpClient: httpClient,
		bridge:     br,
		recorder:   recorder,
		engine:     engine,
	}
}

func (v *devshardValidator) Validate(ctx context.Context, req devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error) {
	inferenceID := strconv.FormatUint(req.InferenceID, 10)
	// devshardd pins the epoch bucket to 0. The executor stored under 0; we
	// must look up under 0 too.
	epochID := uint64(0)

	promptPayload, responsePayload, err := v.fetchPayloadsFromExecutor(ctx, req, inferenceID, epochID)
	if err != nil {
		return nil, fmt.Errorf("fetch payloads from executor: %w", err)
	}

	validationBody, err := internaldevshard.BuildValidationBody(promptPayload, responsePayload, req.InferenceID)
	if err != nil {
		return nil, err
	}

	resp, err := v.engine.doWithLockedNode(ctx, req.Model, func(endpoint string) (*http.Response, error) {
		url := endpoint + "/v1/chat/completions"
		httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(validationBody))
		if reqErr != nil {
			return nil, reqErr
		}
		httpReq.Header.Set("Content-Type", "application/json")
		return v.httpClient.Do(httpReq)
	})
	if err != nil {
		return nil, fmt.Errorf("validate inference: %w", err)
	}
	defer resp.Body.Close()

	return internaldevshard.EvaluateValidationResponse(resp, req, inferenceID, "devshardd", responsePayload)
}

// fetchPayloadsFromExecutor hits the executor host's /payloads endpoint
// through the bridge-decorated URL (which, in tests, routes through versiond
// to the executor's devshardd).
func (v *devshardValidator) fetchPayloadsFromExecutor(
	ctx context.Context,
	req devshardpkg.ValidateRequest,
	inferenceID string,
	epochID uint64,
) ([]byte, []byte, error) {
	return internaldevshard.FetchPayloadsFromExecutor(
		ctx,
		v.httpClient,
		v.bridge,
		v.recorder,
		req,
		inferenceID,
		epochID,
		devshardpkg.VersionedSessionPayloadPath(Version, req.EscrowID),
	)
}

// Compile-time check.
var _ devshardpkg.ValidationEngine = (*devshardValidator)(nil)
