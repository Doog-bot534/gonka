package propagation

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
)

type BundleHeader struct {
	BundleID     [32]byte
	Participant  string
	PocHeight    int64
	PocBlockHash []byte
	RootHash     []byte
	Count        uint32
	Version      uint32
	CreatedAt    int64
	Signature    []byte
}

// bundleHeaderJSON is used for JSON marshaling/unmarshaling
type bundleHeaderJSON struct {
	BundleID     string `json:"bundle_id"`
	Participant  string `json:"participant"`
	PocHeight    int64  `json:"poc_height"`
	PocBlockHash string `json:"poc_block_hash"`
	RootHash     string `json:"root_hash"`
	Count        uint32 `json:"count"`
	Version      uint32 `json:"version"`
	CreatedAt    int64  `json:"created_at"`
	Signature    string `json:"signature"`
}

func (h BundleHeader) MarshalJSON() ([]byte, error) {
	return json.Marshal(bundleHeaderJSON{
		BundleID:     hex.EncodeToString(h.BundleID[:]),
		Participant:  h.Participant,
		PocHeight:    h.PocHeight,
		PocBlockHash: hex.EncodeToString(h.PocBlockHash),
		RootHash:     hex.EncodeToString(h.RootHash),
		Count:        h.Count,
		Version:      h.Version,
		CreatedAt:    h.CreatedAt,
		Signature:    hex.EncodeToString(h.Signature),
	})
}

func (h *BundleHeader) UnmarshalJSON(data []byte) error {
	var j bundleHeaderJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return err
	}

	// Decode bundle_id
	bundleIDBytes, err := hex.DecodeString(j.BundleID)
	if err != nil {
		return err
	}
	if len(bundleIDBytes) != 32 {
		return errors.New("bundle_id must be 32 bytes")
	}
	copy(h.BundleID[:], bundleIDBytes)

	// Decode poc_block_hash
	h.PocBlockHash, err = hex.DecodeString(j.PocBlockHash)
	if err != nil {
		return err
	}

	// Decode root_hash
	h.RootHash, err = hex.DecodeString(j.RootHash)
	if err != nil {
		return err
	}

	// Decode signature
	h.Signature, err = hex.DecodeString(j.Signature)
	if err != nil {
		return err
	}

	h.Participant = j.Participant
	h.PocHeight = j.PocHeight
	h.Count = j.Count
	h.Version = j.Version
	h.CreatedAt = j.CreatedAt

	return nil
}

func MakeBundleID(participant string, pocHeight int64, rootHash []byte, count uint32, version uint32) [32]byte {
	h := sha256.New()
	h.Write([]byte(participant))
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(pocHeight))
	h.Write(buf[:])
	h.Write(rootHash)
	binary.BigEndian.PutUint32(buf[:4], count)
	h.Write(buf[:4])
	binary.BigEndian.PutUint32(buf[:4], version)
	h.Write(buf[:4])
	return sha256.Sum256(h.Sum(nil))
}

type HeaderSigner interface {
	Sign(msg []byte) ([]byte, error)
}

func SignHeader(h BundleHeader, privKey []byte) ([]byte, error) {
	if len(privKey) != 32 {
		return nil, errors.New("invalid private key length")
	}
	key := &secp256k1.PrivKey{Key: privKey}
	msg := headerSigningBytes(h)
	return key.Sign(msg)
}

func SignHeaderWith(h BundleHeader, signer HeaderSigner) ([]byte, error) {
	return signer.Sign(headerSigningBytes(h))
}

func VerifyHeader(h BundleHeader, hexPubKey string) error {
	if h.Signature == nil {
		return errors.New("signature missing")
	}
	pubKeyBytes, err := hex.DecodeString(hexPubKey)
	if err != nil {
		return err
	}
	pubKey := &secp256k1.PubKey{Key: pubKeyBytes}
	msg := headerSigningBytes(h)
	if !pubKey.VerifySignature(msg, h.Signature) {
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
	buf.Write(h.PocBlockHash)
	buf.Write(h.RootHash)
	binary.BigEndian.PutUint32(tmp[:4], h.Count)
	buf.Write(tmp[:4])
	binary.BigEndian.PutUint32(tmp[:4], h.Version)
	buf.Write(tmp[:4])
	binary.BigEndian.PutUint64(tmp[:], uint64(h.CreatedAt))
	buf.Write(tmp[:])
	return buf.Bytes()
}
