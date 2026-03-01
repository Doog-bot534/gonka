package keeper

import (
	"context"
	"encoding/binary"

	corestore "cosmossdk.io/core/store"
	storetypes "cosmossdk.io/store/types"
)

var (
	finishedInferenceQueueEntryPrefix = []byte{0x01}
	finishedInferenceQueueNextSeqKey  = []byte{0x02}
)

// FinishedInferenceQueue stores completed inference IDs in FIFO order.
// We intentionally process this queue in EndBlock to keep Start/Finish tx execution lightweight
// and defer expensive epoch/model reads used for InferenceValidationDetails construction.
func (k Keeper) EnqueueFinishedInference(ctx context.Context, inferenceID string) error {
	transientStore := k.transientStoreService.OpenTransientStore(ctx)
	nextSeq, err := k.getAndIncrementFinishedInferenceQueueSeq(transientStore)
	if err != nil {
		return err
	}
	return transientStore.Set(finishedInferenceQueueEntryKey(nextSeq), []byte(inferenceID))
}

// ListFinishedInferenceIDs lists all queued finished inference IDs in FIFO order.
func (k Keeper) ListFinishedInferenceIDs(ctx context.Context) []string {
	transientStore := k.transientStoreService.OpenTransientStore(ctx)

	// Preallocate the slice by fetching the current sequence length
	var capacity uint64
	if nextSeqBz, err := transientStore.Get(finishedInferenceQueueNextSeqKey); err == nil && len(nextSeqBz) == 8 {
		capacity = binary.BigEndian.Uint64(nextSeqBz)
	}

	it, err := transientStore.Iterator(
		finishedInferenceQueueEntryPrefix,
		storetypes.PrefixEndBytes(finishedInferenceQueueEntryPrefix),
	)
	if err != nil {
		return nil
	}
	defer it.Close()

	finishedInferenceIDs := make([]string, 0, capacity)
	for ; it.Valid(); it.Next() {
		finishedInferenceIDs = append(finishedInferenceIDs, string(it.Value()))
	}
	if err := it.Error(); err != nil {
		return nil
	}
	return finishedInferenceIDs
}

func (k Keeper) getAndIncrementFinishedInferenceQueueSeq(transientStore corestore.KVStore) (uint64, error) {
	nextSeqBz, err := transientStore.Get(finishedInferenceQueueNextSeqKey)
	if err != nil {
		return 0, err
	}

	var nextSeq uint64
	if len(nextSeqBz) == 8 {
		nextSeq = binary.BigEndian.Uint64(nextSeqBz)
	}

	var updatedNextSeqBz [8]byte
	binary.BigEndian.PutUint64(updatedNextSeqBz[:], nextSeq+1)
	if err := transientStore.Set(finishedInferenceQueueNextSeqKey, updatedNextSeqBz[:]); err != nil {
		return 0, err
	}
	return nextSeq, nil
}

func finishedInferenceQueueEntryKey(seq uint64) []byte {
	var key [9]byte
	key[0] = finishedInferenceQueueEntryPrefix[0]
	binary.BigEndian.PutUint64(key[1:], seq)
	return key[:]
}
