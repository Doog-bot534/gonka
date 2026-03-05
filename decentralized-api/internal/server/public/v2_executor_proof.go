package public

import (
	"bytes"
	"crypto/sha256"
	"decentralized-api/logging"
	"decentralized-api/utils"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/productscience/inference/x/inference/types"
)

const (
	v2ExecutorFinishSignDomain = "v2_exec_finish_sig_v1"
	v2ExecutorProofSSEEvent    = "v2_executor_proof"
)

type v2ExecutorProof struct {
	ExecutorAddress       string `json:"executor_address"`
	ExecutorSignerAddress string `json:"executor_signer_address"`
	ExecutorSignerPubKey  string `json:"executor_signer_pubkey"`
	ExecutorSignature     string `json:"executor_signature"`
}

func buildV2ExecutorFinishSigningPreimage(developerRequestBlockSignature string, responsePayloadHash string) []byte {
	var buffer bytes.Buffer
	writeV2LengthPrefixedString(&buffer, v2ExecutorFinishSignDomain)
	writeV2LengthPrefixedString(&buffer, developerRequestBlockSignature)
	writeV2LengthPrefixedString(&buffer, responsePayloadHash)
	return buffer.Bytes()
}

func resolveV2DeveloperRequestBlockSignature(developerChainDelta DeveloperChainDelta, requestID string) string {
	for blockIndex := len(developerChainDelta.Blocks) - 1; blockIndex >= 0; blockIndex-- {
		block := developerChainDelta.Blocks[blockIndex]
		for _, message := range block.Messages {
			if message.Type == v2ChainMessageTypeStartInference && message.RequestID == requestID {
				return strings.TrimSpace(block.Signature)
			}
		}
	}
	if len(developerChainDelta.Blocks) == 0 {
		return ""
	}
	return strings.TrimSpace(developerChainDelta.Blocks[len(developerChainDelta.Blocks)-1].Signature)
}

func (s *Server) buildV2ExecutorProof(developerRequestBlockSignature string, responsePayloadHash string) (*v2ExecutorProof, error) {
	if s.recorder == nil {
		return nil, nil
	}
	developerRequestBlockSignature = strings.TrimSpace(developerRequestBlockSignature)
	responsePayloadHash = strings.TrimSpace(responsePayloadHash)
	if developerRequestBlockSignature == "" || responsePayloadHash == "" {
		return nil, nil
	}

	executorAddress := strings.TrimSpace(s.recorder.GetAccountAddress())
	if executorAddress == "" {
		return nil, fmt.Errorf("executor address unavailable")
	}
	executorSignerAddress := strings.TrimSpace(s.recorder.GetSignerAddress())
	if executorSignerAddress == "" {
		return nil, fmt.Errorf("executor signer address unavailable")
	}
	signerPubKey := s.recorder.GetSignerPubKey()
	if signerPubKey == nil || len(signerPubKey.Bytes()) == 0 {
		return nil, fmt.Errorf("executor signer pubkey unavailable")
	}
	executorSignerPubKey := base64.StdEncoding.EncodeToString(signerPubKey.Bytes())

	preimage := buildV2ExecutorFinishSigningPreimage(developerRequestBlockSignature, responsePayloadHash)
	preimageHash := sha256.Sum256(preimage)
	signingPayload := []byte(fmt.Sprintf("%x", preimageHash[:]))
	signatureBytes, err := s.recorder.SignBytes(signingPayload)
	if err != nil {
		return nil, err
	}

	return &v2ExecutorProof{
		ExecutorAddress:       executorAddress,
		ExecutorSignerAddress: executorSignerAddress,
		ExecutorSignerPubKey:  executorSignerPubKey,
		ExecutorSignature:     base64.StdEncoding.EncodeToString(signatureBytes),
	}, nil
}

func setV2ExecutorProofHeaders(headers http.Header, proof *v2ExecutorProof) {
	if proof == nil {
		return
	}
	if proof.ExecutorAddress != "" {
		headers.Set(utils.XV2ExecutorAddressHeader, proof.ExecutorAddress)
	}
	if proof.ExecutorSignerAddress != "" {
		headers.Set(utils.XV2ExecutorSignerAddressHeader, proof.ExecutorSignerAddress)
	}
	if proof.ExecutorSignerPubKey != "" {
		headers.Set(utils.XV2ExecutorSignerPubKeyHeader, proof.ExecutorSignerPubKey)
	}
	if proof.ExecutorSignature != "" {
		headers.Set(utils.XV2ExecutorSignatureHeader, proof.ExecutorSignature)
	}
}

func writeV2ExecutorProofSSEEvent(writer http.ResponseWriter, proof *v2ExecutorProof) {
	if proof == nil {
		return
	}
	proofJSON, err := json.Marshal(proof)
	if err != nil {
		logging.Warn("Unable to marshal v2 executor proof terminal SSE event", types.Inferences, "error", err)
		return
	}
	_, _ = fmt.Fprintf(writer, "event: %s\n", v2ExecutorProofSSEEvent)
	_, _ = fmt.Fprintf(writer, "data: %s\n\n", string(proofJSON))
}

