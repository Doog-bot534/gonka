package propagation

import (
	"fmt"
	"sync"
)

type MockTransport struct {
	mu        sync.RWMutex
	receivers map[string]ReceiverHandler
}

func NewMockTransport() *MockTransport {
	return &MockTransport{
		receivers: make(map[string]ReceiverHandler),
	}
}

func (m *MockTransport) RegisterReceiver(addr string, handler ReceiverHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.receivers[addr] = handler
}

func (m *MockTransport) SendHeader(treeIdx int, to string, h BundleHeader) error {
	m.mu.RLock()
	receiver := m.receivers[to]
	m.mu.RUnlock()
	
	if receiver == nil {
		return fmt.Errorf("receiver not found: %s", to)
	}
	
	return receiver.OnHeader(h, treeIdx)
}

type MockPubKeyProvider struct {
	keys map[string]string
}

func NewMockPubKeyProvider() *MockPubKeyProvider {
	return &MockPubKeyProvider{
		keys: make(map[string]string),
	}
}

func (m *MockPubKeyProvider) RegisterKey(addr string, hexPubKey string) {
	m.keys[addr] = hexPubKey
}

func (m *MockPubKeyProvider) GetPubKey(addr string) (string, error) {
	key, ok := m.keys[addr]
	if !ok {
		return "", fmt.Errorf("public key not found for %s", addr)
	}
	return key, nil
}
