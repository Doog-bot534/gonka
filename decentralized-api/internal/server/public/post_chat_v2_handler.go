package public

import (
	"bytes"
	"context"
	"decentralized-api/broker"
	"decentralized-api/logging"
	"decentralized-api/utils"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
)

type v2CompletionProxyFunc func(ctx context.Context, requestBody []byte, model string, requestHeaders http.Header) (*http.Response, error)
type v2RelayProxyFunc func(ctx context.Context, requestBody []byte, model string, requestHeaders http.Header, intendedExecutorAddress string, requestID string) (*http.Response, error)
type v2ExecutorSignerPubKeyResolverFunc func(ctx context.Context, executorAddress string, executorSignerAddress string) (string, error)

type v2ContextKey string

const v2EpochIDContextKey v2ContextKey = "v2_epoch_id"

func (s *Server) postChatV2(ctx echo.Context) error {
	logging.Debug("PostChatV2. Received request", types.Inferences, "path", ctx.Request().URL.Path)

	parsedRequest, err := readV2Request(ctx.Request(), ctx.Response().Writer)
	if err != nil {
		return err
	}
	if parsedRequest.openAIRequest.Model == "" {
		logging.Warn("V2 request without model", types.Server, "path", ctx.Request().URL.Path)
		return ErrNoModelSpecified
	}

	requesterAddress := ctx.Request().Header.Get(utils.XRequesterAddressHeader)
	if requesterAddress == "" {
		return ErrV2RequesterRequired
	}

	escrowID := ctx.Request().Header.Get(utils.XEscrowIDHeader)
	if escrowID == "" {
		return ErrV2EscrowIDRequired
	}

	escrowSequenceRaw := ctx.Request().Header.Get(utils.XEscrowSequenceHeader)
	if escrowSequenceRaw == "" {
		return ErrV2EscrowSequenceRequired
	}
	escrowSequence, err := strconv.ParseUint(escrowSequenceRaw, 10, 64)
	if err != nil || escrowSequence == 0 {
		return ErrV2EscrowSequenceInvalid
	}

	record, ok := s.configManager.GetEscrowAccessRecord(escrowID)
	if !ok || record.DeveloperAddress != requesterAddress {
		logging.Warn("V2 escrow access denied", types.Inferences,
			"escrow_id", escrowID,
			"requester_address", requesterAddress,
		)
		return ErrV2EscrowNotAuthorized
	}
	if record.ModelID != "" && record.ModelID != parsedRequest.openAIRequest.Model {
		logging.Warn("V2 escrow model mismatch", types.Inferences,
			"escrow_id", escrowID,
			"requester_address", requesterAddress,
			"request_model", parsedRequest.openAIRequest.Model,
			"escrow_model", record.ModelID,
		)
		return ErrV2EscrowModelMismatch
	}
	epochID, err := resolveV2EscrowEpochID(record.EpochID, ctx.Request().Header)
	if err != nil {
		return err
	}
	requestCtx := context.WithValue(ctx.Request().Context(), v2EpochIDContextKey, epochID)
	participantSelector := s.v2ParticipantSelector
	if participantSelector == nil {
		participantSelector = s.resolveV2ResponsibleParticipants
	}

	responsibleParticipants, err := participantSelector(requestCtx, parsedRequest.openAIRequest.Model, escrowID, escrowSequence)
	if err != nil {
		return err
	}

	participantAddressResolver := s.v2ParticipantAddressResolver
	if participantAddressResolver == nil {
		participantAddressResolver = s.resolveV2LocalParticipantAddress
	}

	localParticipantAddress := participantAddressResolver()
	if localParticipantAddress == "" {
		logging.Error("Unable to resolve local participant address for v2 request", types.Inferences,
			"escrow_id", escrowID,
			"sequence", escrowSequence,
		)
		return ErrV2ParticipantAddressUnavailable
	}
	if !isResponsibleParticipant(responsibleParticipants, localParticipantAddress) {
		logging.Warn("V2 request reached non-responsible participant", types.Inferences,
			"escrow_id", escrowID,
			"sequence", escrowSequence,
			"participant_address", localParticipantAddress,
			"responsible_participants", responsibleParticipants,
		)
		return ErrV2ParticipantNotResponsible
	}
	intendedExecutorAddress, err := resolveV2IntendedExecutorAddress(responsibleParticipants)
	if err != nil {
		return err
	}
	requestID := buildV2RequestID(escrowID, escrowSequence)
	developerRequestBlockSignature := resolveV2DeveloperRequestBlockSignature(parsedRequest.developerChainDelta, requestID)
	if exceedsV2RelayHopLimit(ctx.Request().Header) {
		logging.Warn("V2 relay hop limit reached", types.Inferences,
			"escrow_id", escrowID,
			"sequence", escrowSequence,
			"request_id", requestID,
		)
		return ErrV2IntendedExecutorUnavailable
	}

	requestPayloadHash, err := computeV2RequestPayloadHash(parsedRequest.openAIRequestBody)
	if err != nil {
		logging.Warn("Unable to compute v2 request payload hash", types.Server, "error", err)
		return ErrV2OpenAIRequestInvalid
	}
	developerPubKey, err := s.resolveV2DeveloperPubKey(requestCtx, record.DeveloperAddress, record.DeveloperPubKey)
	if err != nil {
		return err
	}
	if strings.TrimSpace(record.DeveloperPubKey) == "" && strings.TrimSpace(developerPubKey) != "" {
		updatedRecord := record
		updatedRecord.DeveloperPubKey = developerPubKey
		s.configManager.UpsertEscrowAccessRecord(updatedRecord)
	}

	latestBlockSequence, err := s.validateAndStoreV2DeveloperChainDelta(
		requestCtx,
		requesterAddress,
		developerPubKey,
		s.resolveV2DeveloperBlockSignatureChainID(),
		escrowID,
		escrowSequence,
		parsedRequest.openAIRequest.Model,
		requestPayloadHash,
		parsedRequest.developerChainDelta,
		func(ctx context.Context, executorAddress string, executorSignerAddress string) (string, error) {
			resolver := s.v2ExecutorSignerPubKeyResolver
			if resolver == nil {
				resolver = s.resolveV2ExecutorSignerPubKey
			}
			return resolver(ctx, executorAddress, executorSignerAddress)
		},
		func() bool {
			return s.configManager.RecordEscrowSequenceIfIncreasing(escrowID, escrowSequence)
		},
	)
	if err != nil {
		return err
	}

	resp, err := s.getV2RequestDeduper().execute(
		requestID,
		requestPayloadHash,
		func() (*http.Response, error) {
			executionCtx := context.WithoutCancel(requestCtx)
			if localParticipantAddress == intendedExecutorAddress {
				proxy := s.v2CompletionProxy
				if proxy == nil {
					proxy = s.forwardV2CompletionRequest
				}
				return proxy(executionCtx, parsedRequest.openAIRequestBody, parsedRequest.openAIRequest.Model, ctx.Request().Header)
			}

			relayProxy := s.v2RelayProxy
			if relayProxy == nil {
				relayProxy = s.relayV2CompletionToIntended
			}
			return relayProxy(
				executionCtx,
				parsedRequest.envelopeBody,
				parsedRequest.openAIRequest.Model,
				ctx.Request().Header,
				intendedExecutorAddress,
				requestID,
			)
		},
	)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	ctx.Response().Header().Set(utils.XLatestBlockSequenceHeader, strconv.FormatUint(latestBlockSequence, 10))
	shouldGenerateExecutorProof := localParticipantAddress == intendedExecutorAddress
	s.proxyV2ResponseWithExecutorProof(resp, ctx.Response().Writer, developerRequestBlockSignature, shouldGenerateExecutorProof)
	return nil
}

func resolveV2IntendedExecutorAddress(responsibleParticipants []string) (string, error) {
	if len(responsibleParticipants) == 0 || responsibleParticipants[0] == "" {
		return "", ErrV2IntendedExecutorUnavailable
	}
	return responsibleParticipants[0], nil
}

func exceedsV2RelayHopLimit(requestHeaders http.Header) bool {
	relayHopRaw := strings.TrimSpace(requestHeaders.Get(utils.XV2RelayHopHeader))
	if relayHopRaw == "" {
		return false
	}
	relayHop, err := strconv.Atoi(relayHopRaw)
	if err != nil {
		return true
	}
	return relayHop >= 2
}

func (s *Server) relayV2CompletionToIntended(
	ctx context.Context,
	requestBody []byte,
	model string,
	requestHeaders http.Header,
	intendedExecutorAddress string,
	requestID string,
) (*http.Response, error) {
	epochID, ok := ctx.Value(v2EpochIDContextKey).(uint64)
	if !ok || epochID == 0 {
		return nil, ErrV2EscrowEpochUnavailable
	}
	intendedExecutorURL, err := s.resolveV2ParticipantInferenceURL(ctx, epochID, intendedExecutorAddress)
	if err != nil {
		logging.Warn("Unable to resolve intended executor URL for v2 relay", types.Inferences,
			"request_id", requestID,
			"intended_executor", intendedExecutorAddress,
			"error", err,
		)
		return nil, ErrV2IntendedExecutorUnavailable
	}
	relayURL, joinErr := url.JoinPath(intendedExecutorURL, "/v2/chat/completions")
	if joinErr != nil {
		logging.Warn("Unable to build intended executor relay URL for v2 request", types.Inferences,
			"request_id", requestID,
			"intended_executor", intendedExecutorAddress,
			"error", joinErr,
		)
		return nil, ErrV2IntendedExecutorUnavailable
	}

	relayRequest, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, relayURL, bytes.NewReader(requestBody))
	if reqErr != nil {
		return nil, ErrV2IntendedExecutorUnavailable
	}
	relayRequest.Header = cloneHeader(requestHeaders)
	relayRequest.Header.Set(utils.XV2IntendedExecutorHeader, intendedExecutorAddress)
	relayRequest.Header.Set(utils.XV2RequestIDHeader, requestID)
	relayRequest.Header.Set(utils.XV2RelayHopHeader, strconv.Itoa(parseV2RelayHop(requestHeaders)+1))

	relayResponse, relayErr := s.httpClient.Do(relayRequest)
	if relayErr != nil {
		logging.Warn("Unable to relay v2 request to intended executor", types.Inferences,
			"request_id", requestID,
			"intended_executor", intendedExecutorAddress,
			"error", relayErr,
		)
		return nil, ErrV2IntendedExecutorUnavailable
	}
	return relayResponse, nil
}

func parseV2RelayHop(requestHeaders http.Header) int {
	relayHopRaw := strings.TrimSpace(requestHeaders.Get(utils.XV2RelayHopHeader))
	if relayHopRaw == "" {
		return 0
	}
	relayHop, err := strconv.Atoi(relayHopRaw)
	if err != nil || relayHop < 0 {
		return 0
	}
	return relayHop
}

func (s *Server) resolveV2DeveloperPubKey(ctx context.Context, developerAddress string, cachedPubKey string) (string, error) {
	pubKey := strings.TrimSpace(cachedPubKey)
	if pubKey != "" {
		return pubKey, nil
	}
	if s.recorder == nil {
		return "", nil
	}
	queryClient := s.recorder.NewInferenceQueryClient()
	response, err := queryClient.InferenceParticipant(ctx, &types.QueryInferenceParticipantRequest{Address: developerAddress})
	if err != nil {
		logging.Warn("Unable to resolve requester pubkey for v2 block signature validation", types.Inferences,
			"developer_address", developerAddress,
			"error", err,
		)
		return "", ErrV2RequesterPubKeyUnavailable
	}
	pubKey = strings.TrimSpace(response.GetPubkey())
	if pubKey == "" {
		return "", ErrV2RequesterPubKeyUnavailable
	}
	return pubKey, nil
}

func (s *Server) resolveV2ExecutorSignerPubKey(ctx context.Context, executorAddress string, executorSignerAddress string) (string, error) {
	executorAddress = strings.TrimSpace(executorAddress)
	executorSignerAddress = strings.TrimSpace(executorSignerAddress)
	if executorAddress == "" || executorSignerAddress == "" {
		return "", ErrV2DeveloperChainDeltaMalformed
	}

	if s.authzCache != nil {
		pubKey, err := s.authzCache.GetPubKeyForSigner(ctx, executorAddress, executorSignerAddress, "/inference.inference.MsgFinishInference")
		if err != nil {
			return "", ErrV2ExecutorSignerPubKeyUnavailable
		}
		if strings.TrimSpace(pubKey) == "" {
			// Continue to cold-key fallback below when signer==executor.
			if executorAddress != executorSignerAddress {
				return "", ErrV2FinishInferenceSignerUnauthorized
			}
		} else {
			return strings.TrimSpace(pubKey), nil
		}
	}

	return "", ErrV2ExecutorSignerPubKeyUnavailable
}

func (s *Server) resolveV2DeveloperBlockSignatureChainID() string {
	if s.recorder == nil {
		return ""
	}
	return strings.TrimSpace(s.recorder.GetClientContext().ChainID)
}

func (s *Server) resolveV2ParticipantInferenceURL(ctx context.Context, epochID uint64, participantAddress string) (string, error) {
	if s.configManager == nil {
		return "", fmt.Errorf("config manager unavailable")
	}
	if s.epochGroupDataCache != nil {
		cachedURL, ok, err := s.epochGroupDataCache.GetActiveParticipantInferenceURL(
			ctx,
			epochID,
			participantAddress,
			s.configManager.GetChainNodeConfig().Url,
		)
		if err != nil {
			return "", err
		}
		if ok {
			return cachedURL, nil
		}
	}
	return "", fmt.Errorf("participant inference URL not found")
}

func parseV2EpochIDHeader(headers http.Header) (uint64, error) {
	epochIDRaw := strings.TrimSpace(headers.Get(utils.XEpochIdHeader))
	if epochIDRaw == "" {
		return 0, ErrV2EpochIDRequired
	}
	epochID, err := strconv.ParseUint(epochIDRaw, 10, 64)
	if err != nil || epochID == 0 {
		return 0, ErrV2EpochIDInvalid
	}
	return epochID, nil
}

func resolveV2EscrowEpochID(escrowEpochID uint64, headers http.Header) (uint64, error) {
	if escrowEpochID > 0 {
		headerEpochIDRaw := strings.TrimSpace(headers.Get(utils.XEpochIdHeader))
		if headerEpochIDRaw == "" {
			return escrowEpochID, nil
		}
		headerEpochID, err := strconv.ParseUint(headerEpochIDRaw, 10, 64)
		if err != nil || headerEpochID == 0 {
			return 0, ErrV2EpochIDInvalid
		}
		if headerEpochID != escrowEpochID {
			return 0, ErrV2EscrowEpochMismatch
		}
		return escrowEpochID, nil
	}
	headerEpochID, err := parseV2EpochIDHeader(headers)
	if err != nil {
		return 0, ErrV2EscrowEpochUnavailable
	}
	return headerEpochID, nil
}

func (s *Server) forwardV2CompletionRequest(ctx context.Context, requestBody []byte, model string, requestHeaders http.Header) (*http.Response, error) {
	resp, err := broker.DoWithLockedNodeHTTPRetry(
		s.nodeBroker,
		model,
		nil,
		3,
		func(node *broker.Node) (*http.Response, *broker.ActionError) {
			completionsURL, joinErr := url.JoinPath(
				node.InferenceUrlWithVersion(s.configManager.GetCurrentNodeVersion()),
				"/v1/chat/completions",
			)
			if joinErr != nil {
				return nil, broker.NewApplicationActionError(joinErr)
			}

			req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, completionsURL, bytes.NewReader(requestBody))
			if reqErr != nil {
				return nil, broker.NewApplicationActionError(reqErr)
			}

			contentType := requestHeaders.Get("Content-Type")
			if contentType != "" {
				req.Header.Set("Content-Type", contentType)
			}

			if accept := requestHeaders.Get("Accept"); accept != "" {
				req.Header.Set("Accept", accept)
			}

			inferenceResp, postErr := s.httpClient.Do(req)
			if postErr != nil {
				return nil, broker.NewTransportActionError(postErr)
			}
			return inferenceResp, nil
		},
	)
	if err == nil {
		return resp, nil
	}

	if errors.Is(err, broker.ErrNoNodesAvailable) {
		return nil, echo.NewHTTPError(http.StatusServiceUnavailable, "No inference nodes available")
	}
	return nil, echo.NewHTTPError(http.StatusBadGateway, fmt.Sprintf("Failed to process v2 completion request: %v", err))
}
