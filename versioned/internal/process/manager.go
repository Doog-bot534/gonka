package process

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"versioned/internal/config"
	"versioned/internal/download"
	"versioned/internal/health"
	"versioned/internal/oracle"
)

const (
	statusStarting = "starting"
	statusRunning  = "running"
	statusStopped  = "stopped"
)

type child struct {
	version oracle.Version
	port    int
	cancel  context.CancelFunc
	done    chan struct{} // closed when runChild exits
	status  string
}

type Manager struct {
	cfg           config.Config
	processes     map[string]*child
	downloading   map[string]struct{}
	assignedPorts map[string]int // version name -> assigned port (persists for manager lifetime)
	nextPort      int
	mu            sync.Mutex
	routes        atomic.Value // map[string]string
}

func NewManager(cfg config.Config) *Manager {
	m := &Manager{
		cfg:           cfg,
		processes:     make(map[string]*child),
		downloading:   make(map[string]struct{}),
		assignedPorts: make(map[string]int),
		nextPort:      cfg.BasePort,
	}
	m.routes.Store(map[string]string{})
	return m
}

// assignPort returns a stable port for the given version name.
// Once assigned, the same name always gets the same port.
// Must be called with m.mu held.
func (m *Manager) assignPort(name string) int {
	if port, ok := m.assignedPorts[name]; ok {
		return port
	}
	port := m.nextPort
	m.nextPort++
	m.assignedPorts[name] = port
	return port
}

func (m *Manager) RouteTable() *atomic.Value {
	return &m.routes
}

func (m *Manager) Status() []health.StatusEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]health.StatusEntry, 0, len(m.processes))
	for _, c := range m.processes {
		out = append(out, health.StatusEntry{
			Name:   c.version.Name,
			Port:   c.port,
			Status: c.status,
		})
	}
	return out
}

// hashFile computes the sha256 hex digest of a file on disk.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Reconcile compares desired state against local state and converges.
// sha256 is the sole identity for binaries. URLs are download hints only.
func (m *Manager) Reconcile(ctx context.Context, desired []oracle.Version) error {
	// Step 0: build desired set, injecting forced versions.
	desiredSet := make(map[string]oracle.Version, len(desired))
	for _, v := range desired {
		desiredSet[v.Name] = v
	}
	for _, name := range m.cfg.ForceVersions {
		if _, exists := desiredSet[name]; exists {
			continue
		}
		if _, hasOverride := m.cfg.Overrides[name]; !hasOverride {
			slog.Warn("forced version skipped: no override configured", "version", name)
			continue
		}
		desiredSet[name] = oracle.Version{Name: name}
	}

	// Step 1: process desired versions.
	m.mu.Lock()
	var toDownload []versionAction
	var toSwap []versionAction // running version with hash mismatch (zero-downtime swap)
	var toStop []*child
	var toStopNames []string
	for _, v := range desiredSet {
		binPath := filepath.Join(m.cfg.BinDir, v.Name, m.cfg.BinaryName)

		if overrideSrc, isOverride := m.cfg.Overrides[v.Name]; isOverride {
			m.reconcileOverride(ctx, v, overrideSrc, binPath)
			continue
		}

		running, isRunning := m.processes[v.Name]

		desiredHash, err := v.ResolvedSHA256()
		if err != nil {
			slog.Error("cannot resolve sha256, skipping", "version", v.Name, "error", err)
			continue
		}

		if isRunning {
			diskHash, hashErr := hashFile(binPath)
			if hashErr != nil {
				slog.Error("cannot hash running binary, skipping", "version", v.Name, "error", hashErr)
				continue
			}
			if diskHash == desiredHash {
				continue // already running correct binary
			}
			// Hash mismatch: need zero-downtime swap (download first, then stop).
			if _, dl := m.downloading[v.Name]; dl {
				continue
			}
			slog.Info("hash mismatch on running version, scheduling swap",
				"version", v.Name, "disk", diskHash, "desired", desiredHash)
			m.downloading[v.Name] = struct{}{}
			toSwap = append(toSwap, versionAction{version: v, child: running})
			continue
		}

		// Not running.
		if _, dl := m.downloading[v.Name]; dl {
			continue
		}

		diskHash, hashErr := hashFile(binPath)
		if hashErr == nil && diskHash == desiredHash {
			// Binary on disk matches, just start.
			m.startChild(ctx, v)
			continue
		}

		// Binary missing or hash mismatch: (re)download.
		if hashErr == nil {
			slog.Info("cached binary hash mismatch, re-downloading",
				"version", v.Name, "disk", diskHash, "desired", desiredHash)
			os.Remove(binPath)
		}
		m.downloading[v.Name] = struct{}{}
		toDownload = append(toDownload, versionAction{version: v})
	}

	// Step 2: stop versions not in desired set.
	for name, c := range m.processes {
		if _, wanted := desiredSet[name]; !wanted {
			toStop = append(toStop, c)
			toStopNames = append(toStopNames, name)
		}
	}
	for _, name := range toStopNames {
		delete(m.processes, name)
	}

	changed := len(toDownload) > 0 || len(toSwap) > 0 || len(toStop) > 0
	if changed {
		m.rebuildRoutes()
	}
	m.mu.Unlock()

	// Phase 2: downloads outside the lock (can be slow).
	for _, a := range toDownload {
		if err := m.downloadAndStart(ctx, a.version); err != nil {
			slog.Error("download failed, skipping", "version", a.version.Name, "error", err)
		}
	}

	// Phase 3: zero-downtime swaps -- download THEN stop old process.
	for _, a := range toSwap {
		if err := m.downloadAndSwap(ctx, a.version, a.child); err != nil {
			slog.Error("swap failed, keeping old version", "version", a.version.Name, "error", err)
		}
	}

	// Phase 4: stop removed versions outside the lock.
	for i, c := range toStop {
		slog.Info("stopping removed version", "version", toStopNames[i])
		c.cancel()
	}
	for i, c := range toStop {
		waitForChild(c, 5*time.Second, toStopNames[i])
	}

	return nil
}

type versionAction struct {
	version oracle.Version
	child   *child // non-nil for swap actions
}


// reconcileOverride handles a version with a local override binary.
// Must be called with m.mu held.
func (m *Manager) reconcileOverride(ctx context.Context, v oracle.Version, overrideSrc, binPath string) {
	srcHash, err := hashFile(overrideSrc)
	if err != nil {
		slog.Error("override source unreadable", "version", v.Name, "path", overrideSrc, "error", err)
		return
	}

	existing, isRunning := m.processes[v.Name]
	if isRunning {
		diskHash, hashErr := hashFile(binPath)
		if hashErr == nil && diskHash == srcHash {
			return // already running the same override binary
		}
		// Override source changed: stop old, copy new, start.
		slog.Info("override binary changed, restarting", "version", v.Name)
		existing.cancel()
		go func() { waitForChild(existing, 5*time.Second, v.Name) }()
		delete(m.processes, v.Name)
	}

	binDir := filepath.Join(m.cfg.BinDir, v.Name)
	if err := os.MkdirAll(binDir, 0755); err != nil {
		slog.Error("override mkdir failed", "version", v.Name, "error", err)
		return
	}

	if err := atomicCopy(overrideSrc, binPath); err != nil {
		slog.Error("override copy failed", "version", v.Name, "error", err)
		return
	}

	slog.Info("using override binary", "version", v.Name, "path", overrideSrc)
	m.startChild(ctx, v)
}

// atomicCopy copies src to dst via a temp file + rename.
func atomicCopy(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, filepath.Base(dst)+".tmp.*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()

	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	tmp.Close()

	if err := os.Chmod(tmpPath, 0755); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

// downloadAndStart resolves the checksum, downloads the binary, and starts the child.
func (m *Manager) downloadAndStart(ctx context.Context, v oracle.Version) error {
	dlErr := m.downloadBinary(ctx, v)

	m.mu.Lock()
	delete(m.downloading, v.Name)
	if dlErr == nil && ctx.Err() == nil {
		m.startChild(ctx, v)
	}
	m.mu.Unlock()
	return dlErr
}

// downloadAndSwap downloads the new binary, then atomically replaces the old one.
// The old process is stopped only after the new binary is on disk.
func (m *Manager) downloadAndSwap(ctx context.Context, v oracle.Version, old *child) error {
	dlErr := m.downloadBinary(ctx, v)

	m.mu.Lock()
	delete(m.downloading, v.Name)
	m.mu.Unlock()

	if dlErr != nil {
		return dlErr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Stop old process after new binary is on disk.
	slog.Info("stopping old process for swap", "version", v.Name)
	old.cancel()
	waitForChild(old, 5*time.Second, v.Name)

	m.mu.Lock()
	delete(m.processes, v.Name)
	m.startChild(ctx, v)
	m.mu.Unlock()

	return nil
}

func (m *Manager) downloadBinary(ctx context.Context, v oracle.Version) error {
	sha, err := v.ResolvedSHA256()
	if err != nil {
		return fmt.Errorf("resolve checksum: %w", err)
	}
	binDir := filepath.Join(m.cfg.BinDir, v.Name)
	if err := download.Download(ctx, v.Binary, sha, binDir, m.cfg.BinaryName); err != nil {
		return err
	}
	slog.Info("downloaded binary", "version", v.Name)
	return nil
}

// startChild must be called with m.mu held.
func (m *Manager) startChild(ctx context.Context, v oracle.Version) {
	childCtx, childCancel := context.WithCancel(ctx)
	c := &child{
		version: v,
		port:    m.assignPort(v.Name),
		cancel:  childCancel,
		done:    make(chan struct{}),
		status:  statusStarting,
	}
	m.processes[v.Name] = c
	go m.runChild(childCtx, c)
}

func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	children := make([]*child, 0, len(m.processes))
	names := make([]string, 0, len(m.processes))
	for name, c := range m.processes {
		children = append(children, c)
		names = append(names, name)
		c.cancel()
	}
	m.processes = make(map[string]*child)
	m.routes.Store(map[string]string{})
	m.mu.Unlock()

	for i, c := range children {
		slog.Info("shutting down", "version", names[i])
		waitForChild(c, 10*time.Second, names[i])
	}
	return nil
}

// waitForChild waits for a child's goroutine to exit within the timeout.
// The child should already have been cancelled via c.cancel().
// exec.CommandContext sends SIGKILL when the context is cancelled,
// so the process will be killed. We just wait for runChild to finish.
func waitForChild(c *child, timeout time.Duration, name string) {
	select {
	case <-c.done:
	case <-time.After(timeout):
		slog.Warn("child goroutine did not exit in time", "version", name)
	}
}

func (m *Manager) runChild(ctx context.Context, c *child) {
	defer close(c.done)

	binPath := filepath.Join(m.cfg.BinDir, c.version.Name, m.cfg.BinaryName)
	dataDir := filepath.Join(m.cfg.DataDir, c.version.Name)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		slog.Error("create data dir failed", "version", c.version.Name, "error", err)
		return
	}

	backoff := time.Second
	lastStart := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		cmd := exec.CommandContext(ctx, binPath,
			"--data-dir", dataDir,
			"--port", fmt.Sprintf("%d", c.port),
		)
		cmd.Env = append(os.Environ(), fmt.Sprintf("SUBNET_LOG_PREFIX=%s", c.version.Name))
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		// Cancel sends SIGKILL by default. Override to send SIGTERM for graceful shutdown.
		cmd.Cancel = func() error {
			return cmd.Process.Signal(syscall.SIGTERM)
		}
		cmd.WaitDelay = 5 * time.Second // SIGKILL after 5s if SIGTERM didn't work

		lastStart = time.Now()
		slog.Info("starting child", "version", c.version.Name, "port", c.port)

		if err := cmd.Start(); err != nil {
			slog.Error("child start failed", "version", c.version.Name, "error", err)
			break
		}

		// Wait for the child to start accepting connections before routing traffic.
		if !waitForPort(ctx, c.port, 10*time.Second) {
			slog.Warn("child did not start listening in time, routing anyway", "version", c.version.Name)
		}
		m.mu.Lock()
		c.status = statusRunning
		m.rebuildRoutes()
		m.mu.Unlock()

		err := cmd.Wait()

		select {
		case <-ctx.Done():
			return
		default:
		}

		slog.Error("child exited", "version", c.version.Name, "error", err)

		m.mu.Lock()
		c.status = statusStopped
		m.rebuildRoutes()
		m.mu.Unlock()

		if time.Since(lastStart) > 60*time.Second {
			backoff = time.Second
		}

		slog.Info("restarting child after backoff", "version", c.version.Name, "backoff", backoff)

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > 60*time.Second {
			backoff = 60 * time.Second
		}
	}
}

// waitForPort polls until a TCP connection succeeds on the given port.
// Returns true if the port is reachable before the timeout or context cancellation.
func waitForPort(ctx context.Context, port int, timeout time.Duration) bool {
	deadline := time.After(timeout)
	addr := fmt.Sprintf("localhost:%d", port)
	for {
		select {
		case <-ctx.Done():
			return false
		case <-deadline:
			return false
		default:
		}
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// rebuildRoutes rebuilds the atomic route map. Only includes running children.
// Must be called with m.mu held.
func (m *Manager) rebuildRoutes() {
	routes := make(map[string]string)
	for _, c := range m.processes {
		if c.status == statusRunning {
			routes[c.version.Name] = fmt.Sprintf("localhost:%d", c.port)
		}
	}
	m.routes.Store(routes)
}
