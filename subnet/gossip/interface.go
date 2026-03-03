package gossip

// GossipClient notifies peers about subnet state changes.
// Phase 1: interface only, no implementation.
type GossipClient interface {
	NotifyNonce(escrowID string, nonce uint64, stateHash []byte, stateSig []byte, senderSlot uint32) error
}
