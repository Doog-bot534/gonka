package types

import "errors"

var (
	ErrInvalidNonce          = errors.New("invalid nonce: must be sequential")
	ErrInvalidUserSig        = errors.New("invalid user signature")
	ErrInvalidProposerSig    = errors.New("invalid proposer signature: not a group member")
	ErrInvalidExecutorSig    = errors.New("invalid executor signature")
	ErrInsufficientBalance   = errors.New("insufficient escrow balance")
	ErrMultipleStartMsgs     = errors.New("at most one MsgStartInference per diff")
	ErrSessionFinalizing     = errors.New("session is finalizing: no new inferences")
	ErrInferenceNotFound     = errors.New("inference not found")
	ErrInvalidTransition     = errors.New("invalid status transition")
	ErrSelfValidation        = errors.New("validator cannot be the executor")
	ErrWrongExecutorSlot     = errors.New("executor_slot does not match assigned executor")
	ErrActualCostExceedsMax  = errors.New("actual cost exceeds reserved cost")
	ErrInsufficientVotes     = errors.New("insufficient votes for timeout")
	ErrInvalidTimeoutReason  = errors.New("timeout reason does not match inference status")
	ErrDuplicateVote         = errors.New("duplicate vote from slot")
	ErrAlreadyFinalizing     = errors.New("session is already finalizing")
	ErrEmptyTx               = errors.New("subnet tx has no message set")
	ErrDuplicateInferenceID  = errors.New("duplicate inference ID")
	ErrInvalidVoteSig        = errors.New("invalid vote signature")
)
