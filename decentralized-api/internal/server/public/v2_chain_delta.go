package public

import (
	"bytes"
	"context"
	"crypto/sha256"
	"decentralized-api/logging"
	"decentralized-api/utils"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
)

const (
	v2ChainDeltaMaxBlocks           = 128
	v2ChainDeltaMaxMessagesPerBlock = 16

	v2ChainMessageTypeStartInference  = "StartInference"
	v2ChainMessageTypeFinishInference = "FinishInference"
	v2ChainMessageTypeMissedInference = "MissedInference"

	v2DeveloperBlockMessageDomain = "v2_dev_block_msg_v1"
	v2DeveloperBlockSignDomain    = "v2_dev_block_sig_v1"
	v2DeveloperStateHashDomain    = "v2_dev_state_hash_v1"
)

type V2RequestEnvelope struct {
	OpenAIRequest       json.RawMessage      `json:"openai_request"`
	DeveloperChainDelta *DeveloperChainDelta `json:"developer_chain_delta"`
}

type DeveloperChainDelta struct {
	BaseBlockSequence   uint64                `json:"base_block_sequence"`
	Blocks              []DeveloperChainBlock `json:"blocks"`
	LatestBlockSequence uint64                `json:"latest_block_sequence"`
}

type DeveloperChainBlock struct {
	BlockSequence uint64                  `json:"block_sequence"`
	EscrowID      string                  `json:"escrow_id"`
	StateHash     string                  `json:"state_hash"`
	Messages      []DeveloperChainMessage `json:"messages"`
	Signature     string                  `json:"signature"`
}

type DeveloperChainMessage struct {
	Type                    string `json:"type"`
	RequestID               string `json:"request_id"`
	ModelID                 string `json:"model_id,omitempty"`
	RequestPayloadHash      string `json:"request_payload_hash,omitempty"`
	ResponsePayloadHash     string `json:"response_payload_hash,omitempty"`
	ExecutorAddress         string `json:"executor_address,omitempty"`
	ExecutorSignerAddress   string `json:"executor_signer_address,omitempty"`
	ExecutorSignerPubKey    string `json:"executor_signer_pubkey,omitempty"`
	ExecutorSignature       string `json:"executor_signature,omitempty"`
	InputTokenCount         uint64 `json:"input_token_count,omitempty"`
	OutputTokenCount        uint64 `json:"output_token_count,omitempty"`
	MissedInferenceEvidence string `json:"missed_inference_evidence,omitempty"`
	Status                  string `json:"status,omitempty"`
	Timestamp               int64  `json:"timestamp"`
}

func (d *DeveloperChainDelta) UnmarshalJSON(data []byte) error {
	type deltaAlias struct {
		BaseBlockSequence   json.RawMessage       `json:"base_block_sequence"`
		Blocks              []DeveloperChainBlock `json:"blocks"`
		LatestBlockSequence json.RawMessage       `json:"latest_block_sequence"`
	}

	aux := deltaAlias{}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	baseBlockSequence, err := parseFlexibleUint64(aux.BaseBlockSequence)
	if err != nil {
		return fmt.Errorf("base_block_sequence: %w", err)
	}
	latestBlockSequence, err := parseFlexibleUint64(aux.LatestBlockSequence)
	if err != nil {
		return fmt.Errorf("latest_block_sequence: %w", err)
	}

	d.BaseBlockSequence = baseBlockSequence
	d.Blocks = aux.Blocks
	d.LatestBlockSequence = latestBlockSequence
	return nil
}

func (b *DeveloperChainBlock) UnmarshalJSON(data []byte) error {
	type blockAlias struct {
		BlockSequence json.RawMessage         `json:"block_sequence"`
		EscrowID      string                  `json:"escrow_id"`
		StateHash     string                  `json:"state_hash"`
		Messages      []DeveloperChainMessage `json:"messages"`
		Signature     string                  `json:"signature"`
	}

	aux := blockAlias{}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	blockSequence, err := parseFlexibleUint64(aux.BlockSequence)
	if err != nil {
		return fmt.Errorf("block_sequence: %w", err)
	}

	b.BlockSequence = blockSequence
	b.EscrowID = aux.EscrowID
	b.StateHash = aux.StateHash
	b.Messages = aux.Messages
	b.Signature = aux.Signature
	return nil
}

func (m *DeveloperChainMessage) UnmarshalJSON(data []byte) error {
	type messageAlias struct {
		Type                    string          `json:"type"`
		RequestID               string          `json:"request_id"`
		ModelID                 string          `json:"model_id,omitempty"`
		RequestPayloadHash      string          `json:"request_payload_hash,omitempty"`
		ResponsePayloadHash     string          `json:"response_payload_hash,omitempty"`
		ExecutorAddress         string          `json:"executor_address,omitempty"`
		ExecutorSignerAddress   string          `json:"executor_signer_address,omitempty"`
		ExecutorSignerPubKey    string          `json:"executor_signer_pubkey,omitempty"`
		ExecutorSignature       string          `json:"executor_signature,omitempty"`
		InputTokenCount         json.RawMessage `json:"input_token_count,omitempty"`
		OutputTokenCount        json.RawMessage `json:"output_token_count,omitempty"`
		MissedInferenceEvidence string          `json:"missed_inference_evidence,omitempty"`
		Status                  string          `json:"status,omitempty"`
		Timestamp               json.RawMessage `json:"timestamp"`
	}

	aux := messageAlias{}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	timestamp, err := parseFlexibleInt64(aux.Timestamp)
	if err != nil {
		return fmt.Errorf("timestamp: %w", err)
	}
	inputTokenCount, err := parseFlexibleOptionalUint64(aux.InputTokenCount)
	if err != nil {
		return fmt.Errorf("input_token_count: %w", err)
	}
	outputTokenCount, err := parseFlexibleOptionalUint64(aux.OutputTokenCount)
	if err != nil {
		return fmt.Errorf("output_token_count: %w", err)
	}

	m.Type = aux.Type
	m.RequestID = aux.RequestID
	m.ModelID = aux.ModelID
	m.RequestPayloadHash = aux.RequestPayloadHash
	m.ResponsePayloadHash = aux.ResponsePayloadHash
	m.ExecutorAddress = aux.ExecutorAddress
	m.ExecutorSignerAddress = aux.ExecutorSignerAddress
	m.ExecutorSignerPubKey = aux.ExecutorSignerPubKey
	m.ExecutorSignature = aux.ExecutorSignature
	m.InputTokenCount = inputTokenCount
	m.OutputTokenCount = outputTokenCount
	m.MissedInferenceEvidence = aux.MissedInferenceEvidence
	m.Status = aux.Status
	m.Timestamp = timestamp
	return nil
}

type parsedV2Request struct {
	envelopeBody        []byte
	openAIRequestBody   []byte
	openAIRequest       *OpenAiRequest
	developerChainDelta DeveloperChainDelta
}

type v2DeveloperChainState struct {
	latestBlockSequence uint64
	blocksBySeq         map[uint64]DeveloperChainBlock
	deterministicState  v2DeterministicChainState
	stateByBlockSeq     map[uint64]v2DeterministicChainState
}

type v2DeterministicChainState struct {
	executorStats map[string]v2ExecutorDeterministicStats
}

type v2ExecutorDeterministicStats struct {
	processedInferences uint64
	inputTokenTotal     uint64
	outputTokenTotal    uint64
	missedInferences    uint64
}

type v2DeveloperChainStore struct {
	mutex  sync.Mutex
	chains map[string]v2DeveloperChainState
}

func newV2DeveloperChainStore() *v2DeveloperChainStore {
	return &v2DeveloperChainStore{
		chains: make(map[string]v2DeveloperChainState),
	}
}

func readV2Request(request *http.Request, writer http.ResponseWriter) (*parsedV2Request, error) {
	requestBody, err := readRequestBody(request, writer)
	if err != nil {
		logging.Error("Unable to read v2 request body", types.Server, "error", err)
		return nil, echo.NewHTTPError(http.StatusBadRequest, "Unable to read request body")
	}

	envelope := V2RequestEnvelope{}
	if err := json.Unmarshal(requestBody, &envelope); err != nil {
		logging.Warn("Unable to parse v2 request body", types.Server, "error", err)
		return nil, echo.NewHTTPError(http.StatusBadRequest, "Invalid JSON request body")
	}

	openAIRequestBody := envelope.OpenAIRequest
	openAIRequestBodyTrimmed := bytes.TrimSpace(openAIRequestBody)
	if len(openAIRequestBodyTrimmed) == 0 || bytes.Equal(openAIRequestBodyTrimmed, []byte("null")) {
		return nil, ErrV2OpenAIRequestRequired
	}

	openAIRequest := OpenAiRequest{}
	if err := json.Unmarshal(openAIRequestBody, &openAIRequest); err != nil {
		logging.Warn("Unable to parse v2 openai_request payload", types.Server, "error", err)
		return nil, ErrV2OpenAIRequestInvalid
	}

	if err := validateV2DeveloperChainDeltaEnvelope(envelope.DeveloperChainDelta); err != nil {
		return nil, err
	}

	return &parsedV2Request{
		envelopeBody:        requestBody,
		openAIRequestBody:   openAIRequestBody,
		openAIRequest:       &openAIRequest,
		developerChainDelta: *envelope.DeveloperChainDelta,
	}, nil
}

func validateV2DeveloperChainDeltaEnvelope(developerChainDelta *DeveloperChainDelta) error {
	if developerChainDelta == nil {
		return ErrV2DeveloperChainDeltaRequired
	}
	if len(developerChainDelta.Blocks) == 0 {
		return ErrV2DeveloperChainDeltaBlocksRequired
	}
	if len(developerChainDelta.Blocks) > v2ChainDeltaMaxBlocks {
		return ErrV2DeveloperChainDeltaTooLarge
	}

	for _, block := range developerChainDelta.Blocks {
		if block.BlockSequence == 0 {
			return ErrV2DeveloperChainDeltaMalformed
		}
		if strings.TrimSpace(block.EscrowID) == "" {
			return ErrV2DeveloperChainDeltaMalformed
		}
		if strings.TrimSpace(block.StateHash) == "" {
			return ErrV2DeveloperBlockStateHashRequired
		}
		if len(block.Messages) == 0 {
			return ErrV2DeveloperChainDeltaMalformed
		}
		if len(block.Messages) > v2ChainDeltaMaxMessagesPerBlock {
			return ErrV2DeveloperChainDeltaTooLarge
		}

		for _, message := range block.Messages {
			if strings.TrimSpace(message.Type) == "" || strings.TrimSpace(message.RequestID) == "" {
				return ErrV2DeveloperChainDeltaMalformed
			}
			if message.Timestamp <= 0 {
				return ErrV2DeveloperChainDeltaMalformed
			}
		}
	}

	return nil
}

func (s *Server) validateAndStoreV2DeveloperChainDelta(
	ctx context.Context,
	requesterAddress string,
	expectedRequesterPubKey string,
	signatureChainID string,
	escrowID string,
	escrowSequence uint64,
	modelID string,
	requestPayloadHash string,
	developerChainDelta DeveloperChainDelta,
	resolveExecutorSignerPubKey v2ExecutorSignerPubKeyResolverFunc,
	allowChainAdvance func() bool,
	validateMissedInference func(ctx context.Context, message DeveloperChainMessage) error,
) (uint64, error) {
	expectedRequestID := buildV2RequestID(escrowID, escrowSequence)
	chainKey := buildV2DeveloperChainKey(requesterAddress, escrowID)
	return s.getV2DeveloperChainStore().validateAndAppend(
		ctx,
		chainKey,
		developerChainDelta,
		expectedRequestID,
		requestPayloadHash,
		escrowID,
		modelID,
		expectedRequesterPubKey,
		signatureChainID,
		resolveExecutorSignerPubKey,
		allowChainAdvance,
		validateMissedInference,
	)
}

func (s *Server) getV2DeveloperChainStore() *v2DeveloperChainStore {
	if s.v2DeveloperChainStore == nil {
		s.v2DeveloperChainStore = newV2DeveloperChainStore()
	}
	return s.v2DeveloperChainStore
}

func (store *v2DeveloperChainStore) validateAndAppend(
	ctx context.Context,
	chainKey string,
	developerChainDelta DeveloperChainDelta,
	expectedRequestID string,
	expectedRequestPayloadHash string,
	escrowID string,
	modelID string,
	expectedSignerPubKey string,
	signatureChainID string,
	resolveExecutorSignerPubKey v2ExecutorSignerPubKeyResolverFunc,
	allowChainAdvance func() bool,
	validateMissedInference func(ctx context.Context, message DeveloperChainMessage) error,
) (uint64, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()

	if store.chains == nil {
		store.chains = make(map[string]v2DeveloperChainState)
	}

	state := store.chains[chainKey]
	if state.blocksBySeq == nil {
		state.blocksBySeq = make(map[uint64]DeveloperChainBlock)
	}
	if state.stateByBlockSeq == nil {
		state.stateByBlockSeq = make(map[uint64]v2DeterministicChainState)
	}
	baseDeterministicState, err := resolveV2DeterministicStateAtBaseSequence(state, developerChainDelta.BaseBlockSequence)
	if err != nil {
		return 0, err
	}
	startRequestIDsInStoredBlocks := make(map[string]struct{})
	startRequestSignaturesInStoredBlocks := make(map[string]string)
	for sequence := uint64(1); sequence <= state.latestBlockSequence; sequence++ {
		block, ok := state.blocksBySeq[sequence]
		if !ok {
			continue
		}
		for _, message := range block.Messages {
			if message.Type == v2ChainMessageTypeStartInference {
				startRequestIDsInStoredBlocks[message.RequestID] = struct{}{}
				startRequestSignaturesInStoredBlocks[message.RequestID] = block.Signature
			}
		}
	}
	if err := validateV2DeveloperChainDeltaForCurrentRequest(
		ctx,
		developerChainDelta,
		state.latestBlockSequence,
		expectedRequestID,
		expectedRequestPayloadHash,
		escrowID,
		modelID,
		expectedSignerPubKey,
		signatureChainID,
		startRequestIDsInStoredBlocks,
		startRequestSignaturesInStoredBlocks,
		baseDeterministicState,
		func(sequence uint64) (DeveloperChainBlock, bool) {
			block, ok := state.blocksBySeq[sequence]
			return block, ok
		},
		resolveExecutorSignerPubKey,
		validateMissedInference,
	); err != nil {
		return 0, err
	}

	chainAdvanced := false
	overlapBlockCount := 0
	for _, block := range developerChainDelta.Blocks {
		if block.BlockSequence > state.latestBlockSequence {
			chainAdvanced = true
		} else {
			overlapBlockCount++
		}
	}
	if overlapBlockCount > 0 {
		logging.Debug("V2 chain overlap accepted", types.Inferences,
			"chain_key", chainKey,
			"base_block_sequence", developerChainDelta.BaseBlockSequence,
			"stored_latest_block_sequence", state.latestBlockSequence,
			"delta_latest_block_sequence", developerChainDelta.LatestBlockSequence,
			"overlap_blocks", overlapBlockCount,
		)
	}
	if chainAdvanced && allowChainAdvance != nil && !allowChainAdvance() {
		return 0, ErrV2EscrowSequenceNotIncreasing
	}

	for _, block := range developerChainDelta.Blocks {
		if block.BlockSequence <= state.latestBlockSequence {
			continue
		}

		copiedBlock := DeveloperChainBlock{
			BlockSequence: block.BlockSequence,
			EscrowID:      block.EscrowID,
			StateHash:     block.StateHash,
			Messages:      append([]DeveloperChainMessage(nil), block.Messages...),
			Signature:     block.Signature,
		}
		state.blocksBySeq[copiedBlock.BlockSequence] = copiedBlock
		if err := applyV2DeterministicStateBlock(&state.deterministicState, copiedBlock.Messages); err != nil {
			return 0, err
		}
		state.stateByBlockSeq[copiedBlock.BlockSequence] = cloneV2DeterministicChainState(state.deterministicState)
		chainAdvanced = true
	}
	if developerChainDelta.LatestBlockSequence > state.latestBlockSequence {
		state.latestBlockSequence = developerChainDelta.LatestBlockSequence
	}
	store.chains[chainKey] = state

	return state.latestBlockSequence, nil
}

func validateV2DeveloperChainDeltaForCurrentRequest(
	ctx context.Context,
	developerChainDelta DeveloperChainDelta,
	storedLatestBlockSequence uint64,
	expectedRequestID string,
	expectedRequestPayloadHash string,
	escrowID string,
	modelID string,
	expectedSignerPubKey string,
	signatureChainID string,
	startRequestIDsInStoredBlocks map[string]struct{},
	startRequestSignaturesInStoredBlocks map[string]string,
	initialDeterministicState v2DeterministicChainState,
	getStoredBlockBySequence func(sequence uint64) (DeveloperChainBlock, bool),
	resolveExecutorSignerPubKey v2ExecutorSignerPubKeyResolverFunc,
	validateMissedInference func(ctx context.Context, message DeveloperChainMessage) error,
) error {
	if developerChainDelta.BaseBlockSequence > storedLatestBlockSequence {
		return ErrV2DeveloperChainDeltaBaseBlockSequenceMismatch
	}
	var expectedSignerPubKeyBytes []byte
	if strings.TrimSpace(expectedSignerPubKey) != "" {
		decodedPubKey, err := base64.StdEncoding.DecodeString(expectedSignerPubKey)
		if err != nil {
			return ErrV2DeveloperBlockSignatureEncodingInvalid
		}
		expectedSignerPubKeyBytes = decodedPubKey
	}

	expectedBlockSequence := developerChainDelta.BaseBlockSequence + 1
	latestBlockStartRequestID := ""
	latestBlockStartRequestPayloadHash := ""
	knownStartedRequestIDs := make(map[string]struct{}, len(startRequestIDsInStoredBlocks))
	knownStartedRequestSignatures := make(map[string]string, len(startRequestSignaturesInStoredBlocks))
	for requestID := range startRequestIDsInStoredBlocks {
		knownStartedRequestIDs[requestID] = struct{}{}
	}
	for requestID, signature := range startRequestSignaturesInStoredBlocks {
		knownStartedRequestSignatures[requestID] = signature
	}
	workingDeterministicState := cloneV2DeterministicChainState(initialDeterministicState)

	for blockIndex, block := range developerChainDelta.Blocks {
		if block.BlockSequence != expectedBlockSequence {
			return ErrV2DeveloperChainDeltaContinuityInvalid
		}
		expectedBlockSequence++
		if err := validateV2DeveloperBlockSignature(
			block,
			signatureChainID,
			escrowID,
			expectedSignerPubKeyBytes,
		); err != nil {
			return err
		}

		startInferenceCount := 0
		startInferenceRequestID := ""
		startInferenceRequestPayloadHash := ""
		for _, message := range block.Messages {
			switch message.Type {
			case v2ChainMessageTypeStartInference:
				startInferenceCount++
				startInferenceRequestID = message.RequestID
				startInferenceRequestPayloadHash = message.RequestPayloadHash
				if message.ModelID != modelID {
					return ErrV2DeveloperChainDeltaMalformed
				}
				if strings.TrimSpace(message.RequestPayloadHash) == "" {
					return ErrV2DeveloperChainDeltaMalformed
				}
			case v2ChainMessageTypeFinishInference:
				if strings.TrimSpace(message.Status) == "" {
					return ErrV2DeveloperChainDeltaMalformed
				}
				if strings.TrimSpace(message.ResponsePayloadHash) == "" {
					return ErrV2DeveloperChainDeltaMalformed
				}
				if _, started := knownStartedRequestIDs[message.RequestID]; !started {
					return ErrV2DeveloperChainDeltaMalformed
				}
				if strings.TrimSpace(message.ExecutorAddress) == "" ||
					strings.TrimSpace(message.ExecutorSignerAddress) == "" ||
					strings.TrimSpace(message.ExecutorSignerPubKey) == "" ||
					strings.TrimSpace(message.ExecutorSignature) == "" {
					return ErrV2FinishInferenceProofRequired
				}
				expectedExecutorSignerPubKey := strings.TrimSpace(message.ExecutorSignerPubKey)
				if resolveExecutorSignerPubKey != nil {
					resolvedPubKey, err := resolveExecutorSignerPubKey(ctx, message.ExecutorAddress, message.ExecutorSignerAddress)
					if err != nil {
						return err
					}
					if strings.TrimSpace(resolvedPubKey) == "" {
						return ErrV2FinishInferenceSignerUnauthorized
					}
					if strings.TrimSpace(resolvedPubKey) != expectedExecutorSignerPubKey {
						return ErrV2FinishInferenceSignerUnauthorized
					}
				}
				requestBlockSignature := strings.TrimSpace(knownStartedRequestSignatures[message.RequestID])
				if requestBlockSignature == "" {
					return ErrV2DeveloperChainDeltaMalformed
				}
				if err := validateV2ExecutorProofSignature(
					expectedExecutorSignerPubKey,
					message.ExecutorSignature,
					requestBlockSignature,
					message.ResponsePayloadHash,
				); err != nil {
					return ErrV2FinishInferenceProofInvalid
				}
			case v2ChainMessageTypeMissedInference:
				if _, started := knownStartedRequestIDs[message.RequestID]; !started {
					return ErrV2DeveloperChainDeltaMalformed
				}
				if strings.TrimSpace(message.MissedInferenceEvidence) == "" {
					return ErrV2MissedInferenceEvidenceRequired
				}
				if validateMissedInference == nil {
					return ErrV2MissedInferenceEvidenceInvalid
				}
				if err := validateMissedInference(ctx, message); err != nil {
					return err
				}
			default:
				return ErrV2DeveloperChainDeltaMalformed
			}
		}

		if startInferenceCount != 1 {
			return ErrV2DeveloperChainDeltaMalformed
		}
		if err := applyV2DeterministicStateBlock(&workingDeterministicState, block.Messages); err != nil {
			return err
		}
		expectedStateHash := computeV2DeterministicStateHashHex(workingDeterministicState)
		if strings.TrimSpace(block.StateHash) != expectedStateHash {
			return ErrV2DeveloperBlockStateHashInvalid
		}
		if block.BlockSequence <= storedLatestBlockSequence {
			storedBlock, ok := getStoredBlockBySequence(block.BlockSequence)
			if !ok || !developerChainBlockEqual(storedBlock, block) {
				logging.Warn("V2 chain overlap mismatch", types.Inferences,
					"block_sequence", block.BlockSequence,
					"stored_latest_block_sequence", storedLatestBlockSequence,
				)
				if ok {
					return &v2OverlapConflictError{
						storedBlock:   storedBlock,
						incomingBlock: block,
					}
				}
				return ErrV2DeveloperChainDeltaOverlapMismatch
			}
		}
		if blockIndex == len(developerChainDelta.Blocks)-1 {
			latestBlockStartRequestID = startInferenceRequestID
			latestBlockStartRequestPayloadHash = startInferenceRequestPayloadHash
		}
		knownStartedRequestIDs[startInferenceRequestID] = struct{}{}
		knownStartedRequestSignatures[startInferenceRequestID] = block.Signature
	}

	lastBlockSequence := developerChainDelta.Blocks[len(developerChainDelta.Blocks)-1].BlockSequence
	if developerChainDelta.LatestBlockSequence != lastBlockSequence {
		return ErrV2DeveloperChainDeltaContinuityInvalid
	}
	if latestBlockStartRequestID != expectedRequestID {
		return ErrV2DeveloperChainDeltaCurrentRequestMismatch
	}
	if latestBlockStartRequestPayloadHash != expectedRequestPayloadHash {
		return ErrV2DeveloperChainDeltaRequestPayloadHashMismatch
	}

	return nil
}

func validateV2DeveloperBlockSignature(
	block DeveloperChainBlock,
	chainID string,
	escrowID string,
	expectedSignerPubKeyBytes []byte,
) error {
	if strings.TrimSpace(block.Signature) == "" {
		return ErrV2DeveloperBlockSignatureRequired
	}
	if strings.TrimSpace(block.StateHash) == "" {
		return ErrV2DeveloperBlockStateHashRequired
	}
	if strings.TrimSpace(block.EscrowID) == "" || block.EscrowID != escrowID {
		return ErrV2DeveloperChainDeltaMalformed
	}
	if len(expectedSignerPubKeyBytes) == 0 {
		return nil
	}
	signatureBytes, err := base64.StdEncoding.DecodeString(block.Signature)
	if err != nil {
		return ErrV2DeveloperBlockSignatureEncodingInvalid
	}

	blockMessagesHash := computeV2DeveloperBlockMessagesHash(block.Messages)
	preimage := buildV2DeveloperBlockSigningPreimage(chainID, block.EscrowID, block.BlockSequence, blockMessagesHash, block.StateHash)
	preimageHash := sha256.Sum256(preimage)
	signingPayload := []byte(fmt.Sprintf("%x", preimageHash[:]))

	pubKey := secp256k1.PubKey{Key: expectedSignerPubKeyBytes}
	if !pubKey.VerifySignature(signingPayload, signatureBytes) {
		return ErrV2DeveloperBlockSignatureInvalid
	}

	return nil
}

func computeV2DeveloperBlockMessagesHash(messages []DeveloperChainMessage) [32]byte {
	var aggregated bytes.Buffer
	for _, message := range messages {
		messageHash := sha256.Sum256(canonicalV2DeveloperChainMessageBytes(message))
		_, _ = aggregated.Write(messageHash[:])
	}
	return sha256.Sum256(aggregated.Bytes())
}

func canonicalV2DeveloperChainMessageBytes(message DeveloperChainMessage) []byte {
	var buffer bytes.Buffer
	writeV2LengthPrefixedString(&buffer, v2DeveloperBlockMessageDomain)
	writeV2LengthPrefixedString(&buffer, message.Type)
	writeV2LengthPrefixedString(&buffer, message.RequestID)
	writeV2LengthPrefixedString(&buffer, message.ModelID)
	writeV2LengthPrefixedString(&buffer, message.RequestPayloadHash)
	writeV2LengthPrefixedString(&buffer, message.ResponsePayloadHash)
	writeV2LengthPrefixedString(&buffer, message.ExecutorAddress)
	writeV2LengthPrefixedString(&buffer, message.ExecutorSignerAddress)
	writeV2LengthPrefixedString(&buffer, message.ExecutorSignerPubKey)
	writeV2LengthPrefixedString(&buffer, message.ExecutorSignature)
	writeV2Uint64(&buffer, message.InputTokenCount)
	writeV2Uint64(&buffer, message.OutputTokenCount)
	writeV2LengthPrefixedString(&buffer, message.MissedInferenceEvidence)
	writeV2LengthPrefixedString(&buffer, message.Status)
	writeV2Int64(&buffer, message.Timestamp)
	return buffer.Bytes()
}

func buildV2DeveloperBlockSigningPreimage(
	chainID string,
	escrowID string,
	blockSequence uint64,
	blockMessagesHash [32]byte,
	stateHash string,
) []byte {
	var buffer bytes.Buffer
	writeV2LengthPrefixedString(&buffer, v2DeveloperBlockSignDomain)
	writeV2LengthPrefixedString(&buffer, chainID)
	writeV2LengthPrefixedString(&buffer, escrowID)
	writeV2Uint64(&buffer, blockSequence)
	_, _ = buffer.Write(blockMessagesHash[:])
	writeV2LengthPrefixedString(&buffer, stateHash)
	return buffer.Bytes()
}

func writeV2LengthPrefixedString(buffer *bytes.Buffer, value string) {
	var lengthBytes [4]byte
	binary.BigEndian.PutUint32(lengthBytes[:], uint32(len(value)))
	_, _ = buffer.Write(lengthBytes[:])
	_, _ = buffer.WriteString(value)
}

func writeV2Uint64(buffer *bytes.Buffer, value uint64) {
	var valueBytes [8]byte
	binary.BigEndian.PutUint64(valueBytes[:], value)
	_, _ = buffer.Write(valueBytes[:])
}

func writeV2Int64(buffer *bytes.Buffer, value int64) {
	writeV2Uint64(buffer, uint64(value))
}

func computeV2RequestPayloadHash(openAIRequestBody []byte) (string, error) {
	return utils.GenerateSHA256HashBytes(openAIRequestBody), nil
}

func buildV2RequestID(escrowID string, escrowSequence uint64) string {
	return fmt.Sprintf("%s:%d", escrowID, escrowSequence)
}

func parseV2RequestSequence(requestID string, escrowID string) (uint64, error) {
	expectedPrefix := escrowID + ":"
	if !strings.HasPrefix(requestID, expectedPrefix) {
		return 0, fmt.Errorf("request_id does not match escrow")
	}
	sequencePart := strings.TrimPrefix(requestID, expectedPrefix)
	if sequencePart == "" {
		return 0, fmt.Errorf("request sequence is required")
	}
	sequence, err := strconv.ParseUint(sequencePart, 10, 64)
	if err != nil || sequence == 0 {
		return 0, fmt.Errorf("request sequence is invalid")
	}
	return sequence, nil
}

func buildV2DeveloperChainKey(requesterAddress string, escrowID string) string {
	return fmt.Sprintf("%s|%s", requesterAddress, escrowID)
}

func developerChainBlockEqual(left DeveloperChainBlock, right DeveloperChainBlock) bool {
	if left.BlockSequence != right.BlockSequence {
		return false
	}
	if left.EscrowID != right.EscrowID || left.StateHash != right.StateHash || left.Signature != right.Signature {
		return false
	}
	if len(left.Messages) != len(right.Messages) {
		return false
	}
	for idx := range left.Messages {
		if left.Messages[idx] != right.Messages[idx] {
			return false
		}
	}
	return true
}

type v2OverlapConflictError struct {
	storedBlock   DeveloperChainBlock
	incomingBlock DeveloperChainBlock
}

func (e *v2OverlapConflictError) Error() string {
	return ErrV2DeveloperChainDeltaOverlapMismatch.Error()
}

func (e *v2OverlapConflictError) Unwrap() error {
	return ErrV2DeveloperChainDeltaOverlapMismatch
}

func parseFlexibleUint64(raw json.RawMessage) (uint64, error) {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		return 0, fmt.Errorf("value is required")
	}
	if strings.HasPrefix(value, "\"") {
		parsedString, err := strconv.Unquote(value)
		if err != nil {
			return 0, fmt.Errorf("invalid quoted integer")
		}
		value = strings.TrimSpace(parsedString)
	}

	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("must be an unsigned integer")
	}
	return parsed, nil
}

func parseFlexibleInt64(raw json.RawMessage) (int64, error) {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		return 0, fmt.Errorf("value is required")
	}
	if strings.HasPrefix(value, "\"") {
		parsedString, err := strconv.Unquote(value)
		if err != nil {
			return 0, fmt.Errorf("invalid quoted integer")
		}
		value = strings.TrimSpace(parsedString)
	}

	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("must be an integer")
	}
	return parsed, nil
}

func parseFlexibleOptionalUint64(raw json.RawMessage) (uint64, error) {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		return 0, nil
	}
	return parseFlexibleUint64(raw)
}

func applyV2DeterministicStateBlock(state *v2DeterministicChainState, messages []DeveloperChainMessage) error {
	if state.executorStats == nil {
		state.executorStats = make(map[string]v2ExecutorDeterministicStats)
	}
	for _, message := range messages {
		switch message.Type {
		case v2ChainMessageTypeStartInference:
			// StartInference does not mutate deterministic executor accounting.
		case v2ChainMessageTypeFinishInference:
			executorAddress := strings.TrimSpace(message.ExecutorAddress)
			if executorAddress == "" {
				return ErrV2DeveloperChainDeltaMalformed
			}
			stats := state.executorStats[executorAddress]
			stats.processedInferences++
			stats.inputTokenTotal += message.InputTokenCount
			stats.outputTokenTotal += message.OutputTokenCount
			state.executorStats[executorAddress] = stats
		case v2ChainMessageTypeMissedInference:
			intendedExecutorAddress, err := extractV2MissedInferenceIntendedExecutor(message.MissedInferenceEvidence)
			if err != nil {
				return err
			}
			stats := state.executorStats[intendedExecutorAddress]
			stats.missedInferences++
			state.executorStats[intendedExecutorAddress] = stats
		default:
			return ErrV2DeveloperChainDeltaMalformed
		}
	}
	return nil
}

func extractV2MissedInferenceIntendedExecutor(missedInferenceEvidence string) (string, error) {
	evidence := v2MissedInferenceEvidence{}
	if err := json.Unmarshal([]byte(missedInferenceEvidence), &evidence); err != nil {
		return "", ErrV2MissedInferenceEvidenceMalformed
	}
	if len(evidence.RelayErrors) == 0 {
		return "", ErrV2MissedInferenceEvidenceMalformed
	}
	intendedExecutorAddress := strings.TrimSpace(evidence.RelayErrors[0].IntendedExecutorAddress)
	if intendedExecutorAddress == "" {
		return "", ErrV2MissedInferenceEvidenceMalformed
	}
	return intendedExecutorAddress, nil
}

func cloneV2DeterministicChainState(state v2DeterministicChainState) v2DeterministicChainState {
	cloned := v2DeterministicChainState{
		executorStats: make(map[string]v2ExecutorDeterministicStats, len(state.executorStats)),
	}
	for executorAddress, stats := range state.executorStats {
		cloned.executorStats[executorAddress] = stats
	}
	return cloned
}

func resolveV2DeterministicStateAtBaseSequence(state v2DeveloperChainState, baseBlockSequence uint64) (v2DeterministicChainState, error) {
	if baseBlockSequence == 0 {
		return v2DeterministicChainState{}, nil
	}
	if snapshot, ok := state.stateByBlockSeq[baseBlockSequence]; ok {
		return cloneV2DeterministicChainState(snapshot), nil
	}
	rebuiltState := v2DeterministicChainState{}
	for sequence := uint64(1); sequence <= baseBlockSequence; sequence++ {
		block, ok := state.blocksBySeq[sequence]
		if !ok {
			return v2DeterministicChainState{}, ErrV2DeveloperChainDeltaBaseBlockSequenceMismatch
		}
		if err := applyV2DeterministicStateBlock(&rebuiltState, block.Messages); err != nil {
			return v2DeterministicChainState{}, err
		}
	}
	return rebuiltState, nil
}

func computeV2DeterministicStateHashHex(state v2DeterministicChainState) string {
	var buffer bytes.Buffer
	writeV2LengthPrefixedString(&buffer, v2DeveloperStateHashDomain)
	executorAddresses := make([]string, 0, len(state.executorStats))
	for executorAddress := range state.executorStats {
		executorAddresses = append(executorAddresses, executorAddress)
	}
	sort.Strings(executorAddresses)
	writeV2Uint64(&buffer, uint64(len(executorAddresses)))
	for _, executorAddress := range executorAddresses {
		stats := state.executorStats[executorAddress]
		writeV2LengthPrefixedString(&buffer, executorAddress)
		writeV2Uint64(&buffer, stats.processedInferences)
		writeV2Uint64(&buffer, stats.inputTokenTotal)
		writeV2Uint64(&buffer, stats.outputTokenTotal)
		writeV2Uint64(&buffer, stats.missedInferences)
	}
	stateHash := sha256.Sum256(buffer.Bytes())
	return fmt.Sprintf("%x", stateHash[:])
}
