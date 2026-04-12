package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"decentralized-api/completionapi"
	"decentralized-api/internal/server/public"
	"decentralized-api/payloadstorage"

	"devshard"
	mlnodeclient "devshard/mlnode"
	mlnodegen "devshard/mlnode/gen"
)

// devshardEngine implements devshard.InferenceEngine for the standalone
// devshardd binary. Unlike dapi's in-process adapter it has no broker; it
// acquires a locked ML node via NodeManager gRPC, POSTs directly, and releases
// with an outcome reflecting the result.
type devshardEngine struct {
	mlClient     *mlnodeclient.Client
	payloadStore payloadstorage.PayloadStorage
	httpClient   *http.Client
}

func newDevshardEngine(
	mlClient *mlnodeclient.Client,
	payloadStore payloadstorage.PayloadStorage,
	httpClient *http.Client,
) *devshardEngine {
	return &devshardEngine{
		mlClient:     mlClient,
		payloadStore: payloadStore,
		httpClient:   httpClient,
	}
}

// Execute runs an inference on an ML node acquired via NodeManager gRPC.
//
// Flow mirrors the in-process dapi EngineAdapter: ModifyRequestBody ->
// POST to /v1/chat/completions -> processor -> canonicalize + store payloads.
// The only change is node acquisition (gRPC instead of broker) and the retry
// policy, which rotates excluded node IDs on transport errors.
func (e *devshardEngine) Execute(ctx context.Context, req devshard.ExecuteRequest) (*devshard.ExecuteResult, error) {
	seed := int32(req.InferenceID)
	inferenceID := fmt.Sprintf("devshard-%s-%d", req.EscrowID, req.InferenceID)

	modified, err := completionapi.ModifyRequestBody(req.Prompt, seed)
	if err != nil {
		return nil, fmt.Errorf("modify request body: %w", err)
	}

	resp, err := e.doWithLockedNode(ctx, req.Model, func(endpoint string) (*http.Response, error) {
		url := endpoint + "/v1/chat/completions"
		httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(modified.NewBody))
		if reqErr != nil {
			return nil, reqErr
		}
		httpReq.Header.Set("Content-Type", "application/json")
		return e.httpClient.Do(httpReq)
	})
	if err != nil {
		return nil, fmt.Errorf("execute inference: %w", err)
	}
	defer resp.Body.Close()

	processor := completionapi.NewExecutorResponseProcessor(inferenceID)

	contentType := resp.Header.Get("Content-Type")
	isSSE := strings.HasPrefix(contentType, "text/event-stream")

	if req.ResponseWriter != nil && isSSE {
		public.ProxyResponse(resp, req.ResponseWriter, true, processor, inferenceID)
	} else {
		if err := completionapi.ProcessHTTPResponse(resp, processor); err != nil {
			return nil, fmt.Errorf("process response: %w", err)
		}
	}

	completionResp, err := processor.GetResponse()
	if err != nil {
		return nil, fmt.Errorf("get completion response: %w", err)
	}

	bodyBytes, err := completionResp.GetBodyBytes()
	if err != nil {
		return nil, fmt.Errorf("get body bytes: %w", err)
	}

	// Non-streaming response with an SSE client connection: frame as a single
	// SSE data event. Same as the dapi adapter.
	if req.ResponseWriter != nil && !isSSE {
		fmt.Fprintf(req.ResponseWriter, "data: %s\n\ndata: [DONE]\n\n", bodyBytes)
		if f, ok := req.ResponseWriter.(http.Flusher); ok {
			f.Flush()
		}
	}

	hash := sha256.Sum256(bodyBytes)
	usage, err := completionResp.GetUsage()
	if err != nil {
		return nil, fmt.Errorf("get usage: %w", err)
	}

	// Persist the ORIGINAL canonicalized prompt (not the modified one with
	// seed) so validators can re-hash the same bytes the user signed.
	promptPayload, err := devshard.CanonicalizeJSON(req.Prompt)
	if err != nil {
		return nil, fmt.Errorf("canonicalize prompt: %w", err)
	}

	// Epoch bucket is always 0 for devshardd. Validators running in other
	// devshardd instances look up with epoch 0 as well, so payloads match.
	if err := e.payloadStore.Store(ctx, payloadKey(req.EscrowID, req.InferenceID), 0, promptPayload, bodyBytes); err != nil {
		return nil, fmt.Errorf("store payloads: %w", err)
	}

	return &devshard.ExecuteResult{
		ResponseHash: hash[:],
		InputTokens:  usage.PromptTokens,
		OutputTokens: usage.CompletionTokens,
		ResponseBody: bodyBytes,
	}, nil
}

// doWithLockedNode mirrors broker.DoWithLockedNodeHTTPRetry but against the
// NodeManager gRPC client. It tries up to maxAcquireAttempts acquires,
// excluding nodes that failed with a transport-class error on earlier
// attempts. 5xx HTTP responses are also treated as transport-class for the
// purpose of node rotation. 4xx responses are returned as-is (not retried).
func (e *devshardEngine) doWithLockedNode(
	ctx context.Context,
	model string,
	fn func(endpoint string) (*http.Response, error),
) (*http.Response, error) {
	// More attempts than the in-process broker path because dapi's broker
	// may need a few seconds to update node IntendedStatus after an epoch
	// phase transition. The 2s sleep between attempts covers that lag.
	const maxAcquireAttempts = 10
	var excluded []string
	var lastErr error

	for attempt := 0; attempt < maxAcquireAttempts; attempt++ {
		acq, err := e.mlClient.Acquire(ctx, model, excluded)
		if err != nil {
			// Couldn't acquire any node (likely ResourceExhausted = no
			// nodes with IntendedStatus=INFERENCE yet). Sleep before
			// retrying to give the broker time to process epoch events.
			lastErr = fmt.Errorf("acquire: %w", err)
			time.Sleep(2 * time.Second)
			continue
		}

		resp, httpErr := fn(acq.Endpoint)
		outcome := mlnodegen.ReleaseOutcome_SUCCESS

		if httpErr != nil {
			// Transport-class failure on the outbound HTTP. The node may be
			// sick; exclude it and retry.
			outcome = mlnodegen.ReleaseOutcome_TRANSPORT_ERROR
			lastErr = httpErr
		} else if resp.StatusCode >= 500 {
			// Upstream 5xx: also rotate nodes.
			resp.Body.Close()
			outcome = mlnodegen.ReleaseOutcome_TRANSPORT_ERROR
			lastErr = fmt.Errorf("upstream status %d", resp.StatusCode)
			resp = nil
		}

		// Release must fire regardless of outcome to release the lock.
		if releaseErr := e.mlClient.Release(ctx, acq.LockId, outcome); releaseErr != nil {
			// Release failure is logged via lastErr but does not block
			// retries or the caller -- the lock will eventually expire.
			if lastErr == nil {
				lastErr = fmt.Errorf("release: %w", releaseErr)
			}
		}

		if outcome == mlnodegen.ReleaseOutcome_SUCCESS {
			return resp, nil
		}

		// Failure: rotate excluded set and retry.
		if acq.NodeId != "" {
			excluded = append(excluded, acq.NodeId)
		}
	}

	if lastErr == nil {
		lastErr = errors.New("no attempts made")
	}
	return nil, lastErr
}

// Compile-time check.
var _ devshard.InferenceEngine = (*devshardEngine)(nil)
