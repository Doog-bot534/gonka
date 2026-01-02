package tx_manager

import (
	"decentralized-api/logging"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/nats-io/nats.go"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
)

const (
	batchAckWait = time.Minute // must exceed FlushTimeout to prevent redelivery
)

type LaneType string

const (
	LaneStartInference  LaneType = "start_inference"
	LaneFinishInference LaneType = "finish_inference"
	LanePocValidation   LaneType = "poc_validation"
)

var allowedLanes = []LaneType{
	LaneStartInference,
	LaneFinishInference,
	LanePocValidation,
}

var laneExpectedTypes = map[LaneType]sdk.Msg{
	LaneStartInference:  &inference.MsgStartInference{},
	LaneFinishInference: &inference.MsgFinishInference{},
	LanePocValidation:   &inference.MsgSubmitPocValidation{},
}

func (l LaneType) StreamName() string {
	return "txs_batch_" + string(l)
}

func (l LaneType) ConsumerName() string {
	return "batch-" + string(l) + "-consumer"
}

func GetBatchStreams() []string {
	streams := make([]string, 0, len(allowedLanes))
	for _, l := range allowedLanes {
		streams = append(streams, l.StreamName())
	}
	return streams
}

type LaneConfig struct {
	FlushSize    int           `koanf:"flush_size" json:"flush_size"`
	FlushTimeout time.Duration `koanf:"flush_timeout_seconds" json:"flush_timeout_seconds"`
	AckWait      time.Duration `koanf:"ack_wait_seconds" json:"ack_wait_seconds"`
}

type BatchConfig struct {
	FlushSize    int                    `koanf:"flush_size" json:"flush_size"`
	FlushTimeout time.Duration          `koanf:"flush_timeout_seconds" json:"flush_timeout_seconds"`
	AckWait      time.Duration          `koanf:"ack_wait_seconds" json:"ack_wait_seconds"`
	Lanes        map[string]*LaneConfig `koanf:"lanes" json:"lanes"`
}

func (c BatchConfig) Validate() error {
	for laneStr := range c.Lanes {
		found := false
		for _, allowed := range allowedLanes {
			if string(allowed) == laneStr {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("unknown lane: %s", laneStr)
		}
	}

	// Validate global defaults
	if err := c.validateLaneConfig(c.FlushSize, c.FlushTimeout, c.AckWait); err != nil {
		return fmt.Errorf("invalid global batch config: %w", err)
	}

	// Validate per-lane overrides
	for lane := range c.Lanes {
		eff := c.EffectiveLaneConfig(LaneType(lane))
		if err := c.validateLaneConfig(eff.FlushSize, eff.FlushTimeout, eff.AckWait); err != nil {
			return fmt.Errorf("invalid config for lane %s: %w", lane, err)
		}
	}

	return nil
}

func (c BatchConfig) validateLaneConfig(size int, timeout, ackWait time.Duration) error {
	if size < 1 {
		return fmt.Errorf("flush_size must be >= 1")
	}
	if timeout <= 0 {
		return fmt.Errorf("flush_timeout must be > 0")
	}
	// Use default ackWait if 0 for validation purposes
	if ackWait == 0 {
		ackWait = batchAckWait
	}
	if ackWait <= timeout {
		return fmt.Errorf("ack_wait (%v) must be > flush_timeout (%v)", ackWait, timeout)
	}
	return nil
}

func (c BatchConfig) EffectiveLaneConfig(lane LaneType) LaneConfig {
	size := c.FlushSize
	timeout := c.FlushTimeout
	ackWait := c.AckWait

	if l, ok := c.Lanes[string(lane)]; ok && l != nil {
		if l.FlushSize > 0 {
			size = l.FlushSize
		}
		if l.FlushTimeout > 0 {
			timeout = l.FlushTimeout
		}
		if l.AckWait > 0 {
			ackWait = l.AckWait
		}
	}

	if ackWait == 0 {
		ackWait = batchAckWait
	}

	return LaneConfig{
		FlushSize:    size,
		FlushTimeout: timeout,
		AckWait:      ackWait,
	}
}

type natsMessage interface {
	Ack() error
	InProgress() error
	Term() error
	GetData() []byte
}

type natsMsgWrapper struct {
	msg *nats.Msg
}

func (w *natsMsgWrapper) Ack() error        { return w.msg.Ack() }
func (w *natsMsgWrapper) InProgress() error { return w.msg.InProgress() }
func (w *natsMsgWrapper) Term() error       { return w.msg.Term() }
func (w *natsMsgWrapper) GetData() []byte   { return w.msg.Data }

type pendingMsg struct {
	msg     sdk.Msg
	natsMsg natsMessage
}

type BatchLane struct {
	laneType  LaneType
	config    LaneConfig
	mu        sync.Mutex
	pending   []pendingMsg
	createdAt time.Time
	onFlush   func(lane LaneType, batch []pendingMsg)
}

func NewBatchLane(laneType LaneType, config LaneConfig, onFlush func(LaneType, []pendingMsg)) *BatchLane {
	return &BatchLane{
		laneType: laneType,
		config:   config,
		pending:  make([]pendingMsg, 0, config.FlushSize),
		onFlush:  onFlush,
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

	l.onFlush(l.laneType, batch)
}

func (l *BatchLane) ExtendAckDeadlines() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, p := range l.pending {
		_ = p.natsMsg.InProgress()
	}
}

type BatchConsumer struct {
	js        nats.JetStreamContext
	codec     codec.Codec
	txManager TxManager
	config    BatchConfig
	lanes     map[LaneType]*BatchLane
}

func NewBatchConsumer(
	js nats.JetStreamContext,
	cdc codec.Codec,
	txManager TxManager,
	config BatchConfig,
) *BatchConsumer {
	c := &BatchConsumer{
		js:        js,
		codec:     cdc,
		txManager: txManager,
		config:    config,
		lanes:     make(map[LaneType]*BatchLane),
	}

	for _, laneType := range allowedLanes {
		eff := config.EffectiveLaneConfig(laneType)
		c.lanes[laneType] = NewBatchLane(laneType, eff, c.broadcastBatch)
	}

	return c
}

func (c *BatchConsumer) Start() error {
	for _, laneType := range allowedLanes {
		expectedType := laneExpectedTypes[laneType]
		if err := c.subscribeLane(laneType, laneType.StreamName(), laneType.ConsumerName(), expectedType); err != nil {
			return err
		}
	}

	go c.flushLoop()
	logging.Info("Batch consumer started", types.Messages,
		"flushSize", c.config.FlushSize,
		"flushTimeout", c.config.FlushTimeout)
	return nil
}

func (c *BatchConsumer) subscribeLane(laneType LaneType, stream, consumer string, expectedType sdk.Msg) error {
	eff := c.config.EffectiveLaneConfig(laneType)
	_, err := c.js.Subscribe(stream, func(msg *nats.Msg) {
		c.handleLaneMsg(laneType, msg, expectedType)
	},
		nats.Durable(consumer),
		nats.ManualAck(),
		nats.AckWait(eff.AckWait),
	)
	return err
}

func (c *BatchConsumer) handleLaneMsg(laneType LaneType, msg *nats.Msg, expectedType sdk.Msg) {
	c.handleLaneMsgInternal(laneType, &natsMsgWrapper{msg: msg}, expectedType)
}

func (c *BatchConsumer) handleLaneMsgInternal(laneType LaneType, wrapped natsMessage, expectedType sdk.Msg) {
	if err := wrapped.InProgress(); err != nil {
		logging.Error("Failed to mark msg in progress", types.Messages, "lane", laneType, "error", err)
	}
	sdkMsg, err := c.unmarshalMsg(wrapped.GetData())
	if err != nil {
		logging.Error("Failed to unmarshal msg", types.Messages, "lane", laneType, "error", err)
		wrapped.Term()
		return
	}

	// Type assertion
	if reflect.TypeOf(sdkMsg) != reflect.TypeOf(expectedType) {
		logging.Error("Unexpected message type", types.Messages,
			"lane", laneType,
			"expected", reflect.TypeOf(expectedType),
			"got", reflect.TypeOf(sdkMsg))
		wrapped.Term()
		return
	}

	lane := c.lanes[laneType]
	if lane.Add(pendingMsg{msg: sdkMsg, natsMsg: wrapped}) {
		lane.Flush()
	}
}

func (c *BatchConsumer) flushLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for now := range ticker.C {
		for _, lane := range c.lanes {
			lane.ExtendAckDeadlines()
			lane.FlushIfDue(now)
		}
	}
}

func (c *BatchConsumer) broadcastBatch(lane LaneType, batch []pendingMsg) {
	msgs := make([]sdk.Msg, len(batch))
	for i, p := range batch {
		msgs[i] = p.msg
	}

	logging.Info("Broadcasting batch", types.Messages, "lane", lane, "count", len(msgs))

	if err := c.txManager.SendBatchAsyncWithRetry(msgs); err != nil {
		logging.Error("Failed to hand off batch to TxManager", types.Messages, "lane", lane, "error", err)
	}

	for _, p := range batch {
		p.natsMsg.Ack()
	}
}

func (c *BatchConsumer) unmarshalMsg(data []byte) (sdk.Msg, error) {
	var msg sdk.Msg
	if err := c.codec.UnmarshalInterfaceJSON(data, &msg); err != nil {
		return nil, err
	}
	return msg, nil
}

func (c *BatchConsumer) PublishStartInference(msg *inference.MsgStartInference) error {
	return c.publishMsg(LaneStartInference, msg)
}

func (c *BatchConsumer) PublishFinishInference(msg *inference.MsgFinishInference) error {
	return c.publishMsg(LaneFinishInference, msg)
}

func (c *BatchConsumer) PublishPocValidation(msg *inference.MsgSubmitPocValidation) error {
	return c.publishMsg(LanePocValidation, msg)
}

func (c *BatchConsumer) publishMsg(stream LaneType, msg sdk.Msg) error {
	data, err := c.codec.MarshalInterfaceJSON(msg)
	if err != nil {
		return err
	}
	_, err = c.js.Publish(stream.StreamName(), data)
	return err
}
