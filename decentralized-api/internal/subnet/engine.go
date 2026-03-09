package subnet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"decentralized-api/broker"
	"decentralized-api/completionapi"
	"decentralized-api/payloadstorage"
	"decentralized-api/utils"

	"subnet"
)

// EngineAdapter implements subnet.InferenceEngine by delegating to broker and completionapi.
type EngineAdapter struct {
	broker       *broker.Broker
	nodeVersion  string
	payloadStore payloadstorage.PayloadStorage
}

func NewEngineAdapter(b *broker.Broker, nodeVersion string, ps payloadstorage.PayloadStorage) *EngineAdapter {
	return &EngineAdapter{
		broker:       b,
		nodeVersion:  nodeVersion,
		payloadStore: ps,
	}
}

func (e *EngineAdapter) Execute(ctx context.Context, req subnet.ExecuteRequest) (*subnet.ExecuteResult, error) {
	seed := int32(req.InferenceID)

	modified, err := completionapi.ModifyRequestBody(req.Prompt, seed)
	if err != nil {
		return nil, fmt.Errorf("modify request body: %w", err)
	}

	resp, err := broker.DoWithLockedNodeHTTPRetry(e.broker, req.Model, nil, 3,
		func(node *broker.Node) (*http.Response, *broker.ActionError) {
			url := node.InferenceUrlWithVersion(e.nodeVersion) + "/v1/chat/completions"
			httpResp, postErr := http.Post(url, "application/json", bytes.NewReader(modified.NewBody))
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

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	contentType := resp.Header.Get("Content-Type")
	processor := completionapi.NewExecutorResponseProcessor("")

	if isSSE(contentType) {
		lines := splitSSELines(rawBody)
		for _, line := range lines {
			if _, err := processor.ProcessStreamedResponse(line); err != nil {
				return nil, fmt.Errorf("process streamed response: %w", err)
			}
		}
	} else {
		if _, err := processor.ProcessJsonResponse(rawBody); err != nil {
			return nil, fmt.Errorf("process json response: %w", err)
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

	hash := sha256.Sum256(bodyBytes)

	usage, err := completionResp.GetUsage()
	if err != nil {
		return nil, fmt.Errorf("get usage: %w", err)
	}

	canonicalized, err := utils.CanonicalizeJSON(modified.NewBody)
	if err != nil {
		return nil, fmt.Errorf("canonicalize prompt: %w", err)
	}
	promptPayload := []byte(canonicalized)

	inferenceID := strconv.FormatUint(req.InferenceID, 10)
	if err := e.payloadStore.Store(ctx, inferenceID, 0, promptPayload, bodyBytes); err != nil {
		return nil, fmt.Errorf("store payloads: %w", err)
	}

	return &subnet.ExecuteResult{
		ResponseHash: hash[:],
		InputTokens:  usage.PromptTokens,
		OutputTokens: usage.CompletionTokens,
	}, nil
}

func isSSE(contentType string) bool {
	return len(contentType) >= 17 && contentType[:17] == "text/event-stream"
}

func splitSSELines(data []byte) []string {
	var lines []string
	for _, line := range bytes.Split(data, []byte("\n")) {
		s := string(line)
		if s != "" {
			lines = append(lines, s)
		}
	}
	return lines
}

// Compile-time check.
var _ subnet.InferenceEngine = (*EngineAdapter)(nil)
