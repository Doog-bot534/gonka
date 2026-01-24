package keeper

import (
	"context"
	"time"

	"cosmossdk.io/collections"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

// SetInference set a specific inference in the store from its index
func (k Keeper) SetInference(ctx context.Context, inference types.Inference) error {
	// store via collections
	setStart := time.Now()
	pruneStart := time.Now()
	k.addInferenceToPruningList(ctx, inference)
	k.LogInfo("SetInference: pruning list updated", types.Pruning,
		"inference_id", inference.InferenceId,
		"duration_ms", durationMs(pruneStart),
	)

	storeStart := time.Now()
	if err := k.Inferences.Set(ctx, inference.Index, inference); err != nil {
		return err
	}
	k.LogInfo("SetInference: inference stored", types.Inferences,
		"inference_id", inference.InferenceId,
		"duration_ms", durationMs(storeStart),
	)

	devStatStart := time.Now()
	err := k.SetDeveloperStats(ctx, inference)
	if err != nil {
		k.LogError("SetInference: developer stats update failed", types.Stat,
			"inference_id", inference.InferenceId,
			"duration_ms", durationMs(devStatStart),
			"error", err,
		)
	} else {
		k.LogInfo("SetInference: developer stats updated", types.Stat,
			"inference_id", inference.InferenceId,
			"inference_status", inference.Status.String(),
			"developer", inference.RequestedBy,
			"duration_ms", durationMs(devStatStart),
		)
	}
	k.LogInfo("SetInference: complete", types.Inferences,
		"inference_id", inference.InferenceId,
		"duration_ms", durationMs(setStart),
	)
	return nil
}

func (k Keeper) SetInferenceWithoutDevStatComputation(ctx context.Context, inference types.Inference) error {
	k.addInferenceToPruningList(ctx, inference)
	return k.Inferences.Set(ctx, inference.Index, inference)
}

func (k Keeper) addInferenceToPruningList(ctx context.Context, inference types.Inference) {
	if inference.EpochId != 0 {
		key := collections.Join(int64(inference.EpochId), inference.Index)
		err := k.InferencesToPrune.Set(ctx, key, collections.NoValue{})
		if err != nil {
			k.LogError("Unable to set InferencesToPrune", types.Pruning, "error", err, "key", key)
		}
	}
}

// GetInference returns a inference from its index
func (k Keeper) GetInference(
	ctx context.Context,
	index string,

) (val types.Inference, found bool) {
	keyBytes, err := collections.EncodeKeyWithPrefix(k.Inferences.GetPrefix(), k.Inferences.KeyCodec(), index)
	if err != nil {
		k.LogError("GetInference: key encode failed", types.Inferences, "inference_id", index, "error", err)
		return val, false
	}

	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	fetchStart := time.Now()
	valueBytes := storeAdapter.Get(keyBytes)
	k.LogInfo("GetInference: storage fetch complete", types.Inferences,
		"inference_id", index,
		"found", valueBytes != nil,
		"duration_ms", durationMs(fetchStart),
	)
	if valueBytes == nil {
		return val, false
	}

	decodeStart := time.Now()
	val, err = k.Inferences.ValueCodec().Decode(valueBytes)
	k.LogInfo("GetInference: decode complete", types.Inferences,
		"inference_id", index,
		"duration_ms", durationMs(decodeStart),
		"success", err == nil,
	)
	if err != nil {
		k.LogError("GetInference: decode failed", types.Inferences, "inference_id", index, "error", err)
		return val, false
	}
	return val, true
}

// RemoveInference removes a inference from the store
func (k Keeper) RemoveInference(
	ctx context.Context,
	index string,

) {
	_ = k.Inferences.Remove(ctx, index)
}

// GetAllInference returns all inference
func (k Keeper) GetAllInference(ctx context.Context) (list []types.Inference, err error) {
	iter, err := k.Inferences.Iterate(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	vals, err := iter.Values()
	if err != nil {
		return nil, err
	}
	return vals, nil
}

func (k Keeper) GetInferences(ctx context.Context, ids []string) ([]types.Inference, bool) {
	result := make([]types.Inference, len(ids))
	for i, id := range ids {
		v, err := k.Inferences.Get(ctx, id)
		if err != nil {
			return nil, false
		}
		result[i] = v
	}
	return result, true
}
