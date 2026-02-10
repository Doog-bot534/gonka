// Package voting provides types and services for the node voting mechanism.
// This mechanism allows nodes to dispute another node's (the Respondent's) behavior
// by initiating a vote among sampled nodes.
package voting

// ChallengerRole identifies who is initiating the dispute.
// The role determines what proof is required and what verification voters perform.
type ChallengerRole string

const (
	// RoleExecutor indicates an executor is challenging a TA for not sending the payload.
	// Proof: On-chain MsgStartInference.assigned_to == ChallengerAddress
	// No ChallengerDataHash needed (executor has nothing to provide).
	RoleExecutor ChallengerRole = "executor"

	// RoleTA indicates a TA is challenging an executor for not finishing the inference.
	// Proof: On-chain MsgStartInference.creator == ChallengerAddress
	// ChallengerDataHash should contain the hash of the prompt_payload TA sent.
	RoleTA ChallengerRole = "ta"
)

// String returns a human-readable representation of the ChallengerRole.
func (r ChallengerRole) String() string {
	return string(r)
}

// IsValid returns true if the ChallengerRole is a recognized value.
func (r ChallengerRole) IsValid() bool {
	return r == RoleExecutor || r == RoleTA
}

// AssignmentProof contains the minimum data from ChatRequest to prove an inference was assigned.
// If the signature verifies, it proves TA assigned this inference to this executor.
type AssignmentProof struct {
	// InferenceId is the unique identifier of the inference.
	InferenceId string `json:"inference_id"`

	// TransferAddress is the TA's address who assigned the inference.
	TransferAddress string `json:"transfer_address"`

	// ExecutorAddress is the address of the node assigned to execute.
	ExecutorAddress string `json:"executor_address"`

	// TransferSignature is the TA's signature proving assignment (KEY PROOF).
	// Signs: prompt_hash + timestamp + transfer_addr + executor_addr
	TransferSignature string `json:"transfer_signature"`

	// PromptHash is the hash of the prompt payload that was signed.
	PromptHash string `json:"prompt_hash"`

	// Timestamp is the Unix timestamp (in nanoseconds) when the assignment was made.
	Timestamp int64 `json:"timestamp"`

	// AuthKey is the developer's signature for authorization verification (optional).
	AuthKey string `json:"auth_key,omitempty"`

	// RequesterAddress is the developer's address who requested the inference (optional).
	RequesterAddress string `json:"requester_address,omitempty"`

	// Seed is used for deterministic verification (optional).
	Seed string `json:"seed,omitempty"`
}

// VerificationType specifies what voters should verify during the voting process.
type VerificationType string

const (
	// VerifyPayloadFromTA indicates voters should ping the TA's payload endpoint.
	// Used when an executor challenges a TA for not sending the payload
	// Voters will: GET /v1/inference/payloads?inference_id=... on TA's endpoint
	VerifyPayloadFromTA VerificationType = "payload_from_ta"

	// VerifyPayloadFromExecutor indicates voters should ping the executor's payload endpoint.
	// Used when a TA challenges an executor for not storing the response payload.
	// Voters will: GET /v1/inference/payloads?inference_id=... on executor's endpoint
	VerifyPayloadFromExecutor VerificationType = "payload_from_executor"

	// VerifyMsgStartExists indicates voters should check if MsgStartInference exists on-chain.
	// Used when an executor challenges a TA for not posting MsgStartInference.
	// Voters will: Query chain for MsgStartInference with the inference_id,
	// if missing, it will request the MsgStartInference from the TA.
	VerifyMsgStartExists VerificationType = "msg_start_exists"

	// VerifyMsgFinishExists indicates voters should check if MsgFinishInference exists on-chain.
	// Used when a TA challenges an executor for not completing the inference.
	// Voters will: Query chain for MsgFinishInference with the inference_id,
	// if missing, it will request the MsgFinishInference from the executor.
	VerifyMsgFinishExists VerificationType = "msg_finish_exists"

	// VerifyPromptHashMatch indicates voters should verify the TA's payload hash matches on-chain.
	// Used when an executor challenges a TA for payload mismatch.
	// Voters will: Compare TA's payload hash vs MsgStartInference.prompt_hash
	VerifyPromptHashMatch VerificationType = "prompt_hash_match"

	// VerifyResponseHashMatch indicates voters should verify the executor's response hash matches on-chain.
	// Used when a TA challenges an executor for response hash mismatch.
	// Voters will: Compare executor's payload hash vs MsgFinishInference.response_hash
	VerifyResponseHashMatch VerificationType = "response_hash_match"
)

// String returns a human-readable representation of the VerificationType.
func (v VerificationType) String() string {
	return string(v)
}

// IsValid returns true if the VerificationType is a recognized value.
func (v VerificationType) IsValid() bool {
	switch v {
	case VerifyPayloadFromTA,
		VerifyPayloadFromExecutor,
		VerifyMsgStartExists,
		VerifyMsgFinishExists,
		VerifyPromptHashMatch,
		VerifyResponseHashMatch:
		return true
	default:
		return false
	}
}

// VoteType represents the outcome of a node's verification of Respondent behavior.
type VoteType int

const (
	// VotePositive indicates the Respondent responded honestly with valid data.
	VotePositive VoteType = 1
	// VoteNegative indicates the Respondent did not respond or was dishonest.
	VoteNegative VoteType = 2
	// (Placeholder for future errors)
	VoteInvalid VoteType = 3
)

// String returns a human-readable representation of the VoteType.
func (v VoteType) String() string {
	switch v {
	case VotePositive:
		return "positive"
	case VoteNegative:
		return "negative"
	case VoteInvalid:
		return "invalid"
	default:
		return "unknown"
	}
}

// IsValid returns true if the VoteType is a recognized value.
func (v VoteType) IsValid() bool {
	return v == VotePositive || v == VoteNegative || v == VoteInvalid
}

// VotingOutcome represents the final result of all votes.
type VotingOutcome int

const (
	// OutcomePositive indicates that at least one node voted positively for the inference and got the payload.
	OutcomePositive VotingOutcome = 1
	// OutcomeNegative indicates that at all nodes voted negatively for the inference and did not get the payload.
	OutcomeNegative VotingOutcome = 2
	// OutcomeInconclusive for cases, when the voting went wrong
	OutcomeInconclusive VotingOutcome = 3
)

// String returns a human-readable representation of the VotingOutcome.
func (o VotingOutcome) String() string {
	switch o {
	case OutcomePositive:
		return "positive"
	case OutcomeNegative:
		return "negative"
	case OutcomeInconclusive:
		return "inconclusive"
	default:
		return "unknown"
	}
}

// IsValid returns true if the VotingOutcome is a recognized value.
func (o VotingOutcome) IsValid() bool {
	return o == OutcomePositive || o == OutcomeNegative || o == OutcomeInconclusive
}

// SignedVote contains a node's vote with cryptographic proof.
// Each vote is signed by the voting node to ensure authenticity and prevent tampering.
type SignedVote struct {
	// ID of the inference being disputed.
	InferenceId string `json:"inference_id"`

	// VoterAddress is the blockchain address of the node casting the vote.
	// Will be used to verify the signature of the vote.
	VoterAddress string `json:"voter_address"`

	// VoteType indicates the node's assessment of the Respondent's behavior.
	VoteType VoteType `json:"vote_type"`

	// RespondentDataHash is the signed hash of data the Respondent provided (if any).
	// Empty if the Respondent did not respond.
	RespondentDataHash string `json:"respondent_data_hash,omitempty"`

	// Timestamp is the Unix timestamp when the vote was cast.
	Timestamp int64 `json:"timestamp"`

	// VoterSignature is the cryptographic signature of the vote.
	// Signs: inference_id + voter_address + vote_type + respondent_data_hash + timestamp
	// Used for on-chain verification of vote authenticity.
	VoterSignature string `json:"voter_signature"`
}

// VoteResponse represents a node's response to a verification request.
type VoteResponse struct {
	// Vote is the signed vote from the node.
	Vote SignedVote `json:"vote"`

	// Error is any error that occurred during verification.
	// If set, the vote may not be valid.
	Error string `json:"error,omitempty"`
}

// VotingInitiateRequest represents a request from a Challenger to initiate a vote/dispute against a Respondent.
//
// VerificationType determines how voters should verify the disputed inference.
//
// Voters may combine on-chain verification and off-chain checks (e.g., payload availability).
type VotingInitiateRequest struct {
	InferenceId string `json:"inference_id"`

	// ChallengerAddress is the address of the node initiating the dispute.
	ChallengerAddress string `json:"challenger_address"`

	// ChallengerRole identifies who is initiating the dispute.
	// Determines what proof is required and what verification voters perform.
	ChallengerRole ChallengerRole `json:"challenger_role"`

	// VerificationType specifies what kind of verification voters should perform.
	// This is required so that all voters know exactly what check to do,
	// and it allows disambiguation between dispute types (assignment, payload, completion, etc).
	VerificationType VerificationType `json:"verification_type"`

	// RespondentAddress is the address of the node being disputed.
	RespondentAddress string `json:"respondent_address"`

	// AssignmentProof contains cryptographic proof of the inference assignment.
	// The TransferSignature alone proves TA assigned this inference to the executor.
	AssignmentProof *AssignmentProof `json:"assignment_proof,omitempty"`

	// Reason describes why the Challenger is disputing the Respondent.
	Reason string `json:"reason,omitempty"`

	// ChallengerSignature is the Challenger's signature on this request.
	// Signs: inference_id + challenger_address + respondent_address + timestamp
	ChallengerSignature string `json:"challenger_signature"`

	// Timestamp is when the dispute was initiated (Unix timestamp in nanoseconds).
	Timestamp int64 `json:"timestamp"`
}

// VotingResult represents the aggregated result of a voting session.
// TODO! Use this for negative or failed voting cases.
type VotingResult struct {
	// ID of the inference that was disputed.
	InferenceId string `json:"inference_id"`

	// Votes contains all the signed votes collected.
	Votes []SignedVote `json:"votes"`

	// Outcome is the aggregated voting outcome.
	Outcome VotingOutcome `json:"outcome"`

	// NegativeCount is the number of negative votes.
	NegativeCount int `json:"negative_count"`

	// InvalidCount is the number of invalid votes (Respondent served invalid data).
	InvalidCount int `json:"invalid_count"`

	// CompletedAt is the timestamp when voting completed.
	CompletedAt int64 `json:"completed_at"`
}

// VotingConfig holds configuration parameters for the voting mechanism.
type VotingConfig struct {
	// MaxNumNodes is the maximum number of nodes to select for voting until one of them votes positively
	// or all of them vote negatively.
	MaxNumNodes int `json:"max_num_nodes"`

	// VoteTimeout is the maximum time to wait for a node's vote (in ms).
	VoteTimeout int `json:"vote_timeout"`

	// MaxRetries is the maximum number of times to retry contacting a node.
	MaxRetries int `json:"max_retries"`
}
