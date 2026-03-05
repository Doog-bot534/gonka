package public

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
)

func validateV2ExecutorProofSignature(
	executorPubKeyBase64 string,
	executorSignatureBase64 string,
	developerRequestBlockSignature string,
	responsePayloadHash string,
) error {
	executorPubKeyBase64 = strings.TrimSpace(executorPubKeyBase64)
	executorSignatureBase64 = strings.TrimSpace(executorSignatureBase64)
	developerRequestBlockSignature = strings.TrimSpace(developerRequestBlockSignature)
	responsePayloadHash = strings.TrimSpace(responsePayloadHash)

	if executorPubKeyBase64 == "" || executorSignatureBase64 == "" || developerRequestBlockSignature == "" || responsePayloadHash == "" {
		return fmt.Errorf("v2 executor proof fields are required")
	}

	pubKeyBytes, err := base64.StdEncoding.DecodeString(executorPubKeyBase64)
	if err != nil || len(pubKeyBytes) == 0 {
		return fmt.Errorf("v2 executor proof pubkey is malformed")
	}
	signatureBytes, err := base64.StdEncoding.DecodeString(executorSignatureBase64)
	if err != nil || len(signatureBytes) == 0 {
		return fmt.Errorf("v2 executor proof signature is malformed")
	}

	preimage := buildV2ExecutorFinishSigningPreimage(developerRequestBlockSignature, responsePayloadHash)
	preimageHash := sha256.Sum256(preimage)
	signingPayload := []byte(fmt.Sprintf("%x", preimageHash[:]))
	pubKey := secp256k1.PubKey{Key: pubKeyBytes}
	if !pubKey.VerifySignature(signingPayload, signatureBytes) {
		return fmt.Errorf("v2 executor proof signature is invalid")
	}
	return nil
}

