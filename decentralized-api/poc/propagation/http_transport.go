package propagation

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"decentralized-api/logging"
	propagationpb "decentralized-api/poc/propagation/proto"

	"github.com/productscience/inference/x/inference/types"
	"golang.org/x/net/http2"
	"google.golang.org/protobuf/proto"
)

type HTTPTransport struct {
	client           *http.Client
	participantAddrs map[string]string
	mu               sync.RWMutex
	receivers        map[string]ReceiverHandler
	localAddr        string

	outboxMu      sync.Mutex
	outboxes      map[string][]BundleHeader
	flushInterval time.Duration
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
}

func NewHTTPTransport(localAddr string, timeout time.Duration) *HTTPTransport {
	tr := &http.Transport{
		MaxIdleConnsPerHost:   100,
		MaxConnsPerHost:       100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	_ = http2.ConfigureTransport(tr)

	client := &http.Client{
		Timeout:   timeout,
		Transport: tr,
	}

	ctx, cancel := context.WithCancel(context.Background())

	t := &HTTPTransport{
		client:           client,
		participantAddrs: make(map[string]string),
		receivers:        make(map[string]ReceiverHandler),
		localAddr:        localAddr,
		outboxes:         make(map[string][]BundleHeader),
		flushInterval:    30 * time.Millisecond,
		ctx:              ctx,
		cancel:           cancel,
	}

	t.wg.Add(1)
	go t.flushLoop()

	return t
}

func (t *HTTPTransport) SetParticipantURLs(urls map[string]string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.participantAddrs = urls
}

func (t *HTTPTransport) RegisterReceiver(addr string, handler ReceiverHandler) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.receivers[addr] = handler
}

func (t *HTTPTransport) SendHeaderFLTQ(to string, h BundleHeader) error {
	if to == t.localAddr {
		return t.handleLocalHeader(h, t.localAddr)
	}

	t.outboxMu.Lock()
	t.outboxes[to] = append(t.outboxes[to], h)
	t.outboxMu.Unlock()

	return nil
}

func (t *HTTPTransport) flushLoop() {
	defer t.wg.Done()
	ticker := time.NewTicker(t.flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-t.ctx.Done():
			t.flush()
			return
		case <-ticker.C:
			t.flush()
		}
	}
}

func (t *HTTPTransport) flush() {
	t.outboxMu.Lock()
	if len(t.outboxes) == 0 {
		t.outboxMu.Unlock()
		return
	}
	snapshot := t.outboxes
	t.outboxes = make(map[string][]BundleHeader)
	t.outboxMu.Unlock()

	for to, headers := range snapshot {
		t.mu.RLock()
		url := t.participantAddrs[to]
		t.mu.RUnlock()

		if url == "" {
			logging.Warn("HTTPTransport: no URL for participant, dropping batch", types.PoC,
				"to", to, "count", len(headers))
			continue
		}

		if err := t.sendBatch(url, headers); err != nil {
			logging.Warn("HTTPTransport: batch send failed", types.PoC,
				"to", to, "count", len(headers), "error", err)
		}
	}
}

func (t *HTTPTransport) sendBatch(url string, headers []BundleHeader) error {
	var buf bytes.Buffer

	count := uint32(len(headers))
	var countBuf [4]byte
	binary.BigEndian.PutUint32(countBuf[:], count)
	buf.Write(countBuf[:])

	for _, h := range headers {
		pb := HeaderToProto(h)
		data, err := proto.Marshal(pb)
		if err != nil {
			continue
		}
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(data)))
		buf.Write(lenBuf[:])
		buf.Write(data)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", url+"/v1/propagation/headers", &buf)
	if err != nil {
		return fmt.Errorf("create batch request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("http batch request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http batch status %d", resp.StatusCode)
	}

	return nil
}

func (t *HTTPTransport) handleLocalHeader(h BundleHeader, from string) error {
	t.mu.RLock()
	handler := t.receivers[t.localAddr]
	t.mu.RUnlock()

	if handler == nil {
		return fmt.Errorf("no local receiver registered")
	}

	return handler.OnHeader(h, from)
}

func (t *HTTPTransport) HandleHeaderHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		logging.Warn("HTTPTransport: failed to read header", types.PoC, "error", err)
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	var pbHeader propagationpb.PropagationHeader
	if err := proto.Unmarshal(body, &pbHeader); err != nil {
		logging.Warn("HTTPTransport: failed to decode header", types.PoC, "error", err)
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	header, err := ProtoToHeader(&pbHeader)
	if err != nil {
		logging.Warn("HTTPTransport: invalid header", types.PoC, "error", err)
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	t.mu.RLock()
	handler := t.receivers[t.localAddr]
	t.mu.RUnlock()

	if handler == nil {
		http.Error(w, "No receiver registered", http.StatusServiceUnavailable)
		return
	}

	if err := handler.OnHeader(header, header.Participant); err != nil {
		logging.Warn("HTTPTransport: header handler failed", types.PoC, "error", err)
		http.Error(w, "Handler error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (t *HTTPTransport) HandleHeaderBatchHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		logging.Warn("HTTPTransport: failed to read batch body", types.PoC, "error", err)
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	t.mu.RLock()
	handler := t.receivers[t.localAddr]
	t.mu.RUnlock()

	if handler == nil {
		http.Error(w, "No receiver registered", http.StatusServiceUnavailable)
		return
	}

	if len(body) < 4 {
		http.Error(w, "Invalid batch", http.StatusBadRequest)
		return
	}

	count := binary.BigEndian.Uint32(body[:4])
	pos := 4

	for i := uint32(0); i < count; i++ {
		if pos+4 > len(body) {
			break
		}
		msgLen := int(binary.BigEndian.Uint32(body[pos : pos+4]))
		pos += 4

		if pos+msgLen > len(body) {
			break
		}

		var pbHeader propagationpb.PropagationHeader
		if err := proto.Unmarshal(body[pos:pos+msgLen], &pbHeader); err != nil {
			pos += msgLen
			continue
		}
		pos += msgLen

		header, err := ProtoToHeader(&pbHeader)
		if err != nil {
			continue
		}

		if err := handler.OnHeader(header, header.Participant); err != nil {
			logging.Warn("HTTPTransport: batch header handler failed", types.PoC, "error", err)
		}
	}

	w.WriteHeader(http.StatusOK)
}

func (t *HTTPTransport) Close() {
	t.cancel()
	t.wg.Wait()
}
