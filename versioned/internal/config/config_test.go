package config

import (
	"os"
	"testing"
	"time"
)

func TestLoad_MissingOracleURL(t *testing.T) {
	os.Unsetenv("VERSIOND_ORACLE_URL")
	_, err := Load()
	if err == nil {
		t.Fatal("expected error when VERSIOND_ORACLE_URL is missing")
	}
}

func TestLoad_Defaults(t *testing.T) {
	t.Setenv("VERSIOND_ORACLE_URL", "http://oracle:8080/versions")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.OracleURL != "http://oracle:8080/versions" {
		t.Errorf("OracleURL = %q, want %q", cfg.OracleURL, "http://oracle:8080/versions")
	}
	if cfg.ListenAddr != ":8080" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":8080")
	}
	if cfg.PollInterval != 30*time.Second {
		t.Errorf("PollInterval = %v, want %v", cfg.PollInterval, 30*time.Second)
	}
	if cfg.BinDir != "/opt/versiond/bin" {
		t.Errorf("BinDir = %q, want %q", cfg.BinDir, "/opt/versiond/bin")
	}
	if cfg.DataDir != "/opt/versiond/data" {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, "/opt/versiond/data")
	}
	if cfg.BinaryName != "subnet" {
		t.Errorf("BinaryName = %q, want %q", cfg.BinaryName, "subnet")
	}
}

func TestLoad_CustomValues(t *testing.T) {
	t.Setenv("VERSIOND_ORACLE_URL", "http://custom:9090/v")
	t.Setenv("VERSIOND_LISTEN_ADDR", ":9999")
	t.Setenv("VERSIOND_POLL_INTERVAL", "10s")
	t.Setenv("VERSIOND_BIN_DIR", "/tmp/bin")
	t.Setenv("VERSIOND_DATA_DIR", "/tmp/data")
	t.Setenv("VERSIOND_BINARY_NAME", "myapp")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ListenAddr != ":9999" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":9999")
	}
	if cfg.PollInterval != 10*time.Second {
		t.Errorf("PollInterval = %v, want %v", cfg.PollInterval, 10*time.Second)
	}
	if cfg.BinDir != "/tmp/bin" {
		t.Errorf("BinDir = %q, want %q", cfg.BinDir, "/tmp/bin")
	}
	if cfg.BinaryName != "myapp" {
		t.Errorf("BinaryName = %q, want %q", cfg.BinaryName, "myapp")
	}
}

func TestLoad_InvalidPollInterval(t *testing.T) {
	t.Setenv("VERSIOND_ORACLE_URL", "http://oracle:8080/versions")
	t.Setenv("VERSIOND_POLL_INTERVAL", "notaduration")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.PollInterval != 30*time.Second {
		t.Errorf("PollInterval = %v, want fallback %v", cfg.PollInterval, 30*time.Second)
	}
}
