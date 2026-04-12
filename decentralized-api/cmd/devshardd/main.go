// Command devshardd is a standalone devshard host process. It is a temporary
// binary built out of the decentralized-api Go module so that versiond can
// run, download, and manage versioned devshard binaries without waiting for a
// full self-contained rewrite under the devshard/ module.
//
// devshardd reuses dapi's HostManager, ChainBridge, signer, and payload store
// as libraries but strips everything dapi does that a host does not need:
// no admin server, no model manager, no PoC worker, no event dispatcher, no
// block queue, no config sync, no NodeManager gRPC server, no NATS, and no
// transaction manager. devshardd never writes to mainnet.
//
// Versiond's process manager invokes this binary with `--port <N>` and
// `--data-dir <PATH>` as its contract (see versioned/internal/process/manager.go).
// Everything else is configured via env vars.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"decentralized-api/apiconfig"
	internaldevshard "decentralized-api/internal/devshard"
	pserver "decentralized-api/internal/server/public"
	"decentralized-api/payloadstorage"

	igniteclient "github.com/ignite/cli/v28/ignite/pkg/cosmosclient"
	"github.com/labstack/echo/v4"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/codec"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"

	mlnodeclient "devshard/mlnode"
	devshardstorage "devshard/storage"
)

// Version is the devshardd version. Set via ldflags
// -X 'decentralized-api/cmd/devshardd.Version=...'. Defaults to "dev" for
// local builds without an ldflags override.
var Version = "dev"

func main() {
	port := flag.Int("port", 9500, "HTTP listen port (set by versiond)")
	dataDir := flag.String("data-dir", "/var/lib/devshardd", "data directory for sqlite/payloads (set by versiond)")
	flag.Parse()

	prefix := os.Getenv("DEVSHARD_LOG_PREFIX")
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	slog.Info("devshardd starting",
		"version", Version, "prefix", prefix, "port", *port, "data-dir", *dataDir)

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatalf("create data dir %s: %v", *dataDir, err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	nodeConfig := loadNodeConfigFromEnv()
	slog.Info("chain node", "url", nodeConfig.Url, "keyring_backend", nodeConfig.KeyringBackend, "keyring_dir", nodeConfig.KeyringDir)

	ignite, err := newIgniteClient(ctx, nodeConfig)
	if err != nil {
		log.Fatalf("ignite cosmosclient: %v", err)
	}

	apiAccount, err := buildApiAccount(ignite, nodeConfig.SignerKeyName)
	if err != nil {
		log.Fatalf("api account: %v", err)
	}

	recorder, err := newQueryOnlyCosmosClient(ctx, ignite, apiAccount)
	if err != nil {
		log.Fatalf("query-only cosmos client: %v", err)
	}

	signer, err := internaldevshard.NewSignerFromKeyring(*recorder.GetKeyring(), apiAccount.SignerAccount.Name)
	if err != nil {
		log.Fatalf("devshard signer: %v", err)
	}

	br := internaldevshard.NewChainBridge(recorder)

	nmAddr := envOr("NODE_MANAGER_ADDR", "localhost:9400")
	slog.Info("nodemanager", "addr", nmAddr)
	mlClient, err := mlnodeclient.NewClient(nmAddr)
	if err != nil {
		log.Fatalf("mlnode client: %v", err)
	}
	defer mlClient.Close()

	payloadDir := filepath.Join(*dataDir, "payloads")
	if err := os.MkdirAll(payloadDir, 0o755); err != nil {
		log.Fatalf("create payload dir: %v", err)
	}
	payloadStore := payloadstorage.NewPayloadStorage(ctx, payloadDir)

	httpClient := pserver.NewNoRedirectClient(5 * time.Minute)

	engine := newDevshardEngine(mlClient, payloadStore, httpClient)
	validator := newDevshardValidator(mlClient, httpClient, br, recorder, engine)

	storePath := filepath.Join(*dataDir, "devshardd.db")
	store, err := devshardstorage.NewSQLite(storePath)
	if err != nil {
		log.Fatalf("devshard sqlite: %v", err)
	}
	defer store.Close()

	manager := internaldevshard.NewHostManager(store, signer, engine, validator, br, payloadStore, recorder)
	if err := manager.RecoverSessions(); err != nil {
		slog.Warn("recover sessions failed", "error", err)
	}

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.GET("/healthz", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })
	// Mount HostManager routes at the root. Versiond strips the /<version>/
	// prefix before forwarding, so devshardd sees /sessions/:id/* directly.
	manager.Register(e.Group(""))

	addr := fmt.Sprintf(":%d", *port)
	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", addr)
		if err := e.Start(addr); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutdown requested")
	case err := <-errCh:
		slog.Error("server error", "error", err)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = e.Shutdown(shutdownCtx)
	slog.Info("devshardd stopped")
}

// loadNodeConfigFromEnv builds a ChainNodeConfig from the same env vars
// dapi's init-docker.sh already uses (NODE_HOST, KEY_NAME, KEYRING_BACKEND,
// KEYRING_PASSWORD, KEYRING_DIR). Reusing these names avoids inventing
// devshardd-only patterns: anything that exports them for dapi automatically
// configures devshardd too. Defaults match production: file keyring backend,
// /root/.inference dir.
func loadNodeConfigFromEnv() apiconfig.ChainNodeConfig {
	nodeHost := envOr("NODE_HOST", "node")
	return apiconfig.ChainNodeConfig{
		Url:             "http://" + nodeHost + ":26657",
		KeyringBackend:  envOr("KEYRING_BACKEND", "file"),
		KeyringDir:      envOr("KEYRING_DIR", "/root/.inference"),
		SignerKeyName:   envOr("KEY_NAME", ""),
		KeyringPassword: os.Getenv("KEYRING_PASSWORD"),
	}
}

// buildApiAccount constructs an apiconfig.ApiAccount for devshardd using the
// signer key's own pubkey as the "account" key. This is simpler than dapi's
// NewApiAccount (which takes a separate base64-encoded ACCOUNT_PUBKEY env
// var): devshardd only ever signs as one identity, so SignerAccount and
// AccountKey are the same key.
func buildApiAccount(ignite *igniteclient.Client, keyName string) (apiconfig.ApiAccount, error) {
	if keyName == "" {
		return apiconfig.ApiAccount{}, fmt.Errorf("KEY_NAME is required")
	}
	signer, err := ignite.AccountRegistry.GetByName(keyName)
	if err != nil {
		return apiconfig.ApiAccount{}, fmt.Errorf("get signer %q: %w", keyName, err)
	}
	pubKey, err := signer.Record.GetPubKey()
	if err != nil {
		return apiconfig.ApiAccount{}, fmt.Errorf("signer pubkey: %w", err)
	}
	return apiconfig.ApiAccount{
		AccountKey:    pubKey,
		SignerAccount: &signer,
		AddressPrefix: "gonka",
	}, nil
}

// newIgniteClient builds an ignite cosmosclient.Client with the same options
// dapi uses minus the NATS/tx_manager setup. Uses `file` keyring backend
// handling identical to cosmosclient.updateKeyringIfNeeded so devshardd reads
// the same keyring dapi writes.
func newIgniteClient(ctx context.Context, nodeConfig apiconfig.ChainNodeConfig) (*igniteclient.Client, error) {
	keyringDir, err := expandHome(nodeConfig.KeyringDir)
	if err != nil {
		return nil, err
	}

	c, err := igniteclient.New(
		ctx,
		igniteclient.WithAddressPrefix("gonka"),
		igniteclient.WithKeyringServiceName("inferenced"),
		igniteclient.WithNodeAddress(nodeConfig.Url),
		igniteclient.WithKeyringDir(keyringDir),
		igniteclient.WithGasPrices("0ngonka"),
		igniteclient.WithFees("0ngonka"),
		igniteclient.WithGas("auto"),
		igniteclient.WithGasAdjustment(5),
	)
	if err != nil {
		return nil, fmt.Errorf("cosmosclient.New: %w", err)
	}

	// For the `file` keyring backend, replace the registry's keyring with one
	// initialized from the plaintext password so non-interactive processes
	// can sign. Mirrors cosmosclient.updateKeyringIfNeeded.
	if nodeConfig.KeyringBackend == keyring.BackendFile {
		reg := codectypes.NewInterfaceRegistry()
		cryptocodec.RegisterInterfaces(reg)
		cdc := codec.NewProtoCodec(reg)
		kr, err := keyring.New(
			"inferenced",
			nodeConfig.KeyringBackend,
			keyringDir,
			strings.NewReader(nodeConfig.KeyringPassword),
			cdc,
		)
		if err != nil {
			return nil, fmt.Errorf("file keyring: %w", err)
		}
		c.AccountRegistry.Keyring = kr
	}

	return &c, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func expandHome(path string) (string, error) {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, path[2:]), nil
	}
	return filepath.Abs(path)
}
