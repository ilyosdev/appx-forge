package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRoutesList verifies forge routes list sends GET /v1/routes
// and prints a table with APP_NAME, SANDBOX_ID, UPSTREAM columns.
func TestRoutesList(t *testing.T) {
	var gotMethod, gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		json.NewEncoder(w).Encode(map[string]any{
			"routes": []map[string]any{
				{
					"app_name":   "cool-app",
					"sandbox_id": "11111111-2222-3333-4444-555555555555",
					"upstream":   "100.64.1.5:43210",
				},
				{
					"app_name":   "other-app",
					"sandbox_id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
					"upstream":   "100.64.1.6:43211",
				},
			},
		})
	}))
	defer srv.Close()

	buf := new(bytes.Buffer)
	cmd := newRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"routes", "list", "--api-url", srv.URL, "--api-token", "test-token"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("command failed: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("expected GET, got %s", gotMethod)
	}
	if gotPath != "/v1/routes" {
		t.Errorf("expected /v1/routes, got %s", gotPath)
	}

	output := buf.String()
	for _, col := range []string{"APP_NAME", "SANDBOX_ID", "UPSTREAM"} {
		if !strings.Contains(output, col) {
			t.Errorf("output missing column %q:\n%s", col, output)
		}
	}
	if !strings.Contains(output, "cool-app") {
		t.Errorf("output missing 'cool-app':\n%s", output)
	}
	if !strings.Contains(output, "100.64.1.5:43210") {
		t.Errorf("output missing upstream:\n%s", output)
	}
}

// TestRoutesVerifyClean verifies that routes verify exits 0 when routes match running sandboxes.
func TestRoutesVerifyClean(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/routes":
			json.NewEncoder(w).Encode(map[string]any{
				"routes": []map[string]any{
					{
						"app_name":   "cool-app",
						"sandbox_id": "11111111-2222-3333-4444-555555555555",
						"upstream":   "100.64.1.5:43210",
					},
				},
			})
		case "/v1/sandboxes":
			json.NewEncoder(w).Encode(map[string]any{
				"sandboxes": []map[string]any{
					{
						"id":       "11111111-2222-3333-4444-555555555555",
						"app_name": "cool-app",
						"state":    "running",
					},
				},
			})
		}
	}))
	defer srv.Close()

	buf := new(bytes.Buffer)
	cmd := newRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"routes", "verify", "--api-url", srv.URL, "--api-token", "test-token"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("command should succeed when clean, got: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "clean") && !strings.Contains(output, "OK") && !strings.Contains(output, "No drift") {
		t.Errorf("expected clean/OK message, got:\n%s", output)
	}
}

// TestRoutesVerifyDrift verifies that routes verify exits with error when drift is detected.
func TestRoutesVerifyDrift(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/routes":
			// Route exists for orphan-app, but no running sandbox for it
			json.NewEncoder(w).Encode(map[string]any{
				"routes": []map[string]any{
					{
						"app_name":   "orphan-app",
						"sandbox_id": "11111111-0000-0000-0000-000000000000",
						"upstream":   "100.64.1.5:43210",
					},
				},
			})
		case "/v1/sandboxes":
			// Running sandbox exists for missing-app, but no route for it
			json.NewEncoder(w).Encode(map[string]any{
				"sandboxes": []map[string]any{
					{
						"id":       "22222222-0000-0000-0000-000000000000",
						"app_name": "missing-app",
						"state":    "running",
					},
				},
			})
		}
	}))
	defer srv.Close()

	buf := new(bytes.Buffer)
	cmd := newRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"routes", "verify", "--api-url", srv.URL, "--api-token", "test-token"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for drift, but got nil")
	}

	output := buf.String()
	if !strings.Contains(output, "orphan") {
		t.Errorf("expected orphan route mention in output:\n%s", output)
	}
	if !strings.Contains(output, "missing") {
		t.Errorf("expected missing route mention in output:\n%s", output)
	}
}
