package storage

import "subnet/types"

// Storage persists subnet session state and diffs.
type Storage interface {
	CreateSession(escrowID string, config types.SessionConfig, group []types.SlotAssignment, balance uint64) error
	AppendDiff(escrowID string, rec types.DiffRecord) error
	AddSignature(escrowID string, nonce uint64, slotID uint32, sig []byte) error
	GetSignatures(escrowID string, nonce uint64) (map[uint32][]byte, error)
	GetState(escrowID string) (*types.EscrowState, error)
	GetDiffs(escrowID string, fromNonce, toNonce uint64) ([]types.DiffRecord, error)
	MarkFinalized(escrowID string, nonce uint64) error
	LastFinalized(escrowID string) (uint64, error)
}
