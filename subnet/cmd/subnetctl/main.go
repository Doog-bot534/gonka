//go:build !versiond

package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"subnet/bridge"
	"subnet/user"
)

func main() {
	fs := flag.NewFlagSet("subnetctl", flag.ExitOnError)
	escrowID := fs.String("escrow-id", "", "escrow ID (required, or SUBNET_ESCROW_ID env)")
	chainREST := fs.String("chain-rest", "http://localhost:1317", "chain REST API URL")
	model := fs.String("model", "Qwen/Qwen2.5-7B-Instruct", "default model name")
	port := fs.String("port", "8080", "listen port")
	privateKey := fs.String("private-key", "", "private key hex (alternative to SUBNET_PRIVATE_KEY env)")
	storagePath := fs.String("storage-path", "", "SQLite path for crash recovery")

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

	if err := os.MkdirAll(filepath.Dir(sp), 0755); err != nil {
		log.Fatalf("create storage dir: %v", err)
	}

	registry := newStreamRegistry()

	br := bridge.NewRESTBridge(crest)
	cfg := user.HTTPSessionConfig{
		PrivateKeyHex:  keyHex,
		EscrowID:       eid,
		Bridge:         br,
		StoragePath:    sp,
		StreamCallback: registry.callback,
	}

	session, sm, err := user.NewHTTPSession(cfg)
	if err != nil {
		log.Fatalf("create session: %v", err)
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

	addr := ":" + p
	log.Printf("subnetctl listening on %s (escrow=%s model=%s)", addr, eid, mdl)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server: %v", err)
	}
}

