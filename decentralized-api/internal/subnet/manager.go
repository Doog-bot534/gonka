package subnet

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/labstack/echo/v4"

	subnetpkg "subnet"
	"subnet/bridge"
	"subnet/host"
	"subnet/signing"
	"subnet/state"
	"subnet/storage"
	"subnet/transport"
	"subnet/types"
)

type sessionEntry struct {
	server *transport.Server
	host   *host.Host
	store  storage.Storage
}

// HostManager manages per-escrow subnet sessions with lazy creation.
type HostManager struct {
	mu       sync.RWMutex
	sessions map[string]*sessionEntry

	signer    *signing.Secp256k1Signer
	verifier  signing.Verifier
	engine    subnetpkg.InferenceEngine
	validator subnetpkg.ValidationEngine
	bridge    bridge.MainnetBridge
}

func NewHostManager(
	signer *signing.Secp256k1Signer,
	engine subnetpkg.InferenceEngine,
	validator subnetpkg.ValidationEngine,
	br bridge.MainnetBridge,
) *HostManager {
	return &HostManager{
		sessions:  make(map[string]*sessionEntry),
		signer:    signer,
		verifier:  signing.NewSecp256k1Verifier(),
		engine:    engine,
		validator: validator,
		bridge:    br,
	}
}

func (m *HostManager) getOrCreate(escrowID string) (*sessionEntry, error) {
	m.mu.RLock()
	entry, ok := m.sessions[escrowID]
	m.mu.RUnlock()
	if ok {
		return entry, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if entry, ok = m.sessions[escrowID]; ok {
		return entry, nil
	}
	return m.createLocked(escrowID)
}

func (m *HostManager) createLocked(escrowID string) (*sessionEntry, error) {
	group, err := bridge.BuildGroup(escrowID, m.bridge)
	if err != nil {
		return nil, fmt.Errorf("build group: %w", err)
	}

	escrow, err := m.bridge.GetEscrow(escrowID)
	if err != nil {
		return nil, fmt.Errorf("get escrow: %w", err)
	}

	creatorAddr := escrow.CreatorAddress

	config := types.DefaultSessionConfig(len(group))

	store := storage.NewMemory()
	if err := store.CreateSession(escrowID, config, group, escrow.Amount); err != nil {
		return nil, fmt.Errorf("init storage session: %w", err)
	}

	sm := state.NewStateMachine(escrowID, config, group, escrow.Amount, creatorAddr, m.verifier,
		state.WithWarmKeyResolver(m.bridge.VerifyWarmKey),
	)

	h, err := host.NewHost(sm, m.signer, m.engine, escrowID, group, nil,
		host.WithValidator(m.validator),
		host.WithStorage(store),
	)
	if err != nil {
		return nil, fmt.Errorf("create host: %w", err)
	}

	srv, err := transport.NewServer(h, store, escrowID, m.verifier, group, creatorAddr,
		transport.WithBridge(m.bridge),
	)
	if err != nil {
		return nil, fmt.Errorf("create server: %w", err)
	}

	entry := &sessionEntry{
		server: srv,
		host:   h,
		store:  store,
	}
	m.sessions[escrowID] = entry
	return entry, nil
}

// Register mounts subnet session routes on the given echo group.
func (m *HostManager) Register(g *echo.Group) {
	g.POST("/sessions/:id/chat/completions", m.withAuth(func(e *sessionEntry) echo.HandlerFunc { return e.server.HandleInference }))
	g.POST("/sessions/:id/verify-timeout", m.withAuth(func(e *sessionEntry) echo.HandlerFunc { return e.server.HandleVerifyTimeout }))
	g.POST("/sessions/:id/challenge-receipt", m.withAuth(func(e *sessionEntry) echo.HandlerFunc { return e.server.HandleChallengeReceipt }))
	g.POST("/sessions/:id/gossip/nonce", m.withAuth(func(e *sessionEntry) echo.HandlerFunc { return e.server.HandleGossipNonce }))
	g.POST("/sessions/:id/gossip/txs", m.withAuth(func(e *sessionEntry) echo.HandlerFunc { return e.server.HandleGossipTxs }))
	g.GET("/sessions/:id/diffs", m.withoutAuth(func(e *sessionEntry) echo.HandlerFunc { return e.server.HandleGetDiffs }))
	g.GET("/sessions/:id/mempool", m.withoutAuth(func(e *sessionEntry) echo.HandlerFunc { return e.server.HandleGetMempool }))
	g.GET("/sessions/:id/signatures", m.withoutAuth(func(e *sessionEntry) echo.HandlerFunc { return e.server.HandleGetSignatures }))
}

func (m *HostManager) withAuth(pick func(*sessionEntry) echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		entry, err := m.getOrCreate(c.Param("id"))
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return entry.server.AuthMiddleware(pick(entry))(c)
	}
}

func (m *HostManager) withoutAuth(pick func(*sessionEntry) echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		entry, err := m.getOrCreate(c.Param("id"))
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return pick(entry)(c)
	}
}
