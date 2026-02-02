package propagation

import (
	"context"
	"errors"
)

var (
	ErrBundleNotFound = errors.New("bundle not found")
	ErrProofsNotFound = errors.New("proofs not found")
)

type ProofItem struct {
	LeafIndex   uint32   `json:"leaf_index"`
	NonceValue  int32    `json:"nonce_value"`
	VectorBytes string   `json:"vector_bytes"`
	Proof       []string `json:"proof"`
}

type BundleStorage interface {
	StoreHeader(ctx context.Context, h BundleHeader) error
	GetHeader(ctx context.Context, bundleID [32]byte) (BundleHeader, error)
	LatestBundle(ctx context.Context, participant string, pocHeight int64) (BundleHeader, error)
	AllBundlesForHeight(ctx context.Context, pocHeight int64) ([]BundleHeader, error)

	StoreProofs(ctx context.Context, bundleID [32]byte, proofs []ProofItem) error
	GetProofs(ctx context.Context, bundleID [32]byte) ([]ProofItem, error)

	Close() error
}
