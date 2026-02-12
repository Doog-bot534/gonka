package voting

import (
	"bytes"
	"context"
	"decentralized-api/chainphase"
	"decentralized-api/cosmosclient"
	internalutils "decentralized-api/internal/utils"
	"decentralized-api/logging"
	"decentralized-api/internal/server/public"
	"decentralized-api/utils"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
)


type TransferAgentPinger struct {
	httpClient   *http.Client
	recorder     *cosmosclient.InferenceCosmosClient
	phaseTracker *chainphase.ChainPhaseTracker
}

type TransferAgentPingerConfig struct {
	Timeout time.Duration
}

func DefaultTransferAgentPingerConfig() TransferAgentPingerConfig {
	return TransferAgentPingerConfig {
		Timeout: 10 * time.Second,
	}
}

func NewTransferAgentPinger(
	recorder *cosmosclient.InferenceCosmosClient,
	phaseTracker *chainphase.ChainPhaseTracker,
	config TransferAgentPingerConfig,
) *TransferAgentPinger {
	httpClient := utils.NewHttpClient(config.Timeout)
	return &TransferAgentPinger {
		httpClient,
		recorder,
		phaseTracker,
	}
}

func (tap *TransferAgentPinger) RetrievePayloadToRequester(
	ctx context.Context,
	inferenceId string,
) error {
	queryClient := tap.recorder.NewInferenceQueryClient()
	inferenceResp, err := queryClient.Inference(ctx, &types.QueryGetInferenceRequest{Index: inferenceId})
	if err != nil {
		logging.Error("Failed to query inference for epochId verification", types.Voting, "inferenceId", inferenceId, "error", err)
		return err
	}

	executorAddress := inferenceResp.Inference.AssignedTo
	currentAddress := tap.recorder.Address
	if executorAddress != currentAddress {
		// This inference ID was not meant for us; do nothing
		return nil
	}

	// TODO: Consider adding a mechanism to check whether the request started so the TA is not pinged
	// For now, this should act like a basic barrier
	if inferenceResp.Inference.Status != types.InferenceStatus_STARTED {
		logging.Debug("Inference status is not started; skipping", types.Voting, "inferenceId", inferenceId, "status", inferenceResp.Inference.Status)
		return nil
	}

	transferAddress := inferenceResp.Inference.TransferredBy
	logging.Info(
		"Matched inference start event", types.Voting,
		"executorAddress", executorAddress,
		"inferenceId", inferenceId,
		"transferAddress", transferAddress,
	)

	transferURL, err := tap.GetAddressUrl(ctx, transferAddress)
	if err != nil {
		logging.Error("Failed to get transfer URL", types.Voting, "error", err)
		return err
	}

	epochId := inferenceResp.Inference.EpochId
	payload, err := tap.RequestPromptFromTransferAgent(ctx, inferenceId, epochId, transferURL, transferAddress)
	if err != nil {
		logging.Error("Failed to request payload from transfer agent", types.Voting, "epochId", epochId, "inferenceId", inferenceId, "transferURL", transferURL, "transferAddress", transferAddress, "error", err)
		return err
	}

	logging.Debug("Got payload", types.Voting, "payload", payload)

	executorURL, err := tap.GetAddressUrl(ctx, executorAddress)
	if err != nil {
		logging.Error("Failed to get executor URL", types.Voting, "error", err)
		return err
	}

	err = tap.PostChat(executorURL, executorAddress, payload)
	if err != nil {
		logging.Error("Failed to post chat request to executor", types.Voting, "inferenceId", inferenceId, "executorURL", executorURL, "executorAddress", executorAddress, "error", err)
		return err
	}

	return nil
}

func (tap *TransferAgentPinger) PostChat(
	executorURL string,
	executorAddress string,
	payloadBytes []byte,
) error {
	var chatRequest public.ChatRequest
	err := json.Unmarshal(payloadBytes, &chatRequest)
	if err != nil {
		logging.Error("Failed to unmarshal chat request", types.Voting, "error", err)
		return err
	}

	// TODO: It's a bit silly to make a request to ourselves.
	chatURL, err := url.JoinPath(executorURL, "v1/chat/completions")
	if err != nil {
		logging.Error("Failed to build completions URL", types.Voting, "error", err)
		return err
	}

	req, err := http.NewRequest("POST", chatURL, bytes.NewBuffer(chatRequest.Body))
	if err != nil {
		logging.Error("Failed create POST request to completions URL", types.Voting, "chatURL", chatURL, "error", err)
		return err
	}

	req.Header.Set(utils.XInferenceIdHeader, chatRequest.InferenceId)
	req.Header.Set(utils.XSeedHeader, chatRequest.Seed)
	req.Header.Set(utils.AuthorizationHeader, chatRequest.AuthKey)
	req.Header.Set(utils.XTimestampHeader, strconv.FormatInt(chatRequest.Timestamp, 10))
	req.Header.Set(utils.XTransferAddressHeader, chatRequest.TransferAddress)
	req.Header.Set(utils.XRequesterAddressHeader, chatRequest.RequesterAddress)
	req.Header.Set(utils.XTASignatureHeader, chatRequest.TransferSignature)
	req.Header.Set(utils.XPromptHashHeader, chatRequest.PromptHash)
	req.Header.Set(utils.ContentTypeHeader, chatRequest.ContentType)

	resp, err := tap.httpClient.Do(req)
	if err != nil {
		logging.Error("Failed to POST to completions URL", types.Voting, "chatURL", chatURL, "error", err)
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		if err != nil {
			logging.Error("Chat request returned non-200 status code, failed to read response body", types.Voting, "statusCode", resp.StatusCode, "error", err)
			return fmt.Errorf("Chat request returned status code %d, failed to read response body: %t", resp.StatusCode, err)
		}

		logging.Error("Chat request returned non-200 status code", types.Voting, "statusCode", resp.StatusCode, "body", body)
		return fmt.Errorf("Chat request returned status code %d: %s", resp.StatusCode, string(body))
	}
	if err != nil {
		logging.Error("Failed to read response body", types.Voting, "error", err)
		return err
	}

	// TODO: proxy response?
	return nil
}

func (tap *TransferAgentPinger) RequestPromptFromTransferAgent(
	ctx context.Context,
	inferenceId string,
	epochId uint64,
	transferURL string,
	transferAddress string,
) ([]byte, error) {
	retrievalURL, err := url.JoinPath(transferURL, "v1/inference/prompt")
	if err != nil {
		logging.Error("Failed to build retrieval URL", types.Voting, "error", err)
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, retrievalURL, nil)
	if err != nil {
		logging.Error("Failed to create retrieval request", types.Voting, "error", err)
		return nil, err
	}

	executorAddress := tap.recorder.GetAddress()
	timestamp := time.Now().UnixNano()
	signature, err := tap.calculateSignature(
		inferenceId,
		timestamp,
		epochId,
		executorAddress,
		"",
		calculations.Developer,
	)
	if err != nil {
		logging.Error("Failed to calculate signature", types.Voting, "error", err)
		return nil, err
	}

	query := req.URL.Query()
	query.Set("inference_id", inferenceId)
	req.URL.RawQuery = query.Encode()

	req.Header.Set(utils.XRequesterAddressHeader, executorAddress)
	req.Header.Set(utils.XTimestampHeader, strconv.FormatInt(timestamp, 10))
	req.Header.Set(utils.XEpochIdHeader, strconv.FormatUint(epochId, 10))
	req.Header.Set(utils.AuthorizationHeader, signature)

	resp, err := tap.httpClient.Do(req)
	if err != nil {
		logging.Error("Failed to obtain retrieval response", types.Voting, "error", err)
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		if err != nil {
			logging.Error("Transfer agent returned non-200 status code, failed to read response body", types.Voting, "statusCode", resp.StatusCode, "error", err)
			return nil, fmt.Errorf("Transfer agent returned status code %d, failed to read response body: %t", resp.StatusCode, err)
		}

		logging.Error("Transfer agent returned non-200 status code", types.Voting, "statusCode", resp.StatusCode, "body", body)
		return nil, fmt.Errorf("Transfer agent returned status code %d: %s", resp.StatusCode, string(body))
	}
	if err != nil {
		logging.Error("Failed to read response body", types.Voting, "error", err)
		return nil, err
	}

	var response public.PayloadResponse
	err = json.Unmarshal(body, &response)
	if err != nil {
		logging.Error("Failed to unmarshal response body", types.Voting, "error", err)
		return nil, err
	}

	logging.Info("Retrieved prompt from the executor", types.Voting)
	return response.PromptPayload, nil
}

func (tap *TransferAgentPinger) GetAddressUrl(ctx context.Context, address string) (string, error) {
	queryClient := tap.recorder.NewInferenceQueryClient()
	participantResp, err := queryClient.Participant(ctx, &types.QueryGetParticipantRequest{
		Index: address,
	})
	if err != nil {
		logging.Error("Failed to query address", types.Voting, "error", err)
		return "", err
	}
	return participantResp.Participant.InferenceUrl, nil
}

// calculateSignature calculates a signature for the given components and agent type
func (tap *TransferAgentPinger) calculateSignature(
	payload string,
	timestamp int64,
	epochId uint64,
	transferAddress string,
	executorAddress string,
	agentType calculations.SignatureType,
) (string, error) {
	signerAddress := tap.recorder.GetSignerAddress()
	keyring := tap.recorder.GetKeyring()
	signature, err := internalutils.CalculateSignature(
		payload,
		timestamp,
		epochId,
		transferAddress,
		executorAddress,
		agentType,
		signerAddress,
		keyring,
	)
	if err != nil {
		logging.Error("Failed to sign signature", types.Voting, "address", signerAddress, "agentType", agentType, "error", err)
		return "", err
	}
	return signature, nil
}
