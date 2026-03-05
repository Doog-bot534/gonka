package public

import (
	"bufio"
	"bytes"
	"decentralized-api/logging"
	"decentralized-api/utils"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/productscience/inference/x/inference/types"
)

func (s *Server) proxyV2ResponseWithExecutorProof(
	resp *http.Response,
	writer http.ResponseWriter,
	developerRequestBlockSignature string,
	shouldGenerateExecutorProof bool,
) {
	for key, values := range resp.Header {
		for _, value := range values {
			writer.Header().Add(key, value)
		}
	}

	contentType := resp.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "text/event-stream") {
		s.proxyV2TextStreamResponseWithExecutorProof(resp, writer, developerRequestBlockSignature, shouldGenerateExecutorProof)
		return
	}
	s.proxyV2JSONResponseWithExecutorProof(resp, writer, developerRequestBlockSignature, shouldGenerateExecutorProof)
}

func (s *Server) proxyV2JSONResponseWithExecutorProof(
	resp *http.Response,
	writer http.ResponseWriter,
	developerRequestBlockSignature string,
	shouldGenerateExecutorProof bool,
) {
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		logging.Error("Failed to read v2 inference response body", types.Inferences, "error", err)
		http.Error(writer, "Failed to read v2 inference response body", http.StatusInternalServerError)
		return
	}

	responsePayloadHash := utils.GenerateSHA256HashBytes(bodyBytes)
	upstreamProofPresent := strings.TrimSpace(writer.Header().Get(utils.XV2ExecutorSignatureHeader)) != "" &&
		strings.TrimSpace(writer.Header().Get(utils.XV2ExecutorSignerPubKeyHeader)) != ""
	if !upstreamProofPresent {
		if shouldGenerateExecutorProof {
			proof, proofErr := s.buildV2ExecutorProof(developerRequestBlockSignature, responsePayloadHash)
			if proofErr != nil {
				logging.Warn("Unable to create v2 executor proof for non-streaming response", types.Inferences, "error", proofErr)
			} else {
				setV2ExecutorProofHeaders(writer.Header(), proof)
			}
		} else {
			logging.Warn("Upstream non-streaming response missing v2 executor proof headers", types.Inferences)
		}
	}

	writer.WriteHeader(resp.StatusCode)
	_, _ = writer.Write(bodyBytes)
}

func (s *Server) proxyV2TextStreamResponseWithExecutorProof(
	resp *http.Response,
	writer http.ResponseWriter,
	developerRequestBlockSignature string,
	shouldGenerateExecutorProof bool,
) {
	writer.WriteHeader(resp.StatusCode)
	scanner := bufio.NewScanner(resp.Body)
	var streamedResponse bytes.Buffer
	currentEvent := "message"
	sawUpstreamExecutorProof := false

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "event:") {
			currentEvent = strings.TrimSpace(strings.TrimPrefix(trimmed, "event:"))
		}
		isExecutorProofEvent := currentEvent == v2ExecutorProofSSEEvent
		if isExecutorProofEvent {
			sawUpstreamExecutorProof = true
		}
		if !isExecutorProofEvent {
			streamedResponse.WriteString(line)
			streamedResponse.WriteByte('\n')
		}

		if line == "data: [DONE]" {
			responsePayloadHash := utils.GenerateSHA256HashBytes(streamedResponse.Bytes())
			if !sawUpstreamExecutorProof {
				if shouldGenerateExecutorProof {
					proof, proofErr := s.buildV2ExecutorProof(developerRequestBlockSignature, responsePayloadHash)
					if proofErr != nil {
						logging.Warn("Unable to create v2 executor proof for streaming response", types.Inferences, "error", proofErr)
					} else {
						writeV2ExecutorProofSSEEvent(writer, proof)
					}
				} else {
					logging.Warn("Upstream streaming response missing v2 executor proof event", types.Inferences)
				}
			}
		}

		if _, err := fmt.Fprintln(writer, line); err != nil {
			if opErr, ok := err.(*net.OpError); ok {
				logging.Warn("V2 stream cancelled during proxy", types.Inferences, "error", opErr)
				resp.Body.Close()
				return
			}
			logging.Error("Error while proxying v2 streaming response", types.Inferences, "error", err)
			http.Error(writer, err.Error(), http.StatusInternalServerError)
			return
		}
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
		if trimmed == "" {
			currentEvent = "message"
		}
	}

	if err := scanner.Err(); err != nil {
		logging.Error("Error after proxying v2 streaming response", types.Inferences, "error", err)
	}
}

