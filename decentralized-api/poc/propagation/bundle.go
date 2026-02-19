package propagation

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/cometbft/cometbft/crypto/ed25519"
)

// BundleHeader represents the metadata for a bundle of artifacts.
//
// Memory layout (64-bit architecture):
// - BundleID [4]byte: 4 bytes + 4 bytes padding
// - Participant string: 16 bytes (8-byte pointer + 8-byte length)
// - PocHeight int64: 8 bytes
// - RootHash [32]byte: 32 bytes
// - Count uint32: 4 bytes + 4 bytes padding
// - CreatedAt int64: 8 bytes
// - Signature [64]byte: 64 bytes
// Total struct size: 144 bytes
//
// Variable data:
// - Participant: ~43 bytes (bech32 address with "gonka" prefix, e.g., "gonka1abcdef...")
//
// Total memory per BundleHeader: ~187 bytes (144 + 43)
type BundleHeader struct {
	BundleID    [4]byte
	Participant string
	PocHeight   int64
	RootHash    [32]byte
	Count       uint32
	CreatedAt   int64
	Signature   [64]byte
}

type bundleHeaderJSON struct {
	BundleID    string `json:"bundle_id"`
	Participant string `json:"participant"`
	PocHeight   int64  `json:"poc_height"`
	RootHash    string `json:"root_hash"`
	Count       uint32 `json:"count"`
	CreatedAt   int64  `json:"created_at"`
	Signature   string `json:"signature"`
}

// Encoded bundle header is ~336 bytes due to hex expansion of BundleID/RootHash/Signature plus JSON field names and the 43-byte participant string.
func (h BundleHeader) MarshalJSON() ([]byte, error) {
	return json.Marshal(bundleHeaderJSON{
		BundleID:    hex.EncodeToString(h.BundleID[:]),
		Participant: h.Participant,
		PocHeight:   h.PocHeight,
		RootHash:    hex.EncodeToString(h.RootHash[:]),
		Count:       h.Count,
		CreatedAt:   h.CreatedAt,
		Signature:   hex.EncodeToString(h.Signature[:]),
	})
}

func (h *BundleHeader) UnmarshalJSON(data []byte) error {
	var j bundleHeaderJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return err
	}

	bundleIDBytes, err := hex.DecodeString(j.BundleID)
	if err != nil {
		return err
	}
	if len(bundleIDBytes) != 4 {
		return errors.New("bundle_id must be 4 bytes")
	}
	copy(h.BundleID[:], bundleIDBytes)

	rootHashBytes, err := hex.DecodeString(j.RootHash)
	if err != nil {
		return err
	}
	if len(rootHashBytes) != 32 {
		return errors.New("root_hash must be 32 bytes")
	}
	copy(h.RootHash[:], rootHashBytes)

	signatureBytes, err := hex.DecodeString(j.Signature)
	if err != nil {
		return err
	}
	if len(signatureBytes) != 64 {
		return errors.New("signature must be 64 bytes")
	}
	copy(h.Signature[:], signatureBytes)

	h.Participant = j.Participant
	h.PocHeight = j.PocHeight
	h.Count = j.Count
	h.CreatedAt = j.CreatedAt

	return nil
}

func MakeBundleID(participant string, pocHeight int64, rootHash []byte, count uint32) [4]byte {
	h := sha256.New()
	h.Write([]byte(participant))
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(pocHeight))
	h.Write(buf[:])
	h.Write(rootHash)
	binary.BigEndian.PutUint32(buf[:4], count)
	h.Write(buf[:4])
	hash := sha256.Sum256(h.Sum(nil))
	var bundleID [4]byte
	copy(bundleID[:], hash[:4])
	return bundleID
}

type HeaderSigner interface {
	Sign(msg []byte) ([]byte, error)
}

func SignHeader(h BundleHeader, privKey []byte) ([]byte, error) {
	if len(privKey) != 64 {
		return nil, errors.New("invalid ed25519 private key length")
	}
	msg := headerSigningBytes(h)
	key := ed25519.PrivKey(privKey)
	return key.Sign(msg)
}

func SignHeaderWith(h BundleHeader, signer HeaderSigner) ([]byte, error) {
	return signer.Sign(headerSigningBytes(h))
}

func VerifyHeader(h BundleHeader, base64PubKey string) error {
	pubKeyBytes, err := base64.StdEncoding.DecodeString(base64PubKey)
	if err != nil {
		return err
	}

	if len(pubKeyBytes) != 32 {
		return errors.New("invalid ed25519 public key length")
	}

	msg := headerSigningBytes(h)
	pubKey := ed25519.PubKey(pubKeyBytes)

	if !pubKey.VerifySignature(msg, h.Signature[:]) {
		return errors.New("signature verification failed")
	}

	return nil
}

func headerSigningBytes(h BundleHeader) []byte {
	buf := bytes.NewBuffer(nil)
	buf.Write(h.BundleID[:])
	buf.WriteString(h.Participant)
	var tmp [8]byte
	binary.BigEndian.PutUint64(tmp[:], uint64(h.PocHeight))
	buf.Write(tmp[:])
	buf.Write(h.RootHash[:])
	binary.BigEndian.PutUint32(tmp[:4], h.Count)
	buf.Write(tmp[:4])
	binary.BigEndian.PutUint64(tmp[:], uint64(h.CreatedAt))
	buf.Write(tmp[:])
	return buf.Bytes()
}
