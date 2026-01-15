package internal

import (
	"context"
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"fmt"
	"sync"

	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/productscience/inference/x/inference/types"
)

type cachedParticipantsData struct {
	peers   []types.Participant
	pubKeys map[string][]string // Address -> all pubkeys (granter + grantees)
}

type ParticipantsListCache struct {
	mu sync.RWMutex

	// Multi-epoch cache (max 2 epochs)
	epochCache map[uint64]*cachedParticipantsData

	recorder cosmosclient.CosmosMessageClient
}

const granteesByMessageTypeURL = "/inference.inference.MsgStartInference"

func NewParticipantsListCache(recorder cosmosclient.CosmosMessageClient) *ParticipantsListCache {
	return &ParticipantsListCache{
		recorder:   recorder,
		epochCache: make(map[uint64]*cachedParticipantsData),
	}
}

// GetParticipants returns list of participants for specific epoch.
// Uses cache, queries chain only on cache miss. Keeps max 2 epochs.
func (c *ParticipantsListCache) GetParticipants(ctx context.Context, epochIndex uint64) ([]types.Participant, error) {
	c.mu.RLock()
	if cached, ok := c.epochCache[epochIndex]; ok {
		// Return copy to avoid modification
		out := make([]types.Participant, len(cached.peers))
		copy(out, cached.peers)
		c.mu.RUnlock()
		return out, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after acquiring write lock
	if cached, ok := c.epochCache[epochIndex]; ok {
		out := make([]types.Participant, len(cached.peers))
		copy(out, cached.peers)
		return out, nil
	}

	logging.Debug("Fetching participant list", types.Config, "epochIndex", epochIndex)

	qc := c.recorder.NewInferenceQueryClient()
	var participants []types.Participant
	var pageKey []byte
	for {
		resp, err := qc.ParticipantAll(ctx, &types.QueryAllParticipantRequest{
			Pagination: &query.PageRequest{
				Key:   pageKey,
				Limit: 1000,
			},
		})
		if err != nil {
			return nil, err
		}
		participants = append(participants, resp.Participant...)
		if resp.Pagination == nil || len(resp.Pagination.NextKey) == 0 {
			break
		}
		pageKey = resp.Pagination.NextKey
	}

	// Prune if needed (keep max 2 epochs)
	if len(c.epochCache) >= maxCachedEpochs {
		c.pruneOldest(epochIndex)
	}

	// Build pubKey map
	data := &cachedParticipantsData{
		peers:   participants,
		pubKeys: make(map[string][]string),
	}

	c.epochCache[epochIndex] = data

	logging.Debug("Cached participant list", types.Config,
		"epochIndex", epochIndex,
		"count", len(participants))

	out := make([]types.Participant, len(participants))
	copy(out, participants)
	return out, nil
}

// GetParticipantPubKeys returns all pubkeys (granter + grantees).
// Cached per-epoch; fetches from chain on cache miss.
func (c *ParticipantsListCache) GetParticipantPubKeys(ctx context.Context, epochIndex uint64, granterAddress string) ([]string, error) {
	c.mu.RLock()
	if cached, ok := c.epochCache[epochIndex]; ok {
		if keys, ok := cached.pubKeys[granterAddress]; ok {
			out := make([]string, len(keys))
			copy(out, keys)
			c.mu.RUnlock()
			return out, nil
		}
	}
	c.mu.RUnlock()

	qc := c.recorder.NewInferenceQueryClient()
	grantees, err := qc.GranteesByMessageType(ctx, &types.QueryGranteesByMessageTypeRequest{
		GranterAddress: granterAddress,
		MessageTypeUrl: granteesByMessageTypeURL,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get grantees by message type: %w", err)
	}

	granterAccount, err := qc.InferenceParticipant(ctx, &types.QueryInferenceParticipantRequest{Address: granterAddress})
	if err != nil {
		return nil, err
	}
	if granterAccount == nil {
		return nil, fmt.Errorf("participant not found")
	}

	keys := make([]string, 0, len(grantees.Grantees)+1)
	for _, grantee := range grantees.Grantees {
		keys = append(keys, grantee.PubKey)
	}
	keys = append(keys, granterAccount.Pubkey)

	c.mu.Lock()
	cached := c.getOrCreateEpochCache(epochIndex)
	cached.pubKeys[granterAddress] = keys
	c.mu.Unlock()

	out := make([]string, len(keys))
	copy(out, keys)
	return out, nil
}

// pruneOldest removes epochs older than currentEpoch - 1
func (c *ParticipantsListCache) pruneOldest(currentEpoch uint64) {
	for epochId := range c.epochCache {
		if epochId < currentEpoch-1 {
			delete(c.epochCache, epochId)
		}
	}
}

func (c *ParticipantsListCache) getOrCreateEpochCache(epochIndex uint64) *cachedParticipantsData {
	if cached, ok := c.epochCache[epochIndex]; ok {
		return cached
	}
	// Prune if needed (keep max 2 epochs)
	if len(c.epochCache) >= maxCachedEpochs {
		c.pruneOldest(epochIndex)
	}
	data := &cachedParticipantsData{
		peers:   nil,
		pubKeys: make(map[string][]string),
	}
	c.epochCache[epochIndex] = data
	return data
}
