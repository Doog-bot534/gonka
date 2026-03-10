package signing

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func generateBlsKey(t *testing.T) *BLS12_381Signer {
	signer, err := GenerateBLS12_381Key()
	require.NoError(t, err)
	return signer
}

func TestSign_BLS_VerifyAggregated(t *testing.T) {
	signer1 := generateBlsKey(t)
	signer2 := generateBlsKey(t)

	msg := []byte("test message")

	sig1, err := signer1.Sign(msg)
	require.NoError(t, err)
	sig2, err := signer2.Sign(msg)
	require.NoError(t, err)

	pk1 := signer1.PublicKeyBytes()
	pk2 := signer2.PublicKeyBytes()

	isValid, err := VerifyAggregateSignatures([][]byte{sig1, sig2}, [][]byte{pk1, pk2}, msg)
	require.NoError(t, err)
	require.True(t, isValid)
}

func TestSign_BLS_VerifyAggregated_InsufficientSignatures(t *testing.T) {
	signer1 := generateBlsKey(t)
	signer2 := generateBlsKey(t)

	msg := []byte("test message")

	sig1, err := signer1.Sign(msg)
	require.NoError(t, err)

	pk1 := signer1.PublicKeyBytes()
	pk2 := signer2.PublicKeyBytes()

	isValid, err := VerifyAggregateSignatures([][]byte{sig1}, [][]byte{pk1, pk2}, msg)
	require.NoError(t, err)
	require.False(t, isValid)
}

func TestSign_BLS_VerifyAggregated_InsufficientPublicKeys(t *testing.T) {
	signer1 := generateBlsKey(t)
	signer2 := generateBlsKey(t)

	msg := []byte("test message")

	sig1, err := signer1.Sign(msg)
	require.NoError(t, err)
	sig2, err := signer2.Sign(msg)
	require.NoError(t, err)

	pk1 := signer1.PublicKeyBytes()

	isValid, err := VerifyAggregateSignatures([][]byte{sig1, sig2}, [][]byte{pk1}, msg)
	require.NoError(t, err)
	require.False(t, isValid)
}

func TestSign_BLS_VerifyAggregated_TamperedMessage(t *testing.T) {
	signer1 := generateBlsKey(t)
	signer2 := generateBlsKey(t)

	msg := []byte("test message")

	sig1, err := signer1.Sign(msg)
	require.NoError(t, err)
	sig2, err := signer2.Sign(msg)
	require.NoError(t, err)

	pk1 := signer1.PublicKeyBytes()
	pk2 := signer2.PublicKeyBytes()

	tampered := []byte("tampered message")
	isValid, err := VerifyAggregateSignatures([][]byte{sig1, sig2}, [][]byte{pk1, pk2}, tampered)
	require.NoError(t, err)
	require.False(t, isValid)
}
