package public

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
)

const v2RelayErrorSignDomain = "v2_relay_error_sig_v1"

type v2RelayErrorArtifact struct {
	EscrowID                string `json:"escrow_id"`
	RequestID               string `json:"request_id"`
	IntendedExecutorAddress string `json:"intended_executor_address"`
	RelayAddress            string `json:"relay_address"`
	FailureCode             string `json:"failure_code"`
	RelaySignerAddress      string `json:"relay_signer_address"`
	RelaySignerPubKey       string `json:"relay_signer_pubkey"`
	RelaySignature          string `json:"relay_signature"`
	Timestamp               int64  `json:"timestamp"`
}

type v2MissedInferenceEvidence struct {
	RelayErrors []v2RelayErrorArtifact `json:"relay_errors"`
}

type v2RelayExecutionError struct {
	artifact *v2RelayErrorArtifact
}

func (e *v2RelayExecutionError) Error() string {
	return ErrV2IntendedExecutorUnavailable.Error()
}

func (s *Server) buildSignedV2RelayErrorArtifact(
	escrowID string,
	requestID string,
	intendedExecutorAddress string,
	relayAddress string,
	failureCode string,
) (*v2RelayErrorArtifact, error) {
	if s.recorder == nil {
		return nil, fmt.Errorf("recorder unavailable")
	}
	signerAddress := s.recorder.GetSignerAddress()
	if signerAddress == "" {
		signerAddress = s.recorder.GetAccountAddress()
	}
	if signerAddress == "" {
		return nil, fmt.Errorf("relay signer address unavailable")
	}
	signerPubKey := s.recorder.GetSignerPubKey()
	if signerPubKey == nil {
		signerPubKey = s.recorder.GetAccountPubKey()
	}
	if signerPubKey == nil || len(signerPubKey.Bytes()) == 0 {
		return nil, fmt.Errorf("relay signer pubkey unavailable")
	}

	artifact := &v2RelayErrorArtifact{
		EscrowID:                escrowID,
		RequestID:               requestID,
		IntendedExecutorAddress: intendedExecutorAddress,
		RelayAddress:            relayAddress,
		FailureCode:             failureCode,
		RelaySignerAddress:      signerAddress,
		RelaySignerPubKey:       base64.StdEncoding.EncodeToString(signerPubKey.Bytes()),
		Timestamp:               time.Now().Unix(),
	}
	signingPayload := buildV2RelayErrorSigningPayload(*artifact)
	signatureBytes, err := s.recorder.SignBytes(signingPayload)
	if err != nil {
		return nil, err
	}
	artifact.RelaySignature = base64.StdEncoding.EncodeToString(signatureBytes)
	return artifact, nil
}

func (s *Server) validateV2MissedInferenceEvidence(
	ctx context.Context,
	escrowID string,
	modelID string,
	message DeveloperChainMessage,
) error {
	requestSequence, err := parseV2RequestSequence(message.RequestID, escrowID)
	if err != nil {
		return ErrV2MissedInferenceEvidenceMalformed
	}
	responsibleParticipants, err := s.resolveV2ResponsibleParticipants(ctx, modelID, escrowID, requestSequence)
	if err != nil {
		return err
	}
	if len(responsibleParticipants) == 0 {
		return ErrV2MissedInferenceEvidenceMalformed
	}

	evidence := v2MissedInferenceEvidence{}
	if err := json.Unmarshal([]byte(message.MissedInferenceEvidence), &evidence); err != nil {
		return ErrV2MissedInferenceEvidenceMalformed
	}
	if len(evidence.RelayErrors) == 0 {
		return ErrV2MissedInferenceEvidenceMalformed
	}

	responsibleParticipantSet := make(map[string]struct{}, len(responsibleParticipants))
	for _, participantAddress := range responsibleParticipants {
		responsibleParticipantSet[participantAddress] = struct{}{}
	}
	intendedExecutorAddress := responsibleParticipants[0]

	validRelayCount := 0
	countedRelayAddresses := make(map[string]struct{})
	for _, artifact := range evidence.RelayErrors {
		if artifact.EscrowID == "" ||
			artifact.RequestID == "" ||
			artifact.IntendedExecutorAddress == "" ||
			artifact.RelayAddress == "" ||
			artifact.FailureCode == "" ||
			artifact.RelaySignerAddress == "" ||
			artifact.RelaySignerPubKey == "" ||
			artifact.RelaySignature == "" ||
			artifact.Timestamp <= 0 {
			return ErrV2MissedInferenceEvidenceMalformed
		}
		if artifact.EscrowID != escrowID ||
			artifact.RequestID != message.RequestID ||
			artifact.IntendedExecutorAddress != intendedExecutorAddress {
			return ErrV2MissedInferenceEvidenceMalformed
		}
		if _, isResponsible := responsibleParticipantSet[artifact.RelayAddress]; !isResponsible {
			return ErrV2MissedInferenceEvidenceMalformed
		}
		resolvedSignerPubKey, err := s.resolveV2ExecutorSignerPubKey(ctx, artifact.RelayAddress, artifact.RelaySignerAddress)
		if err != nil {
			return err
		}
		if resolvedSignerPubKey == "" || resolvedSignerPubKey != artifact.RelaySignerPubKey {
			return ErrV2MissedInferenceEvidenceInvalid
		}
		if err := validateV2RelayErrorArtifactSignature(artifact); err != nil {
			return ErrV2MissedInferenceEvidenceInvalid
		}
		if _, alreadyCounted := countedRelayAddresses[artifact.RelayAddress]; alreadyCounted {
			continue
		}
		countedRelayAddresses[artifact.RelayAddress] = struct{}{}
		validRelayCount++
	}

	if !hasV2MissedInferenceQuorum(len(responsibleParticipants), validRelayCount) {
		return ErrV2MissedInferenceQuorumInsufficient
	}
	return nil
}

func hasV2MissedInferenceQuorum(totalResponsibleParticipants int, validRelayCount int) bool {
	if totalResponsibleParticipants <= 0 {
		return false
	}
	return validRelayCount > totalResponsibleParticipants/2
}

func validateV2RelayErrorArtifactSignature(artifact v2RelayErrorArtifact) error {
	pubKeyBytes, err := base64.StdEncoding.DecodeString(artifact.RelaySignerPubKey)
	if err != nil || len(pubKeyBytes) == 0 {
		return fmt.Errorf("v2 relay error pubkey is malformed")
	}
	signatureBytes, err := base64.StdEncoding.DecodeString(artifact.RelaySignature)
	if err != nil || len(signatureBytes) == 0 {
		return fmt.Errorf("v2 relay error signature is malformed")
	}
	signingPayload := buildV2RelayErrorSigningPayload(artifact)
	pubKey := secp256k1.PubKey{Key: pubKeyBytes}
	if !pubKey.VerifySignature(signingPayload, signatureBytes) {
		return fmt.Errorf("v2 relay error signature is invalid")
	}
	return nil
}

func buildV2RelayErrorSigningPayload(artifact v2RelayErrorArtifact) []byte {
	preimage := buildV2RelayErrorSigningPreimage(artifact)
	preimageHash := sha256.Sum256(preimage)
	return []byte(fmt.Sprintf("%x", preimageHash[:]))
}

func buildV2RelayErrorSigningPreimage(artifact v2RelayErrorArtifact) []byte {
	var buffer bytes.Buffer
	writeV2LengthPrefixedString(&buffer, v2RelayErrorSignDomain)
	writeV2LengthPrefixedString(&buffer, artifact.EscrowID)
	writeV2LengthPrefixedString(&buffer, artifact.RequestID)
	writeV2LengthPrefixedString(&buffer, artifact.IntendedExecutorAddress)
	writeV2LengthPrefixedString(&buffer, artifact.RelayAddress)
	writeV2LengthPrefixedString(&buffer, artifact.FailureCode)
	writeV2LengthPrefixedString(&buffer, artifact.RelaySignerAddress)
	writeV2LengthPrefixedString(&buffer, artifact.RelaySignerPubKey)
	writeV2Int64(&buffer, artifact.Timestamp)
	return buffer.Bytes()
}
