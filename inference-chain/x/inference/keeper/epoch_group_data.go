package keeper

import (
	"context"
	"time"

	"cosmossdk.io/collections"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

// SetEpochGroupData set a specific epochGroupData in the store from its index
func (k Keeper) SetEpochGroupData(ctx context.Context, epochGroupData types.EpochGroupData) {
	k.EpochGroupDataMap.Set(ctx, collections.Join(epochGroupData.EpochIndex, epochGroupData.ModelId), epochGroupData)
}

// GetEpochGroupData returns a epochGroupData from its index
func (k Keeper) GetEpochGroupData(
	ctx context.Context,
	epochIndex uint64,
	modelId string,
) (val types.EpochGroupData, found bool) {
	key := collections.Join(epochIndex, modelId)
	keyBytes, err := collections.EncodeKeyWithPrefix(k.EpochGroupDataMap.GetPrefix(), k.EpochGroupDataMap.KeyCodec(), key)
	if err != nil {
		k.LogError("GetEpochGroupData: key encode failed", types.EpochGroup, "epoch_index", epochIndex, "model_id", modelId, "error", err)
		return val, false
	}

	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	fetchStart := time.Now()
	valueBytes := storeAdapter.Get(keyBytes)
	k.LogInfo("GetEpochGroupData: storage fetch complete", types.EpochGroup,
		"epoch_index", epochIndex,
		"model_id", modelId,
		"found", valueBytes != nil,
		"duration_ms", durationMs(fetchStart),
	)
	if valueBytes == nil {
		return val, false
	}

	decodeStart := time.Now()
	val, err = k.EpochGroupDataMap.ValueCodec().Decode(valueBytes)
	k.LogInfo("GetEpochGroupData: decode complete", types.EpochGroup,
		"epoch_index", epochIndex,
		"model_id", modelId,
		"duration_ms", durationMs(decodeStart),
		"success", err == nil,
	)
	if err != nil {
		k.LogError("GetEpochGroupData: decode failed", types.EpochGroup, "epoch_index", epochIndex, "model_id", modelId, "error", err)
		return val, false
	}
	return val, true
}

// RemoveEpochGroupData removes a epochGroupData from the store
func (k Keeper) RemoveEpochGroupData(
	ctx context.Context,
	epochIndex uint64,
	modelId string,
) {
	k.EpochGroupDataMap.Remove(ctx, collections.Join(epochIndex, modelId))
}

// GetAllEpochGroupData returns all epochGroupData
func (k Keeper) GetAllEpochGroupData(ctx context.Context) (list []types.EpochGroupData) {
	iter, err := k.EpochGroupDataMap.Iterate(ctx, nil)
	if err != nil {
		return nil
	}
	epochGroupDataList, err := iter.Values()
	if err != nil {
		return nil
	}
	return epochGroupDataList
}
