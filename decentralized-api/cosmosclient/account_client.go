package cosmosclient

import (
	"crypto/sha256"
	"decentralized-api/logging"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	"github.com/cosmos/btcutil/bech32"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/productscience/inference/x/inference/types"
	"golang.org/x/crypto/ripemd160"
)

// PubKeyToAddress converts a public key string to a Cosmos bech32 address.
// Accepts both base64-encoded (standard Cosmos format) and hex-encoded public keys.
func PubKeyToAddress(pubKeyStr string) (string, error) {
	var pubKeyBytes []byte
	var err error

	// Try base64 first (standard Cosmos format from chain queries)
	pubKeyBytes, err = base64.StdEncoding.DecodeString(pubKeyStr)
	if err != nil {
		// Fallback to hex encoding (legacy format)
		pubKeyBytes, err = hex.DecodeString(pubKeyStr)
		if err != nil {
			logging.Error("Invalid public key (not base64 or hex)", types.Participants, "err", err, "error-type", fmt.Sprintf("%T", err))
			return "", err
		}
	}

	// Step 1: SHA-256 hash
	shaHash := sha256.Sum256(pubKeyBytes)

	// Step 2: RIPEMD-160 hash
	ripemdHasher := ripemd160.New()
	ripemdHasher.Write(shaHash[:])
	ripemdHash := ripemdHasher.Sum(nil)

	// Step 3: Bech32 encode
	prefix := "gonka"
	fiveBitData, err := bech32.ConvertBits(ripemdHash, 8, 5, true)
	if err != nil {
		logging.Error("Failed to convert bits", types.Participants, "err", err)
		return "", err
	}

	address, err := bech32.Encode(prefix, fiveBitData)
	if err != nil {
		logging.Error("Failed to encode address", types.Participants, "err", err)
		return "", err
	}

	return address, nil
}

func PubKeyToString(pubKey cryptotypes.PubKey) string {
	return base64.StdEncoding.EncodeToString(pubKey.Bytes())
}
