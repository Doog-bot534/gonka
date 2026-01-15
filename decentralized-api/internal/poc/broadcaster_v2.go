package poc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"decentralized-api/chainphase"
	cosmos_client "decentralized-api/cosmosclient"
	"decentralized-api/internal"
	"decentralized-api/logging"
	"decentralized-api/pocstorage"
	"decentralized-api/utils"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/cmd/inferenced/cmd"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
)

// Re-defining DTOs here to avoid circular dependency with server/public
// In a larger refactor, these should move to a shared 'api/types' or 'dtos' package.
type poCResultV2NodeRequest struct {
	NodeID         string `json:"node_id"`
	Model          string `json:"model"`
	Amount         int64  `json:"amount"`
	Hash           string `json:"hash"`
	TimeSinceBlock int64  `json:"time_since_block"`
}

type poCResultV2Request struct {
	BlockHeight int64                    `json:"block_height"`
	Address     string                   `json:"address"`
	Nodes       []poCResultV2NodeRequest `json:"nodes"`
}

const (
	broadcastFlushInterval           = 100 * time.Millisecond
	broadcastHTTPTimeout             = 5 * time.Second
	broadcastConcurrencyLimit        = 256
	broadcastPortPressureFactor      = 2
	broadcastMaxRecipientsOnPressure = 256
	backoffOnFailureWithTWReuse      = 5 * time.Second
	backoffOnFailureWithoutTWReuse   = 120 * time.Second //TODO: It should be 30 * time.Second in production
	ephemeralPortUsageCacheTTL       = 60 * time.Second
)

type ResultBroadcaster struct {
	recorder     cosmos_client.CosmosMessageClient
	httpClient   *http.Client
	myAddress    string
	cdc          *codec.ProtoCodec
	phaseTracker *chainphase.ChainPhaseTracker

	epochGroupCache   *internal.EpochGroupDataCache
	participantsCache *internal.ParticipantsListCache

	mu               sync.Mutex
	pending          map[string]pocstorage.PoCBatchesGeneratedRecord
	failUntilByAddr  map[string]time.Time
	tcpTwReuse       bool
	backoffOnFailure time.Duration
	portUsageCache   cachedPortUsage
	flushArmed       bool
	accountSigner    *cmd.AccountSigner
	ticker           *time.Ticker
	done             chan struct{}
}

type cachedPortUsage struct {
	total     int
	used      int
	ok        bool
	expiresAt time.Time
}

func NewResultBroadcaster(recorder cosmos_client.CosmosMessageClient, myAddress string, phaseTracker *chainphase.ChainPhaseTracker) *ResultBroadcaster {
	// Create a codec for unmarshaling query results
	interfaceRegistry := codectypes.NewInterfaceRegistry()
	types.RegisterInterfaces(interfaceRegistry)
	cdc := codec.NewProtoCodec(interfaceRegistry)
	twReuse := tcpTwReuseEnabled()
	backoff := backoffOnFailureWithoutTWReuse
	if twReuse {
		backoff = backoffOnFailureWithTWReuse
	}

	return &ResultBroadcaster{
		recorder:          recorder,
		myAddress:         myAddress,
		cdc:               cdc,
		phaseTracker:      phaseTracker,
		epochGroupCache:   internal.NewEpochGroupDataCache(recorder),
		participantsCache: internal.NewParticipantsListCache(recorder),
		httpClient: &http.Client{
			Timeout: broadcastHTTPTimeout,
			Transport: &http.Transport{
				MaxIdleConns:        1000,
				MaxIdleConnsPerHost: 100,
				IdleConnTimeout:     90 * time.Second,
				DisableKeepAlives:   false,
			},
		},
		pending:          make(map[string]pocstorage.PoCBatchesGeneratedRecord),
		failUntilByAddr:  make(map[string]time.Time),
		tcpTwReuse:       twReuse,
		backoffOnFailure: backoff,
		done:             make(chan struct{}),
	}
}

func (b *ResultBroadcaster) Start(ctx context.Context) {
	b.ticker = time.NewTicker(broadcastFlushInterval)
	go b.runLoop(ctx)
}

func (b *ResultBroadcaster) runLoop(ctx context.Context) {
	defer b.ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-b.ticker.C:
			b.flush(ctx)
		case <-b.done:
			return
		}
	}
}

func (b *ResultBroadcaster) Stop() {
	close(b.done)
}

func (b *ResultBroadcaster) Broadcast(ctx context.Context, record pocstorage.PoCBatchesGeneratedRecord) {
	b.mu.Lock()
	defer b.mu.Unlock()
	// Overwrite previous record for this nodeID (aggregation)
	b.pending[record.NodeID] = record
}

func (b *ResultBroadcaster) flush(ctx context.Context) {
	b.mu.Lock()
	if !b.flushArmed {
		if len(b.pending) == 0 {
			b.mu.Unlock()
			return
		}
		b.flushArmed = true
		b.mu.Unlock()
		return
	}
	if len(b.pending) == 0 {
		b.flushArmed = false
		b.mu.Unlock()
		return
	}

	// Snapshot pending records
	records := make([]pocstorage.PoCBatchesGeneratedRecord, 0, len(b.pending))
	for _, rec := range b.pending {
		records = append(records, rec)
	}
	// Clear pending
	b.pending = make(map[string]pocstorage.PoCBatchesGeneratedRecord)
	b.flushArmed = false
	b.mu.Unlock()

	// 1. Prepare Payload (single aggregated request)
	// Assume all records belong to same address/blockHeight (they should, locally)
	// We use the first record for common fields.
	first := records[0]

	nodes := make([]poCResultV2NodeRequest, 0, len(records))
	for _, rec := range records {
		nodes = append(nodes, poCResultV2NodeRequest{
			NodeID:         rec.NodeID,
			Model:          rec.Model,
			Amount:         rec.Amount,
			Hash:           rec.Hash,
			TimeSinceBlock: rec.TimeSinceBlock,
		})
	}

	req := poCResultV2Request{
		BlockHeight: first.BlockHeight,
		Address:     first.Address,
		Nodes:       nodes,
	}

	payloadBytes, err := json.Marshal(req)
	if err != nil {
		logging.Error("Failed to marshal broadcast payload", types.PoC, "error", err)
		return
	}

	// 2. Sign Payload
	canonical, err := utils.CanonicalizeJSON(payloadBytes)
	if err != nil {
		logging.Error("Failed to canonicalize broadcast payload", types.PoC, "error", err)
		return
	}
	payloadHash := utils.GenerateSHA256Hash(canonical)
	components := calculations.SignatureComponents{
		Payload:         payloadHash,
		Timestamp:       0,
		TransferAddress: "",
		ExecutorAddress: "",
	}
	accountSigner, err := b.getAccountSigner()
	if err != nil {
		logging.Error("Failed to get signer for PoC broadcast", types.PoC, "error", err)
		return
	}
	signature, err := calculations.Sign(accountSigner, components, calculations.Developer)
	if err != nil {
		logging.Error("Failed to sign broadcast payload", types.PoC, "error", err)
		return
	}

	// 3. Get Active Participants using EpochGroupCache
	// Determine Epoch
	if b.phaseTracker == nil {
		logging.Error("Phase tracker is nil for PoC results broadcast", types.PoC)
		return
	}
	epochState := b.phaseTracker.GetCurrentEpochState()
	if epochState == nil || !epochState.IsSynced {
		logging.Warn("Phase tracker not ready for PoC results broadcast", types.PoC,
			"is_nil", epochState == nil,
			"is_synced", epochState != nil && epochState.IsSynced)
		return
	}
	// Use LatestEpoch for broadcasting recent results
	currentEpoch := epochState.LatestEpoch.EpochIndex

	// Get Active Set
	groupData, err := b.epochGroupCache.GetEpochGroupData(ctx, currentEpoch)
	if err != nil {
		logging.Error("Failed to get active participants for broadcast", types.PoC, "error", err)
		return
	}

	activeWeights := buildActiveParticipantWeights(groupData.ValidationWeights)
	activeWeights = b.limitActiveWeightsForEphemeralPorts(activeWeights)

	// Get All Participants (to resolve URLs)
	allParticipants, err := b.participantsCache.GetParticipants(ctx, currentEpoch)
	if err != nil {
		logging.Error("Failed to get participants list for broadcast", types.PoC, "error", err)
		return
	}

	// 4. Broadcast
	var wg sync.WaitGroup
	sem := make(chan struct{}, broadcastConcurrencyLimit) // Concurrency limit

	for _, p := range allParticipants {
		if p.Address == b.myAddress {
			continue
		}

		// Filter by Active Set
		if _, isActive := activeWeights[p.Address]; !isActive {
			continue
		}

		if p.InferenceUrl == "" {
			continue
		}

		if b.shouldSkipParticipant(p.Address) {
			continue
		}

		wg.Add(1)
		go func(targetParticipant types.Participant) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			baseUrl := targetParticipant.InferenceUrl
			if len(baseUrl) > 0 && baseUrl[len(baseUrl)-1] == '/' {
				baseUrl = baseUrl[:len(baseUrl)-1]
			}
			targetUrl := fmt.Sprintf("%s/v2/poc/results", baseUrl)

			b.sendResult(ctx, targetParticipant.Address, targetUrl, payloadBytes, signature)
		}(p)
	}
	wg.Wait()
}

func (b *ResultBroadcaster) sendResult(ctx context.Context, participantAddr string, url string, payload []byte, signature string) {
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(utils.XTASignatureHeader, signature)
	req.Header.Set("User-Agent", "Gonka/2.0.2")

	resp, err := b.httpClient.Do(req)
	if err != nil {
		b.markFailure(participantAddr)
		if isEphemeralPortExhaustion(err) {
			if total, used, ok := estimateEphemeralPortUsage(); ok {
				logging.Warn("Broadcast failed due to local port exhaustion", types.PoC,
					"url", url,
					"error", err,
					"ephemeral_total", total,
					"ephemeral_used", used,
					"ephemeral_left", total-used,
					"tcp_tw_reuse", b.tcpTwReuse,
					"backoff_seconds", int(b.backoffOnFailure.Seconds()))
			} else {
				logging.Warn("Broadcast failed due to local port exhaustion", types.PoC,
					"url", url,
					"error", err,
					"tcp_tw_reuse", b.tcpTwReuse,
					"backoff_seconds", int(b.backoffOnFailure.Seconds()))
			}
		} else {
			logging.Debug("Failed to broadcast result", types.PoC,
				"url", url,
				"error", err,
				"backoff_seconds", int(b.backoffOnFailure.Seconds()))
		}
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		logging.Debug("Broadcast result rejected", types.PoC, "url", url, "status", resp.Status, "body", string(respBody))
	}
	b.clearFailure(participantAddr)
}

func (b *ResultBroadcaster) getAccountSigner() (*cmd.AccountSigner, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.accountSigner != nil {
		return b.accountSigner, nil
	}
	signerAddressStr := b.recorder.GetSignerAddress()
	signerAddress, err := sdk.AccAddressFromBech32(signerAddressStr)
	if err != nil {
		return nil, err
	}
	b.accountSigner = &cmd.AccountSigner{
		Addr:    signerAddress,
		Keyring: b.recorder.GetKeyring(),
	}
	return b.accountSigner, nil
}

func buildActiveParticipantWeights(weights []*types.ValidationWeight) map[string]int64 {
	activeWeights := make(map[string]int64, len(weights))
	for _, w := range weights {
		activeWeights[w.MemberAddress] = w.Weight
	}
	return activeWeights
}

func (b *ResultBroadcaster) limitActiveWeightsForEphemeralPorts(activeWeights map[string]int64) map[string]int64 {
	total, used, ok := b.estimateEphemeralPortUsageCached()
	if !ok {
		return activeWeights
	}
	left := total - used
	if left > 0 && len(activeWeights) <= broadcastPortPressureFactor*left {
		return activeWeights
	}

	limit := broadcastMaxRecipientsOnPressure
	if len(activeWeights) < limit {
		limit = len(activeWeights)
	}
	entries := make([]types.ValidationWeight, 0, len(activeWeights))
	for addr, weight := range activeWeights {
		entries = append(entries, types.ValidationWeight{
			MemberAddress: addr,
			Weight:        weight,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Weight != entries[j].Weight {
			return entries[i].Weight > entries[j].Weight
		}
		return entries[i].MemberAddress < entries[j].MemberAddress
	})
	limitedWeights := make(map[string]int64, limit)
	for i := 0; i < limit; i++ {
		limitedWeights[entries[i].MemberAddress] = entries[i].Weight
	}
	logging.Warn("Limiting PoC result broadcast recipients due to low ephemeral ports", types.PoC,
		"active_participants", len(activeWeights),
		"limited_to", len(limitedWeights),
		"ephemeral_total", total,
		"ephemeral_used", used,
		"ephemeral_left", left)
	return limitedWeights
}

func (b *ResultBroadcaster) estimateEphemeralPortUsageCached() (int, int, bool) {
	b.mu.Lock()
	cache := b.portUsageCache
	if time.Now().Before(cache.expiresAt) {
		b.mu.Unlock()
		return cache.total, cache.used, cache.ok
	}
	b.mu.Unlock()

	total, used, ok := estimateEphemeralPortUsage()

	b.mu.Lock()
	b.portUsageCache = cachedPortUsage{
		total:     total,
		used:      used,
		ok:        ok,
		expiresAt: time.Now().Add(ephemeralPortUsageCacheTTL),
	}
	b.mu.Unlock()
	return total, used, ok
}

func (b *ResultBroadcaster) shouldSkipParticipant(addr string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	until, ok := b.failUntilByAddr[addr]
	if !ok {
		return false
	}
	return time.Now().Before(until)
}

func (b *ResultBroadcaster) markFailure(addr string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failUntilByAddr[addr] = time.Now().Add(b.backoffOnFailure)
}

func (b *ResultBroadcaster) clearFailure(addr string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.failUntilByAddr, addr)
}

func tcpTwReuseEnabled() bool {
	v, err := readSysctlInt("/proc/sys/net/ipv4/tcp_tw_reuse")
	if err != nil {
		return false
	}
	return v == 1
}

func readSysctlInt(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

func isEphemeralPortExhaustion(err error) bool {
	if err == nil {
		return false
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		err = opErr.Err
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "cannot assign requested address") ||
		strings.Contains(msg, "address not available") ||
		strings.Contains(msg, "address already in use")
}

func estimateEphemeralPortUsage() (total int, used int, ok bool) {
	minPort, maxPort, err := readEphemeralPortRange()
	if err != nil {
		return 0, 0, false
	}
	total = maxPort - minPort + 1
	if total <= 0 {
		return 0, 0, false
	}

	used = 0
	used += countPortsInUse("/proc/net/tcp", minPort, maxPort)
	used += countPortsInUse("/proc/net/tcp6", minPort, maxPort)
	return total, used, true
}

func readEphemeralPortRange() (int, int, error) {
	b, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	if err != nil {
		return 0, 0, err
	}
	parts := strings.Fields(string(b))
	if len(parts) < 2 {
		return 0, 0, fmt.Errorf("invalid ip_local_port_range: %q", string(b))
	}
	minPort, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, err
	}
	maxPort, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, err
	}
	return minPort, maxPort, nil
}

func countPortsInUse(path string, minPort int, maxPort int) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// Skip header
	if !scanner.Scan() {
		return 0
	}
	count := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		local := fields[1]
		parts := strings.Split(local, ":")
		if len(parts) != 2 {
			continue
		}
		portHex := parts[1]
		port, err := strconv.ParseInt(portHex, 16, 32)
		if err != nil {
			continue
		}
		p := int(port)
		if p >= minPort && p <= maxPort {
			count++
		}
	}
	return count
}
