package keeper

import (
	"context"
	"encoding/binary"

	corestore "cosmossdk.io/core/store"
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
//
// NOTE: We do not use transient store iterators here because some production transient-store
// implementations do not support Iterator reliably. We instead scan by sequence number.
func (k Keeper) ListFinishedInferenceIDs(ctx context.Context) ([]string, error) {
	transientStore := k.transientStoreService.OpenTransientStore(ctx)

	nextSeqBz, err := transientStore.Get(finishedInferenceQueueNextSeqKey)
	if err != nil {
		return nil, err
	}

	var nextSeq uint64
	if len(nextSeqBz) == 8 {
		nextSeq = binary.BigEndian.Uint64(nextSeqBz)
	}

	finishedInferenceIDs := make([]string, 0, nextSeq)
	for seq := uint64(0); seq < nextSeq; seq++ {
		bz, err := transientStore.Get(finishedInferenceQueueEntryKey(seq))
		if err != nil {
			return nil, err
		}
		if len(bz) == 0 {
			continue
		}
		finishedInferenceIDs = append(finishedInferenceIDs, string(bz))
	}

	return finishedInferenceIDs, nil
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
