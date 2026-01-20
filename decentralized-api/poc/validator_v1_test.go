package poc

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSampleNoncesV1_AllNonces(t *testing.T) {
	nonces := []int64{1, 2, 3, 4, 5}
	dist := []float64{0.1, 0.2, 0.3, 0.4, 0.5}

	// Request more samples than available
	result := sampleNoncesV1("pubkey", "blockhash", 100, nonces, dist, 10)

	assert.Equal(t, nonces, result.nonces)
	assert.Equal(t, dist, result.dist)
}

func TestSampleNoncesV1_SampledSubset(t *testing.T) {
	nonces := make([]int64, 100)
	dist := make([]float64, 100)
	for i := 0; i < 100; i++ {
		nonces[i] = int64(i)
		dist[i] = float64(i) * 0.01
	}

	// Request 10 samples from 100
	result := sampleNoncesV1("pubkey", "blockhash", 100, nonces, dist, 10)

	assert.Len(t, result.nonces, 10)
	assert.Len(t, result.dist, 10)

	// All sampled nonces should be within original range
	for _, n := range result.nonces {
		assert.True(t, n >= 0 && n < 100)
	}
}

func TestSampleNoncesV1_Deterministic(t *testing.T) {
	nonces := make([]int64, 100)
	dist := make([]float64, 100)
	for i := 0; i < 100; i++ {
		nonces[i] = int64(i)
		dist[i] = float64(i)
	}

	// Same inputs should produce same outputs
	result1 := sampleNoncesV1("pubkey", "blockhash", 100, nonces, dist, 10)
	result2 := sampleNoncesV1("pubkey", "blockhash", 100, nonces, dist, 10)

	assert.Equal(t, result1.nonces, result2.nonces)
	assert.Equal(t, result1.dist, result2.dist)

	// Different pubkey should produce different samples
	result3 := sampleNoncesV1("different", "blockhash", 100, nonces, dist, 10)
	assert.NotEqual(t, result1.nonces, result3.nonces)
}

func TestDeterministicSampleIndicesV1_AllIndices(t *testing.T) {
	indices := deterministicSampleIndicesV1("pk", "hash", 100, 50, 20)

	// Should return all indices when requesting more than available
	assert.Len(t, indices, 20)
	for i, idx := range indices {
		assert.Equal(t, i, idx)
	}
}

func TestDeterministicSampleIndicesV1_Subset(t *testing.T) {
	indices := deterministicSampleIndicesV1("pk", "hash", 100, 10, 100)

	assert.Len(t, indices, 10)

	// All indices should be unique and within range
	seen := make(map[int]bool)
	for _, idx := range indices {
		assert.False(t, seen[idx], "duplicate index found")
		assert.True(t, idx >= 0 && idx < 100)
		seen[idx] = true
	}
}

func TestDeterministicSampleIndicesV1_DifferentSeeds(t *testing.T) {
	// Same seed (pk, hash, height) should produce same result
	indices1 := deterministicSampleIndicesV1("pk", "hash", 100, 10, 100)
	indices2 := deterministicSampleIndicesV1("pk", "hash", 100, 10, 100)
	assert.Equal(t, indices1, indices2)

	// Different height should produce different result
	indices3 := deterministicSampleIndicesV1("pk", "hash", 101, 10, 100)
	assert.NotEqual(t, indices1, indices3)

	// Different hash should produce different result
	indices4 := deterministicSampleIndicesV1("pk", "different", 100, 10, 100)
	assert.NotEqual(t, indices1, indices4)
}

func TestValidationConfigDefaults(t *testing.T) {
	config := DefaultValidationConfig()

	assert.Equal(t, 10, config.WorkerCount)
	assert.NotZero(t, config.RequestTimeout)
	assert.Equal(t, 3, config.MaxRetries)
	assert.NotZero(t, config.RetryBackoff)
}
