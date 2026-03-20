package config

import (
	"fmt"
	"os"
	"time"
)

type Config struct {
	OracleURL    string
	ListenAddr   string
	PollInterval time.Duration
	BinDir       string
	DataDir      string
	BinaryName   string
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
	}
	return cfg, nil
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
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
