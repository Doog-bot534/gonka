package pocstorage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

var ErrNotFound = errors.New("poc record not found")

// PoCParamsModel is the v2 PoC params model (mirrors the mlnode /init/generate schema).
type PoCParamsModel struct {
	Model  string `json:"model"`
	SeqLen int    `json:"seq_len"`
	KDim   int    `json:"k_dim"`
}

// ArtifactV2 is the v2 artifact element, matching the mlnodeclient callback format:
// - nonce is int64
// - vector is base64-encoded (vector_b64)
type ArtifactV2 struct {
	Nonce     int64  `json:"nonce"`
	VectorB64 string `json:"vector_b64"`
}

// UnmarshalJSON supports both "vector_b64" (new) and "vector" (legacy storage field)
// where "vector" was previously serialized from []byte (which encoding/json encodes as base64 string).
func (a *ArtifactV2) UnmarshalJSON(data []byte) error {
	aux := struct {
		Nonce     int64  `json:"nonce"`
		VectorB64 string `json:"vector_b64"`
		Vector    string `json:"vector"`
	}{}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	a.Nonce = aux.Nonce
	if aux.VectorB64 != "" {
		a.VectorB64 = aux.VectorB64
	} else {
		a.VectorB64 = aux.Vector
	}
	return nil
}

// PoCRun represents a PoC run anchored at a specific block height.
type PoCRun struct {
	BlockHeight      int64          `json:"block_height"`
	EpochLength      int64          `json:"epoch_length"`
	BlockHash        string         `json:"block_hash"`
	BlockTime        time.Time      `json:"block_time"`
	DurationSeconds  int64          `json:"duration"`
	FrequencySeconds int64          `json:"frequency"`
	BatchSize        int            `json:"batch_size"`
	Params           PoCParamsModel `json:"params"`
	InterruptedTime  *time.Time     `json:"interrupted_time,omitempty"`
	CreatedAt        time.Time      `json:"created_at"`
}

// PoCBatchesGeneratedRecord is a single received mlnode emission for a PoC run.
// Each emission is stored as a separate record (per spec).
type PoCBatchesGeneratedRecord struct {
	BlockHeight    int64        `json:"block_height"`
	Address        string       `json:"address"`
	PublicKey      string       `json:"public_key"`
	BlockHash      string       `json:"block_hash"`
	NodeID         string       `json:"node_id"`
	Model          string       `json:"model"`
	Amount         int64        `json:"amount"`
	Hash           string       `json:"hash"`
	TimeSinceBlock int64        `json:"time_since_block"`
	Artifacts      []ArtifactV2 `json:"artifacts"`
	ReceivedAt     time.Time    `json:"received_at"`
}

// UnmarshalJSON supports both "artifacts" (new name) and "nonces" (legacy name) for backwards compatibility.
func (r *PoCBatchesGeneratedRecord) UnmarshalJSON(data []byte) error {
	type Alias PoCBatchesGeneratedRecord
	aux := struct {
		Alias
		Artifacts []ArtifactV2 `json:"artifacts"`
		Nonces    []ArtifactV2 `json:"nonces"`
	}{}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*r = PoCBatchesGeneratedRecord(aux.Alias)
	if len(aux.Artifacts) > 0 {
		r.Artifacts = aux.Artifacts
	} else {
		r.Artifacts = aux.Nonces
	}
	return nil
}

// PoCMlnodeState stores the rolling aggregate state for a specific (run, participant, node, model).
// This enables O(1) updates of Amount/Hash on each new mlnode emission.
type PoCMlnodeState struct {
	BlockHeight int64     `json:"block_height"`
	Address     string    `json:"address"`
	NodeID      string    `json:"node_id"`
	Model       string    `json:"model"`
	Amount      int64     `json:"amount"`
	Hash        string    `json:"hash"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// PoCStorage provides persistence for PoC v2 offchain state.
// Backends should mirror payload storage (file and/or Postgres).
type PoCStorage interface {
	// UpsertRun stores the PoC run metadata keyed by BlockHeight.
	UpsertRun(ctx context.Context, run PoCRun) error

	// MarkInterrupted sets interrupted_time for a run (best-effort if the run exists).
	MarkInterrupted(ctx context.Context, blockHeight int64, interruptedAt time.Time) error

	// GetLatestRun returns the latest stored PoC run by block height.
	GetLatestRun(ctx context.Context) (PoCRun, error)

	// GetClosestRunAtOrBefore returns the run with the greatest block_height <= target.
	GetClosestRunAtOrBefore(ctx context.Context, blockHeight int64) (PoCRun, error)

	// StoreGeneratedRecord stores a single mlnode emission for a given PoC run.
	// Implementations MUST compute and set rec.Amount and rec.Hash based on the rolling per-mlnode state,
	// so callers do not need to reread historical records.
	StoreGeneratedRecord(ctx context.Context, rec PoCBatchesGeneratedRecord) (PoCBatchesGeneratedRecord, error)

	// ListGeneratedRecords returns all stored mlnode emissions for a given PoC run.
	ListGeneratedRecords(ctx context.Context, blockHeight int64) ([]PoCBatchesGeneratedRecord, error)
}

// SortArtifactsDeterministically returns a copy of artifacts sorted by Nonce.
// This makes hashing stable even if emission ordering varies.
func SortArtifactsDeterministically(artifacts []ArtifactV2) []ArtifactV2 {
	out := make([]ArtifactV2, len(artifacts))
	copy(out, artifacts)
	sort.Slice(out, func(i, j int) bool { return out[i].Nonce < out[j].Nonce })
	return out
}

// SortNoncesDeterministically is kept for compatibility (artifacts were previously called nonces).
func SortNoncesDeterministically(nonces []ArtifactV2) []ArtifactV2 {
	return SortArtifactsDeterministically(nonces)
}

func validateRecordKey(rec PoCBatchesGeneratedRecord) error {
	if rec.BlockHeight <= 0 {
		return fmt.Errorf("block_height must be > 0")
	}
	if rec.Address == "" {
		return fmt.Errorf("address is required")
	}
	if rec.NodeID == "" {
		return fmt.Errorf("node_id is required")
	}
	if rec.Model == "" {
		return fmt.Errorf("model is required")
	}
	return nil
}
