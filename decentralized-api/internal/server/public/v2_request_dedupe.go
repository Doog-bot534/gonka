package public

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"
)

type v2RequestDeduper struct {
	mutex   sync.Mutex
	entries map[string]*v2RequestDedupeEntry
}

type v2RequestDedupeEntry struct {
	payloadHash string
	inFlight    bool
	done        chan struct{}
	err         error
	response    *cachedV2HTTPResponse
	stream      *cachedV2StreamResponse
}

type cachedV2HTTPResponse struct {
	statusCode int
	header     http.Header
	body       []byte
}

func newV2RequestDeduper() *v2RequestDeduper {
	return &v2RequestDeduper{
		entries: make(map[string]*v2RequestDedupeEntry),
	}
}

func (s *Server) getV2RequestDeduper() *v2RequestDeduper {
	if s.v2RequestDeduper == nil {
		s.v2RequestDeduper = newV2RequestDeduper()
	}
	return s.v2RequestDeduper
}

func (d *v2RequestDeduper) execute(
	requestID string,
	payloadHash string,
	executor func() (*http.Response, error),
) (*http.Response, error) {
	if strings.TrimSpace(requestID) == "" {
		return executor()
	}

	d.mutex.Lock()
	entry := d.entries[requestID]
	if entry == nil {
		entry = &v2RequestDedupeEntry{
			payloadHash: payloadHash,
			inFlight:    true,
			done:        make(chan struct{}),
		}
		d.entries[requestID] = entry
		d.mutex.Unlock()
		return d.executeLeader(entry, payloadHash, executor)
	}
	if entry.payloadHash != payloadHash {
		d.mutex.Unlock()
		return nil, ErrV2RequestIDConflict
	}
	if entry.stream != nil {
		response := entry.stream.toHTTPResponse()
		d.mutex.Unlock()
		return response, nil
	}
	if entry.inFlight {
		waitChannel := entry.done
		d.mutex.Unlock()
		<-waitChannel

		d.mutex.Lock()
		updatedEntry := d.entries[requestID]
		d.mutex.Unlock()
		if updatedEntry == nil {
			return nil, ErrV2RequestInProgress
		}
		if updatedEntry.err != nil {
			return nil, updatedEntry.err
		}
		if updatedEntry.stream != nil {
			return updatedEntry.stream.toHTTPResponse(), nil
		}
		if updatedEntry.response == nil {
			return nil, ErrV2RequestAlreadyProcessed
		}
		return updatedEntry.response.toHTTPResponse(), nil
	}
	if entry.response != nil {
		response := entry.response.toHTTPResponse()
		d.mutex.Unlock()
		return response, nil
	}
	if entry.err != nil {
		err := entry.err
		d.mutex.Unlock()
		return nil, err
	}
	d.mutex.Unlock()
	return nil, ErrV2RequestAlreadyProcessed
}

func (d *v2RequestDeduper) executeLeader(
	entry *v2RequestDedupeEntry,
	payloadHash string,
	executor func() (*http.Response, error),
) (*http.Response, error) {
	resp, execErr := executor()

	d.mutex.Lock()
	defer d.mutex.Unlock()

	entry.inFlight = false
	entry.payloadHash = payloadHash
	if execErr != nil {
		entry.err = execErr
		close(entry.done)
		return nil, execErr
	}

	if isTextEventStreamResponse(resp) {
		stream := newCachedV2StreamResponse(resp.StatusCode, cloneHeader(resp.Header))
		entry.stream = stream
		close(entry.done)
		go pumpV2StreamResponse(resp.Body, stream)
		return stream.toHTTPResponse(), nil
	}

	bodyBytes, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		entry.err = readErr
		close(entry.done)
		return nil, readErr
	}

	cached := &cachedV2HTTPResponse{
		statusCode: resp.StatusCode,
		header:     cloneHeader(resp.Header),
		body:       append([]byte(nil), bodyBytes...),
	}
	entry.response = cached
	resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	resp.Header = cloneHeader(cached.header)

	close(entry.done)
	return resp, nil
}

func (r *cachedV2HTTPResponse) toHTTPResponse() *http.Response {
	return &http.Response{
		StatusCode: r.statusCode,
		Header:     cloneHeader(r.header),
		Body:       io.NopCloser(bytes.NewReader(r.body)),
	}
}

func isTextEventStreamResponse(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	return strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream")
}

type cachedV2StreamResponse struct {
	statusCode int
	header     http.Header

	mutex       sync.Mutex
	history     []byte
	subscribers map[chan []byte]struct{}
	closed      bool
}

func newCachedV2StreamResponse(statusCode int, header http.Header) *cachedV2StreamResponse {
	return &cachedV2StreamResponse{
		statusCode:  statusCode,
		header:      header,
		subscribers: make(map[chan []byte]struct{}),
	}
}

func (r *cachedV2StreamResponse) appendChunk(chunk []byte) {
	if len(chunk) == 0 {
		return
	}

	r.mutex.Lock()
	defer r.mutex.Unlock()

	if r.closed {
		return
	}

	r.history = append(r.history, chunk...)
	for subscriber := range r.subscribers {
		subscriber <- append([]byte(nil), chunk...)
	}
}

func (r *cachedV2StreamResponse) close() {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if r.closed {
		return
	}
	r.closed = true
	for subscriber := range r.subscribers {
		close(subscriber)
	}
	r.subscribers = make(map[chan []byte]struct{})
}

func (r *cachedV2StreamResponse) newReader() io.ReadCloser {
	pr, pw := io.Pipe()

	r.mutex.Lock()
	historySnapshot := append([]byte(nil), r.history...)
	alreadyClosed := r.closed
	var subscriber chan []byte
	if !alreadyClosed {
		subscriber = make(chan []byte, 256)
		r.subscribers[subscriber] = struct{}{}
	}
	r.mutex.Unlock()

	go func() {
		defer pw.Close()
		if len(historySnapshot) > 0 {
			if _, err := pw.Write(historySnapshot); err != nil {
				if subscriber != nil {
					r.removeSubscriber(subscriber)
				}
				return
			}
		}
		if subscriber == nil {
			return
		}
		for chunk := range subscriber {
			if len(chunk) == 0 {
				continue
			}
			if _, err := pw.Write(chunk); err != nil {
				r.removeSubscriber(subscriber)
				return
			}
		}
	}()

	return pr
}

func (r *cachedV2StreamResponse) removeSubscriber(subscriber chan []byte) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if _, exists := r.subscribers[subscriber]; exists {
		delete(r.subscribers, subscriber)
		close(subscriber)
	}
}

func (r *cachedV2StreamResponse) toHTTPResponse() *http.Response {
	return &http.Response{
		StatusCode: r.statusCode,
		Header:     cloneHeader(r.header),
		Body:       r.newReader(),
	}
}

type streamFanoutReadCloser struct {
	source io.ReadCloser
	stream *cachedV2StreamResponse
}

func newStreamFanoutReadCloser(source io.ReadCloser, stream *cachedV2StreamResponse) io.ReadCloser {
	return &streamFanoutReadCloser{
		source: source,
		stream: stream,
	}
}

func (r *streamFanoutReadCloser) Read(p []byte) (int, error) {
	n, err := r.source.Read(p)
	if n > 0 {
		r.stream.appendChunk(p[:n])
	}
	if err == io.EOF {
		r.stream.close()
	}
	if err != nil && err != io.EOF {
		r.stream.close()
	}
	return n, err
}

func (r *streamFanoutReadCloser) Close() error {
	r.stream.close()
	return r.source.Close()
}

func cloneHeader(source http.Header) http.Header {
	if source == nil {
		return nil
	}
	cloned := make(http.Header, len(source))
	for key, values := range source {
		copiedValues := make([]string, len(values))
		copy(copiedValues, values)
		cloned[key] = copiedValues
	}
	return cloned
}

func pumpV2StreamResponse(source io.ReadCloser, stream *cachedV2StreamResponse) {
	defer source.Close()
	defer stream.close()

	buffer := make([]byte, 32*1024)
	for {
		n, err := source.Read(buffer)
		if n > 0 {
			stream.appendChunk(buffer[:n])
		}
		if err != nil {
			return
		}
	}
}
