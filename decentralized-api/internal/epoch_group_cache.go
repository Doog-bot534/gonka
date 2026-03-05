package internal

import (
	"context"
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"sync"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/productscience/inference/x/inference/types"
)

const maxCachedEpochs = 2

type cachedEpochData struct {
	data       *types.EpochGroupData
	addressSet map[string]struct{} // O(1) lookup for active participants
}

type EpochGroupDataCache struct {
	mu sync.RWMutex

	// Legacy single-epoch cache for GetCurrentEpochGroupData
	cachedEpochIndex uint64
	cachedGroupData  *types.EpochGroupData

	// Multi-epoch cache for GetEpochGroupData (max 2 epochs)
	epochCache map[uint64]*cachedEpochData
	// Model-specific epoch data cache: epoch -> model -> data
	modelEpochCache map[uint64]map[string]*types.EpochGroupData
	// Active participant inference URL cache: epoch -> participant address -> inference URL
	activeParticipantURLCache map[uint64]map[string]string
	// Active participant pubkey cache: epoch -> participant address -> validator pubkey
	activeParticipantPubKeyCache map[uint64]map[string]string

	recorder cosmosclient.CosmosMessageClient
}

func NewEpochGroupDataCache(recorder cosmosclient.CosmosMessageClient) *EpochGroupDataCache {
	return &EpochGroupDataCache{
		recorder:                  recorder,
		epochCache:                make(map[uint64]*cachedEpochData),
		modelEpochCache:           make(map[uint64]map[string]*types.EpochGroupData),
		activeParticipantURLCache: make(map[uint64]map[string]string),
		activeParticipantPubKeyCache: make(map[uint64]map[string]string),
	}
}

func (c *EpochGroupDataCache) GetCurrentEpochGroupData(currentEpochIndex uint64) (*types.EpochGroupData, error) {
	c.mu.RLock()
	if c.cachedGroupData != nil && c.cachedEpochIndex == currentEpochIndex {
		defer c.mu.RUnlock()
		return c.cachedGroupData, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cachedGroupData != nil && c.cachedEpochIndex == currentEpochIndex {
		return c.cachedGroupData, nil
	}

	logging.Info("Fetching new epoch group data", types.Config,
		"cachedEpochIndex", c.cachedEpochIndex, "currentEpochIndex", currentEpochIndex)

	queryClient := c.recorder.NewInferenceQueryClient()
	req := &types.QueryCurrentEpochGroupDataRequest{}
	resp, err := queryClient.CurrentEpochGroupData(context.Background(), req)
	if err != nil {
		logging.Warn("Failed to query current epoch group data", types.Config, "error", err)
		return nil, err
	}

	c.cachedEpochIndex = currentEpochIndex
	c.cachedGroupData = &resp.EpochGroupData

	logging.Info("Updated epoch group data cache", types.Config,
		"epochIndex", currentEpochIndex,
		"validationWeights", len(resp.EpochGroupData.ValidationWeights))

	return c.cachedGroupData, nil
}

// GetEpochGroupData returns epoch group data for specific epoch.
// Uses cache, queries chain only on cache miss. Keeps max 2 epochs.
func (c *EpochGroupDataCache) GetEpochGroupData(ctx context.Context, epochIndex uint64) (*types.EpochGroupData, error) {
	c.mu.RLock()
	if cached, ok := c.epochCache[epochIndex]; ok {
		c.mu.RUnlock()
		return cached.data, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after acquiring write lock
	if cached, ok := c.epochCache[epochIndex]; ok {
		return cached.data, nil
	}

	logging.Debug("Fetching epoch group data", types.Config, "epochIndex", epochIndex)

	queryClient := c.recorder.NewInferenceQueryClient()
	resp, err := queryClient.EpochGroupData(ctx, &types.QueryGetEpochGroupDataRequest{
		EpochIndex: epochIndex,
	})
	if err != nil {
		return nil, err
	}

	// Prune if needed (keep max 2 epochs)
	if len(c.epochCache) >= maxCachedEpochs {
		c.pruneOldEpochs(epochIndex)
	}

	// Build address set for O(1) lookups
	addressSet := make(map[string]struct{}, len(resp.EpochGroupData.ValidationWeights))
	for _, vw := range resp.EpochGroupData.ValidationWeights {
		addressSet[vw.MemberAddress] = struct{}{}
	}

	c.epochCache[epochIndex] = &cachedEpochData{
		data:       &resp.EpochGroupData,
		addressSet: addressSet,
	}

	logging.Debug("Cached epoch group data", types.Config,
		"epochIndex", epochIndex,
		"participants", len(addressSet))

	return &resp.EpochGroupData, nil
}

// GetModelEpochGroupData returns model-specific epoch group data.
// Uses cache and only queries chain on cache miss for (epoch, model).
func (c *EpochGroupDataCache) GetModelEpochGroupData(ctx context.Context, epochIndex uint64, modelID string) (*types.EpochGroupData, error) {
	c.mu.RLock()
	if byModel, ok := c.modelEpochCache[epochIndex]; ok {
		if cached, ok := byModel[modelID]; ok {
			c.mu.RUnlock()
			return cached, nil
		}
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after acquiring write lock.
	if byModel, ok := c.modelEpochCache[epochIndex]; ok {
		if cached, ok := byModel[modelID]; ok {
			return cached, nil
		}
	}

	logging.Debug("Fetching model epoch group data", types.Config,
		"epochIndex", epochIndex,
		"modelId", modelID,
	)

	queryClient := c.recorder.NewInferenceQueryClient()
	resp, err := queryClient.EpochGroupData(ctx, &types.QueryGetEpochGroupDataRequest{
		EpochIndex: epochIndex,
		ModelId:    modelID,
	})
	if err != nil {
		return nil, err
	}

	if len(c.modelEpochCache) >= maxCachedEpochs {
		c.pruneOldEpochs(epochIndex)
	}
	if _, ok := c.modelEpochCache[epochIndex]; !ok {
		c.modelEpochCache[epochIndex] = make(map[string]*types.EpochGroupData)
	}

	c.modelEpochCache[epochIndex][modelID] = &resp.EpochGroupData
	return &resp.EpochGroupData, nil
}

// IsActiveParticipant checks if address is active at given epoch. O(1) lookup.
func (c *EpochGroupDataCache) IsActiveParticipant(ctx context.Context, epochIndex uint64, address string) (bool, error) {
	c.mu.RLock()
	if cached, ok := c.epochCache[epochIndex]; ok {
		_, exists := cached.addressSet[address]
		c.mu.RUnlock()
		return exists, nil
	}
	c.mu.RUnlock()

	// Cache miss - fetch data first
	_, err := c.GetEpochGroupData(ctx, epochIndex)
	if err != nil {
		return false, err
	}

	// Now check again
	c.mu.RLock()
	defer c.mu.RUnlock()
	if cached, ok := c.epochCache[epochIndex]; ok {
		_, exists := cached.addressSet[address]
		return exists, nil
	}
	return false, nil
}

func (c *EpochGroupDataCache) GetActiveParticipantInferenceURL(
	ctx context.Context,
	epochIndex uint64,
	address string,
	chainNodeURL string,
) (string, bool, error) {
	c.mu.RLock()
	byAddress, ok := c.activeParticipantURLCache[epochIndex]
	if ok {
		inferenceURL, found := byAddress[address]
		c.mu.RUnlock()
		if found && inferenceURL != "" {
			return inferenceURL, true, nil
		}
	} else {
		c.mu.RUnlock()
	}

	// Lazy-fill on cache miss (or address miss) and then re-check.
	if err := c.PrewarmActiveParticipantInferenceURLsByEpoch(ctx, epochIndex, chainNodeURL); err != nil {
		return "", false, err
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	byAddress, ok = c.activeParticipantURLCache[epochIndex]
	if !ok {
		return "", false, nil
	}
	inferenceURL, found := byAddress[address]
	if !found || inferenceURL == "" {
		return "", false, nil
	}
	return inferenceURL, true, nil
}

func (c *EpochGroupDataCache) HasActiveParticipantInferenceURLs(epochIndex uint64) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	byAddress, ok := c.activeParticipantURLCache[epochIndex]
	return ok && len(byAddress) > 0
}

func (c *EpochGroupDataCache) GetActiveParticipantPubKey(
	ctx context.Context,
	epochIndex uint64,
	address string,
	chainNodeURL string,
) (string, bool, error) {
	c.mu.RLock()
	byAddress, ok := c.activeParticipantPubKeyCache[epochIndex]
	if ok {
		pubKey, found := byAddress[address]
		c.mu.RUnlock()
		if found && pubKey != "" {
			return pubKey, true, nil
		}
	} else {
		c.mu.RUnlock()
	}

	// Lazy-fill on cache miss (or address miss) and then re-check.
	if err := c.PrewarmActiveParticipantInferenceURLsByEpoch(ctx, epochIndex, chainNodeURL); err != nil {
		return "", false, err
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	byAddress, ok = c.activeParticipantPubKeyCache[epochIndex]
	if !ok {
		return "", false, nil
	}
	pubKey, found := byAddress[address]
	if !found || pubKey == "" {
		return "", false, nil
	}
	return pubKey, true, nil
}

func (c *EpochGroupDataCache) SetActiveParticipantInferenceURLs(epochIndex uint64, byAddress map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setActiveParticipantInferenceURLs(epochIndex, byAddress)
}

func (c *EpochGroupDataCache) setActiveParticipantInferenceURLs(epochIndex uint64, byAddress map[string]string) {
	if len(c.activeParticipantURLCache) >= maxCachedEpochs {
		c.pruneOldEpochs(epochIndex)
	}
	copied := make(map[string]string, len(byAddress))
	for address, inferenceURL := range byAddress {
		if address == "" || inferenceURL == "" {
			continue
		}
		copied[address] = inferenceURL
	}
	c.activeParticipantURLCache[epochIndex] = copied
}

func (c *EpochGroupDataCache) setActiveParticipantPubKeys(epochIndex uint64, byAddress map[string]string) {
	if len(c.activeParticipantPubKeyCache) >= maxCachedEpochs {
		c.pruneOldEpochs(epochIndex)
	}
	copied := make(map[string]string, len(byAddress))
	for address, pubKey := range byAddress {
		if address == "" || pubKey == "" {
			continue
		}
		copied[address] = pubKey
	}
	c.activeParticipantPubKeyCache[epochIndex] = copied
}

// PrewarmActiveParticipantInferenceURLsByEpoch fetches ActiveParticipants for the epoch
// and populates the active participant URL cache keyed by participant address (Index).
func (c *EpochGroupDataCache) PrewarmActiveParticipantInferenceURLsByEpoch(ctx context.Context, epochIndex uint64, chainNodeURL string) error {
	rpcClient, err := cosmosclient.NewRpcClient(chainNodeURL)
	if err != nil {
		return err
	}

	result, err := cosmosclient.QueryByKey(rpcClient, "inference", types.ActiveParticipantsFullKey(epochIndex))
	if err != nil {
		return err
	}
	if len(result.Response.Value) == 0 {
		return nil
	}

	interfaceRegistry := codectypes.NewInterfaceRegistry()
	types.RegisterInterfaces(interfaceRegistry)
	cdc := codec.NewProtoCodec(interfaceRegistry)

	var activeParticipants types.ActiveParticipants
	if err := cdc.Unmarshal(result.Response.Value, &activeParticipants); err != nil {
		return err
	}

	byAddress := make(map[string]string, len(activeParticipants.Participants))
	byAddressPubKey := make(map[string]string, len(activeParticipants.Participants))
	for _, participant := range activeParticipants.Participants {
		if participant == nil || participant.Index == "" {
			continue
		}
		if participant.InferenceUrl != "" {
			byAddress[participant.Index] = participant.InferenceUrl
		}
		if participant.ValidatorKey != "" {
			byAddressPubKey[participant.Index] = participant.ValidatorKey
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.setActiveParticipantInferenceURLs(epochIndex, byAddress)
	c.setActiveParticipantPubKeys(epochIndex, byAddressPubKey)
	return nil
}

// pruneOldEpochs removes epochs older than currentEpoch - 1.
func (c *EpochGroupDataCache) pruneOldEpochs(currentEpoch uint64) {
	if currentEpoch <= 1 {
		return
	}

	for epochId := range c.epochCache {
		if epochId < currentEpoch-1 {
			delete(c.epochCache, epochId)
			logging.Debug("Pruned old epoch from cache", types.Config, "epochId", epochId)
		}
	}
	for epochId := range c.modelEpochCache {
		if epochId < currentEpoch-1 {
			delete(c.modelEpochCache, epochId)
			logging.Debug("Pruned old model epoch from cache", types.Config, "epochId", epochId)
		}
	}
	for epochId := range c.activeParticipantURLCache {
		if epochId < currentEpoch-1 {
			delete(c.activeParticipantURLCache, epochId)
			logging.Debug("Pruned old active participant URL cache", types.Config, "epochId", epochId)
		}
	}
	for epochId := range c.activeParticipantPubKeyCache {
		if epochId < currentEpoch-1 {
			delete(c.activeParticipantPubKeyCache, epochId)
			logging.Debug("Pruned old active participant pubkey cache", types.Config, "epochId", epochId)
		}
	}
}
