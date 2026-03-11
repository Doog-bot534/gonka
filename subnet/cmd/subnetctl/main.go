package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"subnet/bridge"
	"subnet/state"
	"subnet/user"
)

type SettlementJSON struct {
	EscrowID   string              `json:"escrow_id"`
	StateRoot  string              `json:"state_root"`
	Nonce      uint64              `json:"nonce"`
	RestHash   string              `json:"rest_hash"`
	HostStats  []HostStatsJSON     `json:"host_stats"`
	Signatures []SlotSignatureJSON `json:"signatures"`
}

type HostStatsJSON struct {
	SlotID               uint32 `json:"slot_id"`
	Missed               uint32 `json:"missed"`
	Invalid              uint32 `json:"invalid"`
	Cost                 uint64 `json:"cost"`
	RequiredValidations  uint32 `json:"required_validations"`
	CompletedValidations uint32 `json:"completed_validations"`
}

type SlotSignatureJSON struct {
	SlotID    uint32 `json:"slot_id"`
	Signature string `json:"signature"`
}

func main() {
	fs := flag.NewFlagSet("subnetctl", flag.ExitOnError)
	escrowID := fs.String("escrow-id", "", "escrow ID (required)")
	chainREST := fs.String("chain-rest", "http://localhost:1317", "chain REST API URL")
	prompts := fs.Int("prompts", 1, "number of inferences to send")
	model := fs.String("model", "Qwen/Qwen2.5-7B-Instruct", "model name")
	output := fs.String("output", "", "output file (default: stdout)")
	privateKey := fs.String("private-key", "", "private key hex (alternative to SUBNET_PRIVATE_KEY env)")

	if err := fs.Parse(os.Args[1:]); err != nil {
		log.Fatal(err)
	}

	keyHex := *privateKey
	if keyHex == "" {
		keyHex = os.Getenv("SUBNET_PRIVATE_KEY")
	}
	if keyHex == "" {
		log.Fatal("--private-key flag or SUBNET_PRIVATE_KEY env var required")
	}
	if *escrowID == "" {
		log.Fatal("--escrow-id required")
	}

	br := bridge.NewRESTBridge(*chainREST)

	cfg := user.HTTPSessionConfig{
		PrivateKeyHex: keyHex,
		EscrowID:      *escrowID,
		Bridge:        br,
		StreamCallback: func(line string) {
			fmt.Fprintln(os.Stderr, line)
		},
	}

	session, sm, err := user.NewHTTPSession(cfg)
	if err != nil {
		log.Fatalf("create session: %v", err)
	}

	ctx := context.Background()

	for i := 0; i < *prompts; i++ {
		prompt := []byte(fmt.Sprintf(
			`{"model":"%s","messages":[{"role":"user","content":"test prompt %d"}],"max_tokens":%d}`,
			*model, i, 100,
		))
		result, err := session.SendInference(ctx, user.InferenceParams{
			Model:       *model,
			Prompt:      prompt,
			InputLength: uint64(len(prompt)),
			MaxTokens:   100,
			StartedAt:   time.Now().Unix(),
		})
		if err != nil {
			log.Fatalf("inference %d: %v", i, err)
		}
		log.Printf("inference %d: nonce=%d receipt=%v", i, result.Nonce, result.Receipt != nil)
	}

	if err := session.Finalize(ctx); err != nil {
		log.Fatalf("finalize: %v", err)
	}
	log.Println("finalization complete")

	st := sm.SnapshotState()
	finalNonce := session.Nonce()
	payload, err := state.BuildSettlement(*escrowID, st, session.Signatures()[finalNonce], finalNonce)
	if err != nil {
		log.Fatalf("build settlement: %v", err)
	}

	data, err := marshalSettlement(payload)
	if err != nil {
		log.Fatalf("marshal settlement: %v", err)
	}

	if *output != "" {
		if err := os.WriteFile(*output, data, 0644); err != nil {
			log.Fatalf("write output: %v", err)
		}
		log.Printf("settlement written to %s", *output)
	} else {
		// Settlement JSON goes to stdout. Logs go to stderr (log.* default).
		fmt.Println(string(data))
	}
}

func marshalSettlement(p *state.SettlementPayload) ([]byte, error) {
	hsHash, err := state.ComputeHostStatsHash(p.HostStats)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	h.Write(hsHash)
	h.Write(p.RestHash)
	h.Write([]byte{0x02})
	root := h.Sum(nil)

	stats := make([]HostStatsJSON, 0, len(p.HostStats))
	for slot, hs := range p.HostStats {
		stats = append(stats, HostStatsJSON{
			SlotID: slot, Missed: hs.Missed, Invalid: hs.Invalid,
			Cost: hs.Cost, RequiredValidations: hs.RequiredValidations,
			CompletedValidations: hs.CompletedValidations,
		})
	}

	sigs := make([]SlotSignatureJSON, 0, len(p.Signatures))
	for slot, sig := range p.Signatures {
		sigs = append(sigs, SlotSignatureJSON{SlotID: slot, Signature: base64.StdEncoding.EncodeToString(sig)})
	}

	return json.MarshalIndent(SettlementJSON{
		EscrowID: p.EscrowID, StateRoot: base64.StdEncoding.EncodeToString(root),
		Nonce: p.Nonce, RestHash: base64.StdEncoding.EncodeToString(p.RestHash),
		HostStats: stats, Signatures: sigs,
	}, "", "  ")
}

