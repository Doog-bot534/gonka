package main

import (
	"encoding/base64"
	"encoding/json"

	"subnet/state"
	"subnet/types"
)

type SettlementJSON struct {
	EscrowID  string `json:"escrow_id"`
	StateRoot string `json:"state_root"`
	Nonce     uint64 `json:"nonce"`
	// Fees is the total fee amount deducted during session execution.
	Fees       uint64              `json:"fees"`
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

func marshalSettlement(p *state.SettlementPayload) ([]byte, error) {
	hsHash, err := state.ComputeHostStatsHash(p.HostStats)
	if err != nil {
		return nil, err
	}
	root := state.ComputeStateRootFromRestHash(hsHash, p.RestHash, p.Fees, types.PhaseSettlement)

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
		Nonce: p.Nonce, Fees: p.Fees, RestHash: base64.StdEncoding.EncodeToString(p.RestHash),
		HostStats: stats, Signatures: sigs,
	}, "", "  ")
}
