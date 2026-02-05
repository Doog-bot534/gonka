package propagation

import (
	"context"
	"errors"
)

var (
	ErrBundleNotFound  = errors.New("bundle not found")
	ErrProofsNotFound  = errors.New("proofs not found")
	ErrArrivalNotFound = errors.New("first arrival not found")
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

	StoreFirstArrival(ctx context.Context, participant string, pocHeight int64, arrivalTime int64, count uint32) error
	GetFirstArrival(ctx context.Context, participant string, pocHeight int64) (ArrivalInfo, error)
	GetAllFirstArrivals(ctx context.Context, pocHeight int64) (map[string]ArrivalInfo, error)

	StoreObservation(ctx context.Context, obs FirstArrivalObservation) error
	GetObservations(ctx context.Context, pocHeight int64) ([]FirstArrivalObservation, error)

	Close() error
}

type participantPocKey struct {
	Participant string
	PocHeight   int64
}

type observationKey struct {
	ValidatorAddress string
	PocHeight        int64
}
