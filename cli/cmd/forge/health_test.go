package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHealthcheck verifies forge healthcheck sends GET /v1/healthz
// without an Authorization header and prints status.
func TestHealthcheck(t *testing.T) {
	var gotMethod, gotPath, gotAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]any{
			"status":         "ok",
			"postgres":       "ok",
			"uptime_seconds": 3661,
		})
	}))
	defer srv.Close()

	buf := new(bytes.Buffer)
	cmd := newRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"healthcheck", "--api-url", srv.URL, "--api-token", "test-token"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("command failed: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("expected GET, got %s", gotMethod)
	}
	if gotPath != "/v1/healthz" {
		t.Errorf("expected /v1/healthz, got %s", gotPath)
	}
	if gotAuth != "" {
		t.Errorf("healthcheck should not send auth header, got %q", gotAuth)
	}

	output := buf.String()
	if !strings.Contains(output, "ok") || !strings.Contains(output, "OK") {
		// Allow either "ok" or "OK" in output
		if !strings.Contains(strings.ToLower(output), "ok") {
			t.Errorf("output should contain health status:\n%s", output)
		}
	}
	if !strings.Contains(output, "1h") {
		t.Errorf("output should contain formatted uptime (1h):\n%s", output)
	}
}

// TestHealthcheckFail verifies that unhealthy status returns an error.
func TestHealthcheckFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"status":         "degraded",
			"postgres":       "error",
			"uptime_seconds": 60,
		})
	}))
	defer srv.Close()

	buf := new(bytes.Buffer)
	cmd := newRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"healthcheck", "--api-url", srv.URL, "--api-token", "test-token"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for unhealthy status, but got nil")
	}
}

// TestHealthcheckRegistered verifies the healthcheck command is registered on root.
func TestHealthcheckRegistered(t *testing.T) {
	cmd := newRootCmd()
	found := false
	for _, sub := range cmd.Commands() {
		if sub.Name() == "healthcheck" {
			found = true
			break
		}
	}
	if !found {
		t.Error("healthcheck command not registered on root")
	}
}

// TestAllCommandsRegistered verifies all command groups exist on root.
func TestAllCommandsRegistered(t *testing.T) {
	cmd := newRootCmd()
	expected := []string{"node", "sandbox", "routes", "events", "healthcheck"}

	names := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		names[sub.Name()] = true
	}

	for _, name := range expected {
		if !names[name] {
			t.Errorf("command %q not registered on root", name)
		}
	}
}
