package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	OracleURL    string
	ListenAddr   string
	PollInterval time.Duration
	BinDir       string
	DataDir      string
	BinaryName   string
	BasePort     int
	Overrides    map[string]string // version name -> local binary path
}

func Load() (Config, error) {
	oracleURL := os.Getenv("VERSIOND_ORACLE_URL")
	if oracleURL == "" {
		return Config{}, fmt.Errorf("VERSIOND_ORACLE_URL is required")
	}

	cfg := Config{
		OracleURL:    oracleURL,
		ListenAddr:   envOrDefault("VERSIOND_LISTEN_ADDR", ":8080"),
		PollInterval: parseDuration("VERSIOND_POLL_INTERVAL", 30*time.Second),
		BinDir:       envOrDefault("VERSIOND_BIN_DIR", "/opt/versiond/bin"),
		DataDir:      envOrDefault("VERSIOND_DATA_DIR", "/opt/versiond/data"),
		BinaryName:   envOrDefault("VERSIOND_BINARY_NAME", "subnet"),
		BasePort:     parseInt("VERSIOND_BASE_PORT", 9100),
		Overrides:    loadOverrides(),
	}
	return cfg, nil
}

const overridePrefix = "VERSIOND_OVERRIDE_"

// loadOverrides scans env vars for VERSIOND_OVERRIDE_<name>=<path>.
// The suffix after the prefix is the version name (must match oracle exactly).
func loadOverrides() map[string]string {
	overrides := make(map[string]string)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, overridePrefix) {
			continue
		}
		idx := strings.IndexByte(e, '=')
		if idx < 0 {
			continue
		}
		name := e[len(overridePrefix):idx]
		path := e[idx+1:]
		if name != "" && path != "" {
			overrides[name] = path
		}
	}
	return overrides
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func parseDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}
