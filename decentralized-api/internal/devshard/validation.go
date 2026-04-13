package devshard

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strconv"

	"decentralized-api/broker"
	"decentralized-api/chainphase"

	"devshard"
	"devshard/bridge"
)

// ValidationAdapter implements devshard.ValidationEngine by re-executing inference
// with enforced tokens and comparing logits.
type ValidationAdapter struct {
	broker       *broker.Broker
	nodeVersion  string
	phaseTracker *chainphase.ChainPhaseTracker
	httpClient   *http.Client
	bridge       bridge.MainnetBridge
	recorder     PayloadAuthClient
}

func NewValidationAdapter(
	b *broker.Broker,
	nodeVersion string,
	phaseTracker *chainphase.ChainPhaseTracker,
	httpClient *http.Client,
	br bridge.MainnetBridge,
	recorder PayloadAuthClient,
) *ValidationAdapter {
	return &ValidationAdapter{
		broker:       b,
		nodeVersion:  nodeVersion,
		phaseTracker: phaseTracker,
		httpClient:   httpClient,
		bridge:       br,
		recorder:     recorder,
	}
}

func (v *ValidationAdapter) Validate(ctx context.Context, req devshard.ValidateRequest) (*devshard.ValidateResult, error) {
	inferenceID := strconv.FormatUint(req.InferenceID, 10)
	epochID := req.EpochID
	if epochID == 0 {
		epochID = currentEpochID(v.phaseTracker)
	}

	// Fetch payloads from executor
	promptPayload, responsePayload, err := v.fetchPayloadsFromExecutor(ctx, req, inferenceID, epochID)
	if err != nil {
		return nil, fmt.Errorf("fetch payloads from executor: %w", err)
	}

	validationBody, err := BuildValidationBody(promptPayload, responsePayload, req.InferenceID)
	if err != nil {
		return nil, err
	}

	resp, err := broker.DoWithLockedNodeHTTPRetry(v.broker, req.Model, nil, 3,
		func(node *broker.Node) (*http.Response, *broker.ActionError) {
			url := node.InferenceUrlWithVersion(v.nodeVersion) + "/v1/chat/completions"
			httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(validationBody))
			if reqErr != nil {
				return nil, broker.NewApplicationActionError(reqErr)
			}
			httpReq.Header.Set("Content-Type", "application/json")
			httpResp, postErr := v.httpClient.Do(httpReq)
			if postErr != nil {
				return nil, broker.NewTransportActionError(postErr)
			}
			return httpResp, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("broker validate: %w", err)
	}
	defer resp.Body.Close()

	return EvaluateValidationResponse(resp, req, inferenceID, "devshard", responsePayload)
}

// fetchPayloadsFromExecutor retrieves payloads from the executor host using devshard session endpoint.
func (v *ValidationAdapter) fetchPayloadsFromExecutor(ctx context.Context, req devshard.ValidateRequest, inferenceID string, epochID uint64) ([]byte, []byte, error) {
	return FetchPayloadsFromExecutor(
		ctx,
		v.httpClient,
		v.bridge,
		v.recorder,
		req,
		inferenceID,
		epochID,
		devshard.LegacySessionPayloadPath(req.EscrowID),
	)
}

// Compile-time check.
var _ devshard.ValidationEngine = (*ValidationAdapter)(nil)
