package devshard

import (
	"bytes"
	"context"
	"fmt"
	"net/http"

	"decentralized-api/broker"
	"decentralized-api/chainphase"
	"decentralized-api/completionapi"
	"decentralized-api/payloadstorage"

	"devshard"
	devshardserver "devshard/server"
)

// EngineAdapter implements devshard.InferenceEngine by delegating to broker and completionapi.
type EngineAdapter struct {
	broker       *broker.Broker
	nodeVersion  string
	payloadStore payloadstorage.PayloadStorage
	phaseTracker *chainphase.ChainPhaseTracker
	httpClient   *http.Client
}

func NewEngineAdapter(
	b *broker.Broker,
	nodeVersion string,
	ps payloadstorage.PayloadStorage,
	phaseTracker *chainphase.ChainPhaseTracker,
	httpClient *http.Client,
) *EngineAdapter {
	return &EngineAdapter{
		broker:       b,
		nodeVersion:  nodeVersion,
		payloadStore: ps,
		phaseTracker: phaseTracker,
		httpClient:   httpClient,
	}
}

func (e *EngineAdapter) Execute(ctx context.Context, req devshard.ExecuteRequest) (*devshard.ExecuteResult, error) {
	seed := int32(req.InferenceID)
	inferenceId := fmt.Sprintf("devshard-%s-%d", req.EscrowID, req.InferenceID)

	modified, err := completionapi.ModifyRequestBody(req.Prompt, seed)
	if err != nil {
		return nil, fmt.Errorf("modify request body: %w", err)
	}

	resp, err := broker.DoWithLockedNodeHTTPRetry(e.broker, req.Model, nil, 3,
		func(node *broker.Node) (*http.Response, *broker.ActionError) {
			url := node.InferenceUrlWithVersion(e.nodeVersion) + "/v1/chat/completions"
			httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(modified.NewBody))
			if reqErr != nil {
				return nil, broker.NewApplicationActionError(reqErr)
			}
			httpReq.Header.Set("Content-Type", "application/json")
			httpResp, postErr := e.httpClient.Do(httpReq)
			if postErr != nil {
				return nil, broker.NewTransportActionError(postErr)
			}
			return httpResp, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("broker execute: %w", err)
	}
	defer resp.Body.Close()

	processed, err := ProcessExecutionHTTPResponse(req, resp, inferenceId)
	if err != nil {
		return nil, err
	}

	// Store the canonicalized ORIGINAL prompt (not the modified one with seed).
	promptPayload, err := devshard.CanonicalizeJSON(req.Prompt)
	if err != nil {
		return nil, fmt.Errorf("canonicalize prompt: %w", err)
	}

	storageKey := devshardserver.PayloadKey(req.EscrowID, req.InferenceID)
	epochID := currentEpochID(e.phaseTracker)
	if err := e.payloadStore.Store(ctx, storageKey, epochID, promptPayload, processed.ResponseBody); err != nil {
		return nil, fmt.Errorf("store payloads: %w", err)
	}

	return &devshard.ExecuteResult{
		ResponseHash: processed.ResponseHash,
		InputTokens:  processed.InputTokens,
		OutputTokens: processed.OutputTokens,
		ResponseBody: processed.ResponseBody,
	}, nil
}

// DevshardPayloadKey creates a namespaced storage key for devshard payloads.
// Format: "devshard:<escrowID>:<inferenceID>" to prevent cross-session collisions.
func DevshardPayloadKey(escrowID string, inferenceID uint64) string {
	return devshardserver.PayloadKey(escrowID, inferenceID)
}

// Compile-time check.
var _ devshard.InferenceEngine = (*EngineAdapter)(nil)
