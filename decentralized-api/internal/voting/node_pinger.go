// Package voting provides types and services for the node voting mechanism.
package voting

import (
	"bytes"
	"context"
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"decentralized-api/payloadstorage"
	apiutils "decentralized-api/utils"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/cmd/inferenced/cmd"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
)

// NodePinger handles HTTP communication with nodes for payload verification and relay.
// Used by:
// - Voters: to ping respondent and verify payload exists, relay to challenger if found
// - Challengers: to request verification from pre-sampled voters
type NodePinger struct {
	httpClient   *http.Client
	cosmosClient cosmosclient.CosmosMessageClient
	timeout      time.Duration
}

// NodePingerConfig holds configuration for NodePinger.
type NodePingerConfig struct {
	// Timeout for HTTP requests (default: 10s)
	Timeout time.Duration
}

// DefaultNodePingerConfig returns sensible defaults.
func DefaultNodePingerConfig() NodePingerConfig {
	return NodePingerConfig{
		Timeout: 10 * time.Second,
	}
}

// NewNodePinger creates a new NodePinger instance.
func NewNodePinger(cosmosClient cosmosclient.CosmosMessageClient, config NodePingerConfig) *NodePinger {
	if config.Timeout == 0 {
		config.Timeout = 10 * time.Second
	}

	return &NodePinger{
		httpClient: &http.Client{
			Timeout: config.Timeout,
		},
		cosmosClient: cosmosClient,
		timeout:      config.Timeout,
	}
}

// Types for Payload Retrieval (used by voters to ping respondent)

// PayloadPingResponse represents the response from a node's payload endpoint.
// Mirrors the public/admin/payload_handlers.go PayloadResponse struct.
// TODO! Consider moving to a shared payload DTO package for reuse across public/admin/voting.
type PayloadPingResponse struct {
	InferenceId   string `json:"inference_id"`
	PromptPayload []byte `json:"prompt_payload"`
	// ResponsePayload is included for completeness but not used in "challenger requests payload from respondent" case
	ResponsePayload []byte `json:"response_payload,omitempty"`
	// RespondentSignature: when respondent is executor, this is the executor's signature on the response (wire format: executor_signature).
	RespondentSignature string `json:"executor_signature,omitempty"`
}

// PingResult contains the result of pinging a node for payload.
type PingResult struct {
	// Success indicates if the ping was successful and data was retrieved.
	Success bool
	// Payload contains the retrieved payload data (if successful).
	Payload *PayloadPingResponse
	// PromptHash is the computed hash of the prompt payload.
	PromptHash string
	// Error contains any error that occurred.
	Error error
}

// Types for Verification Request (used by challenger to request from voters)

// VerificationRequest is sent by challenger to voters asking them to verify the respondent.
type VerificationRequest struct {
	InferenceId       string `json:"inference_id"`
	RespondentAddress string `json:"respondent_address"`
	RespondentURL     string `json:"respondent_url"`
	ChallengerURL     string `json:"challenger_url"` // Where voter should relay payload if found
	EpochId           uint64 `json:"epoch_id"`
	PromptHash        string `json:"prompt_hash"` // Expected hash from on-chain
	ChallengerSig     string `json:"challenger_signature"`
}

// VerificationResponse is returned by voters after verification.
type VerificationResponse struct {
	InferenceId    string   `json:"inference_id"`
	Vote           VoteType `json:"vote"`
	VoterAddress   string   `json:"voter_address"`
	VoterSignature string   `json:"voter_signature"`
	// DataFound indicates if respondent had the payload
	DataFound bool `json:"data_found"`
	// DataRelayed indicates if payload was successfully relayed to challenger
	DataRelayed bool `json:"data_relayed"`
	// PromptHash is the hash of payload found (if any)
	PromptHash string `json:"prompt_hash,omitempty"`
	// Error message if verification failed
	ErrorMsg string `json:"error,omitempty"`
}

// Voter Functions: Ping Respondent and Relay to Challenger

// PingRespondentForPayload pings the respondent's payload endpoint to check if they have the payload.
// Used by voters during verification.
func (np *NodePinger) PingRespondentForPayload(
	ctx context.Context,
	respondentURL string,
	inferenceId string,
	epochId uint64,
) (*PingResult, error) {
	// Build URL with inference_id as query parameter
	baseUrl, err := url.JoinPath(respondentURL, "v1/inference/prompt")
	if err != nil {
		return &PingResult{Success: false, Error: fmt.Errorf("failed to build URL: %w", err)}, err
	}

	parsedUrl, err := url.Parse(baseUrl)
	if err != nil {
		return &PingResult{Success: false, Error: fmt.Errorf("failed to parse URL: %w", err)}, err
	}

	query := parsedUrl.Query()
	query.Set("inference_id", inferenceId)
	parsedUrl.RawQuery = query.Encode()
	requestUrl := parsedUrl.String()

	// Sign the request
	timestamp := time.Now().UnixNano()
	voterAddress := np.cosmosClient.GetAccountAddress()

	signature, err := np.signPayloadRequest(inferenceId, timestamp, voterAddress, epochId)
	if err != nil {
		return &PingResult{Success: false, Error: fmt.Errorf("failed to sign request: %w", err)}, err
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestUrl, nil)
	if err != nil {
		return &PingResult{Success: false, Error: fmt.Errorf("failed to create request: %w", err)}, err
	}

	// Set authentication headers
	req.Header.Set(apiutils.XRequesterAddressHeader, voterAddress)
	req.Header.Set(apiutils.XTimestampHeader, strconv.FormatInt(timestamp, 10))
	req.Header.Set(apiutils.XEpochIdHeader, strconv.FormatUint(epochId, 10))
	req.Header.Set(apiutils.AuthorizationHeader, signature)

	// Execute request
	resp, err := np.httpClient.Do(req)
	if err != nil {
		logging.Debug("Payload ping to respondent failed", types.Validation,
			"respondentURL", respondentURL, "inferenceId", inferenceId, "error", err)
		return &PingResult{Success: false, Error: fmt.Errorf("request failed: %w", err)}, err
	}
	defer resp.Body.Close()

	// Handle response codes
	if resp.StatusCode == http.StatusNotFound {
		logging.Debug("Payload not found on respondent", types.Validation,
			"respondentURL", respondentURL, "inferenceId", inferenceId)
		return &PingResult{Success: false, Error: nil}, nil // Not found is not an error, just no data
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		err := fmt.Errorf("respondent returned status %d: %s", resp.StatusCode, string(body))
		return &PingResult{Success: false, Error: err}, err
	}

	// Parse response
	var payloadResp PayloadPingResponse
	if err := json.NewDecoder(resp.Body).Decode(&payloadResp); err != nil {
		return &PingResult{Success: false, Error: fmt.Errorf("failed to decode response: %w", err)}, err
	}

	// Compute prompt hash
	promptHash, err := payloadstorage.ComputePromptHash(payloadResp.PromptPayload)
	if err != nil {
		logging.Warn("Failed to compute prompt hash", types.Validation,
			"inferenceId", inferenceId, "error", err)
		promptHash = ""
	}

	logging.Debug("Successfully pinged respondent for payload", types.Validation,
		"respondentURL", respondentURL, "inferenceId", inferenceId, "promptHash", promptHash)

	return &PingResult{
		Success:    true,
		Payload:    &payloadResp,
		PromptHash: promptHash,
	}, nil
}

// RelayPayloadToChallenger sends the payload to the challenger.
// Used by voters after retrieving payload from respondent.
func (np *NodePinger) RelayPayloadToChallenger(
	ctx context.Context,
	challengerURL string,
	inferenceId string,
	payload *PayloadPingResponse,
) (bool, error) {
	// Build URL for relay endpoint
	// TODO! Add the relay endpoint to the challenger's URL.
	relayUrl, err := url.JoinPath(challengerURL, "v1/inference/payloads/relay")
	if err != nil {
		return false, fmt.Errorf("failed to build relay URL: %w", err)
	}

	// Marshal payload
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return false, fmt.Errorf("failed to marshal payload: %w", err)
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, relayUrl, bytes.NewReader(payloadBytes))
	if err != nil {
		return false, fmt.Errorf("failed to create relay request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	// Sign the relay request
	timestamp := time.Now().UnixNano()
	voterAddress := np.cosmosClient.GetAccountAddress()

	signature, err := np.signRelayRequest(inferenceId, timestamp, voterAddress)
	if err != nil {
		return false, fmt.Errorf("failed to sign relay request: %w", err)
	}

	req.Header.Set(apiutils.XValidatorAddressHeader, voterAddress)
	req.Header.Set(apiutils.XTimestampHeader, strconv.FormatInt(timestamp, 10))
	req.Header.Set(apiutils.AuthorizationHeader, signature)

	// Execute request
	resp, err := np.httpClient.Do(req)
	if err != nil {
		logging.Warn("Relay to challenger failed", types.Validation,
			"challengerURL", challengerURL, "inferenceId", inferenceId, "error", err)
		return false, fmt.Errorf("relay request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("relay returned status %d: %s", resp.StatusCode, string(body))
	}

	logging.Info("Successfully relayed payload to challenger", types.Validation,
		"challengerURL", challengerURL, "inferenceId", inferenceId)

	return true, nil
}

// VerifyAndRelay is the main voter function: ping respondent, relay to challenger if found.
// Returns the verification result that will be used for voting.
func (np *NodePinger) VerifyAndRelay(
	ctx context.Context,
	respondentURL string,
	challengerURL string,
	inferenceId string,
	epochId uint64,
	expectedPromptHash string,
) *VerificationResponse {
	voterAddress := np.cosmosClient.GetAccountAddress()

	response := &VerificationResponse{
		InferenceId:  inferenceId,
		VoterAddress: voterAddress,
		Vote:         VoteInvalid, // Default to invalid until we determine
	}

	// Step 1: Ping respondent for payload
	pingResult, err := np.PingRespondentForPayload(ctx, respondentURL, inferenceId, epochId)
	if err != nil {
		response.ErrorMsg = err.Error()
		return response
	}

	if !pingResult.Success || pingResult.Payload == nil {
		// Respondent doesn't have payload - negative vote
		response.Vote = VoteNegative
		response.DataFound = false
		logging.Info("Voter verification: respondent does not have payload", types.Validation,
			"inferenceId", inferenceId, "voterAddress", voterAddress)
		return response
	}

	// Respondent has payload
	response.DataFound = true
	response.PromptHash = pingResult.PromptHash

	// Step 2: Verify hash matches (if expected hash provided)
	if expectedPromptHash != "" && pingResult.PromptHash != expectedPromptHash {
		// Hash mismatch - respondent has wrong payload
		response.Vote = VoteNegative
		logging.Warn("Voter verification: respondent has wrong payload (hash mismatch)", types.Validation,
			"inferenceId", inferenceId, "expected", expectedPromptHash, "actual", pingResult.PromptHash)
		return response
	}

	// Step 3: Relay to challenger (if URL provided)
	if challengerURL != "" {
		relayed, err := np.RelayPayloadToChallenger(ctx, challengerURL, inferenceId, pingResult.Payload)
		if err != nil {
			logging.Warn("Voter verification: relay to challenger failed", types.Validation,
				"inferenceId", inferenceId, "error", err)
		}
		response.DataRelayed = relayed
	}

	// Respondent has correct payload - positive vote
	response.Vote = VotePositive
	logging.Info("Voter verification: respondent has correct payload", types.Validation,
		"inferenceId", inferenceId, "voterAddress", voterAddress, "relayed", response.DataRelayed)

	return response
}

// Challenger Functions: Request Verification from Voters

// VoterVerificationResult contains a single voter's verification outcome.
type VoterVerificationResult struct {
	VoterURL  string
	Response  *VerificationResponse
	Error     error
	Reachable bool
}

// ChallengerVotingResult contains the aggregated result of requesting verification from voters.
// Used by challenger to track voting progress.
type ChallengerVotingResult struct {
	InferenceId string
	// VoterResults contains results from all voters that were contacted
	VoterResults []VoterVerificationResult
	// NegativeVotes is the count of negative votes
	NegativeVotes int
	// FirstPositive is the first positive voter result (if any)
	FirstPositive *VoterVerificationResult
	// StoppedEarly indicates if we stopped after finding a positive vote
	StoppedEarly bool
}

// RequestVerificationFromVoters contacts pre-sampled voters to verify the respondent.
// Voters are pinged in parallel with per-voter timeouts and retries.
// The process stops early once the first positive vote is received.
// Used by challenger to supervise the voting process.
func (np *NodePinger) RequestVerificationFromVoters(
	ctx context.Context,
	voterURLs []string,
	request *VerificationRequest,
	cfg VotingConfig,
) (*ChallengerVotingResult, error) {
	result := &ChallengerVotingResult{
		InferenceId:  request.InferenceId,
		VoterResults: make([]VoterVerificationResult, 0, len(voterURLs)),
	}

	if len(voterURLs) == 0 {
		logging.Warn("No voters provided for verification", types.Validation,
			"inferenceId", request.InferenceId)
		return result, nil
	}

	// Apply sane defaults from config.
	if cfg.MaxNumNodes <= 0 || cfg.MaxNumNodes > len(voterURLs) {
		cfg.MaxNumNodes = len(voterURLs)
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 1
	}
	if cfg.VoteTimeout <= 0 {
		// Fall back to NodePinger's HTTP client timeout if not specified.
		cfg.VoteTimeout = int(np.timeout.Milliseconds())
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type voterJobResult struct {
		voterURL string
		result   VoterVerificationResult
	}

	resultsCh := make(chan voterJobResult, cfg.MaxNumNodes)

	var wg sync.WaitGroup
	maxVoters := cfg.MaxNumNodes

	for i := 0; i < maxVoters; i++ {
		voterURL := voterURLs[i]

		wg.Add(1)
		go func(voterURL string) {
			defer wg.Done()

			var lastResult VoterVerificationResult

			for attempt := 0; attempt < cfg.MaxRetries; attempt++ {
				select {
				case <-ctx.Done():
					return
				default:
				}

				logging.Debug("Requesting verification from voter", types.Validation,
					"inferenceId", request.InferenceId, "voterURL", voterURL,
					"attempt", attempt+1, "maxAttempts", cfg.MaxRetries)

				// Apply per-voter timeout on top of the HTTP client's timeout.
				callCtx, cancelCall := context.WithTimeout(ctx, time.Duration(cfg.VoteTimeout)*time.Millisecond)
				res := np.requestVerificationFromSingleVoter(callCtx, voterURL, request)
				cancelCall()

				lastResult = res

				// If the voter was reachable and responded, no need to retry.
				if res.Error == nil || !res.Reachable {
					break
				}
			}

			select {
			case <-ctx.Done():
				return
			case resultsCh <- voterJobResult{voterURL: voterURL, result: lastResult}:
			}
		}(voterURL)
	}

	// Close results channel once all goroutines complete.
	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	for jobRes := range resultsCh {
		voterResult := jobRes.result
		result.VoterResults = append(result.VoterResults, voterResult)

		// If we already stopped early due to a positive vote, just record results without
		// updating the aggregated outcome further.
		if result.StoppedEarly && result.FirstPositive != nil {
			continue
		}

		if voterResult.Error != nil || !voterResult.Reachable {
			result.NegativeVotes++
			continue
		}

		if voterResult.Response != nil {
			switch voterResult.Response.Vote {
			case VotePositive:
				// Capture the first positive vote and stop further work.
				resultCopy := voterResult
				result.FirstPositive = &resultCopy
				result.StoppedEarly = true

				logging.Info("Found positive vote, stopping verification", types.Validation,
					"inferenceId", request.InferenceId, "voterURL", jobRes.voterURL,
					"contacted", len(result.VoterResults))

				// Cancel the shared context so in-flight requests can be aborted.
				cancel()

			case VoteNegative:
				result.NegativeVotes++

			default:
				// Invalid or unknown vote - treat as negative for aggregation.
				result.NegativeVotes++
			}
		}
	}

	if result.StoppedEarly && result.FirstPositive != nil {
		return result, nil
	}

	// If context was cancelled externally and we didn't stop because of a positive vote,
	// surface the cancellation error.
	if err := ctx.Err(); err != nil && err != context.Canceled {
		logging.Warn("Verification request cancelled or timed out", types.Validation,
			"inferenceId", request.InferenceId, "contacted", len(result.VoterResults), "error", err)
		return result, err
	}

	logging.Info("All voters completed without positive vote", types.Validation,
		"inferenceId", request.InferenceId, "totalVoters", len(result.VoterResults))

	return result, nil
}

// requestVerificationFromSingleVoter sends a verification request to one voter.
func (np *NodePinger) requestVerificationFromSingleVoter(
	ctx context.Context,
	voterURL string,
	request *VerificationRequest,
) VoterVerificationResult {
	result := VoterVerificationResult{
		VoterURL:  voterURL,
		Reachable: false,
	}

	// Build URL for voter's verify endpoint
	// TODO! Add the verify endpoint to the voter's URL.
	verifyUrl, err := url.JoinPath(voterURL, "v1/voting/verify")
	if err != nil {
		result.Error = fmt.Errorf("failed to build verify URL: %w", err)
		return result
	}

	// Marshal request
	requestBytes, err := json.Marshal(request)
	if err != nil {
		result.Error = fmt.Errorf("failed to marshal request: %w", err)
		return result
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, verifyUrl, bytes.NewReader(requestBytes))
	if err != nil {
		result.Error = fmt.Errorf("failed to create request: %w", err)
		return result
	}

	req.Header.Set("Content-Type", "application/json")

	// Sign the request
	timestamp := time.Now().UnixNano()
	challengerAddress := np.cosmosClient.GetAccountAddress()

	signature, err := np.signVerificationRequest(request.InferenceId, timestamp, challengerAddress)
	if err != nil {
		result.Error = fmt.Errorf("failed to sign request: %w", err)
		return result
	}

	req.Header.Set(apiutils.XValidatorAddressHeader, challengerAddress)
	req.Header.Set(apiutils.XTimestampHeader, strconv.FormatInt(timestamp, 10))
	req.Header.Set(apiutils.AuthorizationHeader, signature)

	// Execute request
	resp, err := np.httpClient.Do(req)
	if err != nil {
		result.Error = fmt.Errorf("request failed: %w", err)
		return result
	}
	defer resp.Body.Close()

	result.Reachable = true

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		result.Error = fmt.Errorf("voter returned status %d: %s", resp.StatusCode, string(body))
		return result
	}

	// Parse response
	var verifyResp VerificationResponse
	if err := json.NewDecoder(resp.Body).Decode(&verifyResp); err != nil {
		result.Error = fmt.Errorf("failed to decode response: %w", err)
		return result
	}

	result.Response = &verifyResp
	return result
}

// Signature Helpers

// signPayloadRequest signs a payload retrieval request.
func (np *NodePinger) signPayloadRequest(
	inferenceId string,
	timestamp int64,
	voterAddress string,
	epochId uint64,
) (string, error) {
	components := calculations.SignatureComponents{
		Payload:         inferenceId,
		EpochId:         epochId,
		Timestamp:       timestamp,
		TransferAddress: voterAddress,
		ExecutorAddress: "",
	}

	return np.sign(components)
}

// signRelayRequest signs a payload relay request.
func (np *NodePinger) signRelayRequest(
	inferenceId string,
	timestamp int64,
	voterAddress string,
) (string, error) {
	components := calculations.SignatureComponents{
		Payload:         inferenceId,
		EpochId:         0,
		Timestamp:       timestamp,
		TransferAddress: voterAddress,
		ExecutorAddress: "",
	}

	return np.sign(components)
}

// signVerificationRequest signs a verification request from challenger to voter.
func (np *NodePinger) signVerificationRequest(
	inferenceId string,
	timestamp int64,
	challengerAddress string,
) (string, error) {
	components := calculations.SignatureComponents{
		Payload:         inferenceId,
		EpochId:         0,
		Timestamp:       timestamp,
		TransferAddress: challengerAddress,
		ExecutorAddress: "",
	}

	return np.sign(components)
}

// sign is a helper to sign with the cosmos client's keyring.
func (np *NodePinger) sign(components calculations.SignatureComponents) (string, error) {
	signerAddressStr := np.cosmosClient.GetSignerAddress()
	signerAddress, err := sdk.AccAddressFromBech32(signerAddressStr)
	if err != nil {
		return "", fmt.Errorf("invalid signer address: %w", err)
	}

	accountSigner := &cmd.AccountSigner{
		Addr:    signerAddress,
		Keyring: np.cosmosClient.GetKeyring(),
	}

	return calculations.Sign(accountSigner, components, calculations.Developer)
}

