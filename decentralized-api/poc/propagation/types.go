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

type ArrivalInfo struct {
	Time  int64  `json:"time"`
	Count uint32 `json:"count"`
}

type FirstArrivalObservation struct {
	ValidatorAddress string                 `json:"validator_address"`
	PocHeight        int64                  `json:"poc_height"`
	Arrivals         map[string]ArrivalInfo `json:"arrivals"`
	Timestamp        int64                  `json:"timestamp"`
	Signature        []byte                 `json:"signature"`
}

type firstArrivalObservationJSON struct {
	ValidatorAddress string                 `json:"validator_address"`
	PocHeight        int64                  `json:"poc_height"`
	Arrivals         map[string]ArrivalInfo `json:"arrivals"`
	Timestamp        int64                  `json:"timestamp"`
	Signature        string                 `json:"signature"`
}

func (o FirstArrivalObservation) MarshalJSON() ([]byte, error) {
	return json.Marshal(firstArrivalObservationJSON{
		ValidatorAddress: o.ValidatorAddress,
		PocHeight:        o.PocHeight,
		Arrivals:         o.Arrivals,
		Timestamp:        o.Timestamp,
		Signature:        hex.EncodeToString(o.Signature),
	})
}

func (o *FirstArrivalObservation) UnmarshalJSON(data []byte) error {
	var j firstArrivalObservationJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return err
	}

	sig, err := hex.DecodeString(j.Signature)
	if err != nil {
		return err
	}

	o.ValidatorAddress = j.ValidatorAddress
	o.PocHeight = j.PocHeight
	o.Arrivals = j.Arrivals
	o.Timestamp = j.Timestamp
	o.Signature = sig

	return nil
}

func MakeObservationID(validatorAddress string, pocHeight int64) [32]byte {
	h := sha256.New()
	h.Write([]byte(validatorAddress))
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(pocHeight))
	h.Write(buf[:])
	return sha256.Sum256(h.Sum(nil))
}

func observationSigningBytes(o FirstArrivalObservation) []byte {
	buf := bytes.NewBuffer(nil)
	buf.WriteString(o.ValidatorAddress)

	var tmp [8]byte
	binary.BigEndian.PutUint64(tmp[:], uint64(o.PocHeight))
	buf.Write(tmp[:])

	participants := make([]string, 0, len(o.Arrivals))
	for p := range o.Arrivals {
		participants = append(participants, p)
	}
	for i := 0; i < len(participants); i++ {
		for j := i + 1; j < len(participants); j++ {
			if participants[i] > participants[j] {
				participants[i], participants[j] = participants[j], participants[i]
			}
		}
	}
	for _, p := range participants {
		buf.WriteString(p)
		arrival := o.Arrivals[p]
		binary.BigEndian.PutUint64(tmp[:], uint64(arrival.Time))
		buf.Write(tmp[:])
		binary.BigEndian.PutUint32(tmp[:4], arrival.Count)
		buf.Write(tmp[:4])
	}

	binary.BigEndian.PutUint64(tmp[:], uint64(o.Timestamp))
	buf.Write(tmp[:])

	return buf.Bytes()
}

func SignObservation(o FirstArrivalObservation, privKey []byte) ([]byte, error) {
	if len(privKey) != 64 {
		return nil, errors.New("invalid ed25519 private key length")
	}
	msg := observationSigningBytes(o)
	key := ed25519.PrivKey(privKey)
	return key.Sign(msg)
}

func SignObservationWith(o FirstArrivalObservation, signer HeaderSigner) ([]byte, error) {
	return signer.Sign(observationSigningBytes(o))
}

func VerifyObservation(o FirstArrivalObservation, base64PubKey string) error {
	if o.Signature == nil {
		return errors.New("signature missing")
	}

	pubKeyBytes, err := base64.StdEncoding.DecodeString(base64PubKey)
	if err != nil {
		return err
	}

	if len(pubKeyBytes) != 32 {
		return errors.New("invalid ed25519 public key length")
	}

	msg := observationSigningBytes(o)
	pubKey := ed25519.PubKey(pubKeyBytes)

	if !pubKey.VerifySignature(msg, o.Signature) {
		return errors.New("signature verification failed")
	}

	return nil
}

type ObservationMessage struct {
	Observation FirstArrivalObservation `json:"observation"`
	From        string                  `json:"from,omitempty"`
}
