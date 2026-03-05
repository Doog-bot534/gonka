package public

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateV2ExecutorProofSignature_Valid(t *testing.T) {
	executorKey := newTestKey()
	developerRequestBlockSignature := "dev-block-signature-1"
	responsePayloadHash := "abc123"

	preimage := buildV2ExecutorFinishSigningPreimage(developerRequestBlockSignature, responsePayloadHash)
	preimageHash := sha256.Sum256(preimage)
	signingPayload := []byte(fmt.Sprintf("%x", preimageHash[:]))
	signatureBytes, err := executorKey.key.Sign(signingPayload)
	require.NoError(t, err)

	err = validateV2ExecutorProofSignature(
		executorKey.GetPubKeyBase64(),
		base64.StdEncoding.EncodeToString(signatureBytes),
		developerRequestBlockSignature,
		responsePayloadHash,
	)
	require.NoError(t, err)
}

func TestValidateV2ExecutorProofSignature_WrongKey(t *testing.T) {
	executorKey := newTestKey()
	wrongKey := newTestKey()
	developerRequestBlockSignature := "dev-block-signature-1"
	responsePayloadHash := "abc123"

	preimage := buildV2ExecutorFinishSigningPreimage(developerRequestBlockSignature, responsePayloadHash)
	preimageHash := sha256.Sum256(preimage)
	signingPayload := []byte(fmt.Sprintf("%x", preimageHash[:]))
	signatureBytes, err := wrongKey.key.Sign(signingPayload)
	require.NoError(t, err)

	err = validateV2ExecutorProofSignature(
		executorKey.GetPubKeyBase64(),
		base64.StdEncoding.EncodeToString(signatureBytes),
		developerRequestBlockSignature,
		responsePayloadHash,
	)
	require.Error(t, err)
}

func TestValidateV2ExecutorProofSignature_WrongResponseHash(t *testing.T) {
	executorKey := newTestKey()
	developerRequestBlockSignature := "dev-block-signature-1"
	responsePayloadHash := "abc123"

	preimage := buildV2ExecutorFinishSigningPreimage(developerRequestBlockSignature, responsePayloadHash)
	preimageHash := sha256.Sum256(preimage)
	signingPayload := []byte(fmt.Sprintf("%x", preimageHash[:]))
	signatureBytes, err := executorKey.key.Sign(signingPayload)
	require.NoError(t, err)

	err = validateV2ExecutorProofSignature(
		executorKey.GetPubKeyBase64(),
		base64.StdEncoding.EncodeToString(signatureBytes),
		developerRequestBlockSignature,
		"different-hash",
	)
	require.Error(t, err)
}

func TestValidateV2ExecutorProofSignature_MalformedSignature(t *testing.T) {
	executorKey := newTestKey()
	err := validateV2ExecutorProofSignature(
		executorKey.GetPubKeyBase64(),
		"%%%not-base64%%%",
		"dev-block-signature-1",
		"abc123",
	)
	require.Error(t, err)
}

func TestValidateV2ExecutorProofSignature_MissingFields(t *testing.T) {
	err := validateV2ExecutorProofSignature("", "", "", "")
	require.Error(t, err)
}

