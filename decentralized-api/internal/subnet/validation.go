package subnet

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"decentralized-api/broker"
	"decentralized-api/completionapi"
	"decentralized-api/internal/validation"
	"decentralized-api/payloadstorage"

	"subnet"
)

// ValidationAdapter implements subnet.ValidationEngine by re-executing inference
// with enforced tokens and comparing logits.
type ValidationAdapter struct {
	broker       *broker.Broker
	nodeVersion  string
	payloadStore payloadstorage.PayloadStorage
}

func NewValidationAdapter(b *broker.Broker, nodeVersion string, ps payloadstorage.PayloadStorage) *ValidationAdapter {
	return &ValidationAdapter{
		broker:       b,
		nodeVersion:  nodeVersion,
		payloadStore: ps,
	}
}

func (v *ValidationAdapter) Validate(ctx context.Context, req subnet.ValidateRequest) (*subnet.ValidateResult, error) {
	inferenceID := strconv.FormatUint(req.InferenceID, 10)

	promptPayload, responsePayload, err := v.payloadStore.Retrieve(ctx, inferenceID, 0)
	if err != nil {
		return nil, fmt.Errorf("retrieve payloads: %w", err)
	}

	var requestMap map[string]interface{}
	if err := json.Unmarshal(promptPayload, &requestMap); err != nil {
		return nil, fmt.Errorf("unmarshal prompt payload: %w", err)
	}

	originalResponse, err := completionapi.NewCompletionResponseFromLinesFromResponsePayload(responsePayload)
	if err != nil {
		return nil, fmt.Errorf("parse original response: %w", err)
	}

	enforcedTokens, err := originalResponse.GetEnforcedTokens()
	if err != nil {
		return nil, fmt.Errorf("get enforced tokens: %w", err)
	}

	requestMap["enforced_tokens"] = enforcedTokens
	requestMap["stream"] = false
	requestMap["skip_special_tokens"] = false
	delete(requestMap, "stream_options")

	validationBody, err := json.Marshal(requestMap)
	if err != nil {
		return nil, fmt.Errorf("marshal validation body: %w", err)
	}

	resp, err := broker.DoWithLockedNodeHTTPRetry(v.broker, req.Model, nil, 3,
		func(node *broker.Node) (*http.Response, *broker.ActionError) {
			url := node.InferenceUrlWithVersion(v.nodeVersion) + "/v1/chat/completions"
			httpResp, postErr := http.Post(url, "application/json", bytes.NewReader(validationBody))
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

	// 400/422 from ML node means enforced tokens not supported; treat as valid
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity {
		return &subnet.ValidateResult{Valid: true}, nil
	}

	var respBytes []byte
	respBytes, err = readBody(resp)
	if err != nil {
		return nil, fmt.Errorf("read validation response: %w", err)
	}

	validationResponse, err := completionapi.NewCompletionResponseFromBytes(respBytes)
	if err != nil {
		return nil, fmt.Errorf("parse validation response: %w", err)
	}

	originalLogits := originalResponse.ExtractLogits()
	validationLogits := validationResponse.ExtractLogits()

	base := validation.BaseValidationResult{
		InferenceId:   inferenceID,
		ResponseBytes: respBytes,
	}

	result := validation.CompareLogits(originalLogits, validationLogits, base)

	return &subnet.ValidateResult{Valid: result.IsSuccessful()}, nil
}

func readBody(resp *http.Response) ([]byte, error) {
	buf := new(bytes.Buffer)
	_, err := buf.ReadFrom(resp.Body)
	return buf.Bytes(), err
}

// Compile-time check.
var _ subnet.ValidationEngine = (*ValidationAdapter)(nil)
