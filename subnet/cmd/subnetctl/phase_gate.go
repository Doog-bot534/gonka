package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultChainPhasePollInterval = 5 * time.Second

	epochPhaseInference           = "Inference"
	epochPhasePoCGenerate         = "PoCGenerate"
	epochPhasePoCGenerateWindDown = "PoCGenerateWindDown"
	epochPhasePoCValidate         = "PoCValidate"
	epochPhasePoCValidateWindDown = "PoCValidateWindDown"

	confirmationPoCInactive   = "CONFIRMATION_POC_INACTIVE"
	confirmationPoCGeneration = "CONFIRMATION_POC_GENERATION"
	confirmationPoCValidation = "CONFIRMATION_POC_VALIDATION"
)

type ChainPhaseSnapshot struct {
	BlockHeight          int64     `json:"block_height,omitempty"`
	EpochIndex           uint64    `json:"epoch_index,omitempty"`
	EpochPhase           string    `json:"chain_phase,omitempty"`
	ConfirmationPoCPhase string    `json:"confirmation_poc_phase,omitempty"`
	RequestsBlocked      bool      `json:"requests_blocked"`
	BlockReason          string    `json:"block_reason,omitempty"`
	LastUpdatedAt        time.Time `json:"last_updated_at,omitempty"`
	LastError            string    `json:"last_error,omitempty"`
}

type ChainPhaseGate struct {
	endpoint                      string
	client                        *http.Client
	pollInterval                  time.Duration
	defaultMaxSpeculativeAttempts int

	mu       sync.RWMutex
	snapshot ChainPhaseSnapshot

	stopCh chan struct{}
	doneCh chan struct{}
}

type chainEpochInfoResponse struct {
	BlockHeight             jsonInt64                         `json:"block_height"`
	Phase                   string                            `json:"phase"`
	LatestEpoch             chainLatestEpoch                  `json:"latest_epoch"`
	IsConfirmationPoCActive bool                              `json:"is_confirmation_poc_active"`
	ActiveConfirmationPoC   *chainConfirmationPoCEventPayload `json:"active_confirmation_poc_event,omitempty"`
}

type chainLatestEpoch struct {
	Index               jsonUint64 `json:"index"`
	PocStartBlockHeight jsonInt64  `json:"poc_start_block_height"`
}

type chainConfirmationPoCEventPayload struct {
	Phase confirmationPoCPhaseValue `json:"phase"`
}

type jsonInt64 int64

func (n *jsonInt64) UnmarshalJSON(data []byte) error {
	parsed, err := parseFlexibleInt64(data)
	if err != nil {
		return err
	}
	*n = jsonInt64(parsed)
	return nil
}

type jsonUint64 uint64

func (n *jsonUint64) UnmarshalJSON(data []byte) error {
	parsed, err := parseFlexibleUint64(data)
	if err != nil {
		return err
	}
	*n = jsonUint64(parsed)
	return nil
}

type confirmationPoCPhaseValue string

func (p *confirmationPoCPhaseValue) UnmarshalJSON(data []byte) error {
	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		*p = confirmationPoCPhaseValue(asString)
		return nil
	}

	var asInt int
	if err := json.Unmarshal(data, &asInt); err == nil {
		switch asInt {
		case 0:
			*p = confirmationPoCPhaseValue(confirmationPoCInactive)
		case 2:
			*p = confirmationPoCPhaseValue(confirmationPoCGeneration)
		case 3:
			*p = confirmationPoCPhaseValue(confirmationPoCValidation)
		default:
			*p = confirmationPoCPhaseValue(strconv.Itoa(asInt))
		}
		return nil
	}

	return fmt.Errorf("unsupported confirmation PoC phase %s", string(data))
}

type RequestAdmissionError struct {
	Reason  string
	Message string
}

func (e *RequestAdmissionError) Error() string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.Message) != "" {
		return e.Message
	}
	if strings.TrimSpace(e.Reason) != "" {
		return e.Reason
	}
	return "request admission blocked"
}

func NewChainPhaseGate(baseURL string, pollInterval time.Duration) *ChainPhaseGate {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return nil
	}
	if pollInterval <= 0 {
		pollInterval = defaultChainPhasePollInterval
	}
	return &ChainPhaseGate{
		endpoint:                      strings.TrimRight(baseURL, "/") + "/v1/epochs/latest",
		client:                        &http.Client{Timeout: 5 * time.Second},
		pollInterval:                  pollInterval,
		defaultMaxSpeculativeAttempts: CurrentMaxSpeculativeAttempts(),
		stopCh:                        make(chan struct{}),
		doneCh:                        make(chan struct{}),
	}
}

func (g *ChainPhaseGate) Start() {
	if g == nil {
		return
	}
	go g.run()
}

func (g *ChainPhaseGate) Stop() {
	if g == nil {
		return
	}
	select {
	case <-g.doneCh:
		return
	default:
	}
	close(g.stopCh)
	<-g.doneCh
	g.restoreDefaultSpeculativeAttempts()
}

func (g *ChainPhaseGate) Snapshot() ChainPhaseSnapshot {
	if g == nil {
		return ChainPhaseSnapshot{}
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.snapshot
}

func (g *ChainPhaseGate) AdmissionError() error {
	if g == nil {
		return nil
	}
	snapshot := g.Snapshot()
	if !snapshot.RequestsBlocked {
		return nil
	}
	return &RequestAdmissionError{
		Reason:  snapshot.BlockReason,
		Message: fmt.Sprintf("subnet temporarily unavailable during %s", humanizePhaseBlockReason(snapshot)),
	}
}

func (g *ChainPhaseGate) run() {
	defer close(g.doneCh)
	g.refresh()

	ticker := time.NewTicker(g.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			g.refresh()
		case <-g.stopCh:
			return
		}
	}
}

func (g *ChainPhaseGate) refresh() {
	if g == nil {
		return
	}
	resp, err := g.fetchEpochInfo()
	if err != nil {
		g.recordError(err)
		log.Printf("chain phase poll failed: %v", err)
		return
	}
	snapshot := deriveChainPhaseSnapshot(resp)
	g.applySpeculativeAttemptPolicy(snapshot)
	g.storeSnapshot(snapshot)
}

func (g *ChainPhaseGate) fetchEpochInfo() (*chainEpochInfoResponse, error) {
	resp, err := g.client.Get(g.endpoint)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("epoch info status %d", resp.StatusCode)
	}

	var payload chainEpochInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

func (g *ChainPhaseGate) storeSnapshot(snapshot ChainPhaseSnapshot) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.snapshot = snapshot
}

func (g *ChainPhaseGate) applySpeculativeAttemptPolicy(snapshot ChainPhaseSnapshot) {
	if g == nil {
		return
	}
	if snapshot.RequestsBlocked {
		SetMaxSpeculativeAttempts(1)
		return
	}
	g.restoreDefaultSpeculativeAttempts()
}

func (g *ChainPhaseGate) restoreDefaultSpeculativeAttempts() {
	if g == nil {
		return
	}
	SetMaxSpeculativeAttempts(g.defaultMaxSpeculativeAttempts)
}

func (g *ChainPhaseGate) recordError(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.snapshot.LastError = err.Error()
}

func deriveChainPhaseSnapshot(resp *chainEpochInfoResponse) ChainPhaseSnapshot {
	if resp == nil {
		return ChainPhaseSnapshot{}
	}

	snapshot := ChainPhaseSnapshot{
		BlockHeight:   int64(resp.BlockHeight),
		EpochIndex:    uint64(resp.LatestEpoch.Index),
		EpochPhase:    deriveEpochPhase(resp),
		LastUpdatedAt: time.Now().UTC(),
	}

	if resp.ActiveConfirmationPoC != nil {
		snapshot.ConfirmationPoCPhase = string(resp.ActiveConfirmationPoC.Phase)
	}
	snapshot.RequestsBlocked, snapshot.BlockReason = shouldBlockRequests(snapshot.EpochPhase, snapshot.ConfirmationPoCPhase)
	return snapshot
}

func deriveEpochPhase(resp *chainEpochInfoResponse) string {
	if resp == nil {
		return ""
	}
	return strings.TrimSpace(resp.Phase)
}

func shouldBlockRequests(epochPhase, confirmationPhase string) (bool, string) {
	switch epochPhase {
	case epochPhasePoCGenerate, epochPhasePoCGenerateWindDown, epochPhasePoCValidate, epochPhasePoCValidateWindDown:
		return true, "poc"
	}
	if confirmationPhase == confirmationPoCGeneration || confirmationPhase == confirmationPoCValidation {
		return true, "confirmation_poc"
	}
	return false, ""
}

func humanizePhaseBlockReason(snapshot ChainPhaseSnapshot) string {
	if snapshot.BlockReason == "poc" {
		switch snapshot.EpochPhase {
		case epochPhasePoCGenerate:
			return "PoC generation"
		case epochPhasePoCGenerateWindDown:
			return "PoC generation wind down"
		case epochPhasePoCValidate:
			return "PoC validation"
		case epochPhasePoCValidateWindDown:
			return "PoC validation wind down"
		}
		return "PoC"
	}
	if snapshot.BlockReason == "confirmation_poc" {
		switch snapshot.ConfirmationPoCPhase {
		case confirmationPoCGeneration:
			return "confirmation PoC generation"
		case confirmationPoCValidation:
			return "confirmation PoC validation"
		}
		return "confirmation PoC"
	}
	if strings.TrimSpace(snapshot.BlockReason) != "" {
		return strings.ReplaceAll(snapshot.BlockReason, "_", " ")
	}
	if strings.TrimSpace(snapshot.EpochPhase) != "" {
		return snapshot.EpochPhase
	}
	return "chain admission controls"
}

func parseFlexibleInt64(data []byte) (int64, error) {
	var asInt int64
	if err := json.Unmarshal(data, &asInt); err == nil {
		return asInt, nil
	}

	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		var parsed int64
		if _, err := fmt.Sscan(asString, &parsed); err != nil {
			return 0, err
		}
		return parsed, nil
	}

	return 0, fmt.Errorf("unsupported int64 value %s", string(data))
}

func parseFlexibleUint64(data []byte) (uint64, error) {
	var asUint uint64
	if err := json.Unmarshal(data, &asUint); err == nil {
		return asUint, nil
	}

	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		var parsed uint64
		if _, err := fmt.Sscan(asString, &parsed); err != nil {
			return 0, err
		}
		return parsed, nil
	}

	return 0, fmt.Errorf("unsupported uint64 value %s", string(data))
}
