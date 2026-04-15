package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestServer_HealthzPublic(t *testing.T) {
	// /v1/healthz should be accessible without any auth header
	srv := NewServer(nil, &mockPinger{err: nil}, nil, nil, nil, nil, 0)

	req := httptest.NewRequest(http.MethodGet, "/v1/healthz", nil)
	// No Authorization header
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("healthz without auth: status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestServer_AuthenticatedRouteRejectsNoToken(t *testing.T) {
	// An authenticated route (e.g. /v1/sandboxes) should return 401 without token
	cfg := &serverConfig{apiToken: "test-secret"}
	srv := NewServer(cfg, &mockPinger{err: nil}, nil, nil, nil, nil, 0)

	req := httptest.NewRequest(http.MethodGet, "/v1/sandboxes", nil)
	// No Authorization header
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("sandboxes without auth: status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestServer_AuthenticatedRouteAcceptsValidToken(t *testing.T) {
	// An authenticated route with valid token should get through to handler
	// Since no handler is registered for GET /v1/sandboxes, we expect 404 or 405
	cfg := &serverConfig{apiToken: "test-secret"}
	srv := NewServer(cfg, &mockPinger{err: nil}, nil, nil, nil, nil, 0)

	req := httptest.NewRequest(http.MethodGet, "/v1/sandboxes", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	// With valid auth, should get 404 (route exists in group but no handler registered)
	// or 405 -- NOT 401
	if rec.Code == http.StatusUnauthorized {
		t.Error("sandboxes with valid auth should not return 401")
	}
}

func TestServer_NotFoundRoute(t *testing.T) {
	srv := NewServer(nil, &mockPinger{err: nil}, nil, nil, nil, nil, 0)

	req := httptest.NewRequest(http.MethodGet, "/nonexistent", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("nonexistent route: status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
