package keeper

import (
	"context"
	"encoding/binary"
	"encoding/json"

	"github.com/productscience/inference/x/inference/types"
)

type epochDataTransientParticipantCache struct {
	Weight     int64 `json:"weight"`
	Reputation int32 `json:"reputation"`
}

type epochDataTransientModelMetaCacheEntry struct {
	EpochPolicy         string         `json:"epoch_policy"`
	TotalWeight         int64          `json:"total_weight"`
	ValidationThreshold *types.Decimal `json:"validation_threshold,omitempty"`
	SubGroupModels      []string       `json:"sub_group_models,omitempty"`
}

func (k Keeper) BuildEpochDataTransientCache(ctx context.Context) error {
	currentEpochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		return nil
	}

	for _, epochIndex := range cachedEpochDataEpochs(currentEpochIndex) {
		rootGroupData, found := k.GetEpochGroupData(ctx, epochIndex, "")
		if !found {
			continue
		}
		transientStore := k.transientStoreService.OpenTransientStore(ctx)
		if err := setModelTransientCacheEntries(transientStore, epochIndex, "", rootGroupData); err != nil {
			return err
		}

		for _, modelID := range rootGroupData.SubGroupModels {
			modelGroupData, found := k.GetEpochGroupData(ctx, epochIndex, modelID)
			if !found {
				continue
			}
			if err := setModelTransientCacheEntries(transientStore, epochIndex, modelID, modelGroupData); err != nil {
				return err
			}
		}
	}

	return nil
}

func (k Keeper) GetCachedEpochDataModelMeta(
	ctx context.Context,
	epochIndex uint64,
	modelID string,
) (epochDataTransientModelMetaCacheEntry, bool, error) {
	transientStore := k.transientStoreService.OpenTransientStore(ctx)

	bz, err := transientStore.Get(epochDataModelMetaCacheKey(epochIndex, modelID))
	if err != nil || len(bz) == 0 {
		return epochDataTransientModelMetaCacheEntry{}, false, err
	}

	var entry epochDataTransientModelMetaCacheEntry
	if err := json.Unmarshal(bz, &entry); err != nil {
		return epochDataTransientModelMetaCacheEntry{}, false, err
	}
	return entry, true, nil
}

func (k Keeper) GetCachedEpochDataModelWeight(
	ctx context.Context,
	epochIndex uint64,
	modelID string,
	validator string,
) (epochDataTransientParticipantCache, bool, error) {
	transientStore := k.transientStoreService.OpenTransientStore(ctx)

	bz, err := transientStore.Get(epochDataModelWeightCacheKey(epochIndex, modelID, validator))
	if err != nil || len(bz) == 0 {
		return epochDataTransientParticipantCache{}, false, err
	}

	var entry epochDataTransientParticipantCache
	if err := json.Unmarshal(bz, &entry); err != nil {
		return epochDataTransientParticipantCache{}, false, err
	}
	return entry, true, nil
}

func epochDataModelMetaCacheKey(epochIndex uint64, modelID string) []byte {
	prefix := types.TransientEpochDataModelMetaKey
	key := make([]byte, len(prefix)+8+1+len(modelID))
	copy(key, prefix)
	offset := len(prefix)
	binary.BigEndian.PutUint64(key[offset:offset+8], epochIndex)
	offset += 8
	key[offset] = '|'
	offset++
	copy(key[offset:], modelID)
	return key
}

func epochDataModelWeightCacheKey(epochIndex uint64, modelID, validator string) []byte {
	prefix := types.TransientEpochDataModelWeightKey
	key := make([]byte, len(prefix)+8+1+len(modelID)+1+len(validator))
	copy(key, prefix)
	offset := len(prefix)
	binary.BigEndian.PutUint64(key[offset:offset+8], epochIndex)
	offset += 8
	key[offset] = '|'
	offset++
	copy(key[offset:], modelID)
	offset += len(modelID)
	key[offset] = '|'
	offset++
	copy(key[offset:], validator)
	return key
}

func setModelTransientCacheEntries(
	transientStore transientStoreSetter,
	epochIndex uint64,
	modelID string,
	groupData types.EpochGroupData,
) error {
	metaEntry := epochDataTransientModelMetaCacheEntry{
		EpochPolicy:    groupData.EpochPolicy,
		TotalWeight:    groupData.TotalWeight,
		SubGroupModels: groupData.SubGroupModels,
	}
	if groupData.ModelSnapshot != nil {
		metaEntry.ValidationThreshold = groupData.ModelSnapshot.ValidationThreshold
	}
	metaBz, err := json.Marshal(metaEntry)
	if err != nil {
		return err
	}
	if err := transientStore.Set(epochDataModelMetaCacheKey(epochIndex, modelID), metaBz); err != nil {
		return err
	}

	for _, weight := range groupData.ValidationWeights {
		if weight == nil {
			continue
		}
		validationEntry := epochDataTransientParticipantCache{
			Weight:     weight.Weight,
			Reputation: weight.Reputation,
		}
		validationBz, err := json.Marshal(validationEntry)
		if err != nil {
			return err
		}
		if err := transientStore.Set(epochDataModelWeightCacheKey(epochIndex, modelID, weight.MemberAddress), validationBz); err != nil {
			return err
		}
	}

	return nil
}

type transientStoreSetter interface {
	Set(key, value []byte) error
}

func cachedEpochDataEpochs(currentEpochIndex uint64) []uint64 {
	if currentEpochIndex == 0 {
		return []uint64{0}
	}
	return []uint64{currentEpochIndex, currentEpochIndex - 1}
}
