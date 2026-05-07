package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNodeList verifies forge node list sends GET /v1/nodes with Bearer auth
// and prints a table with the expected columns.
func TestNodeList(t *testing.T) {
	var gotMethod, gotPath, gotAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")

		json.NewEncoder(w).Encode(map[string]any{
			"nodes": []map[string]any{
				{
					"id":                "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
					"hostname":          "node-1",
					"tailscale_ip":      "100.64.1.5",
					"capacity_mb":       20000,
					"used_mb":           8000,
					"status":            "healthy",
					"running_sandboxes": 3,
					"agent_version":     "0.1.0",
				},
			},
		})
	}))
	defer srv.Close()

	buf := new(bytes.Buffer)
	cmd := newRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"node", "list", "--api-url", srv.URL, "--api-token", "test-token"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("command failed: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("expected GET, got %s", gotMethod)
	}
	if gotPath != "/v1/nodes" {
		t.Errorf("expected /v1/nodes, got %s", gotPath)
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("expected 'Bearer test-token', got %q", gotAuth)
	}

	output := buf.String()
	for _, col := range []string{"ID", "HOSTNAME", "STATUS", "CAPACITY", "USED", "SANDBOXES", "VERSION"} {
		if !strings.Contains(output, col) {
			t.Errorf("output missing column %q:\n%s", col, output)
		}
	}
	if !strings.Contains(output, "node-1") {
		t.Errorf("output missing hostname 'node-1':\n%s", output)
	}
	if !strings.Contains(output, "healthy") {
		t.Errorf("output missing status 'healthy':\n%s", output)
	}
}

// TestNodeAdd verifies forge node add sends POST /v1/nodes/register with correct body.
func TestNodeAdd(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody)

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"node_id":                    "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			"agent_token":                "secret-token-value",
			"heartbeat_interval_seconds": 15,
		})
	}))
	defer srv.Close()

	buf := new(bytes.Buffer)
	cmd := newRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"node", "add",
		"--hostname", "node-2",
		"--tailscale-ip", "100.64.1.8",
		"--capacity-mb", "16000",
		"--agent-version", "0.2.0",
		"--api-url", srv.URL,
		"--api-token", "test-token",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("command failed: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("expected POST, got %s", gotMethod)
	}
	if gotPath != "/v1/nodes/register" {
		t.Errorf("expected /v1/nodes/register, got %s", gotPath)
	}

	if gotBody["hostname"] != "node-2" {
		t.Errorf("expected hostname 'node-2', got %v", gotBody["hostname"])
	}
	if gotBody["tailscale_ip"] != "100.64.1.8" {
		t.Errorf("expected tailscale_ip '100.64.1.8', got %v", gotBody["tailscale_ip"])
	}
	// JSON numbers decode as float64
	if gotBody["capacity_mb"] != float64(16000) {
		t.Errorf("expected capacity_mb 16000, got %v", gotBody["capacity_mb"])
	}
	if gotBody["agent_version"] != "0.2.0" {
		t.Errorf("expected agent_version '0.2.0', got %v", gotBody["agent_version"])
	}

	output := buf.String()
	if !strings.Contains(output, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee") {
		t.Errorf("output missing node_id:\n%s", output)
	}
}

// TestNodeDrain verifies forge node drain sends POST /v1/nodes/{id}/drain.
func TestNodeDrain(t *testing.T) {
	var gotMethod, gotPath, gotAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	buf := new(bytes.Buffer)
	cmd := newRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"node", "drain", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"--api-url", srv.URL,
		"--api-token", "test-token",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("command failed: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("expected POST, got %s", gotMethod)
	}
	if gotPath != "/v1/nodes/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee/drain" {
		t.Errorf("expected /v1/nodes/{id}/drain, got %s", gotPath)
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("expected 'Bearer test-token', got %q", gotAuth)
	}
}

// TestNodeRemove verifies forge node remove sends DELETE /v1/nodes/{id}.
func TestNodeRemove(t *testing.T) {
	var gotMethod, gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	buf := new(bytes.Buffer)
	cmd := newRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"node", "remove", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"--api-url", srv.URL,
		"--api-token", "test-token",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("command failed: %v", err)
	}

	if gotMethod != http.MethodDelete {
		t.Errorf("expected DELETE, got %s", gotMethod)
	}
	if gotPath != "/v1/nodes/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Errorf("expected /v1/nodes/{id}, got %s", gotPath)
	}
}

// TestNodeListEnvVars verifies that node commands read FORGE_API_URL and FORGE_API_TOKEN from env.
func TestNodeListEnvVars(t *testing.T) {
	var gotAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]any{"nodes": []any{}})
	}))
	defer srv.Close()

	t.Setenv("FORGE_API_URL", srv.URL)
	t.Setenv("FORGE_API_TOKEN", "env-token-value")

	buf := new(bytes.Buffer)
	cmd := newRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"node", "list"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("command failed: %v", err)
	}

	if gotAuth != "Bearer env-token-value" {
		t.Errorf("expected 'Bearer env-token-value', got %q", gotAuth)
	}
}
