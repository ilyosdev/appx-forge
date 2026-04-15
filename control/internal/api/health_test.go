package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type mockPinger struct {
	err error
}

func (m *mockPinger) Ping(ctx context.Context) error {
	return m.err
}

func TestHandleHealthz_Healthy(t *testing.T) {
	srv := NewServer(nil, &mockPinger{err: nil}, nil, nil, 0)

	req := httptest.NewRequest(http.MethodGet, "/v1/healthz", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/json")
	}

	var body map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if body["status"] != "ok" {
		t.Errorf("status = %v, want %q", body["status"], "ok")
	}
	if body["postgres"] != "ok" {
		t.Errorf("postgres = %v, want %q", body["postgres"], "ok")
	}
	if _, ok := body["uptime_seconds"]; !ok {
		t.Error("missing uptime_seconds field")
	}
}

func TestHandleHealthz_Unhealthy(t *testing.T) {
	srv := NewServer(nil, &mockPinger{err: errors.New("connection refused")}, nil, nil, 0)

	req := httptest.NewRequest(http.MethodGet, "/v1/healthz", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}

	var body map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if body["status"] != "error" {
		t.Errorf("status = %v, want %q", body["status"], "error")
	}
	if body["postgres"] != "unreachable" {
		t.Errorf("postgres = %v, want %q", body["postgres"], "unreachable")
	}
}
