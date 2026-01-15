package mlnodeclient

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand"
)

type ProofBatch struct {
	PublicKey   string    `json:"public_key"`
	BlockHash   string    `json:"block_hash"`
	BlockHeight int64     `json:"block_height"`
	Nonces      []int64   `json:"nonces"`
	Dist        []float64 `json:"dist"`
	NodeNum     uint64    `json:"node_id"`
}

type ValidatedBatch struct {
	ProofBatch // Inherits from ProofBatch

	// New fields
	ReceivedDist      []float64 `json:"received_dist"`
	RTarget           float64   `json:"r_target"`
	FraudThreshold    float64   `json:"fraud_threshold"`
	NInvalid          int64     `json:"n_invalid"`
	ProbabilityHonest float64   `json:"probability_honest"`
	FraudDetected     bool      `json:"fraud_detected"`
}

func (pb ProofBatch) SampleNoncesToValidate(
	validatorPublicKey string,
	nNonces int64,
	samplingBlockHash string,
) ProofBatch {
	totalNonces := int64(len(pb.Nonces))
	if nNonces >= totalNonces {
		return pb
	}

	nonceIndexes := deterministicSampleIndices(
		validatorPublicKey,
		samplingBlockHash,
		pb.BlockHeight,
		nNonces,
		totalNonces,
	)

	sampledNonces := make([]int64, nNonces)
	sampledDist := make([]float64, nNonces)

	for i, idx := range nonceIndexes {
		sampledNonces[i] = pb.Nonces[idx]
		sampledDist[i] = pb.Dist[idx]
	}

	return ProofBatch{
		PublicKey:   pb.PublicKey,
		BlockHash:   pb.BlockHash,
		BlockHeight: pb.BlockHeight,
		Nonces:      sampledNonces,
		Dist:        sampledDist,
	}
}

func deterministicSampleIndices(
	validatorPublicKey string,
	blockHash string,
	blockHeight int64,
	nSamples int64,
	totalItems int64,
) []int {
	if nSamples >= totalItems {
		indices := make([]int, totalItems)
		for i := int64(0); i < totalItems; i++ {
			indices[i] = int(i)
		}
		return indices
	}

	seedInput := fmt.Sprintf("%s:%s:%d", validatorPublicKey, blockHash, blockHeight)
	hash := sha256.Sum256([]byte(seedInput))
	seed := int64(binary.BigEndian.Uint64(hash[:8]))

	source := rand.NewSource(seed)
	rng := rand.New(source)
	indices := rng.Perm(int(totalItems))[:nSamples]

	return indices
}
