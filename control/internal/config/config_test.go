package config

import (
	"os"
	"testing"
)

func TestLoad_AllFieldsParsed(t *testing.T) {
	// Set all required env vars
	envVars := map[string]string{
		"FORGE_LISTEN_ADDR":                ":9090",
		"FORGE_DATABASE_URL":               "postgres://user:pass@localhost/forge",
		"FORGE_API_TOKEN":                  "test-token-abc123",
		"FORGE_HMAC_SECRET":                "hmac-secret-xyz",
		"FORGE_LOG_LEVEL":                  "debug",
		"FORGE_HEARTBEAT_INTERVAL_SECONDS": "30",
		"FORGE_HEARTBEAT_MISS_THRESHOLD":   "5",
	}
	for k, v := range envVars {
		os.Setenv(k, v)
		defer os.Unsetenv(k)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.ListenAddr != ":9090" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":9090")
	}
	if cfg.DatabaseURL != "postgres://user:pass@localhost/forge" {
		t.Errorf("DatabaseURL = %q, want postgres URL", cfg.DatabaseURL)
	}
	if cfg.APIToken != "test-token-abc123" {
		t.Errorf("APIToken = %q, want %q", cfg.APIToken, "test-token-abc123")
	}
	if cfg.HMACSecret != "hmac-secret-xyz" {
		t.Errorf("HMACSecret = %q, want %q", cfg.HMACSecret, "hmac-secret-xyz")
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "debug")
	}
	if cfg.HeartbeatIntervalSeconds != 30 {
		t.Errorf("HeartbeatIntervalSeconds = %d, want 30", cfg.HeartbeatIntervalSeconds)
	}
	if cfg.HeartbeatMissThreshold != 5 {
		t.Errorf("HeartbeatMissThreshold = %d, want 5", cfg.HeartbeatMissThreshold)
	}
}

func TestLoad_Defaults(t *testing.T) {
	// Only set required fields, let defaults apply
	os.Setenv("FORGE_DATABASE_URL", "postgres://localhost/forge")
	os.Setenv("FORGE_API_TOKEN", "required-token")
	os.Setenv("FORGE_HMAC_SECRET", "required-hmac")
	defer os.Unsetenv("FORGE_DATABASE_URL")
	defer os.Unsetenv("FORGE_API_TOKEN")
	defer os.Unsetenv("FORGE_HMAC_SECRET")

	// Clear optional vars to test defaults
	os.Unsetenv("FORGE_LISTEN_ADDR")
	os.Unsetenv("FORGE_LOG_LEVEL")
	os.Unsetenv("FORGE_HEARTBEAT_INTERVAL_SECONDS")
	os.Unsetenv("FORGE_HEARTBEAT_MISS_THRESHOLD")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.ListenAddr != ":8080" {
		t.Errorf("ListenAddr default = %q, want %q", cfg.ListenAddr, ":8080")
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel default = %q, want %q", cfg.LogLevel, "info")
	}
	if cfg.HeartbeatIntervalSeconds != 15 {
		t.Errorf("HeartbeatIntervalSeconds default = %d, want 15", cfg.HeartbeatIntervalSeconds)
	}
	if cfg.HeartbeatMissThreshold != 3 {
		t.Errorf("HeartbeatMissThreshold default = %d, want 3", cfg.HeartbeatMissThreshold)
	}
}

func TestLoad_MissingRequired(t *testing.T) {
	// Clear all env vars
	os.Unsetenv("FORGE_DATABASE_URL")
	os.Unsetenv("FORGE_API_TOKEN")
	os.Unsetenv("FORGE_HMAC_SECRET")
	os.Unsetenv("FORGE_LISTEN_ADDR")
	os.Unsetenv("FORGE_LOG_LEVEL")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() should fail when DATABASE_URL is missing")
	}
}
