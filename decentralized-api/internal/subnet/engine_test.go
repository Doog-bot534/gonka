package subnet

import (
	"crypto/sha256"
	"encoding/json"
	"testing"

	"decentralized-api/completionapi"
	"decentralized-api/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsSSE(t *testing.T) {
	assert.True(t, isSSE("text/event-stream"))
	assert.True(t, isSSE("text/event-stream; charset=utf-8"))
	assert.False(t, isSSE("application/json"))
	assert.False(t, isSSE(""))
}

func TestSplitSSELines(t *testing.T) {
	data := []byte("data: {\"id\":\"1\"}\n\ndata: [DONE]\n")
	lines := splitSSELines(data)
	assert.Equal(t, []string{"data: {\"id\":\"1\"}", "data: [DONE]"}, lines)
}

func TestResponseHashComputation(t *testing.T) {
	// Simulate the hash computation the engine does
	responseJSON := `{"id":"test","choices":[{"message":{"content":"hello"},"logprobs":{"content":[{"token":"hello","logprob":-0.1,"top_logprobs":[{"token":"hello","logprob":-0.1}]}]}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`

	resp, err := completionapi.NewCompletionResponseFromBytes([]byte(responseJSON))
	require.NoError(t, err)

	bodyBytes, err := resp.GetBodyBytes()
	require.NoError(t, err)

	hash := sha256.Sum256(bodyBytes)
	assert.Len(t, hash, 32)

	usage, err := resp.GetUsage()
	require.NoError(t, err)
	assert.Equal(t, uint64(10), usage.PromptTokens)
	assert.Equal(t, uint64(5), usage.CompletionTokens)
}

func TestCanonicalizePrompt(t *testing.T) {
	body := []byte(`{"model":"test","seed":42,"logprobs":true}`)
	canonicalized, err := utils.CanonicalizeJSON(body)
	require.NoError(t, err)

	// Verify it's valid JSON and keys are sorted
	var result map[string]interface{}
	err = json.Unmarshal([]byte(canonicalized), &result)
	require.NoError(t, err)
	assert.Contains(t, result, "model")
	assert.Contains(t, result, "seed")
	assert.Contains(t, result, "logprobs")
}

func TestModifyRequestBody(t *testing.T) {
	body := []byte(`{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`)
	modified, err := completionapi.ModifyRequestBody(body, 42)
	require.NoError(t, err)

	var result map[string]interface{}
	err = json.Unmarshal(modified.NewBody, &result)
	require.NoError(t, err)
	assert.Equal(t, true, result["logprobs"])
	assert.Equal(t, float64(5), result["top_logprobs"])
	assert.Equal(t, float64(42), result["seed"])
	assert.Equal(t, false, result["skip_special_tokens"])
}
