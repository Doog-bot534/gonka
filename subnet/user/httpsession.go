package user

import (
	"fmt"

	"subnet/bridge"
	"subnet/signing"
	"subnet/state"
	"subnet/transport"
	"subnet/types"
)

// HTTPSessionConfig holds the parameters needed to create an HTTP-backed user session.
type HTTPSessionConfig struct {
	PrivateKeyHex string
	EscrowID      string
	Bridge        bridge.MainnetBridge
}

// NewHTTPSession creates a user Session wired with HTTP clients to real dapi hosts.
// It queries the bridge for escrow and group info, then creates transport clients
// for each slot.
func NewHTTPSession(cfg HTTPSessionConfig) (*Session, *state.StateMachine, error) {
	signer, err := signing.SignerFromHex(cfg.PrivateKeyHex)
	if err != nil {
		return nil, nil, fmt.Errorf("create signer: %w", err)
	}
	verifier := signing.NewSecp256k1Verifier()

	group, err := bridge.BuildGroup(cfg.EscrowID, cfg.Bridge)
	if err != nil {
		return nil, nil, fmt.Errorf("build group: %w", err)
	}

	escrow, err := cfg.Bridge.GetEscrow(cfg.EscrowID)
	if err != nil {
		return nil, nil, fmt.Errorf("get escrow: %w", err)
	}

	config := types.DefaultSessionConfig(len(group))

	sm := state.NewStateMachine(cfg.EscrowID, config, group, escrow.Amount, escrow.CreatorAddress, verifier,
		state.WithWarmKeyResolver(cfg.Bridge.VerifyWarmKey),
	)

	urlForAddr := make(map[string]string, len(group))
	for _, slot := range group {
		if _, ok := urlForAddr[slot.ValidatorAddress]; ok {
			continue
		}
		info, err := cfg.Bridge.GetValidatorInfo(slot.ValidatorAddress)
		if err != nil {
			return nil, nil, fmt.Errorf("get validator info for %s: %w", slot.ValidatorAddress, err)
		}
		urlForAddr[slot.ValidatorAddress] = info.URL
	}

	clients := make([]HostClient, len(group))
	clientCache := make(map[string]*transport.HTTPClient)
	for i, slot := range group {
		baseURL := urlForAddr[slot.ValidatorAddress]
		if c, ok := clientCache[baseURL]; ok {
			clients[i] = c
			continue
		}
		c := transport.NewHTTPClient(baseURL, cfg.EscrowID, signer)
		clientCache[baseURL] = c
		clients[i] = c
	}

	session, err := NewSession(sm, signer, cfg.EscrowID, group, clients, verifier)
	if err != nil {
		return nil, nil, fmt.Errorf("create session: %w", err)
	}

	return session, sm, nil
}
