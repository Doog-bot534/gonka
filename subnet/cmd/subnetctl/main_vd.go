//go:build versiond

package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"subnet/bridge"
	"subnet/user"
)

func main() {
	port := flag.Int("port", 8080, "listen port")
	dataDir := flag.String("data-dir", "", "per-version data directory (versiond)")
	flag.Parse()

	prefix := os.Getenv("SUBNET_LOG_PREFIX")

	loadEnvFile("/opt/versiond/subnet.env")

	keyHex := os.Getenv("SUBNET_PRIVATE_KEY")
	if keyHex == "" {
		log.Fatalf("[%s] SUBNET_PRIVATE_KEY required", prefix)
	}

	eid := os.Getenv("SUBNET_ESCROW_ID")
	if eid == "" {
		log.Fatalf("[%s] SUBNET_ESCROW_ID required", prefix)
	}

	chainRest := os.Getenv("SUBNET_CHAIN_REST")
	if chainRest == "" {
		chainRest = "http://localhost:1317"
	}

	mdl := os.Getenv("SUBNET_MODEL")
	if mdl == "" {
		mdl = "Qwen/Qwen2.5-7B-Instruct"
	}

	sp := os.Getenv("SUBNET_STORAGE_PATH")
	if sp == "" && *dataDir != "" {
		sp = filepath.Join(*dataDir, fmt.Sprintf("subnet-%s.db", eid))
	}
	if sp == "" {
		sp = fmt.Sprintf("/tmp/subnet-%s.db", eid)
	}

	if err := os.MkdirAll(filepath.Dir(sp), 0755); err != nil {
		log.Fatalf("[%s] create storage dir: %v", prefix, err)
	}

	log.Printf("[%s] subnetctl-vd starting port=%d escrow=%s model=%s chain=%s storage=%s",
		prefix, *port, eid, mdl, chainRest, sp)

	registry := newStreamRegistry()

	br := bridge.NewRESTBridge(chainRest)
	cfg := user.HTTPSessionConfig{
		PrivateKeyHex:  keyHex,
		EscrowID:       eid,
		Bridge:         br,
		StoragePath:    sp,
		StreamCallback: registry.callback,
	}

	session, sm, err := user.NewHTTPSession(cfg)
	if err != nil {
		log.Fatalf("[%s] create session: %v", prefix, err)
	}
	defer session.Close()

	proxy := &Proxy{
		session:  session,
		sm:       sm,
		escrowID: eid,
		model:    mdl,
		registry: registry,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", proxy.handleChatCompletions)
	mux.HandleFunc("/v1/finalize", proxy.handleFinalize)
	mux.HandleFunc("/v1/status", proxy.handleStatus)
	mux.HandleFunc("/v1/debug/pending", proxy.handleDebugPending)
	mux.HandleFunc("/v1/debug/state", proxy.handleDebugState)
	mux.HandleFunc("/v1/inference", proxy.handleInference)

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("[%s] listening on %s", prefix, addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("[%s] server: %v", prefix, err)
	}
}

// loadEnvFile reads KEY=VALUE lines from path into the process environment.
// Existing env vars take precedence (file values are only set if not already present).
func loadEnvFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}
