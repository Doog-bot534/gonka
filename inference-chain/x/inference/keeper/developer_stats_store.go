package keeper

import (
	"context"
	"time"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

const (
	StatsDevelopersByEpoch             = "stats/developers/epoch"
	StatsDevelopersByTime              = "stats/developers/time"
	StatsDevelopersByInference         = "stats/developers/inference"
	StatsDevelopersByInferenceAndModel = "stats/model/inference"
)

func (k Keeper) setOrUpdateInferenceStatByTime(ctx context.Context, developer string, infStats types.InferenceStats, inferenceTime int64, epochId uint64) (uint64, error) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	byInferenceStore := prefix.NewStore(storeAdapter, types.KeyPrefix(StatsDevelopersByInference))
	byTimeStore := prefix.NewStore(storeAdapter, types.KeyPrefix(StatsDevelopersByTime))

	inferenceKeyStart := time.Now()
	timeKey := byInferenceStore.Get([]byte(infStats.InferenceId))
	k.LogInfo("setOrUpdateInferenceStatByTime: by-inference lookup complete", types.Stat,
		"inference_id", infStats.InferenceId,
		"developer", developer,
		"found", timeKey != nil,
		"duration_ms", durationMs(inferenceKeyStart),
	)
	if timeKey == nil {
		// completely new record
		k.LogInfo("completely new record, create record by time", types.Stat, "inference_id", infStats.InferenceId, "developer", developer)
		timeKey = developerByTimeAndInferenceKey(developer, uint64(inferenceTime), infStats.InferenceId)
		stats := types.DeveloperStatsByTime{
			EpochId:   epochId,
			Timestamp: inferenceTime,
			Inference: &infStats,
		}
		marshalStart := time.Now()
		encoded := k.cdc.MustMarshal(&stats)
		k.LogInfo("setOrUpdateInferenceStatByTime: marshal new stats", types.Stat,
			"inference_id", infStats.InferenceId,
			"developer", developer,
			"duration_ms", durationMs(marshalStart),
		)
		setByTimeStart := time.Now()
		byTimeStore.Set(timeKey, encoded)
		k.LogInfo("setOrUpdateInferenceStatByTime: by-time set complete", types.Stat,
			"inference_id", infStats.InferenceId,
			"developer", developer,
			"duration_ms", durationMs(setByTimeStart),
		)
		setByInferenceStart := time.Now()
		byInferenceStore.Set([]byte(infStats.InferenceId), timeKey)
		k.LogInfo("setOrUpdateInferenceStatByTime: by-inference set complete", types.Stat,
			"inference_id", infStats.InferenceId,
			"developer", developer,
			"duration_ms", durationMs(setByInferenceStart),
		)
		return 0, nil
	}

	var (
		statsByTime types.DeveloperStatsByTime
		prevEpochId uint64
	)

	timeLookupStart := time.Now()
	if val := byTimeStore.Get(timeKey); val != nil {
		k.LogInfo("setOrUpdateInferenceStatByTime: by-time lookup complete", types.Stat,
			"inference_id", infStats.InferenceId,
			"developer", developer,
			"found", true,
			"duration_ms", durationMs(timeLookupStart),
		)
		k.LogInfo("record found by time key", types.Stat, "inference_id", infStats.InferenceId, "developer", developer)
		unmarshalStart := time.Now()
		k.cdc.MustUnmarshal(val, &statsByTime)
		k.LogInfo("setOrUpdateInferenceStatByTime: unmarshal existing stats", types.Stat,
			"inference_id", infStats.InferenceId,
			"developer", developer,
			"duration_ms", durationMs(unmarshalStart),
		)
		prevEpochId = statsByTime.EpochId

		prevInferenceTime := statsByTime.Timestamp
		if prevInferenceTime != inferenceTime {
			statsByTime.Timestamp = inferenceTime
			deleteStart := time.Now()
			byTimeStore.Delete(timeKey)
			k.LogInfo("setOrUpdateInferenceStatByTime: by-time delete complete", types.Stat,
				"inference_id", infStats.InferenceId,
				"developer", developer,
				"duration_ms", durationMs(deleteStart),
			)
			timeKey = developerByTimeAndInferenceKey(developer, uint64(inferenceTime), infStats.InferenceId)
		}

		statsByTime.EpochId = epochId
		statsByTime.Inference.Status = infStats.Status
		statsByTime.Inference.TotalTokenCount = infStats.TotalTokenCount
		statsByTime.Inference.EpochId = infStats.EpochId
		statsByTime.Inference.ActualCostInCoins = infStats.ActualCostInCoins
	} else {
		k.LogInfo("setOrUpdateInferenceStatByTime: by-time lookup complete", types.Stat,
			"inference_id", infStats.InferenceId,
			"developer", developer,
			"found", false,
			"duration_ms", durationMs(timeLookupStart),
		)
		k.LogInfo("time key exists, record DO NOT exist", types.Stat, "inference_id", infStats.InferenceId, "developer", developer)
		statsByTime = types.DeveloperStatsByTime{
			EpochId:   epochId,
			Timestamp: inferenceTime,
			Inference: &infStats,
		}
	}
	marshalStart := time.Now()
	encoded := k.cdc.MustMarshal(&statsByTime)
	k.LogInfo("setOrUpdateInferenceStatByTime: marshal stats", types.Stat,
		"inference_id", infStats.InferenceId,
		"developer", developer,
		"duration_ms", durationMs(marshalStart),
	)
	setByTimeStart := time.Now()
	byTimeStore.Set(timeKey, encoded)
	k.LogInfo("setOrUpdateInferenceStatByTime: by-time set complete", types.Stat,
		"inference_id", infStats.InferenceId,
		"developer", developer,
		"duration_ms", durationMs(setByTimeStart),
	)
	setByInferenceStart := time.Now()
	byInferenceStore.Set([]byte(infStats.InferenceId), timeKey)
	k.LogInfo("setOrUpdateInferenceStatByTime: by-inference set complete", types.Stat,
		"inference_id", infStats.InferenceId,
		"developer", developer,
		"duration_ms", durationMs(setByInferenceStart),
	)

	return prevEpochId, nil
}

func (k Keeper) setInferenceStatsByModel(ctx context.Context, developer string, stats types.InferenceStats, inferenceTime int64) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	byModelsStore := prefix.NewStore(storeAdapter, types.KeyPrefix(StatsDevelopersByInferenceAndModel))

	modelKey := modelByTimeKey(stats.Model, inferenceTime, stats.InferenceId)
	setStart := time.Now()
	byModelsStore.Set(modelKey, developerByTimeAndInferenceKey(developer, uint64(inferenceTime), stats.InferenceId))
	k.LogInfo("setInferenceStatsByModel: set complete", types.Stat,
		"inference_id", stats.InferenceId,
		"developer", developer,
		"model", stats.Model,
		"duration_ms", durationMs(setStart),
	)
}

func (k Keeper) setOrUpdateInferenceStatsByEpoch(ctx context.Context, developer string, infStats types.InferenceStats, currentEpochId, prevEpochId uint64) {
	k.LogDebug("stat set by epoch", types.Stat, "inference_id", infStats.InferenceId, "developer", developer, "epoch_id", currentEpochId, "previously_known_epoch_id", prevEpochId)
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	epochStore := prefix.NewStore(storeAdapter, types.KeyPrefix(StatsDevelopersByEpoch))

	// === CASE 1: inference already exists, but was tagged by different epoch ===
	if prevEpochId != 0 && prevEpochId != currentEpochId {
		k.LogDebug("stat set by epoch: inference already exists, but was tagged by different epoch, clean up", types.Stat, "inference_id", infStats.InferenceId, "developer", developer, "epoch_id", currentEpochId)
		oldKey := developerByEpochKey(developer, prevEpochId)
		oldLookupStart := time.Now()
		if bz := epochStore.Get(oldKey); bz != nil {
			k.LogInfo("setOrUpdateInferenceStatsByEpoch: old epoch lookup complete", types.Stat,
				"inference_id", infStats.InferenceId,
				"developer", developer,
				"epoch_id", prevEpochId,
				"found", true,
				"duration_ms", durationMs(oldLookupStart),
			)
			var oldStats types.DeveloperStatsByEpoch
			unmarshalStart := time.Now()
			k.cdc.MustUnmarshal(bz, &oldStats)
			k.LogInfo("setOrUpdateInferenceStatsByEpoch: old epoch unmarshal complete", types.Stat,
				"inference_id", infStats.InferenceId,
				"developer", developer,
				"epoch_id", prevEpochId,
				"duration_ms", durationMs(unmarshalStart),
			)

			oldStats.InferenceIds = removeInferenceId(oldStats.InferenceIds, infStats.InferenceId)
			marshalStart := time.Now()
			encoded := k.cdc.MustMarshal(&oldStats)
			k.LogInfo("setOrUpdateInferenceStatsByEpoch: old epoch marshal complete", types.Stat,
				"inference_id", infStats.InferenceId,
				"developer", developer,
				"epoch_id", prevEpochId,
				"duration_ms", durationMs(marshalStart),
			)
			setStart := time.Now()
			epochStore.Set(oldKey, encoded)
			k.LogInfo("setOrUpdateInferenceStatsByEpoch: old epoch set complete", types.Stat,
				"inference_id", infStats.InferenceId,
				"developer", developer,
				"epoch_id", prevEpochId,
				"duration_ms", durationMs(setStart),
			)
		} else {
			k.LogInfo("setOrUpdateInferenceStatsByEpoch: old epoch lookup complete", types.Stat,
				"inference_id", infStats.InferenceId,
				"developer", developer,
				"epoch_id", prevEpochId,
				"found", false,
				"duration_ms", durationMs(oldLookupStart),
			)
		}
	}

	// === CASE 2: create new record or update existing with current_epoch_id ===
	k.LogDebug("stat set by epoch: new record or same epoch", types.Stat, "inference_id", infStats.InferenceId, "developer", developer, "epoch_id", currentEpochId)
	newKey := developerByEpochKey(developer, currentEpochId)
	var newStats types.DeveloperStatsByEpoch
	newLookupStart := time.Now()
	if bz := epochStore.Get(newKey); bz != nil {
		k.LogInfo("setOrUpdateInferenceStatsByEpoch: current epoch lookup complete", types.Stat,
			"inference_id", infStats.InferenceId,
			"developer", developer,
			"epoch_id", currentEpochId,
			"found", true,
			"duration_ms", durationMs(newLookupStart),
		)
		unmarshalStart := time.Now()
		k.cdc.MustUnmarshal(bz, &newStats)
		k.LogInfo("setOrUpdateInferenceStatsByEpoch: current epoch unmarshal complete", types.Stat,
			"inference_id", infStats.InferenceId,
			"developer", developer,
			"epoch_id", currentEpochId,
			"duration_ms", durationMs(unmarshalStart),
		)
		if newStats.InferenceIds == nil {
			newStats.InferenceIds = make([]string, 0)
		}
	} else {
		k.LogInfo("setOrUpdateInferenceStatsByEpoch: current epoch lookup complete", types.Stat,
			"inference_id", infStats.InferenceId,
			"developer", developer,
			"epoch_id", currentEpochId,
			"found", false,
			"duration_ms", durationMs(newLookupStart),
		)
		newStats = types.DeveloperStatsByEpoch{
			EpochId:      currentEpochId,
			InferenceIds: make([]string, 0),
		}
	}

	if !inferenceIdExists(newStats.InferenceIds, infStats.InferenceId) {
		newStats.InferenceIds = append(newStats.InferenceIds, infStats.InferenceId)
		marshalStart := time.Now()
		encoded := k.cdc.MustMarshal(&newStats)
		k.LogInfo("setOrUpdateInferenceStatsByEpoch: current epoch marshal complete", types.Stat,
			"inference_id", infStats.InferenceId,
			"developer", developer,
			"epoch_id", currentEpochId,
			"duration_ms", durationMs(marshalStart),
		)
		setStart := time.Now()
		epochStore.Set(newKey, encoded)
		k.LogInfo("setOrUpdateInferenceStatsByEpoch: current epoch set complete", types.Stat,
			"inference_id", infStats.InferenceId,
			"developer", developer,
			"epoch_id", currentEpochId,
			"duration_ms", durationMs(setStart),
		)
	}
	k.LogDebug("stat set by epoch: inference successfully added to epoch", types.Stat, "inference_id", infStats.InferenceId, "developer", developer, "epoch_id", currentEpochId)
}
