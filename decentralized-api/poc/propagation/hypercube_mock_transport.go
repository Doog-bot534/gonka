package propagation

import (
	"fmt"
	"sync"
)

type HypercubeReceiverHandler interface {
	OnHeaderHypercube(h BundleHeader, from string) error
	OnProofsHypercube(bundleID [32]byte, proofs []ProofItem, from string) error
}

type HypercubeMockTransport struct {
	mu        sync.RWMutex
	receivers map[string]HypercubeReceiverHandler
}

func NewHypercubeMockTransport() *HypercubeMockTransport {
	return &HypercubeMockTransport{
		receivers: make(map[string]HypercubeReceiverHandler),
	}
}

func (m *HypercubeMockTransport) RegisterReceiver(addr string, handler HypercubeReceiverHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.receivers[addr] = handler
}

func (m *HypercubeMockTransport) SendHeaderHypercube(to string, h BundleHeader) error {
	return m.SendHeaderHypercubeFrom("unknown", to, h)
}

func (m *HypercubeMockTransport) SendHeaderHypercubeFrom(from string, to string, h BundleHeader) error {
	m.mu.RLock()
	receiver := m.receivers[to]
	m.mu.RUnlock()

	if receiver == nil {
		return fmt.Errorf("receiver not found: %s", to)
	}

	return receiver.OnHeaderHypercube(h, from)
}

func (m *HypercubeMockTransport) SendProofsHypercubeFrom(from string, to string, bundleID [32]byte, proofs []ProofItem) error {
	m.mu.RLock()
	receiver := m.receivers[to]
	m.mu.RUnlock()

	if receiver == nil {
		return fmt.Errorf("receiver not found: %s", to)
	}

	return receiver.OnProofsHypercube(bundleID, proofs, from)
}

type HypercubePerParticipantSender struct {
	transport *HypercubeMockTransport
	fromAddr  string
}

func (m *HypercubeMockTransport) NewSenderFor(addr string) HypercubeSender {
	return &HypercubePerParticipantSender{
		transport: m,
		fromAddr:  addr,
	}
}

func (p *HypercubePerParticipantSender) SendHeaderHypercube(to string, h BundleHeader) error {
	return p.transport.SendHeaderHypercubeFrom(p.fromAddr, to, h)
}

func (p *HypercubePerParticipantSender) SendProofsHypercube(to string, bundleID [32]byte, proofs []ProofItem) error {
	return p.transport.SendProofsHypercubeFrom(p.fromAddr, to, bundleID, proofs)
}
