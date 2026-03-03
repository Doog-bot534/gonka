package storage

import "subnet/types"

// Storage persists subnet session state and diffs.
type Storage interface {
	CreateSession(escrowID string, group []types.SlotAssignment, balance uint64) error
	AppendDiff(escrowID string, rec types.DiffRecord) error
	AddSignature(escrowID string, nonce uint64, slotID uint32, sig []byte) error
	GetState(escrowID string) (*types.EscrowState, error)
	GetDiffs(escrowID string, fromNonce, toNonce uint64) ([]types.DiffRecord, error)
}
