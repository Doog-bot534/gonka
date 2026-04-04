package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"subnet/state"
)

type SettlementJSON struct {
	EscrowID   string              `json:"escrow_id"`
	StateRoot  string              `json:"state_root"`
	Nonce      uint64              `json:"nonce"`
	RestHash   string              `json:"rest_hash"`
	HostStats  []HostStatsJSON     `json:"host_stats"`
	Signatures []SlotSignatureJSON `json:"signatures"`
}

type HostStatsJSON struct {
	SlotID               uint32 `json:"slot_id"`
	Missed               uint32 `json:"missed"`
	Invalid              uint32 `json:"invalid"`
	Cost                 uint64 `json:"cost"`
	RequiredValidations  uint32 `json:"required_validations"`
	CompletedValidations uint32 `json:"completed_validations"`
}

type SlotSignatureJSON struct {
	SlotID    uint32 `json:"slot_id"`
	Signature string `json:"signature"`
}

func main() {
	fs := flag.NewFlagSet("subnetctl", flag.ExitOnError)
	escrowID := fs.String("escrow-id", "", "escrow ID (required, or SUBNET_ESCROW_ID env)")
	chainREST := fs.String("chain-rest", "http://localhost:1317", "chain REST API URL")
	model := fs.String("model", "Qwen/Qwen2.5-7B-Instruct", "default model name")
	port := fs.String("port", "8080", "listen port")
	privateKey := fs.String("private-key", "", "private key hex (alternative to SUBNET_PRIVATE_KEY env)")
	storagePath := fs.String("storage-path", "", "SQLite path for crash recovery")
	storageDir := fs.String("storage-dir", "", "base directory for multi-subnet SQLite files")

	if err := fs.Parse(os.Args[1:]); err != nil {
		log.Fatal(err)
	}

	keyHex := *privateKey
	if keyHex == "" {
		keyHex = os.Getenv("SUBNET_PRIVATE_KEY")
	}
	if keyHex == "" {
		log.Fatal("--private-key flag or SUBNET_PRIVATE_KEY env var required")
	}

	eid := *escrowID
	if eid == "" {
		eid = os.Getenv("SUBNET_ESCROW_ID")
	}
	if eid == "" {
		log.Fatal("--escrow-id flag or SUBNET_ESCROW_ID env var required")
	}

	crest := *chainREST
	if v := os.Getenv("SUBNET_CHAIN_REST"); v != "" && *chainREST == "http://localhost:1317" {
		crest = v
	}

	mdl := *model
	if v := os.Getenv("SUBNET_MODEL"); v != "" && *model == "Qwen/Qwen2.5-7B-Instruct" {
		mdl = v
	}

	p := *port
	if v := os.Getenv("SUBNET_PORT"); v != "" && *port == "8080" {
		p = v
	}

	sp := *storagePath
	if sp == "" {
		sp = os.Getenv("SUBNET_STORAGE_PATH")
	}
	if sp == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "/tmp"
		}
		sp = filepath.Join(home, ".cache", "gonka", fmt.Sprintf("subnet-%s.db", eid))
	}

	baseStorageDir := *storageDir
	if baseStorageDir == "" {
		baseStorageDir = os.Getenv("SUBNET_STORAGE_DIR")
	}
	if baseStorageDir == "" {
		baseStorageDir = filepath.Dir(sp)
	}
	if err := os.MkdirAll(baseStorageDir, 0o755); err != nil {
		log.Fatalf("create storage dir: %v", err)
	}

	runtimeCfgs, err := resolveRuntimeConfigs(eid, keyHex, mdl, sp)
	if err != nil {
		log.Fatal(err)
	}
	runtimeCfgs, err = finalizeRuntimeConfigs(runtimeCfgs, mdl, baseStorageDir)
	if err != nil {
		log.Fatal(err)
	}
	runtimes, err := buildRuntimes(runtimeCfgs, crest, mdl)
	if err != nil {
		log.Fatalf("create runtimes: %v", err)
	}

	DefaultRequestMaxTokens = uint64(readInt64Env("GATEWAY_DEFAULT_MAX_TOKENS", int64(DefaultRequestMaxTokens)))

	limiter := NewGatewayLimiter(
		readInt64Env("GATEWAY_MAX_CONCURRENT_REQUESTS", 0),
		readInt64Env("GATEWAY_MAX_INPUT_TOKENS_IN_FLIGHT", 0),
	)
	gateway := NewGateway(runtimes, limiter, mdl)
	defer gateway.Close()

	var handler http.Handler = gateway.Handler()
	apiKeys := parseAPIKeys(os.Getenv("SUBNET_API_KEYS"))
	if len(apiKeys) > 0 {
		log.Printf("API key auth enabled (%d key(s))", len(apiKeys))
		handler = bearerAuthMiddleware(apiKeys, handler)
	}

	addr := ":" + p
	log.Printf("subnetctl gateway listening on %s (subnets=%d default_max_tokens=%d)", addr, len(runtimes), DefaultRequestMaxTokens)
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("server: %v", err)
	}
}

func parseAPIKeys(raw string) map[string]struct{} {
	keys := make(map[string]struct{})
	for _, k := range strings.Split(raw, ",") {
		k = strings.TrimSpace(k)
		if k != "" {
			keys[k] = struct{}{}
		}
	}
	return keys
}

func isAuthExemptPath(path string) bool {
	return path == "/v1/status" || strings.HasSuffix(path, "/v1/status")
}

func bearerAuthMiddleware(validKeys map[string]struct{}, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAuthExemptPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"message":"Missing or invalid Authorization header. Expected: Bearer <api-key>","type":"invalid_request_error","code":"invalid_api_key"}}`)
			return
		}

		key := strings.TrimPrefix(auth, "Bearer ")
		if _, ok := validKeys[key]; !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"message":"Invalid API key provided.","type":"invalid_request_error","code":"invalid_api_key"}}`)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func readInt64Env(name string, fallback int64) int64 {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	var v int64
	if _, err := fmt.Sscan(raw, &v); err != nil {
		log.Printf("invalid %s=%q, using %d", name, raw, fallback)
		return fallback
	}
	return v
}

func marshalSettlement(p *state.SettlementPayload) ([]byte, error) {
	hsHash, err := state.ComputeHostStatsHash(p.HostStats)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	h.Write(hsHash)
	h.Write(p.RestHash)
	h.Write([]byte{0x02})
	root := h.Sum(nil)

	stats := make([]HostStatsJSON, 0, len(p.HostStats))
	for slot, hs := range p.HostStats {
		stats = append(stats, HostStatsJSON{
			SlotID: slot, Missed: hs.Missed, Invalid: hs.Invalid,
			Cost: hs.Cost, RequiredValidations: hs.RequiredValidations,
			CompletedValidations: hs.CompletedValidations,
		})
	}

	sigs := make([]SlotSignatureJSON, 0, len(p.Signatures))
	for slot, sig := range p.Signatures {
		sigs = append(sigs, SlotSignatureJSON{SlotID: slot, Signature: base64.StdEncoding.EncodeToString(sig)})
	}

	return json.MarshalIndent(SettlementJSON{
		EscrowID: p.EscrowID, StateRoot: base64.StdEncoding.EncodeToString(root),
		Nonce: p.Nonce, RestHash: base64.StdEncoding.EncodeToString(p.RestHash),
		HostStats: stats, Signatures: sigs,
	}, "", "  ")
}
