package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"decentralized-api/completionapi"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal/validation"
	"decentralized-api/logging"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/cmd/inferenced/cmd"
	"github.com/productscience/inference/x/inference/calculations"
	chaintypes "github.com/productscience/inference/x/inference/types"

	"devshard"
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
	recorder   cosmosclient.CosmosMessageClient
	engine     *devshardEngine // reused for doWithLockedNode retry loop
}

func newDevshardValidator(
	mlClient *mlnodeclient.Client,
	httpClient *http.Client,
	br bridge.MainnetBridge,
	recorder cosmosclient.CosmosMessageClient,
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

func (v *devshardValidator) Validate(ctx context.Context, req devshard.ValidateRequest) (*devshard.ValidateResult, error) {
	inferenceID := strconv.FormatUint(req.InferenceID, 10)
	// devshardd pins the epoch bucket to 0. The executor stored under 0; we
	// must look up under 0 too.
	epochID := uint64(0)

	promptPayload, responsePayload, err := v.fetchPayloadsFromExecutor(ctx, req, inferenceID, epochID)
	if err != nil {
		return nil, fmt.Errorf("fetch payloads from executor: %w", err)
	}

	// Apply the same seed/logprobs modifications the executor used, then
	// override with enforced tokens for deterministic replay.
	seed := int32(req.InferenceID)
	modified, err := completionapi.ModifyRequestBody(promptPayload, seed)
	if err != nil {
		return nil, fmt.Errorf("modify request body for validation: %w", err)
	}

	var requestMap map[string]interface{}
	if err := json.Unmarshal(modified.NewBody, &requestMap); err != nil {
		return nil, fmt.Errorf("unmarshal modified prompt: %w", err)
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
	delete(requestMap, "stream_options")

	validationBody, err := json.Marshal(requestMap)
	if err != nil {
		return nil, fmt.Errorf("marshal validation body: %w", err)
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

	// 400/422 from ML node means enforced tokens unsupported; treat as valid.
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity {
		return &devshard.ValidateResult{Valid: true}, nil
	}

	respBytes, err := readValidationBody(resp)
	if err != nil {
		return nil, fmt.Errorf("read validation response: %w", err)
	}

	validationResponse, err := completionapi.NewCompletionResponseFromBytes(respBytes)
	if err != nil {
		return nil, fmt.Errorf("parse validation response: %w", err)
	}

	if validationUsage, err := validationResponse.GetUsage(); err == nil {
		if req.InputTokens > validationUsage.PromptTokens || req.OutputTokens > validationUsage.CompletionTokens {
			logging.Warn("devshardd validation failed: inflated token counts",
				chaintypes.Validation, "inferenceId", inferenceID,
				"claimedInput", req.InputTokens, "validationInput", validationUsage.PromptTokens,
				"claimedOutput", req.OutputTokens, "validationOutput", validationUsage.CompletionTokens)
			return &devshard.ValidateResult{Valid: false}, nil
		}
	}

	originalLogits := originalResponse.ExtractLogits()
	validationLogits := validationResponse.ExtractLogits()

	base := validation.BaseValidationResult{
		InferenceId:   inferenceID,
		ResponseBytes: respBytes,
	}

	result := validation.CompareLogits(originalLogits, validationLogits, base)
	return &devshard.ValidateResult{Valid: result.IsSuccessful()}, nil
}

// fetchPayloadsFromExecutor hits the executor host's /payloads endpoint
// through the bridge-decorated URL (which, in tests, routes through versiond
// to the executor's devshardd).
func (v *devshardValidator) fetchPayloadsFromExecutor(
	ctx context.Context,
	req devshard.ValidateRequest,
	inferenceID string,
	epochID uint64,
) ([]byte, []byte, error) {
	executorInfo, err := v.bridge.GetHostInfo(req.ExecutorAddress)
	if err != nil {
		return nil, nil, fmt.Errorf("get executor info: %w", err)
	}
	if executorInfo.URL == "" {
		return nil, nil, fmt.Errorf("executor has no URL")
	}

	// Other devshardd instances are reachable through their pair's proxy at
	// /devshard/<version>/sessions/.../payloads. The chain-registered URL
	// already points at the proxy, so we just append the versioned prefix.
	// Version is the ldflags constant set at build time.
	requestURL, err := validation.BuildPayloadRequestURL(
		executorInfo.URL,
		fmt.Sprintf("devshard/%s/sessions/%s/payloads", Version, req.EscrowID),
		inferenceID,
	)
	if err != nil {
		return nil, nil, err
	}

	timestamp := time.Now().UnixNano()
	validatorAddress := v.recorder.GetAccountAddress()
	signature, err := v.signPayloadRequest(inferenceID, timestamp, validatorAddress, epochID)
	if err != nil {
		return nil, nil, fmt.Errorf("sign request: %w", err)
	}

	payloadResp, err := validation.FetchPayloadsHTTP(ctx, v.httpClient, requestURL, validatorAddress, timestamp, epochID, signature)
	if err != nil {
		return nil, nil, err
	}

	encodedPubKeys, err := v.resolveExecutorPubKeys(ctx, req.ExecutorAddress)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve executor pubkeys: %w", err)
	}

	if err := validation.VerifyExecutorPayloadSignature(
		inferenceID,
		payloadResp.PromptPayload,
		payloadResp.ResponsePayload,
		payloadResp.ExecutorSignature,
		req.ExecutorAddress,
		encodedPubKeys,
	); err != nil {
		return nil, nil, fmt.Errorf("verify executor signature: %w", err)
	}

	promptHash := sha256.Sum256(payloadResp.PromptPayload)
	if !bytes.Equal(promptHash[:], req.PromptHash) {
		return nil, nil, fmt.Errorf("prompt hash mismatch: expected %x, got %x", req.PromptHash, promptHash[:])
	}
	responseHash := sha256.Sum256(payloadResp.ResponsePayload)
	if !bytes.Equal(responseHash[:], req.ResponseHash) {
		return nil, nil, fmt.Errorf("response hash mismatch: expected %x, got %x", req.ResponseHash, responseHash[:])
	}

	return payloadResp.PromptPayload, payloadResp.ResponsePayload, nil
}

func (v *devshardValidator) resolveExecutorPubKeys(ctx context.Context, executorAddress string) ([]string, error) {
	qc := v.recorder.NewInferenceQueryClient()

	grantees, err := qc.GranteesByMessageType(ctx, &chaintypes.QueryGranteesByMessageTypeRequest{
		GranterAddress: executorAddress,
		MessageTypeUrl: "/inference.inference.MsgStartInference",
	})
	if err != nil {
		return nil, fmt.Errorf("query executor grantees: %w", err)
	}
	pubkeys := make([]string, 0, len(grantees.Grantees)+1)
	for _, g := range grantees.Grantees {
		pubkeys = append(pubkeys, g.PubKey)
	}

	participant, err := qc.AccountByAddress(ctx, &chaintypes.QueryAccountByAddressRequest{
		Address: executorAddress,
	})
	if err != nil {
		return nil, fmt.Errorf("query executor participant: %w", err)
	}
	if participant.Pubkey != "" {
		pubkeys = append(pubkeys, participant.Pubkey)
	}
	return pubkeys, nil
}

func (v *devshardValidator) signPayloadRequest(inferenceID string, timestamp int64, validatorAddress string, epochID uint64) (string, error) {
	components := calculations.SignatureComponents{
		Payload:         inferenceID,
		EpochId:         epochID,
		Timestamp:       timestamp,
		TransferAddress: validatorAddress,
		ExecutorAddress: "",
	}

	signerAddressStr := v.recorder.GetSignerAddress()
	signerAddress, err := sdk.AccAddressFromBech32(signerAddressStr)
	if err != nil {
		return "", err
	}
	accountSigner := &cmd.AccountSigner{
		Addr:    signerAddress,
		Keyring: v.recorder.GetKeyring(),
	}
	return calculations.Sign(accountSigner, components, calculations.Developer)
}

func readValidationBody(resp *http.Response) ([]byte, error) {
	buf := new(bytes.Buffer)
	_, err := buf.ReadFrom(resp.Body)
	return buf.Bytes(), err
}

// Compile-time check.
var _ devshard.ValidationEngine = (*devshardValidator)(nil)
