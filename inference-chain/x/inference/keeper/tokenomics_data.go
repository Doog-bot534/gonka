package keeper

import (
	"context"
	"fmt"
	"math"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

// safeAddUint64 returns a + b, or an error if the addition would overflow.
func safeAddUint64(a, b uint64, field string) (uint64, error) {
	if a > math.MaxUint64-b {
		return 0, fmt.Errorf("tokenomics %s overflow: %d + %d exceeds uint64", field, a, b)
	}
	return a + b, nil
}

// SetTokenomicsData set tokenomicsData in the store
func (k Keeper) SetTokenomicsData(ctx context.Context, tokenomicsData types.TokenomicsData) error {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.TokenomicsDataKey))
	b, err := k.cdc.Marshal(&tokenomicsData)
	if err != nil {
		return err
	}
	store.Set([]byte{0}, b)
	return nil
}

// GetTokenomicsData returns tokenomicsData
func (k Keeper) GetTokenomicsData(ctx context.Context) (val types.TokenomicsData, found bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.TokenomicsDataKey))

	b := store.Get([]byte{0})
	if b == nil {
		return val, false
	}

	err := k.cdc.Unmarshal(b, &val)
	if err != nil {
		return val, false
	}
	return val, true
}

func (k Keeper) AddTokenomicsData(ctx context.Context, tokenomicsData *types.TokenomicsData) error {
	k.LogInfo("Adding tokenomics data", types.Tokenomics, "tokenomicsData", tokenomicsData)
	current, found := k.GetTokenomicsData(ctx)
	if !found {
		k.LogError("Tokenomics data not found", types.Tokenomics)
	}
	var addErr error
	if current.TotalBurned, addErr = safeAddUint64(current.TotalBurned, tokenomicsData.TotalBurned, "TotalBurned"); addErr != nil {
		return addErr
	}
	if current.TotalFees, addErr = safeAddUint64(current.TotalFees, tokenomicsData.TotalFees, "TotalFees"); addErr != nil {
		return addErr
	}
	if current.TotalSubsidies, addErr = safeAddUint64(current.TotalSubsidies, tokenomicsData.TotalSubsidies, "TotalSubsidies"); addErr != nil {
		return addErr
	}
	if current.TotalRefunded, addErr = safeAddUint64(current.TotalRefunded, tokenomicsData.TotalRefunded, "TotalRefunded"); addErr != nil {
		return addErr
	}
	err := k.SetTokenomicsData(ctx, current)
	if err != nil {
		return err
	}
	newData, _ := k.GetTokenomicsData(ctx)
	k.LogInfo("Tokenomics data added", types.Tokenomics, "tokenomicsData", newData)
	return nil
}
