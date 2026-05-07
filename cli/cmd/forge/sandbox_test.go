package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSandboxList verifies forge sandbox list sends GET /v1/sandboxes
// and prints a table with the expected columns.
func TestSandboxList(t *testing.T) {
	var gotMethod, gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		json.NewEncoder(w).Encode(map[string]any{
			"sandboxes": []map[string]any{
				{
					"id":       "11111111-2222-3333-4444-555555555555",
					"app_name": "cool-app",
					"state":    "running",
					"node_id":  "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
					"url":      "https://cool-app.myappx.live",
				},
			},
		})
	}))
	defer srv.Close()

	buf := new(bytes.Buffer)
	cmd := newRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"sandbox", "list", "--api-url", srv.URL, "--api-token", "test-token"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("command failed: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("expected GET, got %s", gotMethod)
	}
	if gotPath != "/v1/sandboxes" {
		t.Errorf("expected /v1/sandboxes, got %s", gotPath)
	}

	output := buf.String()
	for _, col := range []string{"ID", "APP_NAME", "STATE", "NODE", "URL"} {
		if !strings.Contains(output, col) {
			t.Errorf("output missing column %q:\n%s", col, output)
		}
	}
	if !strings.Contains(output, "cool-app") {
		t.Errorf("output missing app_name 'cool-app':\n%s", output)
	}
}

// TestSandboxListWithStateFilter verifies --state flag sends state query param.
func TestSandboxListWithStateFilter(t *testing.T) {
	var gotQuery string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		json.NewEncoder(w).Encode(map[string]any{"sandboxes": []any{}})
	}))
	defer srv.Close()

	buf := new(bytes.Buffer)
	cmd := newRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"sandbox", "list",
		"--state", "running",
		"--api-url", srv.URL,
		"--api-token", "test-token",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("command failed: %v", err)
	}

	if !strings.Contains(gotQuery, "state=running") {
		t.Errorf("expected query to contain state=running, got %q", gotQuery)
	}
}

// TestSandboxListWithAppFilter verifies --app flag sends app_name query param.
func TestSandboxListWithAppFilter(t *testing.T) {
	var gotQuery string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		json.NewEncoder(w).Encode(map[string]any{"sandboxes": []any{}})
	}))
	defer srv.Close()

	buf := new(bytes.Buffer)
	cmd := newRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"sandbox", "list",
		"--app", "my-app",
		"--api-url", srv.URL,
		"--api-token", "test-token",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("command failed: %v", err)
	}

	if !strings.Contains(gotQuery, "app_name=my-app") {
		t.Errorf("expected query to contain app_name=my-app, got %q", gotQuery)
	}
}

// TestSandboxInspect verifies forge sandbox inspect sends GET /v1/sandboxes/{id}
// and prints JSON output.
func TestSandboxInspect(t *testing.T) {
	var gotMethod, gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		json.NewEncoder(w).Encode(map[string]any{
			"id":       "11111111-2222-3333-4444-555555555555",
			"app_name": "cool-app",
			"state":    "running",
			"node_id":  "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			"url":      "https://cool-app.myappx.live",
			"resources": map[string]any{
				"cpu_cores": 0.5,
				"memory_mb": 512,
			},
		})
	}))
	defer srv.Close()

	buf := new(bytes.Buffer)
	cmd := newRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"sandbox", "inspect", "11111111-2222-3333-4444-555555555555",
		"--api-url", srv.URL,
		"--api-token", "test-token",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("command failed: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("expected GET, got %s", gotMethod)
	}
	if gotPath != "/v1/sandboxes/11111111-2222-3333-4444-555555555555" {
		t.Errorf("expected /v1/sandboxes/{id}, got %s", gotPath)
	}

	output := buf.String()
	// Inspect should output JSON
	if !strings.Contains(output, "cool-app") {
		t.Errorf("output missing app_name:\n%s", output)
	}
	if !strings.Contains(output, "running") {
		t.Errorf("output missing state:\n%s", output)
	}
}

// TestSandboxLogs verifies forge sandbox logs sends GET /v1/sandboxes/{id}/logs
// with tail query param and prints text output.
func TestSandboxLogs(t *testing.T) {
	var gotMethod, gotPath, gotQuery string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("2026-04-15 Starting Metro...\n2026-04-15 Ready on port 8081\n"))
	}))
	defer srv.Close()

	buf := new(bytes.Buffer)
	cmd := newRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"sandbox", "logs", "11111111-2222-3333-4444-555555555555",
		"--tail", "50",
		"--api-url", srv.URL,
		"--api-token", "test-token",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("command failed: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("expected GET, got %s", gotMethod)
	}
	if gotPath != "/v1/sandboxes/11111111-2222-3333-4444-555555555555/logs" {
		t.Errorf("expected /v1/sandboxes/{id}/logs, got %s", gotPath)
	}
	if !strings.Contains(gotQuery, "tail=50") {
		t.Errorf("expected query to contain tail=50, got %q", gotQuery)
	}

	output := buf.String()
	if !strings.Contains(output, "Starting Metro") {
		t.Errorf("output missing log content:\n%s", output)
	}
}

// TestSandboxRestart verifies forge sandbox restart sends POST /v1/sandboxes/{id}/restart.
func TestSandboxRestart(t *testing.T) {
	var gotMethod, gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	buf := new(bytes.Buffer)
	cmd := newRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"sandbox", "restart", "11111111-2222-3333-4444-555555555555",
		"--api-url", srv.URL,
		"--api-token", "test-token",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("command failed: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("expected POST, got %s", gotMethod)
	}
	if gotPath != "/v1/sandboxes/11111111-2222-3333-4444-555555555555/restart" {
		t.Errorf("expected /v1/sandboxes/{id}/restart, got %s", gotPath)
	}
}

// TestSandboxDestroy verifies forge sandbox destroy sends DELETE /v1/sandboxes/{id}.
func TestSandboxDestroy(t *testing.T) {
	var gotMethod, gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	buf := new(bytes.Buffer)
	cmd := newRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"sandbox", "destroy", "11111111-2222-3333-4444-555555555555",
		"--api-url", srv.URL,
		"--api-token", "test-token",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("command failed: %v", err)
	}

	if gotMethod != http.MethodDelete {
		t.Errorf("expected DELETE, got %s", gotMethod)
	}
	if gotPath != "/v1/sandboxes/11111111-2222-3333-4444-555555555555" {
		t.Errorf("expected /v1/sandboxes/{id}, got %s", gotPath)
	}
}
