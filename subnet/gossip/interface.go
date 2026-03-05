package gossip

import (
	"context"

	"subnet/types"
)

// PeerClient sends gossip messages to a single peer.
type PeerClient interface {
	GossipNonce(ctx context.Context, nonce uint64, stateHash, stateSig []byte, slotID uint32) error
	GossipTxs(ctx context.Context, txs []*types.SubnetTx) error
}
