package signing

import (
	"crypto/sha256"
	"fmt"

	"github.com/ethereum/go-ethereum/crypto"
	blst "github.com/supranational/blst/bindings/go"
)

var (
	// See https://www.ietf.org/archive/id/draft-irtf-cfrg-bls-signature-05.html#section-4.2.1
	BlsDst      = []byte("BLS_SIG_BLS12381G2_XMD:SHA-256_SSWU_RO_NUL_")
	KeyGenDstV1 = []byte("GONKA-SUBNET-BLS-KEYGEN-V1-")
)

// BLS12_381Signer signs messages using a BLS12-381 private key.
// The key is derived from the ECDSA key.
type BLS12_381Signer struct {
	blsKey      *blst.SecretKey
	ecdsaSigner *Secp256k1Signer
}

func NewBLS12_381Signer(blsKey *blst.SecretKey, ecdsaSigner *Secp256k1Signer) *BLS12_381Signer {
	return &BLS12_381Signer{
		blsKey,
		ecdsaSigner,
	}
}

func Secp256k1ToBLS(ecdsaSigner *Secp256k1Signer) *BLS12_381Signer {
	secpPriv := crypto.FromECDSA(ecdsaSigner.key)
	blsKey := blst.KeyGen(secpPriv, KeyGenDstV1)
	return NewBLS12_381Signer(blsKey, ecdsaSigner)
}

func GenerateBLS12_381Key() (*BLS12_381Signer, error) {
	ecdsa, err := GenerateKey()
	if err != nil {
		return nil, err
	}

	return Secp256k1ToBLS(ecdsa), nil
}

func AggregateSignatures(sigBytes [][]byte) (*blst.P2Affine, error) {
	sigs := make([]*blst.P2Affine, len(sigBytes))

	for i, b := range sigBytes {
		sig := new(blst.P2Affine).Deserialize(b)
		if sig == nil {
			return nil, fmt.Errorf("invalid signature: %v", b)
		}

		sigs[i] = sig
	}

	sigAgg := new(blst.P2Aggregate)
	if !sigAgg.Aggregate(sigs, true) {
		return nil, fmt.Errorf("could not aggregate signatures: %v", sigs)
	}

	result := sigAgg.ToAffine()
	if result == nil {
		return nil, fmt.Errorf("could not convert to affine: %v", sigAgg) // TODO: check if this can happen
	}

	return result, nil
}

func ToPublicKeys(pkBytes [][]byte) ([]*blst.P1Affine, error) {
	pubKeys := make([]*blst.P1Affine, len(pkBytes))
	for i, b := range pkBytes {
		pubKey := new(blst.P1Affine).Deserialize(b)
		if pubKey == nil {
			return []*blst.P1Affine{}, fmt.Errorf("invalid public key: %v", b)
		}

		pubKeys[i] = pubKey
	}

	return pubKeys, nil
}

func VerifyAggregateSignatures(sigBytes [][]byte, pkBytes [][]byte, msg blst.Message) (bool, error) {
	sigAgg, err := AggregateSignatures(sigBytes)
	if err != nil {
		return false, err
	}

	pubKeys, err := ToPublicKeys(pkBytes)
	if err != nil {
		return false, err
	}

	hash := sha256.Sum256(msg)
	return sigAgg.FastAggregateVerify(true, pubKeys, hash[:], BlsDst), nil
}

func (s *BLS12_381Signer) Sign(message []byte) ([]byte, error) {
	hash := sha256.Sum256(message)
	signature := new(blst.P2Affine).Sign(s.blsKey, hash[:], BlsDst)
	return signature.Serialize(), nil
}

func (s *BLS12_381Signer) Address() string {
	return s.ecdsaSigner.Address()
}

func (s *BLS12_381Signer) PublicKeyBytes() []byte {
	return new(blst.P1Affine).From(s.blsKey).Serialize()
}
