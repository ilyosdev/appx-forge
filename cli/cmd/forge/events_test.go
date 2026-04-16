package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestEventsList verifies forge events sends GET /v1/events with default limit
// and prints a table with TIME, TYPE, SANDBOX_ID, TRANSITION columns.
func TestEventsList(t *testing.T) {
	var gotMethod, gotPath, gotQuery string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		json.NewEncoder(w).Encode(map[string]any{
			"events": []map[string]any{
				{
					"id":         1,
					"sandbox_id": "11111111-2222-3333-4444-555555555555",
					"event_type": "sandbox_created",
					"prev_state": "",
					"next_state": "pending",
					"created_at": time.Now().Add(-2 * time.Minute).Format("2006-01-02T15:04:05Z"),
				},
				{
					"id":         2,
					"sandbox_id": "11111111-2222-3333-4444-555555555555",
					"event_type": "sandbox_started",
					"prev_state": "pending",
					"next_state": "running",
					"created_at": time.Now().Add(-1 * time.Minute).Format("2006-01-02T15:04:05Z"),
				},
			},
		})
	}))
	defer srv.Close()

	buf := new(bytes.Buffer)
	cmd := newRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"events", "--api-url", srv.URL, "--api-token", "test-token"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("command failed: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("expected GET, got %s", gotMethod)
	}
	if gotPath != "/v1/events" {
		t.Errorf("expected /v1/events, got %s", gotPath)
	}
	if !strings.Contains(gotQuery, "limit=100") {
		t.Errorf("expected default limit=100 in query, got %q", gotQuery)
	}

	output := buf.String()
	for _, col := range []string{"TIME", "TYPE", "SANDBOX_ID", "TRANSITION"} {
		if !strings.Contains(output, col) {
			t.Errorf("output missing column %q:\n%s", col, output)
		}
	}
	if !strings.Contains(output, "sandbox_created") {
		t.Errorf("output missing event_type 'sandbox_created':\n%s", output)
	}
}

// TestEventsWithSandboxFilter verifies --sandbox flag sends sandbox_id query param.
func TestEventsWithSandboxFilter(t *testing.T) {
	var gotQuery string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		json.NewEncoder(w).Encode(map[string]any{"events": []any{}})
	}))
	defer srv.Close()

	buf := new(bytes.Buffer)
	cmd := newRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"events",
		"--sandbox", "11111111-2222-3333-4444-555555555555",
		"--api-url", srv.URL,
		"--api-token", "test-token",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("command failed: %v", err)
	}

	if !strings.Contains(gotQuery, "sandbox_id=11111111-2222-3333-4444-555555555555") {
		t.Errorf("expected sandbox_id in query, got %q", gotQuery)
	}
}

// TestEventsWithTypeFilter verifies --type flag sends event_type query param.
func TestEventsWithTypeFilter(t *testing.T) {
	var gotQuery string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		json.NewEncoder(w).Encode(map[string]any{"events": []any{}})
	}))
	defer srv.Close()

	buf := new(bytes.Buffer)
	cmd := newRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"events",
		"--type", "sandbox_created",
		"--api-url", srv.URL,
		"--api-token", "test-token",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("command failed: %v", err)
	}

	if !strings.Contains(gotQuery, "event_type=sandbox_created") {
		t.Errorf("expected event_type in query, got %q", gotQuery)
	}
}

// TestEventsWithSinceFilter verifies --since filters events client-side by timestamp.
func TestEventsWithSinceFilter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"events": []map[string]any{
				{
					"id":         1,
					"event_type": "old_event",
					"prev_state": "",
					"next_state": "pending",
					"created_at": time.Now().Add(-2 * time.Hour).Format("2006-01-02T15:04:05Z"),
				},
				{
					"id":         2,
					"event_type": "recent_event",
					"prev_state": "pending",
					"next_state": "running",
					"created_at": time.Now().Add(-1 * time.Minute).Format("2006-01-02T15:04:05Z"),
				},
			},
		})
	}))
	defer srv.Close()

	buf := new(bytes.Buffer)
	cmd := newRootCmd()
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"events",
		"--since", "5m",
		"--api-url", srv.URL,
		"--api-token", "test-token",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("command failed: %v", err)
	}

	output := buf.String()
	// recent_event (1m ago) should be included
	if !strings.Contains(output, "recent_event") {
		t.Errorf("output should contain recent_event:\n%s", output)
	}
	// old_event (2h ago) should be filtered out by --since=5m
	if strings.Contains(output, "old_event") {
		t.Errorf("output should NOT contain old_event (older than 5m):\n%s", output)
	}
}
