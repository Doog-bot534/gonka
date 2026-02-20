package propagation

import (
	propagationpb "decentralized-api/poc/propagation/proto"
	"fmt"
	"sync"

	"google.golang.org/protobuf/proto"
)

type FLTQReceiverHandler interface {
	OnHeader(h BundleHeader, from string) error
}

type FLTQMockTransport struct {
	mu        sync.RWMutex
	receivers map[string]FLTQReceiverHandler
}

func NewFLTQMockTransport() *FLTQMockTransport {
	return &FLTQMockTransport{
		receivers: make(map[string]FLTQReceiverHandler),
	}
}

func (m *FLTQMockTransport) RegisterReceiver(addr string, handler FLTQReceiverHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.receivers[addr] = handler
}

func (m *FLTQMockTransport) SendHeaderFLTQ(to string, h BundleHeader) error {
	return m.SendHeaderFLTQFrom("unknown", to, h)
}

func (m *FLTQMockTransport) SendHeaderFLTQFrom(from string, to string, h BundleHeader) error {
	_ = from
	m.mu.RLock()
	receiver, ok := m.receivers[to]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("receiver not found: %s", to)
	}

	pbHeader := HeaderToProto(h)
	data, err := proto.Marshal(pbHeader)
	if err != nil {
		return fmt.Errorf("marshal header: %w", err)
	}

	var decoded propagationpb.PropagationHeader
	if err := proto.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("unmarshal header: %w", err)
	}

	header, err := ProtoToHeader(&decoded)
	if err != nil {
		return fmt.Errorf("convert header: %w", err)
	}

	return receiver.OnHeader(header, header.Participant)
}

type FLTQPerParticipantSender struct {
	transport *FLTQMockTransport
	fromAddr  string
}

func (m *FLTQMockTransport) NewSenderFor(addr string) FLTQSender {
	return &FLTQPerParticipantSender{
		transport: m,
		fromAddr:  addr,
	}
}

func (p *FLTQPerParticipantSender) SendHeaderFLTQ(to string, h BundleHeader) error {
	return p.transport.SendHeaderFLTQFrom(p.fromAddr, to, h)
}
