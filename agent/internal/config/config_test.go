package config_test

import (
	"os"
	"testing"

	"github.com/appx/forge/agent/internal/config"
)

// helper sets env vars and returns a cleanup function.
func setEnv(t *testing.T, vars map[string]string) {
	t.Helper()
	for k, v := range vars {
		t.Setenv(k, v)
	}
}

// requiredEnv returns the minimum env vars needed for a valid config.
func requiredEnv() map[string]string {
	return map[string]string{
		"FORGE_CONTROL_URL": "http://control.example.com",
		"FORGE_HOSTNAME":    "node-1",
		"FORGE_HMAC_SECRET": "test-secret-key",
		"FORGE_CAPACITY_MB": "8000",
		"FORGE_API_TOKEN":   "test-token",
	}
}

func TestConfig_LoadsControlURL(t *testing.T) {
	setEnv(t, requiredEnv())

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.ControlURL != "http://control.example.com" {
		t.Errorf("ControlURL = %q, want %q", cfg.ControlURL, "http://control.example.com")
	}
}

func TestConfig_LoadsHostname(t *testing.T) {
	setEnv(t, requiredEnv())

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Hostname != "node-1" {
		t.Errorf("Hostname = %q, want %q", cfg.Hostname, "node-1")
	}
}

func TestConfig_MissingControlURL_ReturnsError(t *testing.T) {
	env := requiredEnv()
	delete(env, "FORGE_CONTROL_URL")
	setEnv(t, env)

	// Explicitly unset in case it's inherited from parent process
	os.Unsetenv("FORGE_CONTROL_URL")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for missing FORGE_CONTROL_URL, got nil")
	}
}

func TestConfig_DefaultsAgentPort(t *testing.T) {
	setEnv(t, requiredEnv())

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.AgentPort != 8090 {
		t.Errorf("AgentPort = %d, want %d", cfg.AgentPort, 8090)
	}
}

func TestConfig_DefaultsPortRange(t *testing.T) {
	setEnv(t, requiredEnv())

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.PortRangeMin != 40000 {
		t.Errorf("PortRangeMin = %d, want %d", cfg.PortRangeMin, 40000)
	}
	if cfg.PortRangeMax != 50000 {
		t.Errorf("PortRangeMax = %d, want %d", cfg.PortRangeMax, 50000)
	}
}

func TestConfig_DefaultsHMACSecret(t *testing.T) {
	setEnv(t, requiredEnv())

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.HMACSecret != "test-secret-key" {
		t.Errorf("HMACSecret = %q, want %q", cfg.HMACSecret, "test-secret-key")
	}
}

func TestConfig_DefaultsSandboxDir(t *testing.T) {
	setEnv(t, requiredEnv())

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.SandboxDir != "/var/lib/forge/sandboxes" {
		t.Errorf("SandboxDir = %q, want %q", cfg.SandboxDir, "/var/lib/forge/sandboxes")
	}
}

func TestConfig_DefaultsAgentVersion(t *testing.T) {
	setEnv(t, requiredEnv())

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.AgentVersion != "0.1.0" {
		t.Errorf("AgentVersion = %q, want %q", cfg.AgentVersion, "0.1.0")
	}
}

func TestConfig_DefaultsSandboxImage(t *testing.T) {
	setEnv(t, requiredEnv())

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.SandboxImage != "appx/sandbox:v1" {
		t.Errorf("SandboxImage = %q, want %q", cfg.SandboxImage, "appx/sandbox:v1")
	}
}

func TestConfig_OverrideDefaults(t *testing.T) {
	env := requiredEnv()
	env["FORGE_AGENT_PORT"] = "9090"
	env["FORGE_PORT_RANGE_MIN"] = "30000"
	env["FORGE_PORT_RANGE_MAX"] = "35000"
	env["FORGE_SANDBOX_DIR"] = "/tmp/sandboxes"
	env["FORGE_AGENT_VERSION"] = "1.0.0"
	env["FORGE_SANDBOX_IMAGE"] = "appx/sandbox:v2"
	setEnv(t, env)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.AgentPort != 9090 {
		t.Errorf("AgentPort = %d, want %d", cfg.AgentPort, 9090)
	}
	if cfg.PortRangeMin != 30000 {
		t.Errorf("PortRangeMin = %d, want %d", cfg.PortRangeMin, 30000)
	}
	if cfg.PortRangeMax != 35000 {
		t.Errorf("PortRangeMax = %d, want %d", cfg.PortRangeMax, 35000)
	}
	if cfg.SandboxDir != "/tmp/sandboxes" {
		t.Errorf("SandboxDir = %q, want %q", cfg.SandboxDir, "/tmp/sandboxes")
	}
	if cfg.AgentVersion != "1.0.0" {
		t.Errorf("AgentVersion = %q, want %q", cfg.AgentVersion, "1.0.0")
	}
	if cfg.SandboxImage != "appx/sandbox:v2" {
		t.Errorf("SandboxImage = %q, want %q", cfg.SandboxImage, "appx/sandbox:v2")
	}
}

func TestConfig_MissingHostname_ReturnsError(t *testing.T) {
	env := requiredEnv()
	delete(env, "FORGE_HOSTNAME")
	setEnv(t, env)

	os.Unsetenv("FORGE_HOSTNAME")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for missing FORGE_HOSTNAME, got nil")
	}
}

func TestConfig_MissingHMACSecret_ReturnsError(t *testing.T) {
	env := requiredEnv()
	delete(env, "FORGE_HMAC_SECRET")
	setEnv(t, env)

	os.Unsetenv("FORGE_HMAC_SECRET")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for missing FORGE_HMAC_SECRET, got nil")
	}
}
