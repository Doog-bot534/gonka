package public

import (
	"bytes"
	"context"
	"crypto/sha256"
	"decentralized-api/apiconfig"
	"decentralized-api/cosmosclient"
	"decentralized-api/utils"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdkclient "github.com/cosmos/cosmos-sdk/client"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

const (
	v2TestDeveloperBlockSignerChainID = ""
)

var v2TestDeveloperBlockSigner = newTestKey()
var v2TestDeveloperBlockSignerPubKey = v2TestDeveloperBlockSigner.GetPubKeyBase64()
var v2TestExecutorProofSigner = newTestKey()
var v2TestExecutorProofSignerAddress = "gonka1execsigner"
var v2TestExecutorAddress = "gonka1exec"

func TestReadV2Request_InvalidJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v2/chat/completions", bytes.NewBufferString("{"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	parsed, err := readV2Request(req, rec)
	require.Error(t, err)
	require.Nil(t, parsed)
}

func TestReadV2Request_Success(t *testing.T) {
	requestBody := mustMarshalV2Envelope(
		t,
		defaultV2OpenAIRequest("Qwen/Qwen2.5-7B-Instruct"),
		validV2ChainDelta("Qwen/Qwen2.5-7B-Instruct", "escrow-1", "escrow-1:1", 0, 1),
	)
	req := httptest.NewRequest(http.MethodPost, "/v2/chat/completions", bytes.NewBufferString(requestBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	parsed, err := readV2Request(req, rec)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.Equal(t, "Qwen/Qwen2.5-7B-Instruct", parsed.openAIRequest.Model)
	require.Equal(t, uint64(1), parsed.developerChainDelta.LatestBlockSequence)
	require.Contains(t, string(parsed.openAIRequestBody), `"model":"Qwen/Qwen2.5-7B-Instruct"`)
}

func TestReadV2Request_SupportsQuotedNumericChainFields(t *testing.T) {
	requestBody := `{
		"openai_request":{
			"model":"model-1",
			"messages":[{"content":"hi"}]
		},
		"developer_chain_delta":{
			"base_block_sequence":"0",
			"blocks":[
				{
					"block_sequence":"1",
					"escrow_id":"escrow-1",
					"state_hash":"state-hash-1",
					"messages":[
						{
							"type":"StartInference",
							"request_id":"escrow-1:1",
							"model_id":"model-1",
							"timestamp":"1710000000"
						}
					]
				}
			],
			"latest_block_sequence":"1"
		}
	}`

	req := httptest.NewRequest(http.MethodPost, "/v2/chat/completions", bytes.NewBufferString(requestBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	parsed, err := readV2Request(req, rec)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.Equal(t, uint64(0), parsed.developerChainDelta.BaseBlockSequence)
	require.Equal(t, uint64(1), parsed.developerChainDelta.Blocks[0].BlockSequence)
	require.Equal(t, int64(1710000000), parsed.developerChainDelta.Blocks[0].Messages[0].Timestamp)
}

func TestPostChatV2_RequiresModel(t *testing.T) {
	e := echo.New()
	body := mustMarshalV2Envelope(
		t,
		defaultV2OpenAIRequest(""),
		validV2ChainDelta("", "escrow-1", "escrow-1:1", 0, 1),
	)
	ctx, _ := newV2RequestContext(e, body, "", "", "")

	s := &Server{
		v2CompletionProxy: func(_ context.Context, _ []byte, _ string, _ http.Header) (*http.Response, error) {
			t.Fatal("v2CompletionProxy should not be called for model-less requests")
			return nil, nil
		},
	}

	err := s.postChatV2(ctx)
	require.Error(t, err)

	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusBadRequest, httpErr.Code)
}

func TestPostChatV2_ProxiesResponse(t *testing.T) {
	e := echo.New()
	body := mustMarshalV2Envelope(
		t,
		defaultV2OpenAIRequest("model-1"),
		validV2ChainDelta("model-1", "escrow-1", "escrow-1:1", 0, 1),
	)
	ctx, rec := newV2RequestContext(e, body, "gonka1dev", "escrow-1", "1")

	cfg := &apiconfig.ConfigManager{}
	cfg.UpsertEscrowAccessRecord(apiconfig.EscrowAccessRecord{
		EscrowID:         "escrow-1",
		DeveloperAddress: "gonka1dev",
		DeveloperPubKey:  v2TestDeveloperBlockSignerPubKey,
		ModelID:          "model-1",
	})

	s := &Server{
		configManager: cfg,
		v2ParticipantSelector: func(_ context.Context, model string, escrowID string, sequence uint64) ([]string, error) {
			require.Equal(t, "model-1", model)
			require.Equal(t, "escrow-1", escrowID)
			require.Equal(t, uint64(1), sequence)
			return []string{"gonka1participant"}, nil
		},
		v2ParticipantAddressResolver: func() string {
			return "gonka1participant"
		},
		v2CompletionProxy: func(_ context.Context, requestBody []byte, model string, requestHeaders http.Header) (*http.Response, error) {
			require.Equal(t, "model-1", model)
			require.Contains(t, string(requestBody), `"model":"model-1"`)
			require.Equal(t, "application/json", requestHeaders.Get("Content-Type"))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
				},
				Body: io.NopCloser(bytes.NewBufferString(`{"id":"resp-1","object":"chat.completion"}`)),
			}, nil
		},
	}

	err := s.postChatV2(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"id":"resp-1"`)
	require.Equal(t, "1", rec.Header().Get(utils.XLatestBlockSequenceHeader))
}

func TestPostChatV2_SetsExecutorProofHeadersForJSONResponse(t *testing.T) {
	e := echo.New()
	developerChainDelta := validV2ChainDelta("model-1", "escrow-1", "escrow-1:1", 0, 1)
	signMissingV2DeveloperBlockSignaturesForTests(t, &developerChainDelta)
	body := mustMarshalV2EnvelopeWithoutAutoSign(
		t,
		defaultV2OpenAIRequest("model-1"),
		developerChainDelta,
	)
	ctx, rec := newV2RequestContext(e, body, "gonka1dev", "escrow-1", "1")

	cfg := &apiconfig.ConfigManager{}
	cfg.UpsertEscrowAccessRecord(apiconfig.EscrowAccessRecord{
		EscrowID:         "escrow-1",
		DeveloperAddress: "gonka1dev",
		DeveloperPubKey:  v2TestDeveloperBlockSignerPubKey,
		ModelID:          "model-1",
	})

	responseBody := []byte(`{"id":"resp-1","object":"chat.completion"}`)
	upstreamSignature := "upstream-signature-base64"
	upstreamPubKey := "upstream-pubkey-base64"
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.On("GetClientContext").Return(sdkclient.Context{})

	s := &Server{
		configManager: cfg,
		recorder:      mockRecorder,
		v2ParticipantSelector: func(_ context.Context, _ string, _ string, _ uint64) ([]string, error) {
			return []string{"gonka1participant"}, nil
		},
		v2ParticipantAddressResolver: func() string {
			return "gonka1participant"
		},
		v2CompletionProxy: func(_ context.Context, _ []byte, _ string, _ http.Header) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type":                       []string{"application/json"},
					utils.XV2ExecutorAddressHeader:       []string{"gonka1exec-upstream"},
					utils.XV2ExecutorSignerAddressHeader: []string{"gonka1execwarm-upstream"},
					utils.XV2ExecutorSignerPubKeyHeader:  []string{upstreamPubKey},
					utils.XV2ExecutorSignatureHeader:     []string{upstreamSignature},
				},
				Body: io.NopCloser(bytes.NewReader(responseBody)),
			}, nil
		},
	}

	err := s.postChatV2(ctx)
	require.NoError(t, err)
	require.Equal(t, "gonka1exec-upstream", rec.Header().Get(utils.XV2ExecutorAddressHeader))
	require.Equal(t, "gonka1execwarm-upstream", rec.Header().Get(utils.XV2ExecutorSignerAddressHeader))
	require.Equal(t, upstreamPubKey, rec.Header().Get(utils.XV2ExecutorSignerPubKeyHeader))
	require.Equal(t, upstreamSignature, rec.Header().Get(utils.XV2ExecutorSignatureHeader))
	mockRecorder.AssertExpectations(t)
}

func TestPostChatV2_StreamingResponseIncludesExecutorProofTerminalEvent(t *testing.T) {
	e := echo.New()
	developerChainDelta := validV2ChainDelta("model-1", "escrow-1", "escrow-1:1", 0, 1)
	signMissingV2DeveloperBlockSignaturesForTests(t, &developerChainDelta)
	body := mustMarshalV2EnvelopeWithoutAutoSign(
		t,
		defaultV2OpenAIRequest("model-1"),
		developerChainDelta,
	)
	ctx, rec := newV2RequestContext(e, body, "gonka1dev", "escrow-1", "1")

	cfg := &apiconfig.ConfigManager{}
	cfg.UpsertEscrowAccessRecord(apiconfig.EscrowAccessRecord{
		EscrowID:         "escrow-1",
		DeveloperAddress: "gonka1dev",
		DeveloperPubKey:  v2TestDeveloperBlockSignerPubKey,
		ModelID:          "model-1",
	})

	streamBody := "data: {\"id\":\"chunk-1\"}\n\nevent: v2_executor_proof\ndata: {\"executor_address\":\"gonka1exec-upstream\",\"executor_signer_address\":\"gonka1execwarm-upstream\",\"executor_signer_pubkey\":\"stream-pubkey-base64\",\"executor_signature\":\"stream-proof-signature-base64\"}\n\ndata: [DONE]\n\n"
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.On("GetClientContext").Return(sdkclient.Context{})

	s := &Server{
		configManager: cfg,
		recorder:      mockRecorder,
		v2ParticipantSelector: func(_ context.Context, _ string, _ string, _ uint64) ([]string, error) {
			return []string{"gonka1participant"}, nil
		},
		v2ParticipantAddressResolver: func() string {
			return "gonka1participant"
		},
		v2CompletionProxy: func(_ context.Context, _ []byte, _ string, _ http.Header) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"text/event-stream"},
				},
				Body: io.NopCloser(strings.NewReader(streamBody)),
			}, nil
		},
	}

	err := s.postChatV2(ctx)
	require.NoError(t, err)
	require.Contains(t, rec.Body.String(), "event: v2_executor_proof")
	require.Contains(t, rec.Body.String(), "\"executor_address\":\"gonka1exec-upstream\"")
	require.Contains(t, rec.Body.String(), "\"executor_signer_address\":\"gonka1execwarm-upstream\"")
	require.Contains(t, rec.Body.String(), "\"executor_signature\":\"stream-proof-signature-base64\"")
	require.Less(t, strings.Index(rec.Body.String(), "event: v2_executor_proof"), strings.Index(rec.Body.String(), "data: [DONE]"))
	mockRecorder.AssertExpectations(t)
}

func TestPostChatV2_RequiresRequesterAddressHeader(t *testing.T) {
	e := echo.New()
	body := mustMarshalV2Envelope(
		t,
		defaultV2OpenAIRequest("model-1"),
		validV2ChainDelta("model-1", "escrow-1", "escrow-1:1", 0, 1),
	)
	ctx, _ := newV2RequestContext(e, body, "", "escrow-1", "1")

	s := &Server{}
	err := s.postChatV2(ctx)
	require.Error(t, err)
	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusBadRequest, httpErr.Code)
}

func TestPostChatV2_RequiresEscrowIDHeader(t *testing.T) {
	e := echo.New()
	body := mustMarshalV2Envelope(
		t,
		defaultV2OpenAIRequest("model-1"),
		validV2ChainDelta("model-1", "escrow-1", "escrow-1:1", 0, 1),
	)
	ctx, _ := newV2RequestContext(e, body, "gonka1dev", "", "1")

	s := &Server{}
	err := s.postChatV2(ctx)
	require.Error(t, err)
	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusBadRequest, httpErr.Code)
}

func TestPostChatV2_RequiresEscrowSequenceHeader(t *testing.T) {
	e := echo.New()
	body := mustMarshalV2Envelope(
		t,
		defaultV2OpenAIRequest("model-1"),
		validV2ChainDelta("model-1", "escrow-1", "escrow-1:1", 0, 1),
	)
	ctx, _ := newV2RequestContext(e, body, "gonka1dev", "escrow-1", "")

	s := &Server{}
	err := s.postChatV2(ctx)
	require.Error(t, err)
	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusBadRequest, httpErr.Code)
}

func TestPostChatV2_RejectsInvalidEscrowSequenceHeader(t *testing.T) {
	e := echo.New()
	body := mustMarshalV2Envelope(
		t,
		defaultV2OpenAIRequest("model-1"),
		validV2ChainDelta("model-1", "escrow-1", "escrow-1:1", 0, 1),
	)
	ctx, _ := newV2RequestContext(e, body, "gonka1dev", "escrow-1", "not-a-number")

	s := &Server{}
	err := s.postChatV2(ctx)
	require.Error(t, err)
	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusBadRequest, httpErr.Code)
}

func TestPostChatV2_RequiresEpochIDHeader(t *testing.T) {
	e := echo.New()
	cfg := &apiconfig.ConfigManager{}
	cfg.UpsertEscrowAccessRecord(apiconfig.EscrowAccessRecord{
		EscrowID:         "escrow-1",
		DeveloperAddress: "gonka1dev",
		DeveloperPubKey:  v2TestDeveloperBlockSignerPubKey,
		ModelID:          "model-1",
		EpochID:          7,
	})
	body := mustMarshalV2Envelope(
		t,
		defaultV2OpenAIRequest("model-1"),
		validV2ChainDelta("model-1", "escrow-1", "escrow-1:1", 0, 1),
	)
	ctx, _ := newV2RequestContext(e, body, "gonka1dev", "escrow-1", "1")
	ctx.Request().Header.Del(utils.XEpochIdHeader)

	s := &Server{
		configManager: cfg,
		v2ParticipantSelector: func(_ context.Context, _ string, _ string, _ uint64) ([]string, error) {
			return []string{"gonka1participant"}, nil
		},
		v2ParticipantAddressResolver: func() string {
			return "gonka1participant"
		},
		v2CompletionProxy: func(_ context.Context, _ []byte, _ string, _ http.Header) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
				},
				Body: io.NopCloser(bytes.NewBufferString(`{"id":"resp-1"}`)),
			}, nil
		},
	}
	err := s.postChatV2(ctx)
	require.NoError(t, err)
}

func TestPostChatV2_RejectsInvalidEpochIDHeader(t *testing.T) {
	e := echo.New()
	cfg := &apiconfig.ConfigManager{}
	cfg.UpsertEscrowAccessRecord(apiconfig.EscrowAccessRecord{
		EscrowID:         "escrow-1",
		DeveloperAddress: "gonka1dev",
		DeveloperPubKey:  v2TestDeveloperBlockSignerPubKey,
		ModelID:          "model-1",
		EpochID:          7,
	})
	body := mustMarshalV2Envelope(
		t,
		defaultV2OpenAIRequest("model-1"),
		validV2ChainDelta("model-1", "escrow-1", "escrow-1:1", 0, 1),
	)
	ctx, _ := newV2RequestContext(e, body, "gonka1dev", "escrow-1", "1")
	ctx.Request().Header.Set(utils.XEpochIdHeader, "not-a-number")

	s := &Server{
		configManager: cfg,
	}
	err := s.postChatV2(ctx)
	require.Error(t, err)
	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusBadRequest, httpErr.Code)
}

func TestPostChatV2_RejectsNonIncreasingEscrowSequence(t *testing.T) {
	e := echo.New()
	cfg := &apiconfig.ConfigManager{}
	cfg.UpsertEscrowAccessRecord(apiconfig.EscrowAccessRecord{
		EscrowID:         "escrow-1",
		DeveloperAddress: "gonka1dev",
		ModelID:          "model-1",
	})

	s := &Server{
		configManager: cfg,
		v2ParticipantSelector: func(_ context.Context, _ string, _ string, _ uint64) ([]string, error) {
			return []string{"gonka1participant"}, nil
		},
		v2ParticipantAddressResolver: func() string {
			return "gonka1participant"
		},
		v2CompletionProxy: func(_ context.Context, _ []byte, _ string, _ http.Header) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
				},
				Body: io.NopCloser(bytes.NewBufferString(`{"id":"resp-1"}`)),
			}, nil
		},
	}

	proxyCalls := 0
	s.v2CompletionProxy = func(_ context.Context, _ []byte, _ string, _ http.Header) (*http.Response, error) {
		proxyCalls++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"application/json"},
			},
			Body: io.NopCloser(bytes.NewBufferString(`{"id":"resp-1"}`)),
		}, nil
	}

	makeRequest := func(sequence string, body string) (*echo.HTTPError, int) {
		ctx, rec := newV2RequestContext(e, body, "gonka1dev", "escrow-1", sequence)
		err := s.postChatV2(ctx)
		if err == nil {
			return nil, rec.Code
		}
		httpErr, _ := err.(*echo.HTTPError)
		return httpErr, rec.Code
	}

	validBody := mustMarshalV2Envelope(
		t,
		defaultV2OpenAIRequest("model-1"),
		validV2ChainDelta("model-1", "escrow-1", "escrow-1:2", 0, 1),
	)

	firstErr, firstCode := makeRequest("2", validBody)
	require.Nil(t, firstErr)
	require.Equal(t, http.StatusOK, firstCode)

	secondErr, secondCode := makeRequest("2", validBody)
	require.Nil(t, secondErr)
	require.Equal(t, http.StatusOK, secondCode)

	advancingReplayBody := mustMarshalV2Envelope(
		t,
		defaultV2OpenAIRequest("model-1"),
		validV2ChainDelta("model-1", "escrow-1", "escrow-1:2", 1, 2),
	)
	thirdErr, _ := makeRequest("2", advancingReplayBody)
	require.NotNil(t, thirdErr)
	require.Equal(t, http.StatusConflict, thirdErr.Code)
	require.Equal(t, 1, proxyCalls)
}

func TestPostChatV2_RejectsUnauthorizedEscrowAccess(t *testing.T) {
	e := echo.New()
	body := mustMarshalV2Envelope(
		t,
		defaultV2OpenAIRequest("model-1"),
		validV2ChainDelta("model-1", "escrow-1", "escrow-1:1", 0, 1),
	)
	ctx, _ := newV2RequestContext(e, body, "gonka1dev2", "escrow-1", "1")

	cfg := &apiconfig.ConfigManager{}
	cfg.UpsertEscrowAccessRecord(apiconfig.EscrowAccessRecord{
		EscrowID:         "escrow-1",
		DeveloperAddress: "gonka1dev1",
		ModelID:          "model-1",
	})

	s := &Server{
		configManager: cfg,
	}

	err := s.postChatV2(ctx)
	require.Error(t, err)
	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusForbidden, httpErr.Code)
}

func TestPostChatV2_RejectsInvalidatedEscrow(t *testing.T) {
	e := echo.New()
	body := mustMarshalV2Envelope(
		t,
		defaultV2OpenAIRequest("model-1"),
		validV2ChainDelta("model-1", "escrow-1", "escrow-1:1", 0, 1),
	)
	ctx, _ := newV2RequestContext(e, body, "gonka1dev", "escrow-1", "1")

	cfg := &apiconfig.ConfigManager{}
	cfg.UpsertEscrowAccessRecord(apiconfig.EscrowAccessRecord{
		EscrowID:         "escrow-1",
		DeveloperAddress: "gonka1dev",
		ModelID:          "model-1",
		Invalidated:      true,
	})

	s := &Server{
		configManager: cfg,
	}

	err := s.postChatV2(ctx)
	require.Error(t, err)
	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusConflict, httpErr.Code)
}

func TestPostChatV2_RejectsModelMismatch(t *testing.T) {
	e := echo.New()
	body := mustMarshalV2Envelope(
		t,
		defaultV2OpenAIRequest("model-2"),
		validV2ChainDelta("model-2", "escrow-1", "escrow-1:1", 0, 1),
	)
	ctx, _ := newV2RequestContext(e, body, "gonka1dev", "escrow-1", "1")

	cfg := &apiconfig.ConfigManager{}
	cfg.UpsertEscrowAccessRecord(apiconfig.EscrowAccessRecord{
		EscrowID:         "escrow-1",
		DeveloperAddress: "gonka1dev",
		ModelID:          "model-1",
	})

	s := &Server{
		configManager: cfg,
	}

	err := s.postChatV2(ctx)
	require.Error(t, err)
	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusForbidden, httpErr.Code)
}

func TestPostChatV2_RejectsWhenParticipantNotResponsible(t *testing.T) {
	e := echo.New()
	body := mustMarshalV2Envelope(
		t,
		defaultV2OpenAIRequest("model-1"),
		validV2ChainDelta("model-1", "escrow-1", "escrow-1:2", 0, 1),
	)
	ctx, _ := newV2RequestContext(e, body, "gonka1dev", "escrow-1", "2")

	cfg := &apiconfig.ConfigManager{}
	cfg.UpsertEscrowAccessRecord(apiconfig.EscrowAccessRecord{
		EscrowID:         "escrow-1",
		DeveloperAddress: "gonka1dev",
		ModelID:          "model-1",
	})

	s := &Server{
		configManager: cfg,
		v2ParticipantSelector: func(_ context.Context, _ string, _ string, _ uint64) ([]string, error) {
			return []string{"gonka1participantA", "gonka1participantB"}, nil
		},
		v2ParticipantAddressResolver: func() string {
			return "gonka1participantC"
		},
		v2CompletionProxy: func(_ context.Context, _ []byte, _ string, _ http.Header) (*http.Response, error) {
			t.Fatal("v2CompletionProxy should not be called when local participant is not responsible")
			return nil, nil
		},
	}

	err := s.postChatV2(ctx)
	require.Error(t, err)
	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusForbidden, httpErr.Code)
}

func TestPostChatV2_RelaysWhenLocalParticipantIsResponsibleButNotIntended(t *testing.T) {
	e := echo.New()
	body := mustMarshalV2Envelope(
		t,
		defaultV2OpenAIRequest("model-1"),
		validV2ChainDelta("model-1", "escrow-1", "escrow-1:1", 0, 1),
	)
	ctx, rec := newV2RequestContext(e, body, "gonka1dev", "escrow-1", "1")

	cfg := &apiconfig.ConfigManager{}
	cfg.UpsertEscrowAccessRecord(apiconfig.EscrowAccessRecord{
		EscrowID:         "escrow-1",
		DeveloperAddress: "gonka1dev",
		ModelID:          "model-1",
	})

	relayCalls := 0
	s := &Server{
		configManager: cfg,
		v2ParticipantSelector: func(_ context.Context, _ string, _ string, _ uint64) ([]string, error) {
			return []string{"gonka1participant-intended", "gonka1participant-local"}, nil
		},
		v2ParticipantAddressResolver: func() string {
			return "gonka1participant-local"
		},
		v2RelayProxy: func(_ context.Context, _ []byte, _ string, _ http.Header, intendedExecutorAddress string, requestID string) (*http.Response, error) {
			relayCalls++
			require.Equal(t, "gonka1participant-intended", intendedExecutorAddress)
			require.Equal(t, "escrow-1:1", requestID)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
				},
				Body: io.NopCloser(bytes.NewBufferString(`{"id":"resp-relayed"}`)),
			}, nil
		},
		v2CompletionProxy: func(_ context.Context, _ []byte, _ string, _ http.Header) (*http.Response, error) {
			t.Fatal("v2CompletionProxy should not be called when relaying")
			return nil, nil
		},
	}

	err := s.postChatV2(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 1, relayCalls)
	require.Contains(t, rec.Body.String(), `"id":"resp-relayed"`)
}

func TestPostChatV2_RelayUnavailableReturnsDeterministicFailure(t *testing.T) {
	e := echo.New()
	body := mustMarshalV2Envelope(
		t,
		defaultV2OpenAIRequest("model-1"),
		validV2ChainDelta("model-1", "escrow-1", "escrow-1:1", 0, 1),
	)
	ctx, _ := newV2RequestContext(e, body, "gonka1dev", "escrow-1", "1")

	cfg := &apiconfig.ConfigManager{}
	cfg.UpsertEscrowAccessRecord(apiconfig.EscrowAccessRecord{
		EscrowID:         "escrow-1",
		DeveloperAddress: "gonka1dev",
		ModelID:          "model-1",
	})

	s := &Server{
		configManager: cfg,
		v2ParticipantSelector: func(_ context.Context, _ string, _ string, _ uint64) ([]string, error) {
			return []string{"gonka1participant-intended", "gonka1participant-local"}, nil
		},
		v2ParticipantAddressResolver: func() string {
			return "gonka1participant-local"
		},
		v2RelayProxy: func(_ context.Context, _ []byte, _ string, _ http.Header, _ string, _ string) (*http.Response, error) {
			return nil, ErrV2IntendedExecutorUnavailable
		},
		v2CompletionProxy: func(_ context.Context, _ []byte, _ string, _ http.Header) (*http.Response, error) {
			t.Fatal("v2CompletionProxy should not be called when relaying")
			return nil, nil
		},
	}

	err := s.postChatV2(ctx)
	require.Error(t, err)
	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusServiceUnavailable, httpErr.Code)
}

func TestPostChatV2_RejectsChainDeltaBaseMismatch(t *testing.T) {
	e := echo.New()
	cfg := &apiconfig.ConfigManager{}
	cfg.UpsertEscrowAccessRecord(apiconfig.EscrowAccessRecord{
		EscrowID:         "escrow-1",
		DeveloperAddress: "gonka1dev",
		ModelID:          "model-1",
	})

	proxyCalls := 0
	s := &Server{
		configManager: cfg,
		v2ParticipantSelector: func(_ context.Context, _ string, _ string, _ uint64) ([]string, error) {
			return []string{"gonka1participant"}, nil
		},
		v2ParticipantAddressResolver: func() string {
			return "gonka1participant"
		},
		v2CompletionProxy: func(_ context.Context, _ []byte, _ string, _ http.Header) (*http.Response, error) {
			proxyCalls++
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
				},
				Body: io.NopCloser(bytes.NewBufferString(`{"id":"resp-1"}`)),
			}, nil
		},
	}

	firstBody := mustMarshalV2Envelope(
		t,
		defaultV2OpenAIRequest("model-1"),
		validV2ChainDelta("model-1", "escrow-1", "escrow-1:1", 0, 1),
	)
	firstCtx, firstRec := newV2RequestContext(e, firstBody, "gonka1dev", "escrow-1", "1")
	require.NoError(t, s.postChatV2(firstCtx))
	require.Equal(t, http.StatusOK, firstRec.Code)

	secondBody := mustMarshalV2Envelope(
		t,
		defaultV2OpenAIRequest("model-1"),
		validV2ChainDelta("model-1", "escrow-1", "escrow-1:2", 0, 1),
	)
	secondCtx, _ := newV2RequestContext(e, secondBody, "gonka1dev", "escrow-1", "2")
	err := s.postChatV2(secondCtx)
	require.Error(t, err)

	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusConflict, httpErr.Code)
	require.Equal(t, 1, proxyCalls)

	validRetryBody := mustMarshalV2Envelope(
		t,
		defaultV2OpenAIRequest("model-1"),
		validV2ChainDelta("model-1", "escrow-1", "escrow-1:2", 1, 2),
	)
	retryCtx, retryRec := newV2RequestContext(e, validRetryBody, "gonka1dev", "escrow-1", "2")
	require.NoError(t, s.postChatV2(retryCtx))
	require.Equal(t, http.StatusOK, retryRec.Code)
	require.Equal(t, "2", retryRec.Header().Get(utils.XLatestBlockSequenceHeader))
	require.Equal(t, 2, proxyCalls)
}

func TestPostChatV2_AcceptsMatchingChainDeltaOverlap(t *testing.T) {
	e := echo.New()
	cfg := &apiconfig.ConfigManager{}
	cfg.UpsertEscrowAccessRecord(apiconfig.EscrowAccessRecord{
		EscrowID:         "escrow-1",
		DeveloperAddress: "gonka1dev",
		ModelID:          "model-1",
	})

	proxyCalls := 0
	s := &Server{
		configManager: cfg,
		v2ParticipantSelector: func(_ context.Context, _ string, _ string, _ uint64) ([]string, error) {
			return []string{"gonka1participant"}, nil
		},
		v2ParticipantAddressResolver: func() string {
			return "gonka1participant"
		},
		v2CompletionProxy: func(_ context.Context, _ []byte, _ string, _ http.Header) (*http.Response, error) {
			proxyCalls++
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
				},
				Body: io.NopCloser(bytes.NewBufferString(`{"id":"resp-1"}`)),
			}, nil
		},
	}

	firstBody := mustMarshalV2Envelope(
		t,
		defaultV2OpenAIRequest("model-1"),
		validV2ChainDelta("model-1", "escrow-1", "escrow-1:1", 0, 1),
	)
	firstCtx, firstRec := newV2RequestContext(e, firstBody, "gonka1dev", "escrow-1", "1")
	require.NoError(t, s.postChatV2(firstCtx))
	require.Equal(t, http.StatusOK, firstRec.Code)
	require.Equal(t, "1", firstRec.Header().Get(utils.XLatestBlockSequenceHeader))

	overlapDelta := DeveloperChainDelta{
		BaseBlockSequence: 0,
		Blocks: []DeveloperChainBlock{
			startInferenceBlock(1, "escrow-1:1", "escrow-1", "model-1"),
			startInferenceBlock(2, "escrow-1:2", "escrow-1", "model-1"),
		},
		LatestBlockSequence: 2,
	}
	secondBody := mustMarshalV2Envelope(t, defaultV2OpenAIRequest("model-1"), overlapDelta)
	secondCtx, secondRec := newV2RequestContext(e, secondBody, "gonka1dev", "escrow-1", "2")
	require.NoError(t, s.postChatV2(secondCtx))
	require.Equal(t, http.StatusOK, secondRec.Code)
	require.Equal(t, "2", secondRec.Header().Get(utils.XLatestBlockSequenceHeader))
	require.Equal(t, 2, proxyCalls)
}

func TestPostChatV2_RejectsMismatchedChainDeltaOverlap(t *testing.T) {
	e := echo.New()
	cfg := &apiconfig.ConfigManager{}
	cfg.UpsertEscrowAccessRecord(apiconfig.EscrowAccessRecord{
		EscrowID:         "escrow-1",
		DeveloperAddress: "gonka1dev",
		ModelID:          "model-1",
	})

	proxyCalls := 0
	s := &Server{
		configManager: cfg,
		v2ParticipantSelector: func(_ context.Context, _ string, _ string, _ uint64) ([]string, error) {
			return []string{"gonka1participant"}, nil
		},
		v2ParticipantAddressResolver: func() string {
			return "gonka1participant"
		},
		v2CompletionProxy: func(_ context.Context, _ []byte, _ string, _ http.Header) (*http.Response, error) {
			proxyCalls++
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
				},
				Body: io.NopCloser(bytes.NewBufferString(`{"id":"resp-1"}`)),
			}, nil
		},
	}

	firstBody := mustMarshalV2Envelope(
		t,
		defaultV2OpenAIRequest("model-1"),
		validV2ChainDelta("model-1", "escrow-1", "escrow-1:1", 0, 1),
	)
	firstCtx, firstRec := newV2RequestContext(e, firstBody, "gonka1dev", "escrow-1", "1")
	require.NoError(t, s.postChatV2(firstCtx))
	require.Equal(t, http.StatusOK, firstRec.Code)

	mismatchedOverlapDelta := DeveloperChainDelta{
		BaseBlockSequence: 0,
		Blocks: []DeveloperChainBlock{
			startInferenceBlock(1, "escrow-1:1-mismatch", "escrow-1", "model-1"),
			startInferenceBlock(2, "escrow-1:2", "escrow-1", "model-1"),
		},
		LatestBlockSequence: 2,
	}
	secondBody := mustMarshalV2Envelope(t, defaultV2OpenAIRequest("model-1"), mismatchedOverlapDelta)
	secondCtx, _ := newV2RequestContext(e, secondBody, "gonka1dev", "escrow-1", "2")
	err := s.postChatV2(secondCtx)
	require.Error(t, err)
	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusConflict, httpErr.Code)
	require.Equal(t, 1, proxyCalls)
}

func TestPostChatV2_RejectsChainDeltaCurrentRequestMismatch(t *testing.T) {
	e := echo.New()
	cfg := &apiconfig.ConfigManager{}
	cfg.UpsertEscrowAccessRecord(apiconfig.EscrowAccessRecord{
		EscrowID:         "escrow-1",
		DeveloperAddress: "gonka1dev",
		ModelID:          "model-1",
	})

	s := &Server{
		configManager: cfg,
		v2ParticipantSelector: func(_ context.Context, _ string, _ string, _ uint64) ([]string, error) {
			return []string{"gonka1participant"}, nil
		},
		v2ParticipantAddressResolver: func() string {
			return "gonka1participant"
		},
		v2CompletionProxy: func(_ context.Context, _ []byte, _ string, _ http.Header) (*http.Response, error) {
			t.Fatal("v2CompletionProxy should not be called when chain delta does not match current request")
			return nil, nil
		},
	}

	body := mustMarshalV2Envelope(
		t,
		defaultV2OpenAIRequest("model-1"),
		validV2ChainDelta("model-1", "escrow-1", "escrow-1:999", 0, 1),
	)
	ctx, _ := newV2RequestContext(e, body, "gonka1dev", "escrow-1", "1")
	err := s.postChatV2(ctx)
	require.Error(t, err)
	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusConflict, httpErr.Code)
}

func TestPostChatV2_RejectsMalformedChainDeltaBlock(t *testing.T) {
	e := echo.New()
	cfg := &apiconfig.ConfigManager{}
	cfg.UpsertEscrowAccessRecord(apiconfig.EscrowAccessRecord{
		EscrowID:         "escrow-1",
		DeveloperAddress: "gonka1dev",
		ModelID:          "model-1",
	})

	s := &Server{
		configManager: cfg,
		v2ParticipantSelector: func(_ context.Context, _ string, _ string, _ uint64) ([]string, error) {
			return []string{"gonka1participant"}, nil
		},
		v2ParticipantAddressResolver: func() string {
			return "gonka1participant"
		},
		v2CompletionProxy: func(_ context.Context, _ []byte, _ string, _ http.Header) (*http.Response, error) {
			t.Fatal("v2CompletionProxy should not be called for malformed chain delta")
			return nil, nil
		},
	}

	malformedDelta := DeveloperChainDelta{
		BaseBlockSequence: 0,
		Blocks: []DeveloperChainBlock{
			{
				BlockSequence: 1,
				EscrowID:      "escrow-1",
				Messages: []DeveloperChainMessage{
					{
						Type:      v2ChainMessageTypeFinishInference,
						RequestID: "escrow-1:1",
						Status:    "finished",
						Timestamp: 1710000000,
					},
				},
			},
		},
		LatestBlockSequence: 1,
	}
	body := mustMarshalV2Envelope(t, defaultV2OpenAIRequest("model-1"), malformedDelta)
	ctx, _ := newV2RequestContext(e, body, "gonka1dev", "escrow-1", "1")
	err := s.postChatV2(ctx)
	require.Error(t, err)
	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusBadRequest, httpErr.Code)
}

func TestPostChatV2_AcceptsFinishInferenceLinkedToPriorStart(t *testing.T) {
	e := echo.New()
	cfg := &apiconfig.ConfigManager{}
	cfg.UpsertEscrowAccessRecord(apiconfig.EscrowAccessRecord{
		EscrowID:         "escrow-1",
		DeveloperAddress: "gonka1dev",
		ModelID:          "model-1",
	})

	proxyCalls := 0
	s := &Server{
		configManager: cfg,
		v2ExecutorSignerPubKeyResolver: func(_ context.Context, executorAddress string, executorSignerAddress string) (string, error) {
			if executorAddress == v2TestExecutorAddress && executorSignerAddress == v2TestExecutorProofSignerAddress {
				return v2TestExecutorProofSigner.GetPubKeyBase64(), nil
			}
			return "", nil
		},
		v2ParticipantSelector: func(_ context.Context, _ string, _ string, _ uint64) ([]string, error) {
			return []string{"gonka1participant"}, nil
		},
		v2ParticipantAddressResolver: func() string {
			return "gonka1participant"
		},
		v2CompletionProxy: func(_ context.Context, _ []byte, _ string, _ http.Header) (*http.Response, error) {
			proxyCalls++
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
				},
				Body: io.NopCloser(bytes.NewBufferString(`{"id":"resp-1"}`)),
			}, nil
		},
	}

	firstRequest := defaultV2OpenAIRequest("model-1")
	firstDelta := validV2ChainDelta("model-1", "escrow-1", "escrow-1:1", 0, 1)
	firstBody := mustMarshalV2Envelope(
		t,
		firstRequest,
		firstDelta,
	)
	firstCtx, firstRec := newV2RequestContext(e, firstBody, "gonka1dev", "escrow-1", "1")
	require.NoError(t, s.postChatV2(firstCtx))
	require.Equal(t, http.StatusOK, firstRec.Code)

	secondRequest := defaultV2OpenAIRequest("model-1")
	secondDelta := DeveloperChainDelta{
		BaseBlockSequence: 1,
		Blocks: []DeveloperChainBlock{
			{
				BlockSequence: 2,
				EscrowID:      "escrow-1",
				Messages: []DeveloperChainMessage{
					{
						Type:                  v2ChainMessageTypeFinishInference,
						RequestID:             "escrow-1:1",
						Status:                "finished",
						ResponsePayloadHash:   "resp-hash-1",
						ExecutorAddress:       v2TestExecutorAddress,
						ExecutorSignerAddress: v2TestExecutorProofSignerAddress,
						ExecutorSignerPubKey:  v2TestExecutorProofSigner.GetPubKeyBase64(),
						ExecutorSignature:     mustSignV2ExecutorProofForTests(t, firstDelta.Blocks[0].Signature, "resp-hash-1"),
						Timestamp:             1710000001,
					},
					{
						Type:               v2ChainMessageTypeStartInference,
						RequestID:          "escrow-1:2",
						ModelID:            "model-1",
						RequestPayloadHash: mustComputeV2RequestPayloadHash(secondRequest),
						Timestamp:          1710000002,
					},
				},
			},
		},
		LatestBlockSequence: 2,
	}
	secondBody := mustMarshalV2Envelope(t, secondRequest, secondDelta)
	secondCtx, secondRec := newV2RequestContext(e, secondBody, "gonka1dev", "escrow-1", "2")
	require.NoError(t, s.postChatV2(secondCtx))
	require.Equal(t, http.StatusOK, secondRec.Code)
	require.Equal(t, 2, proxyCalls)
}

func TestPostChatV2_RejectsFinishInferenceMissingResponsePayloadHash(t *testing.T) {
	e := echo.New()
	cfg := &apiconfig.ConfigManager{}
	cfg.UpsertEscrowAccessRecord(apiconfig.EscrowAccessRecord{
		EscrowID:         "escrow-1",
		DeveloperAddress: "gonka1dev",
		ModelID:          "model-1",
	})

	proxyCalls := 0
	s := &Server{
		configManager: cfg,
		v2ParticipantSelector: func(_ context.Context, _ string, _ string, _ uint64) ([]string, error) {
			return []string{"gonka1participant"}, nil
		},
		v2ParticipantAddressResolver: func() string {
			return "gonka1participant"
		},
		v2CompletionProxy: func(_ context.Context, _ []byte, _ string, _ http.Header) (*http.Response, error) {
			proxyCalls++
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
				},
				Body: io.NopCloser(bytes.NewBufferString(`{"id":"resp-1"}`)),
			}, nil
		},
	}

	firstBody := mustMarshalV2Envelope(
		t,
		defaultV2OpenAIRequest("model-1"),
		validV2ChainDelta("model-1", "escrow-1", "escrow-1:1", 0, 1),
	)
	firstCtx, _ := newV2RequestContext(e, firstBody, "gonka1dev", "escrow-1", "1")
	require.NoError(t, s.postChatV2(firstCtx))

	secondRequest := defaultV2OpenAIRequest("model-1")
	secondDelta := DeveloperChainDelta{
		BaseBlockSequence: 1,
		Blocks: []DeveloperChainBlock{
			{
				BlockSequence: 2,
				EscrowID:      "escrow-1",
				Messages: []DeveloperChainMessage{
					{
						Type:      v2ChainMessageTypeFinishInference,
						RequestID: "escrow-1:1",
						Status:    "finished",
						Timestamp: 1710000001,
					},
					{
						Type:               v2ChainMessageTypeStartInference,
						RequestID:          "escrow-1:2",
						ModelID:            "model-1",
						RequestPayloadHash: mustComputeV2RequestPayloadHash(secondRequest),
						Timestamp:          1710000002,
					},
				},
			},
		},
		LatestBlockSequence: 2,
	}
	secondBody := mustMarshalV2Envelope(t, secondRequest, secondDelta)
	secondCtx, _ := newV2RequestContext(e, secondBody, "gonka1dev", "escrow-1", "2")
	err := s.postChatV2(secondCtx)
	require.Error(t, err)
	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusBadRequest, httpErr.Code)
	require.Equal(t, 1, proxyCalls)
}

func TestPostChatV2_RejectsFinishInferenceMissingExecutorProof(t *testing.T) {
	e := echo.New()
	cfg := &apiconfig.ConfigManager{}
	cfg.UpsertEscrowAccessRecord(apiconfig.EscrowAccessRecord{
		EscrowID:         "escrow-1",
		DeveloperAddress: "gonka1dev",
		ModelID:          "model-1",
	})

	s := &Server{
		configManager: cfg,
		v2ParticipantSelector: func(_ context.Context, _ string, _ string, _ uint64) ([]string, error) {
			return []string{"gonka1participant"}, nil
		},
		v2ParticipantAddressResolver: func() string {
			return "gonka1participant"
		},
		v2CompletionProxy: func(_ context.Context, _ []byte, _ string, _ http.Header) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewBufferString(`{"id":"resp-1"}`)),
			}, nil
		},
	}

	firstDelta := validV2ChainDelta("model-1", "escrow-1", "escrow-1:1", 0, 1)
	firstBody := mustMarshalV2Envelope(t, defaultV2OpenAIRequest("model-1"), firstDelta)
	firstCtx, _ := newV2RequestContext(e, firstBody, "gonka1dev", "escrow-1", "1")
	require.NoError(t, s.postChatV2(firstCtx))

	secondRequest := defaultV2OpenAIRequest("model-1")
	secondDelta := DeveloperChainDelta{
		BaseBlockSequence: 1,
		Blocks: []DeveloperChainBlock{
			{
				BlockSequence: 2,
				EscrowID:      "escrow-1",
				Messages: []DeveloperChainMessage{
					{
						Type:                v2ChainMessageTypeFinishInference,
						RequestID:           "escrow-1:1",
						Status:              "finished",
						ResponsePayloadHash: "resp-hash-1",
						Timestamp:           1710000001,
					},
					{
						Type:               v2ChainMessageTypeStartInference,
						RequestID:          "escrow-1:2",
						ModelID:            "model-1",
						RequestPayloadHash: mustComputeV2RequestPayloadHash(secondRequest),
						Timestamp:          1710000002,
					},
				},
			},
		},
		LatestBlockSequence: 2,
	}
	secondBody := mustMarshalV2Envelope(t, secondRequest, secondDelta)
	secondCtx, _ := newV2RequestContext(e, secondBody, "gonka1dev", "escrow-1", "2")
	err := s.postChatV2(secondCtx)
	require.Error(t, err)
	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusBadRequest, httpErr.Code)
}

func TestPostChatV2_RejectsFinishInferenceInvalidExecutorProof(t *testing.T) {
	e := echo.New()
	cfg := &apiconfig.ConfigManager{}
	cfg.UpsertEscrowAccessRecord(apiconfig.EscrowAccessRecord{
		EscrowID:         "escrow-1",
		DeveloperAddress: "gonka1dev",
		ModelID:          "model-1",
	})

	s := &Server{
		configManager: cfg,
		v2ExecutorSignerPubKeyResolver: func(_ context.Context, executorAddress string, executorSignerAddress string) (string, error) {
			if executorAddress == v2TestExecutorAddress && executorSignerAddress == v2TestExecutorProofSignerAddress {
				return v2TestExecutorProofSigner.GetPubKeyBase64(), nil
			}
			return "", nil
		},
		v2ParticipantSelector: func(_ context.Context, _ string, _ string, _ uint64) ([]string, error) {
			return []string{"gonka1participant"}, nil
		},
		v2ParticipantAddressResolver: func() string {
			return "gonka1participant"
		},
		v2CompletionProxy: func(_ context.Context, _ []byte, _ string, _ http.Header) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewBufferString(`{"id":"resp-1"}`)),
			}, nil
		},
	}

	firstDelta := validV2ChainDelta("model-1", "escrow-1", "escrow-1:1", 0, 1)
	firstBody := mustMarshalV2Envelope(t, defaultV2OpenAIRequest("model-1"), firstDelta)
	firstCtx, _ := newV2RequestContext(e, firstBody, "gonka1dev", "escrow-1", "1")
	require.NoError(t, s.postChatV2(firstCtx))

	secondRequest := defaultV2OpenAIRequest("model-1")
	secondDelta := DeveloperChainDelta{
		BaseBlockSequence: 1,
		Blocks: []DeveloperChainBlock{
			{
				BlockSequence: 2,
				EscrowID:      "escrow-1",
				Messages: []DeveloperChainMessage{
					{
						Type:                  v2ChainMessageTypeFinishInference,
						RequestID:             "escrow-1:1",
						Status:                "finished",
						ResponsePayloadHash:   "resp-hash-1",
						ExecutorAddress:       v2TestExecutorAddress,
						ExecutorSignerAddress: v2TestExecutorProofSignerAddress,
						ExecutorSignerPubKey:  v2TestExecutorProofSigner.GetPubKeyBase64(),
						ExecutorSignature:     "not-a-valid-signature",
						Timestamp:             1710000001,
					},
					{
						Type:               v2ChainMessageTypeStartInference,
						RequestID:          "escrow-1:2",
						ModelID:            "model-1",
						RequestPayloadHash: mustComputeV2RequestPayloadHash(secondRequest),
						Timestamp:          1710000002,
					},
				},
			},
		},
		LatestBlockSequence: 2,
	}
	secondBody := mustMarshalV2Envelope(t, secondRequest, secondDelta)
	secondCtx, _ := newV2RequestContext(e, secondBody, "gonka1dev", "escrow-1", "2")
	err := s.postChatV2(secondCtx)
	require.Error(t, err)
	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusUnauthorized, httpErr.Code)
}

func TestPostChatV2_RejectsFinishInferenceForUnknownRequestID(t *testing.T) {
	e := echo.New()
	cfg := &apiconfig.ConfigManager{}
	cfg.UpsertEscrowAccessRecord(apiconfig.EscrowAccessRecord{
		EscrowID:         "escrow-1",
		DeveloperAddress: "gonka1dev",
		ModelID:          "model-1",
	})

	s := &Server{
		configManager: cfg,
		v2ExecutorSignerPubKeyResolver: func(_ context.Context, executorAddress string, executorSignerAddress string) (string, error) {
			if executorAddress == v2TestExecutorAddress && executorSignerAddress == v2TestExecutorProofSignerAddress {
				return v2TestExecutorProofSigner.GetPubKeyBase64(), nil
			}
			return "", nil
		},
		v2ParticipantSelector: func(_ context.Context, _ string, _ string, _ uint64) ([]string, error) {
			return []string{"gonka1participant"}, nil
		},
		v2ParticipantAddressResolver: func() string {
			return "gonka1participant"
		},
		v2CompletionProxy: func(_ context.Context, _ []byte, _ string, _ http.Header) (*http.Response, error) {
			t.Fatal("v2CompletionProxy should not be called for malformed finish linkage")
			return nil, nil
		},
	}

	request := defaultV2OpenAIRequest("model-1")
	delta := DeveloperChainDelta{
		BaseBlockSequence: 0,
		Blocks: []DeveloperChainBlock{
			{
				BlockSequence: 1,
				EscrowID:      "escrow-1",
				Messages: []DeveloperChainMessage{
					{
						Type:                  v2ChainMessageTypeFinishInference,
						RequestID:             "escrow-1:999",
						Status:                "finished",
						ResponsePayloadHash:   "resp-hash-999",
						ExecutorAddress:       v2TestExecutorAddress,
						ExecutorSignerAddress: v2TestExecutorProofSignerAddress,
						ExecutorSignerPubKey:  v2TestExecutorProofSigner.GetPubKeyBase64(),
						ExecutorSignature:     "invalid-proof-for-unknown-request",
						Timestamp:             1710000001,
					},
					{
						Type:               v2ChainMessageTypeStartInference,
						RequestID:          "escrow-1:1",
						ModelID:            "model-1",
						RequestPayloadHash: mustComputeV2RequestPayloadHash(request),
						Timestamp:          1710000002,
					},
				},
			},
		},
		LatestBlockSequence: 1,
	}
	body := mustMarshalV2Envelope(t, request, delta)
	ctx, _ := newV2RequestContext(e, body, "gonka1dev", "escrow-1", "1")
	err := s.postChatV2(ctx)
	require.Error(t, err)
	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusBadRequest, httpErr.Code)
}

func TestPostChatV2_RejectsRequestPayloadHashMismatch(t *testing.T) {
	e := echo.New()
	cfg := &apiconfig.ConfigManager{}
	cfg.UpsertEscrowAccessRecord(apiconfig.EscrowAccessRecord{
		EscrowID:         "escrow-1",
		DeveloperAddress: "gonka1dev",
		ModelID:          "model-1",
	})

	s := &Server{
		configManager: cfg,
		v2ParticipantSelector: func(_ context.Context, _ string, _ string, _ uint64) ([]string, error) {
			return []string{"gonka1participant"}, nil
		},
		v2ParticipantAddressResolver: func() string {
			return "gonka1participant"
		},
		v2CompletionProxy: func(_ context.Context, _ []byte, _ string, _ http.Header) (*http.Response, error) {
			t.Fatal("v2CompletionProxy should not be called when request payload hash does not match")
			return nil, nil
		},
	}

	invalidHashDelta := DeveloperChainDelta{
		BaseBlockSequence: 0,
		Blocks: []DeveloperChainBlock{
			startInferenceBlockWithHash(1, "escrow-1:1", "escrow-1", "model-1", "invalid-hash"),
		},
		LatestBlockSequence: 1,
	}
	body := mustMarshalV2Envelope(t, defaultV2OpenAIRequest("model-1"), invalidHashDelta)
	ctx, _ := newV2RequestContext(e, body, "gonka1dev", "escrow-1", "1")

	err := s.postChatV2(ctx)
	require.Error(t, err)
	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusConflict, httpErr.Code)
}

func TestPostChatV2_RejectsMissingRequestPayloadHash(t *testing.T) {
	e := echo.New()
	cfg := &apiconfig.ConfigManager{}
	cfg.UpsertEscrowAccessRecord(apiconfig.EscrowAccessRecord{
		EscrowID:         "escrow-1",
		DeveloperAddress: "gonka1dev",
		ModelID:          "model-1",
	})

	s := &Server{
		configManager: cfg,
		v2ParticipantSelector: func(_ context.Context, _ string, _ string, _ uint64) ([]string, error) {
			return []string{"gonka1participant"}, nil
		},
		v2ParticipantAddressResolver: func() string {
			return "gonka1participant"
		},
		v2CompletionProxy: func(_ context.Context, _ []byte, _ string, _ http.Header) (*http.Response, error) {
			t.Fatal("v2CompletionProxy should not be called when request payload hash is missing")
			return nil, nil
		},
	}

	missingHashDelta := DeveloperChainDelta{
		BaseBlockSequence: 0,
		Blocks: []DeveloperChainBlock{
			startInferenceBlockWithHash(1, "escrow-1:1", "escrow-1", "model-1", ""),
		},
		LatestBlockSequence: 1,
	}
	body := mustMarshalV2Envelope(t, defaultV2OpenAIRequest("model-1"), missingHashDelta)
	ctx, _ := newV2RequestContext(e, body, "gonka1dev", "escrow-1", "1")

	err := s.postChatV2(ctx)
	require.Error(t, err)
	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusBadRequest, httpErr.Code)
}

func TestPostChatV2_RejectsMissingDeveloperBlockSignatureFields(t *testing.T) {
	e := echo.New()
	cfg := &apiconfig.ConfigManager{}
	cfg.UpsertEscrowAccessRecord(apiconfig.EscrowAccessRecord{
		EscrowID:         "escrow-1",
		DeveloperAddress: "gonka1dev",
		ModelID:          "model-1",
	})

	s := &Server{
		configManager: cfg,
		v2ParticipantSelector: func(_ context.Context, _ string, _ string, _ uint64) ([]string, error) {
			return []string{"gonka1participant"}, nil
		},
		v2ParticipantAddressResolver: func() string {
			return "gonka1participant"
		},
		v2CompletionProxy: func(_ context.Context, _ []byte, _ string, _ http.Header) (*http.Response, error) {
			t.Fatal("v2CompletionProxy should not be called when block signature fields are missing")
			return nil, nil
		},
	}

	delta := validV2ChainDelta("model-1", "escrow-1", "escrow-1:1", 0, 1)
	delta.Blocks[0].Signature = ""

	body := mustMarshalV2EnvelopeWithoutAutoSign(t, defaultV2OpenAIRequest("model-1"), delta)
	ctx, _ := newV2RequestContext(e, body, "gonka1dev", "escrow-1", "1")
	err := s.postChatV2(ctx)
	require.Error(t, err)

	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusBadRequest, httpErr.Code)
}

func TestPostChatV2_RejectsDeveloperBlockEscrowMismatch(t *testing.T) {
	e := echo.New()
	cfg := &apiconfig.ConfigManager{}
	cfg.UpsertEscrowAccessRecord(apiconfig.EscrowAccessRecord{
		EscrowID:         "escrow-1",
		DeveloperAddress: "gonka1dev",
		DeveloperPubKey:  v2TestDeveloperBlockSignerPubKey,
		ModelID:          "model-1",
	})

	s := &Server{
		configManager: cfg,
		v2ParticipantSelector: func(_ context.Context, _ string, _ string, _ uint64) ([]string, error) {
			return []string{"gonka1participant"}, nil
		},
		v2ParticipantAddressResolver: func() string {
			return "gonka1participant"
		},
		v2CompletionProxy: func(_ context.Context, _ []byte, _ string, _ http.Header) (*http.Response, error) {
			t.Fatal("v2CompletionProxy should not be called when block escrow_id mismatches")
			return nil, nil
		},
	}

	delta := validV2ChainDelta("model-1", "escrow-1", "escrow-1:1", 0, 1)
	signV2DeveloperBlockForTests(t, &delta.Blocks[0], "escrow-1", v2TestDeveloperBlockSignerChainID)
	delta.Blocks[0].EscrowID = "escrow-2"

	body := mustMarshalV2Envelope(t, defaultV2OpenAIRequest("model-1"), delta)
	ctx, _ := newV2RequestContext(e, body, "gonka1dev", "escrow-1", "1")
	err := s.postChatV2(ctx)
	require.Error(t, err)

	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusBadRequest, httpErr.Code)
}

func TestPostChatV2_RejectsInvalidDeveloperBlockSignature(t *testing.T) {
	e := echo.New()
	cfg := &apiconfig.ConfigManager{}
	cfg.UpsertEscrowAccessRecord(apiconfig.EscrowAccessRecord{
		EscrowID:         "escrow-1",
		DeveloperAddress: "gonka1dev",
		DeveloperPubKey:  v2TestDeveloperBlockSignerPubKey,
		ModelID:          "model-1",
	})

	s := &Server{
		configManager: cfg,
		v2ParticipantSelector: func(_ context.Context, _ string, _ string, _ uint64) ([]string, error) {
			return []string{"gonka1participant"}, nil
		},
		v2ParticipantAddressResolver: func() string {
			return "gonka1participant"
		},
		v2CompletionProxy: func(_ context.Context, _ []byte, _ string, _ http.Header) (*http.Response, error) {
			t.Fatal("v2CompletionProxy should not be called when block signature is invalid")
			return nil, nil
		},
	}

	delta := validV2ChainDelta("model-1", "escrow-1", "escrow-1:1", 0, 1)
	signV2DeveloperBlockForTests(t, &delta.Blocks[0], "escrow-1", v2TestDeveloperBlockSignerChainID)
	delta.Blocks[0].Messages[0].RequestID = "escrow-1:1-tampered"

	body := mustMarshalV2Envelope(t, defaultV2OpenAIRequest("model-1"), delta)
	ctx, _ := newV2RequestContext(e, body, "gonka1dev", "escrow-1", "1")
	err := s.postChatV2(ctx)
	require.Error(t, err)

	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusUnauthorized, httpErr.Code)
}

func TestPostChatV2_ReturnsSignedRelayErrorArtifactWhenRelayFails(t *testing.T) {
	e := echo.New()
	cfg := &apiconfig.ConfigManager{}
	cfg.UpsertEscrowAccessRecord(apiconfig.EscrowAccessRecord{
		EscrowID:         "escrow-1",
		DeveloperAddress: "gonka1dev",
		ModelID:          "model-1",
	})

	expectedArtifact := &v2RelayErrorArtifact{
		EscrowID:                "escrow-1",
		RequestID:               "escrow-1:1",
		IntendedExecutorAddress: "gonka1intended",
		RelayAddress:            "gonka1relay",
		FailureCode:             "relay_transport_failure",
		RelaySignerAddress:      "gonka1relaywarm",
		RelaySignerPubKey:       "pubkey",
		RelaySignature:          "signature",
		Timestamp:               1710000000,
	}
	s := &Server{
		configManager: cfg,
		v2ParticipantSelector: func(_ context.Context, _ string, _ string, _ uint64) ([]string, error) {
			return []string{"gonka1intended", "gonka1relay"}, nil
		},
		v2ParticipantAddressResolver: func() string {
			return "gonka1relay"
		},
		v2RelayProxy: func(_ context.Context, _ []byte, _ string, _ http.Header, _ string, _ string) (*http.Response, error) {
			return nil, &v2RelayExecutionError{artifact: expectedArtifact}
		},
	}

	body := mustMarshalV2Envelope(
		t,
		defaultV2OpenAIRequest("model-1"),
		validV2ChainDelta("model-1", "escrow-1", "escrow-1:1", 0, 1),
	)
	ctx, _ := newV2RequestContext(e, body, "gonka1dev", "escrow-1", "1")
	err := s.postChatV2(ctx)
	require.Error(t, err)
	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusServiceUnavailable, httpErr.Code)
	actualArtifact, ok := httpErr.Message.(*v2RelayErrorArtifact)
	require.True(t, ok)
	require.Equal(t, expectedArtifact, actualArtifact)
}

func defaultV2OpenAIRequest(model string) OpenAiRequest {
	return OpenAiRequest{
		Model:    model,
		Messages: []Message{{Content: "hi"}},
	}
}

func validV2ChainDelta(model string, escrowID string, requestID string, baseSequence uint64, blockSequence uint64) DeveloperChainDelta {
	return DeveloperChainDelta{
		BaseBlockSequence: baseSequence,
		Blocks: []DeveloperChainBlock{
			startInferenceBlock(blockSequence, requestID, escrowID, model),
		},
		LatestBlockSequence: blockSequence,
	}
}

func startInferenceBlock(sequence uint64, requestID string, escrowID string, model string) DeveloperChainBlock {
	return startInferenceBlockWithHash(
		sequence,
		requestID,
		escrowID,
		model,
		mustComputeV2RequestPayloadHash(defaultV2OpenAIRequest(model)),
	)
}

func startInferenceBlockWithHash(sequence uint64, requestID string, escrowID string, model string, requestPayloadHash string) DeveloperChainBlock {
	return DeveloperChainBlock{
		BlockSequence: sequence,
		EscrowID:      escrowID,
		Messages: []DeveloperChainMessage{
			{
				Type:               v2ChainMessageTypeStartInference,
				RequestID:          requestID,
				ModelID:            model,
				RequestPayloadHash: requestPayloadHash,
				Timestamp:          1710000000,
			},
		},
	}
}

func mustComputeV2RequestPayloadHash(openAIRequest OpenAiRequest) string {
	openAIRequestBody, err := json.Marshal(openAIRequest)
	if err != nil {
		panic(err)
	}
	requestPayloadHash, err := computeV2RequestPayloadHash(openAIRequestBody)
	if err != nil {
		panic(err)
	}
	return requestPayloadHash
}

func mustMarshalV2Envelope(t *testing.T, openAIRequest OpenAiRequest, developerChainDelta DeveloperChainDelta) string {
	t.Helper()
	signMissingV2DeveloperBlockSignaturesForTests(t, &developerChainDelta)
	return mustMarshalV2EnvelopeWithoutAutoSign(t, openAIRequest, developerChainDelta)
}

func mustMarshalV2EnvelopeWithoutAutoSign(t *testing.T, openAIRequest OpenAiRequest, developerChainDelta DeveloperChainDelta) string {
	t.Helper()
	openAIRequestBody, err := json.Marshal(openAIRequest)
	require.NoError(t, err)

	envelope := V2RequestEnvelope{
		OpenAIRequest:       openAIRequestBody,
		DeveloperChainDelta: &developerChainDelta,
	}

	body, err := json.Marshal(envelope)
	require.NoError(t, err)
	return string(body)
}

func signMissingV2DeveloperBlockSignaturesForTests(t *testing.T, developerChainDelta *DeveloperChainDelta) {
	t.Helper()
	state := v2DeterministicChainState{}
	for blockIdx := range developerChainDelta.Blocks {
		block := &developerChainDelta.Blocks[blockIdx]
		if err := applyV2DeterministicStateBlock(&state, block.Messages); err == nil {
			if strings.TrimSpace(block.StateHash) == "" {
				block.StateHash = computeV2DeterministicStateHashHex(state)
			}
		} else if strings.TrimSpace(block.StateHash) == "" {
			// Keep malformed-block tests focused on their targeted validation path.
			block.StateHash = "invalid-state-hash-for-malformed-block"
		}
		if strings.TrimSpace(block.StateHash) == "" {
			block.StateHash = computeV2DeterministicStateHashHex(state)
		}
		if strings.TrimSpace(block.Signature) != "" {
			continue
		}
		escrowID := strings.TrimSpace(block.EscrowID)
		if strings.TrimSpace(escrowID) == "" {
			continue
		}
		signV2DeveloperBlockForTests(t, block, escrowID, v2TestDeveloperBlockSignerChainID)
	}
}

func mustSignV2ExecutorProofForTests(t *testing.T, developerRequestBlockSignature string, responsePayloadHash string) string {
	t.Helper()
	preimage := buildV2ExecutorFinishSigningPreimage(developerRequestBlockSignature, responsePayloadHash)
	preimageHash := sha256.Sum256(preimage)
	signingPayload := []byte(fmt.Sprintf("%x", preimageHash[:]))
	signature, err := v2TestExecutorProofSigner.SignBytes(signingPayload)
	require.NoError(t, err)
	return signature
}

func signV2DeveloperBlockForTests(
	t *testing.T,
	block *DeveloperChainBlock,
	escrowID string,
	chainID string,
) {
	t.Helper()
	blockMessagesHash := computeV2DeveloperBlockMessagesHash(block.Messages)
	preimage := buildV2DeveloperBlockSigningPreimage(chainID, escrowID, block.BlockSequence, blockMessagesHash, block.StateHash)
	preimageHash := sha256.Sum256(preimage)
	signingPayload := []byte(fmt.Sprintf("%x", preimageHash[:]))

	signature, err := v2TestDeveloperBlockSigner.SignBytes(signingPayload)
	require.NoError(t, err)
	block.Signature = signature
}

func newV2RequestContext(
	e *echo.Echo,
	body string,
	requesterAddress string,
	escrowID string,
	sequence string,
) (echo.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodPost, "/v2/chat/completions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if requesterAddress != "" {
		req.Header.Set(utils.XRequesterAddressHeader, requesterAddress)
	}
	if escrowID != "" {
		req.Header.Set(utils.XEscrowIDHeader, escrowID)
	}
	if sequence != "" {
		req.Header.Set(utils.XEscrowSequenceHeader, sequence)
	}
	req.Header.Set(utils.XEpochIdHeader, "1")
	rec := httptest.NewRecorder()
	ctx := e.NewContext(req, rec)
	return ctx, rec
}
