package tx_manager

import (
	"context"
	"sync"
	"testing"
	"time"

	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/golang/protobuf/proto"
	"github.com/google/uuid"
	"github.com/ignite/cli/v28/ignite/pkg/cosmosclient/mocks"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/productscience/inference/api/inference/inference"
	testutil "github.com/productscience/inference/testutil/cosmoclient"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"decentralized-api/apiconfig"
)

type mockTxManager struct {
	sendBatchCalls [][]sdk.Msg
	mu             sync.Mutex
	onSendBatch    func()
}

func (m *mockTxManager) SendBatchAsyncWithRetry(msgs []sdk.Msg) error {
	m.mu.Lock()
	m.sendBatchCalls = append(m.sendBatchCalls, msgs)
	m.mu.Unlock()
	if m.onSendBatch != nil {
		m.onSendBatch()
	}
	return nil
}

type mockNatsMsg struct {
	ackCalled        bool
	inProgressCalled int
	termCalled       bool
	data             []byte
	mu               sync.Mutex
}

func (m *mockNatsMsg) Ack() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ackCalled = true
	return nil
}

func (m *mockNatsMsg) InProgress() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inProgressCalled++
	return nil
}

func (m *mockNatsMsg) Term() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.termCalled = true
	return nil
}

func (m *mockNatsMsg) GetData() []byte {
	return m.data
}

func (m *mockNatsMsg) isAcked() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ackCalled
}

func (m *mockTxManager) getBatchCalls() [][]sdk.Msg {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sendBatchCalls
}

func (m *mockTxManager) SendTransactionAsyncWithRetry(sdk.Msg) (*sdk.TxResponse, error) {
	return &sdk.TxResponse{}, nil
}
func (m *mockTxManager) SendTransactionAsyncNoRetry(sdk.Msg) (*sdk.TxResponse, error) {
	return &sdk.TxResponse{}, nil
}
func (m *mockTxManager) SendTransactionSyncNoRetry(proto.Message) (*ctypes.ResultTx, error) {
	return nil, nil
}
func (m *mockTxManager) BroadcastMessages(string, ...sdk.Msg) (*sdk.TxResponse, time.Time, error) {
	return &sdk.TxResponse{}, time.Now(), nil
}
func (m *mockTxManager) GetClientContext() client.Context    { return client.Context{} }
func (m *mockTxManager) GetKeyring() *keyring.Keyring        { return nil }
func (m *mockTxManager) GetApiAccount() apiconfig.ApiAccount { return apiconfig.ApiAccount{} }
func (m *mockTxManager) Status(context.Context) (*ctypes.ResultStatus, error) {
	return nil, nil
}
func (m *mockTxManager) BankBalances(context.Context, string) ([]sdk.Coin, error) {
	return nil, nil
}
func (m *mockTxManager) GetJetStream() nats.JetStreamContext { return nil }

func startTestNatsServer(t *testing.T) (*server.Server, nats.JetStreamContext) {
	opts := &server.Options{
		Host:      "127.0.0.1",
		Port:      -1, // random port
		JetStream: true,
		StoreDir:  t.TempDir(),
	}

	ns, err := server.NewServer(opts)
	require.NoError(t, err)

	go ns.Start()
	require.True(t, ns.ReadyForConnections(5*time.Second))

	nc, err := nats.Connect(ns.ClientURL())
	require.NoError(t, err)

	js, err := nc.JetStream()
	require.NoError(t, err)

	// Create test streams
	for _, stream := range GetBatchStreams() {
		_, err = js.AddStream(&nats.StreamConfig{
			Name:     stream,
			Subjects: []string{stream},
			Storage:  nats.MemoryStorage,
		})
		require.NoError(t, err)
	}

	t.Cleanup(func() {
		nc.Close()
		ns.Shutdown()
	})

	return ns, js
}

func getTestCodec(t *testing.T) codec.Codec {
	const (
		network     = "cosmos"
		accountName = "cosmosaccount"
		mnemonic    = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
		passphrase  = "testpass"
	)

	rpc := mocks.NewRPCClient(t)
	client := testutil.NewMockClient(t, rpc, network, accountName, mnemonic, passphrase)
	return client.Context().Codec
}

func TestBatchConsumer_FlushOnSize(t *testing.T) {
	_, js := startTestNatsServer(t)
	cdc := getTestCodec(t)

	mockMgr := &mockTxManager{}

	config := BatchConfig{
		FlushSize:    5,
		FlushTimeout: 10 * time.Second,
	}

	consumer := NewBatchConsumer(js, cdc, mockMgr, config)
	err := consumer.Start()
	require.NoError(t, err)

	// Publish 5 start inference messages (should trigger flush)
	for i := 0; i < 5; i++ {
		msg := &inference.MsgStartInference{
			Creator:     "creator",
			InferenceId: uuid.New().String(),
			Model:       "test-model",
		}
		err := consumer.PublishStartInference(msg)
		require.NoError(t, err)
	}

	// Wait for processing
	time.Sleep(500 * time.Millisecond)

	calls := mockMgr.getBatchCalls()
	require.Len(t, calls, 1)
	assert.Len(t, calls[0], 5)
}

func TestBatchConsumer_FlushOnTimeout(t *testing.T) {
	_, js := startTestNatsServer(t)
	cdc := getTestCodec(t)

	mockMgr := &mockTxManager{}

	config := BatchConfig{
		FlushSize:    100, // high threshold
		FlushTimeout: 2 * time.Second,
	}

	consumer := NewBatchConsumer(js, cdc, mockMgr, config)
	err := consumer.Start()
	require.NoError(t, err)

	// Publish only 2 messages (below threshold)
	for i := 0; i < 2; i++ {
		msg := &inference.MsgStartInference{
			Creator:     "creator",
			InferenceId: uuid.New().String(),
		}
		err := consumer.PublishStartInference(msg)
		require.NoError(t, err)
	}

	// Wait for messages to be consumed
	time.Sleep(500 * time.Millisecond)
	assert.Len(t, mockMgr.getBatchCalls(), 0)

	// Wait for timeout flush (ticker checks every second, timeout is 2s)
	time.Sleep(3 * time.Second)
	assert.Len(t, mockMgr.getBatchCalls(), 1)
}

func TestBatchConsumer_SeparateQueues(t *testing.T) {
	_, js := startTestNatsServer(t)
	cdc := getTestCodec(t)

	mockMgr := &mockTxManager{}

	config := BatchConfig{
		FlushSize:    3,
		FlushTimeout: 10 * time.Second,
	}

	consumer := NewBatchConsumer(js, cdc, mockMgr, config)
	err := consumer.Start()
	require.NoError(t, err)

	// Publish 3 start messages
	for i := 0; i < 3; i++ {
		msg := &inference.MsgStartInference{
			Creator:     "creator",
			InferenceId: uuid.New().String(),
		}
		err := consumer.PublishStartInference(msg)
		require.NoError(t, err)
	}

	// Publish 3 finish messages
	for i := 0; i < 3; i++ {
		msg := &inference.MsgFinishInference{
			Creator:     "creator",
			InferenceId: uuid.New().String(),
		}
		err := consumer.PublishFinishInference(msg)
		require.NoError(t, err)
	}

	time.Sleep(500 * time.Millisecond)

	// Should have 2 batch calls (one for start, one for finish)
	calls := mockMgr.getBatchCalls()
	assert.Len(t, calls, 2)
}

func TestBatchConsumer_Persistence(t *testing.T) {
	_, js := startTestNatsServer(t)
	cdc := getTestCodec(t)

	mockMgr := &mockTxManager{}

	config := BatchConfig{
		FlushSize:    10,
		FlushTimeout: 2 * time.Second,
	}

	// Publish messages before consumer starts (simulating restart)
	for i := 0; i < 3; i++ {
		msg := &inference.MsgStartInference{
			Creator:     "creator",
			InferenceId: uuid.New().String(),
		}
		data, err := cdc.MarshalInterfaceJSON(msg)
		require.NoError(t, err)
		_, err = js.Publish(LaneStartInference.StreamName(), data)
		require.NoError(t, err)
	}

	// Now start consumer (simulating restart recovery)
	consumer := NewBatchConsumer(js, cdc, mockMgr, config)
	err := consumer.Start()
	require.NoError(t, err)

	// Wait for messages to be consumed and timeout flush
	time.Sleep(3 * time.Second)

	// Messages should be recovered and broadcast
	assert.Len(t, mockMgr.getBatchCalls(), 1)
}

func TestBatchConsumer_TimerResets(t *testing.T) {
	cdc := getTestCodec(t)
	mockMgr := &mockTxManager{}
	config := BatchConfig{
		FlushSize:    10,
		FlushTimeout: 2 * time.Second,
	}

	consumer := NewBatchConsumer(nil, cdc, mockMgr, config)
	lane := consumer.lanes[LaneStartInference]

	// Enqueue first message
	msg1 := &inference.MsgStartInference{Creator: "c1", InferenceId: "i1"}
	data1, _ := cdc.MarshalInterfaceJSON(msg1)
	m1 := &mockNatsMsg{data: data1}
	consumer.handleLaneMsgInternal(LaneStartInference, m1, &types.MsgStartInference{})

	assert.False(t, lane.createdAt.IsZero())
	firstCreatedAt := lane.createdAt

	// Flush manually
	lane.Flush()

	assert.True(t, lane.createdAt.IsZero())
	assert.Len(t, mockMgr.getBatchCalls(), 1)

	// Enqueue second message
	msg2 := &inference.MsgStartInference{Creator: "c2", InferenceId: "i2"}
	data2, _ := cdc.MarshalInterfaceJSON(msg2)
	m2 := &mockNatsMsg{data: data2}
	consumer.handleLaneMsgInternal(LaneStartInference, m2, &types.MsgStartInference{})

	assert.False(t, lane.createdAt.IsZero())
	assert.NotEqual(t, firstCreatedAt, lane.createdAt)
}

func TestBatchConsumer_AckExtension(t *testing.T) {
	cdc := getTestCodec(t)
	mockMgr := &mockTxManager{}
	config := BatchConfig{
		FlushSize:    10,
		FlushTimeout: 10 * time.Second,
	}

	consumer := NewBatchConsumer(nil, cdc, mockMgr, config)
	lane := consumer.lanes[LaneStartInference]

	msg := &inference.MsgStartInference{Creator: "c1", InferenceId: "i1"}
	data, _ := cdc.MarshalInterfaceJSON(msg)
	m1 := &mockNatsMsg{data: data}

	consumer.handleLaneMsgInternal(LaneStartInference, m1, &types.MsgStartInference{})

	// Initial InProgress call in handleMsg
	assert.Equal(t, 1, m1.inProgressCalled)

	lane.ExtendAckDeadlines()
	assert.Equal(t, 2, m1.inProgressCalled)
}

func TestBatchConsumer_AckAfterHandoff(t *testing.T) {
	cdc := getTestCodec(t)
	var ackCalled bool
	mockMgr := &mockTxManager{}
	config := BatchConfig{
		FlushSize: 1,
	}

	consumer := NewBatchConsumer(nil, cdc, mockMgr, config)

	msg := &inference.MsgStartInference{Creator: "c1", InferenceId: "i1"}
	data, _ := cdc.MarshalInterfaceJSON(msg)
	m1 := &mockNatsMsg{data: data}

	mockMgr.onSendBatch = func() {
		ackCalled = m1.isAcked()
	}

	consumer.handleLaneMsgInternal(LaneStartInference, m1, &types.MsgStartInference{})

	assert.False(t, ackCalled, "Ack should be called AFTER handoff to TxManager")
	assert.True(t, m1.isAcked(), "Ack should have been called by now")
}

func TestBatchConsumer_UnmarshalFailure_Term(t *testing.T) {
	cdc := getTestCodec(t)
	consumer := NewBatchConsumer(nil, cdc, nil, BatchConfig{})

	m1 := &mockNatsMsg{data: []byte("invalid json")}

	consumer.handleLaneMsgInternal(LaneStartInference, m1, &types.MsgStartInference{})

	assert.True(t, m1.termCalled)
	assert.Len(t, consumer.lanes[LaneStartInference].pending, 0)
}

func TestBatchConsumer_WrongType_Term(t *testing.T) {
	cdc := getTestCodec(t)
	consumer := NewBatchConsumer(nil, cdc, nil, BatchConfig{})

	// Message is valid SDK Msg but wrong type for the lane
	msg := &inference.MsgFinishInference{Creator: "c1", InferenceId: "i1"}
	data, _ := cdc.MarshalInterfaceJSON(msg)
	m1 := &mockNatsMsg{data: data}

	consumer.handleLaneMsgInternal(LaneStartInference, m1, &types.MsgStartInference{})

	assert.True(t, m1.termCalled)
	assert.Len(t, consumer.lanes[LaneStartInference].pending, 0)
}

func TestBatchConsumer_Lanes(t *testing.T) {
	assert.Equal(t, 2, len(allowedLanes))
	assert.Contains(t, allowedLanes, LaneStartInference)
	assert.Contains(t, allowedLanes, LaneFinishInference)
}

func TestBatchConfig_EffectiveLaneConfig(t *testing.T) {
	baseConfig := BatchConfig{
		FlushSize:    10,
		FlushTimeout: 5 * time.Second,
		AckWait:      30 * time.Second,
	}

	t.Run("defaults when no overrides", func(t *testing.T) {
		eff := baseConfig.EffectiveLaneConfig(LaneStartInference)
		assert.Equal(t, 10, eff.FlushSize)
		assert.Equal(t, 5*time.Second, eff.FlushTimeout)
		assert.Equal(t, 30*time.Second, eff.AckWait)
	})

	t.Run("per-lane overrides", func(t *testing.T) {
		config := baseConfig
		config.Lanes = map[string]*LaneConfig{
			string(LaneStartInference): {
				FlushSize:    20,
				FlushTimeout: 10 * time.Second,
				AckWait:      60 * time.Second,
			},
		}

		effStart := config.EffectiveLaneConfig(LaneStartInference)
		assert.Equal(t, 20, effStart.FlushSize)
		assert.Equal(t, 10*time.Second, effStart.FlushTimeout)
		assert.Equal(t, 60*time.Second, effStart.AckWait)

		effFinish := config.EffectiveLaneConfig(LaneFinishInference)
		assert.Equal(t, 10, effFinish.FlushSize)
		assert.Equal(t, 5*time.Second, effFinish.FlushTimeout)
		assert.Equal(t, 30*time.Second, effFinish.AckWait)
	})

	t.Run("partial overrides", func(t *testing.T) {
		config := baseConfig
		config.Lanes = map[string]*LaneConfig{
			string(LaneStartInference): {
				FlushSize: 20,
			},
		}

		eff := config.EffectiveLaneConfig(LaneStartInference)
		assert.Equal(t, 20, eff.FlushSize)
		assert.Equal(t, 5*time.Second, eff.FlushTimeout)
		assert.Equal(t, 30*time.Second, eff.AckWait)
	})

	t.Run("hard-coded ack-wait default", func(t *testing.T) {
		config := BatchConfig{
			FlushSize:    10,
			FlushTimeout: 5 * time.Second,
		}
		eff := config.EffectiveLaneConfig(LaneStartInference)
		assert.Equal(t, batchAckWait, eff.AckWait)
	})
}

func TestBatchConfig_Validate(t *testing.T) {
	t.Run("valid default", func(t *testing.T) {
		config := BatchConfig{
			FlushSize:    10,
			FlushTimeout: 5 * time.Second,
			AckWait:      30 * time.Second,
		}
		assert.NoError(t, config.Validate())
	})

	t.Run("invalid size", func(t *testing.T) {
		config := BatchConfig{
			FlushSize:    0,
			FlushTimeout: 5 * time.Second,
		}
		assert.Error(t, config.Validate())
	})

	t.Run("invalid timeout", func(t *testing.T) {
		config := BatchConfig{
			FlushSize:    10,
			FlushTimeout: 0,
		}
		assert.Error(t, config.Validate())
	})

	t.Run("ack_wait <= flush_timeout", func(t *testing.T) {
		config := BatchConfig{
			FlushSize:    10,
			FlushTimeout: 10 * time.Second,
			AckWait:      10 * time.Second,
		}
		assert.Error(t, config.Validate())
	})

	t.Run("unknown lane", func(t *testing.T) {
		config := BatchConfig{
			FlushSize:    10,
			FlushTimeout: 5 * time.Second,
			Lanes: map[string]*LaneConfig{
				"unknown": {FlushSize: 5},
			},
		}
		assert.Error(t, config.Validate())
	})

	t.Run("invalid lane override", func(t *testing.T) {
		config := BatchConfig{
			FlushSize:    10,
			FlushTimeout: 5 * time.Second,
			Lanes: map[string]*LaneConfig{
				string(LaneStartInference): {
					FlushSize:    5,
					FlushTimeout: 10 * time.Second,
					AckWait:      5 * time.Second, // less than timeout
				},
			},
		}
		assert.Error(t, config.Validate())
	})
}
