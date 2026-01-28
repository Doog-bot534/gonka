package propagation

import (
	"context"
	"fmt"
	"sync"

	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

type EpochDataProvider interface {
	GetEpochGroupData(ctx context.Context, epochIndex uint64) (*types.EpochGroupData, error)
}

type TreeManager struct {
	mu                sync.RWMutex
	trees             []*Tree
	epochDataProvider EpochDataProvider
	numTrees          int
	fanout            int
	currentEpochIndex uint64
}

func NewTreeManager(epochDataProvider EpochDataProvider, numTrees, fanout int) *TreeManager {
	return &TreeManager{
		epochDataProvider: epochDataProvider,
		numTrees:          numTrees,
		fanout:            fanout,
		trees:             []*Tree{},
	}
}

func (tm *TreeManager) GetTrees() []*Tree {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return tm.trees
}

func (tm *TreeManager) RebuildTreesForEpoch(ctx context.Context, currentEpochIndex uint64, blockHash []byte) ([]*Tree, error) {
	if currentEpochIndex == 0 {
		logging.Warn("Cannot rebuild trees for epoch 0 (no previous epoch)", types.PoC)
		return []*Tree{}, nil
	}

	previousEpochIndex := currentEpochIndex - 1

	logging.Info("Fetching participants from previous epoch for tree building", types.PoC,
		"currentEpoch", currentEpochIndex,
		"previousEpoch", previousEpochIndex)

	epochData, err := tm.epochDataProvider.GetEpochGroupData(ctx, previousEpochIndex)
	if err != nil {
		logging.Error("Failed to fetch epoch group data from previous epoch", types.PoC,
			"previousEpoch", previousEpochIndex,
			"error", err)
		return nil, fmt.Errorf("failed to fetch epoch group data for epoch %d: %w", previousEpochIndex, err)
	}

	if epochData == nil || len(epochData.ValidationWeights) == 0 {
		logging.Warn("No participants found in previous epoch", types.PoC,
			"previousEpoch", previousEpochIndex)
		return []*Tree{}, nil
	}

	participants := make([]WeightedParticipant, 0, len(epochData.ValidationWeights))
	for _, vw := range epochData.ValidationWeights {
		if vw.Weight > 0 {
			participants = append(participants, WeightedParticipant{
				Address: vw.MemberAddress,
				Weight:  uint64(vw.Weight),
			})
		}
	}

	logging.Info("Building propagation trees with weighted participants", types.PoC,
		"currentEpoch", currentEpochIndex,
		"participantCount", len(participants),
		"numTrees", tm.numTrees,
		"fanout", tm.fanout)

	trees := BuildTreesWithWeights(participants, blockHash, tm.numTrees, tm.fanout)

	tm.mu.Lock()
	tm.trees = trees
	tm.currentEpochIndex = currentEpochIndex
	tm.mu.Unlock()

	logging.Info("Propagation trees rebuilt successfully", types.PoC,
		"currentEpoch", currentEpochIndex,
		"treeCount", len(trees),
		"participantCount", len(participants))

	return trees, nil
}

func (tm *TreeManager) GetCurrentEpochIndex() uint64 {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return tm.currentEpochIndex
}
