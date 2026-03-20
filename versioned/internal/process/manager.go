package process

import (
	"context"
	"fmt"
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
	assignedPorts map[string]int // version name -> assigned port
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

func (m *Manager) Reconcile(ctx context.Context, desired []oracle.Version) error {
	desiredSet := make(map[string]oracle.Version, len(desired))
	for _, v := range desired {
		desiredSet[v.Name] = v
	}

	// Phase 1: under lock, identify what needs downloading and what needs stopping.
	m.mu.Lock()
	var toDownload []oracle.Version
	var toStop []*child
	var toStopNames []string

	for _, v := range desired {
		// Override: symlink a local binary, skip download and hash check.
		if localPath, ok := m.cfg.Overrides[v.Name]; ok {
			if _, running := m.processes[v.Name]; running {
				continue
			}
			binDir := filepath.Join(m.cfg.BinDir, v.Name)
			if err := os.MkdirAll(binDir, 0755); err != nil {
				slog.Error("override mkdir failed", "version", v.Name, "error", err)
				continue
			}
			target := filepath.Join(binDir, m.cfg.BinaryName)
			os.Remove(target)
			if err := os.Symlink(localPath, target); err != nil {
				slog.Error("override symlink failed", "version", v.Name, "error", err)
				continue
			}
			slog.Info("using override binary", "version", v.Name, "path", localPath)
			m.startChild(ctx, v)
			continue
		}

		if existing, running := m.processes[v.Name]; running {
			// If binary URL changed, stop + re-download.
			if existing.version.Binary != v.Binary {
				slog.Info("version config changed, restarting", "version", v.Name)
				toStop = append(toStop, existing)
				toStopNames = append(toStopNames, v.Name)
				delete(m.processes, v.Name)
				// If binary URL changed, force re-download by removing cached binary.
				if existing.version.Binary != v.Binary {
					binPath := filepath.Join(m.cfg.BinDir, v.Name, m.cfg.BinaryName)
					os.Remove(binPath)
				}
				// Fall through to download/start logic below.
			} else {
				continue
			}
		}
		if _, dl := m.downloading[v.Name]; dl {
			continue
		}
		binPath := filepath.Join(m.cfg.BinDir, v.Name, m.cfg.BinaryName)
		if _, err := os.Stat(binPath); err != nil {
			m.downloading[v.Name] = struct{}{}
			toDownload = append(toDownload, v)
		} else {
			m.startChild(ctx, v)
		}
	}

	for name, c := range m.processes {
		if _, wanted := desiredSet[name]; !wanted {
			toStop = append(toStop, c)
			toStopNames = append(toStopNames, name)
		}
	}
	for _, name := range toStopNames {
		delete(m.processes, name)
	}
	changed := len(toDownload) > 0 || len(toStop) > 0
	if changed {
		m.rebuildRoutes()
	}
	m.mu.Unlock()

	// Phase 2: downloads outside the lock (can be slow).
	for _, v := range toDownload {
		if err := m.downloadAndStart(ctx, v); err != nil {
			slog.Error("download failed, skipping", "version", v.Name, "error", err)
		}
	}

	// Phase 3: stop removed versions outside the lock.
	for i, c := range toStop {
		slog.Info("stopping removed version", "version", toStopNames[i])
		c.cancel()
	}
	for i, c := range toStop {
		waitForChild(c, 5*time.Second, toStopNames[i])
	}

	return nil
}

// downloadAndStart resolves the checksum, downloads the binary, and starts the child.
func (m *Manager) downloadAndStart(ctx context.Context, v oracle.Version) error {
	// downloadBinary does the slow work outside the lock.
	dlErr := m.downloadBinary(ctx, v)

	m.mu.Lock()
	delete(m.downloading, v.Name)
	if dlErr == nil && ctx.Err() == nil {
		m.startChild(ctx, v)
	}
	m.mu.Unlock()
	return dlErr
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
		status:  "starting",
	}
	m.processes[v.Name] = c
	go m.runChild(childCtx, c)
}

func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	// Collect children and cancel them all first.
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

	// Wait for children outside the lock.
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
		c.status = "running"
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
		c.status = "stopped"
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
		if c.status == "running" {
			routes[c.version.Name] = fmt.Sprintf("localhost:%d", c.port)
		}
	}
	m.routes.Store(routes)
}
