package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/appx/forge/control/internal/store"
)

// ── Mock MetricsStore ──────────────────────────────────────────────────

type mockMetricsStore struct {
	counts []store.CountSandboxesByStateRow
	nodes  []store.Node
	err    error
}

func (m *mockMetricsStore) CountSandboxesByState(ctx context.Context) ([]store.CountSandboxesByStateRow, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.counts, nil
}

func (m *mockMetricsStore) ListNodes(ctx context.Context) ([]store.Node, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.nodes, nil
}

// ── Tests ──────────────────────────────────────────────────────────────

func TestMetrics_Returns200WithSandboxCount(t *testing.T) {
	ms := &mockMetricsStore{
		counts: []store.CountSandboxesByStateRow{
			{State: "running", Count: 3},
			{State: "pending", Count: 1},
		},
		nodes: []store.Node{
			{Hostname: "node-1", CapacityMb: 8192, UsedMb: 4096},
		},
	}

	srv := NewServer(nil, &mockPinger{err: nil}, nil, nil, nil, nil, 0)
	srv.SetMetricsStore(ms)

	req := httptest.NewRequest(http.MethodGet, "/v1/metrics", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain prefix", ct)
	}

	body := rec.Body.String()
	if !strings.Contains(body, `forge_sandbox_count{state="running"} 3`) {
		t.Errorf("body missing forge_sandbox_count for running state:\n%s", body)
	}
	if !strings.Contains(body, `forge_sandbox_count{state="pending"} 1`) {
		t.Errorf("body missing forge_sandbox_count for pending state:\n%s", body)
	}
}

func TestMetrics_ReturnsNodeUtilization(t *testing.T) {
	ms := &mockMetricsStore{
		counts: []store.CountSandboxesByStateRow{},
		nodes: []store.Node{
			{Hostname: "node-1", CapacityMb: 8192, UsedMb: 6144},
		},
	}

	srv := NewServer(nil, &mockPinger{err: nil}, nil, nil, nil, nil, 0)
	srv.SetMetricsStore(ms)

	req := httptest.NewRequest(http.MethodGet, "/v1/metrics", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()
	// 6144 / 8192 = 0.75
	if !strings.Contains(body, `forge_node_utilization_ratio{node="node-1"} 0.75`) {
		t.Errorf("body missing forge_node_utilization_ratio:\n%s", body)
	}
}

func TestMetrics_UnauthenticatedAccess(t *testing.T) {
	// /v1/metrics should be accessible without Bearer token, like /healthz
	ms := &mockMetricsStore{
		counts: []store.CountSandboxesByStateRow{},
		nodes:  []store.Node{},
	}

	cfg := &serverConfig{apiToken: "secret-token"}
	srv := NewServer(cfg, &mockPinger{err: nil}, nil, nil, nil, nil, 0)
	srv.SetMetricsStore(ms)

	req := httptest.NewRequest(http.MethodGet, "/v1/metrics", nil)
	// No Authorization header
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code == http.StatusUnauthorized {
		t.Error("/v1/metrics should not require auth, got 401")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestMetrics_StoreError(t *testing.T) {
	ms := &mockMetricsStore{
		err: errors.New("database unreachable"),
	}

	srv := NewServer(nil, &mockPinger{err: nil}, nil, nil, nil, nil, 0)
	srv.SetMetricsStore(ms)

	req := httptest.NewRequest(http.MethodGet, "/v1/metrics", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d on store error", rec.Code, http.StatusInternalServerError)
	}
}

func TestMetrics_NotConfigured(t *testing.T) {
	// No MetricsStore set -- should return 503
	srv := NewServer(nil, &mockPinger{err: nil}, nil, nil, nil, nil, 0)

	req := httptest.NewRequest(http.MethodGet, "/v1/metrics", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d when metrics not configured", rec.Code, http.StatusServiceUnavailable)
	}
}
