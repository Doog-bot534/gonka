package propagation

import (
	"fmt"

	propagationpb "decentralized-api/poc/propagation/proto"
)

func HeaderToProto(h BundleHeader) *propagationpb.PropagationHeader {
	rootHash := make([]byte, 32)
	copy(rootHash, h.RootHash[:])
	sig := make([]byte, 64)
	copy(sig, h.Signature[:])
	bundleID := make([]byte, 4)
	copy(bundleID, h.BundleID[:])

	return &propagationpb.PropagationHeader{
		BundleId:    bundleID,
		Participant: h.Participant,
		PocHeight:   h.PocHeight,
		RootHash:    rootHash,
		Count:       h.Count,
		CreatedAt:   h.CreatedAt,
		Signature:   sig,
	}
}

func ProtoToHeader(p *propagationpb.PropagationHeader) (BundleHeader, error) {
	if len(p.BundleId) != 4 {
		return BundleHeader{}, fmt.Errorf("bundle_id must be 4 bytes, got %d", len(p.BundleId))
	}
	if len(p.RootHash) != 32 {
		return BundleHeader{}, fmt.Errorf("root_hash must be 32 bytes, got %d", len(p.RootHash))
	}
	if len(p.Signature) != 64 {
		return BundleHeader{}, fmt.Errorf("signature must be 64 bytes, got %d", len(p.Signature))
	}

	var h BundleHeader
	copy(h.BundleID[:], p.BundleId)
	h.Participant = p.Participant
	h.PocHeight = p.PocHeight
	copy(h.RootHash[:], p.RootHash)
	h.Count = p.Count
	h.CreatedAt = p.CreatedAt
	copy(h.Signature[:], p.Signature)
	return h, nil
}
