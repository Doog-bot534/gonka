package pocstorage

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
)

func computeBatchHash(nonces []ArtifactV2) string {
	// Hash is computed deterministically over the sorted nonce list to avoid order sensitivity.
	sorted := SortArtifactsDeterministically(nonces)

	h := sha256.New()
	var nonceBuf [8]byte
	for _, n := range sorted {
		binary.BigEndian.PutUint64(nonceBuf[:], uint64(n.Nonce))
		_, _ = h.Write(nonceBuf[:])

		// Prefer hashing raw vector bytes for compatibility with legacy storage.
		// If decoding fails, fall back to hashing the literal string bytes (still deterministic).
		if b, err := base64.StdEncoding.DecodeString(n.VectorB64); err == nil {
			_, _ = h.Write(b)
		} else {
			_, _ = h.Write([]byte(n.VectorB64))
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func computeRollingHash(prevHashHex string, batchHashHex string, newAmount int64) (string, error) {
	prev, err := decodeHexOrEmpty(prevHashHex)
	if err != nil {
		return "", fmt.Errorf("decode prev hash: %w", err)
	}
	batch, err := decodeHexOrEmpty(batchHashHex)
	if err != nil {
		return "", fmt.Errorf("decode batch hash: %w", err)
	}

	h := sha256.New()
	_, _ = h.Write(prev)
	_, _ = h.Write(batch)

	var amtBuf [8]byte
	binary.BigEndian.PutUint64(amtBuf[:], uint64(newAmount))
	_, _ = h.Write(amtBuf[:])

	return hex.EncodeToString(h.Sum(nil)), nil
}

func decodeHexOrEmpty(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	return hex.DecodeString(s)
}
