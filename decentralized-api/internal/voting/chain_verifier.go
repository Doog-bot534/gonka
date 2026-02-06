// Package voting provides types and services for the node voting mechanism.
package voting

import (
	"context"
	"fmt"

	"decentralized-api/cosmosclient"

	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
)

// ChainVerifier queries the chain for inference state and validates challenger claims.
// Used by the voting service at dispute initiation.
type ChainVerifier struct {
	recorder cosmosclient.CosmosMessageClient
}

// NewChainVerifier creates a new ChainVerifier with the given cosmos client.
func NewChainVerifier(recorder cosmosclient.CosmosMessageClient) *ChainVerifier {
	return &ChainVerifier{
		recorder: recorder,
	}
}

// QueryInferenceState queries the chain for MsgStartInference and MsgFinishInference data.
// Returns an universal OnChainProof containing all relevant on-chain data for the inference.
func (cv *ChainVerifier) QueryInferenceState(ctx context.Context, inferenceId string) (*OnChainProof, error) {
	queryClient := cv.recorder.NewInferenceQueryClient()

	response, err := queryClient.Inference(ctx, &types.QueryGetInferenceRequest{Index: inferenceId})
	if err != nil {
		// Inference not found on chain
		return &OnChainProof{
			InferenceExists: false,
		}, nil
	}

	inference := response.Inference

	// Determine if MsgFinishInference exists by checking if ResponseHash is set
	// (ResponseHash is only set when executor completes the inference)
	finishExists := inference.ResponseHash != ""

	return &OnChainProof{
		InferenceExists:      true,
		AssignedTo:           inference.AssignedTo,
		CreatedBy:            inference.TransferredBy,
		FinishExists:         finishExists,
		ExpectedPromptHash:   inference.PromptHash,
		ExpectedResponseHash: inference.ResponseHash,
		RequestTimestamp:     inference.RequestTimestamp,
		TransferSignature:    inference.TransferSignature,
	}, nil
}

// ValidateAssignmentProof verifies the TransferSignature in the AssignmentProof cryptographically.
// The signature alone proves assignment.
// Returns nil if valid, error if invalid.
func (cv *ChainVerifier) ValidateAssignmentProof(ctx context.Context, proof *AssignmentProof) error {
	if proof == nil {
		return fmt.Errorf("assignment proof is nil")
	}

	if proof.TransferSignature == "" {
		return fmt.Errorf("transfer signature is empty")
	}

	if proof.TransferAddress == "" {
		return fmt.Errorf("transfer address is empty")
	}

	// Get the TA's public key from chain to verify signature
	queryClient := cv.recorder.NewInferenceQueryClient()
	participant, err := queryClient.InferenceParticipant(ctx, &types.QueryInferenceParticipantRequest{
		Address: proof.TransferAddress,
	})
	if err != nil {
		return fmt.Errorf("failed to get transfer agent participant: %w", err)
	}

	if participant.Pubkey == "" {
		return fmt.Errorf("transfer agent not found or has no public key: %s", proof.TransferAddress)
	}

	// Build the signature components that the TA signed
	// TransferSignature signs: prompt_hash + timestamp + transfer_addr + executor_addr
	components := calculations.SignatureComponents{
		Payload:         proof.PromptHash,
		Timestamp:       proof.Timestamp,
		TransferAddress: proof.TransferAddress,
		ExecutorAddress: proof.ExecutorAddress,
	}

	// Validate the signature using TA's public key
	err = calculations.ValidateSignatureWithGrantees(
		components,
		calculations.TransferAgent,
		[]string{participant.Pubkey},
		proof.TransferSignature,
	)
	if err != nil {
		return fmt.Errorf("invalid transfer signature: %w", err)
	}

	return nil
}

// ValidateChallengerRole verifies that the challenger is legitimately associated with the inference.
// For RoleExecutor: verifies on-chain AssignedTo == ChallengerAddress
// For RoleTA: verifies on-chain CreatedBy (TransferredBy) == ChallengerAddress
func (cv *ChainVerifier) ValidateChallengerRole(onChain *OnChainProof, req *VotingInitiateRequest) error {
	if !onChain.InferenceExists {
		// If inference doesn't exist on-chain, challenger must provide AssignmentProof
		if req.AssignmentProof == nil {
			return fmt.Errorf("inference not found on-chain and no assignment proof provided")
		}
		// AssignmentProof validation should be done separately via ValidateAssignmentProof
		return nil
	}

	switch req.ChallengerRole {
	case RoleExecutor:
		// Executor challenging TA: verify executor was assigned this inference
		if onChain.AssignedTo != req.ChallengerAddress {
			return fmt.Errorf("challenger %s is not the assigned executor (expected %s)",
				req.ChallengerAddress, onChain.AssignedTo)
		}

	case RoleTA:
		// TA challenging executor: verify TA created this inference
		if onChain.CreatedBy != req.ChallengerAddress {
			return fmt.Errorf("challenger %s is not the transfer agent (expected %s)",
				req.ChallengerAddress, onChain.CreatedBy)
		}

	default:
		return fmt.Errorf("invalid challenger role: %s", req.ChallengerRole)
	}

	return nil
}

// DetermineVerificationOutcome checks the on-chain state and determines what the vote should be.
// This is used by validators to determine their vote based on verification type.
// Currently only supports VerifyPayloadFromTA (executor challenging TA).
// TODO: Add other verification types when needed.
func (cv *ChainVerifier) DetermineVerificationOutcomeAndDeliverPayload(
	onChain *OnChainProof,
	verificationType VerificationType,
	actualDataHash string,
) VoteType {
	switch verificationType {
	case VerifyPayloadFromTA:
		// Executor challenges TA: verify TA has the correct payload
		// actualDataHash is the prompt hash computed from TA's payload endpoint response

		// Check 1: TA must have the payload
		if actualDataHash == "" {
			return VoteNegative // TA doesn't have payload
		}

		// Check 2: If on-chain prompt hash exists, verify it matches
		// This ensures TA serves the correct payload, not just any payload
		if onChain.ExpectedPromptHash != "" && actualDataHash != onChain.ExpectedPromptHash {
			return VoteNegative // TA has wrong payload (hash mismatch)
		}

		// Check 3: If MsgFinishInference exists, then the dispute is invalidated
		if onChain.FinishExists {
			return VoteNegative // Dispute is invalidated
		}

		// TA has payload and hash matches (or no on-chain hash to compare)
		// TODO: Deliver the payload to the challenger in this function.
		return VotePositive

	// TODO: Implement other verification types when needed
	// case VerifyMsgStartExists:
	// 	// Check if MsgStartInference exists
	// 	if onChain.InferenceExists {
	// 		return VotePositive // TA posted the message
	// 	}
	// 	return VoteNegative // TA didn't post
	//
	// case VerifyMsgFinishExists:
	// 	// Check if MsgFinishInference exists
	// 	if onChain.FinishExists {
	// 		return VotePositive // Executor completed
	// 	}
	// 	return VoteNegative // Executor didn't complete
	//
	// case VerifyPromptHashMatch:
	// 	// Compare actual payload hash with on-chain prompt_hash
	// 	if actualDataHash == onChain.ExpectedPromptHash {
	// 		return VotePositive // Hash matches
	// 	}
	// 	return VoteNegative // Hash mismatch
	//
	// case VerifyResponseHashMatch:
	// 	// Compare actual payload hash with on-chain response_hash
	// 	if actualDataHash == onChain.ExpectedResponseHash {
	// 		return VotePositive // Hash matches
	// 	}
	// 	return VoteNegative // Hash mismatch
	//
	// case VerifyPayloadFromExecutor:
	// 	// TA challenges executor: verify executor has the payload
	// 	if actualDataHash != "" {
	// 		return VotePositive // Got payload
	// 	}
	// 	return VoteNegative // No payload
	//
	// case VerifyResponseDeliveryToTA:
	// 	// TA claims they didn't receive response from executor.
	// 	// We verify: Did executor complete their job?
	// 	if onChain.FinishExists && actualDataHash != "" {
	// 		return VotePositive // Executor completed and has payload - not at fault
	// 	}
	// 	return VoteNegative // Executor didn't complete or doesn't have payload

	default:
		return VoteInvalid
	}
}
