package tx_manager

import (
	"fmt"
	"sync"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

type LaneType string

const (
	LaneStartInference  LaneType = "start_inference"
	LaneFinishInference LaneType = "finish_inference"
	LanePocValidation   LaneType = "poc_validation"
	LanePocBatch        LaneType = "poc_batch"
)

var allowedLanes = []LaneType{
	LaneStartInference,
	LaneFinishInference,
	LanePocValidation,
	LanePocBatch,
}

var laneExpectedTypes = map[LaneType]sdk.Msg{
	// These have to be `types`, not `inference` like the rest of the API code,
	// because when we serialize/deserialize using the client Codec, they are converted from
	// `inference` to `types`
	LaneStartInference:  &types.MsgStartInference{},
	LaneFinishInference: &types.MsgFinishInference{},
	LanePocValidation:   &types.MsgSubmitPocValidation{},
	LanePocBatch:        &types.MsgSubmitPocBatch{},
}

func (l LaneType) StreamName() string {
	return "txs_batch_" + string(l)
}

func (l LaneType) ConsumerName() string {
	return "batch-" + string(l) + "-consumer"
}

type BatchLane struct {
	laneType       LaneType
	config         LaneConfig
	mu             sync.Mutex
	pending        []pendingMsg
	createdAt      time.Time
	onFlush        func(lane *BatchLane, batch []pendingMsg)
	batchConverter func(msgs []sdk.Msg) (sdk.Msg, error)
}

func NewBatchLane(laneType LaneType, config LaneConfig, onFlush func(*BatchLane, []pendingMsg)) *BatchLane {
	return &BatchLane{
		laneType:       laneType,
		config:         config,
		pending:        make([]pendingMsg, 0, config.FlushSize),
		onFlush:        onFlush,
		batchConverter: GetConverter(laneType),
	}
}

func (l *BatchLane) Add(msg pendingMsg) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.pending) == 0 {
		l.createdAt = time.Now()
	}
	l.pending = append(l.pending, msg)
	return len(l.pending) >= l.config.FlushSize
}

func (l *BatchLane) FlushIfDue(now time.Time) {
	l.mu.Lock()
	shouldFlush := len(l.pending) > 0 && now.Sub(l.createdAt) >= l.config.FlushTimeout
	l.mu.Unlock()
	if shouldFlush {
		l.Flush()
	}
}

func (l *BatchLane) Flush() {
	l.mu.Lock()
	batch := l.pending
	if len(batch) == 0 {
		l.mu.Unlock()
		return
	}
	l.pending = make([]pendingMsg, 0, l.config.FlushSize)
	l.createdAt = time.Time{}
	l.mu.Unlock()

	l.onFlush(l, batch)
}

func (l *BatchLane) ExtendAckDeadlines() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, p := range l.pending {
		_ = p.natsMsg.InProgress()
	}
}

func GetBatchStreams() []string {
	streams := make([]string, 0, len(allowedLanes))
	for _, l := range allowedLanes {
		streams = append(streams, l.StreamName())
	}
	return streams
}

func GetConverter(laneType LaneType) func(msgs []sdk.Msg) (sdk.Msg, error) {
	if laneType == LanePocValidation {
		return ConvertPocValidationBatch
	}
	return nil
}

func ConvertPocValidationBatch(msgs []sdk.Msg) (sdk.Msg, error) {
	if len(msgs) == 0 {
		return nil, fmt.Errorf("zero length list of messages")
	}
	data := make([]*types.PocValidationData, len(msgs))
	creator := ""
	for i, msg := range msgs {
		validation, ok := msg.(*types.MsgSubmitPocValidation)
		if !ok {
			return nil, fmt.Errorf("unexpected message type: %T", msg)
		}
		data[i] = validation.Data
		if creator == "" {
			creator = validation.Creator
		} else {
			if creator != validation.Creator {
				return nil, fmt.Errorf("all messages must be from the same creator")
			}
		}
	}
	return &types.MsgSubmitPocValidationBatch{
		Creator: creator,
		Data:    data,
	}, nil
}
