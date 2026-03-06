package state

import (
	"crypto/sha256"

	"subnet/types"
)

// SettlementPayload contains the data needed for on-chain settlement.
type SettlementPayload struct {
	StateRoot  []byte
	RestHash   []byte
	HostStats  map[uint32]*types.HostStats
	Signatures map[uint32][]byte
	Nonce      uint64
}

// BuildSettlement constructs a SettlementPayload from the final escrow state.
func BuildSettlement(st types.EscrowState, signatures map[uint32][]byte, nonce uint64) (*SettlementPayload, error) {
	hostStatsHash, err := ComputeHostStatsHash(st.HostStats)
	if err != nil {
		return nil, err
	}

	restHash, err := ComputeRestHash(st.Balance, st.Inferences)
	if err != nil {
		return nil, err
	}

	h := sha256.New()
	h.Write(hostStatsHash)
	h.Write(restHash)
	stateRoot := h.Sum(nil)

	return &SettlementPayload{
		StateRoot:  stateRoot,
		RestHash:   restHash,
		HostStats:  st.HostStats,
		Signatures: signatures,
		Nonce:      nonce,
	}, nil
}
