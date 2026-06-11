package routing

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ── Test Helpers ────────────────────────────────────────────────────

// caddyRouteJSON is the JSON shape returned by Caddy's Admin API for a single route.
type caddyRouteJSON struct {
	ID     string        `json:"@id"`
	Match  []matchJSON   `json:"match"`
	Handle []handlerJSON `json:"handle"`
}

type matchJSON struct {
	Host []string `json:"host"`
}

type handlerJSON struct {
	Handler   string         `json:"handler"`
	Upstreams []upstreamJSON `json:"upstreams"`
	Transport *transportJSON `json:"transport,omitempty"`
	Headers   *headersJSON   `json:"headers,omitempty"`
}

type upstreamJSON struct {
	Dial string `json:"dial"`
}

type transportJSON struct {
	Protocol              string `json:"protocol"`
	DialTimeout           string `json:"dial_timeout"`
	ResponseHeaderTimeout string `json:"response_header_timeout"`
	ReadTimeout           string `json:"read_timeout"`
	WriteTimeout          string `json:"write_timeout"`
}

type headersJSON struct {
	Request *requestHeadersJSON `json:"request,omitempty"`
}

type requestHeadersJSON struct {
	Set map[string][]string `json:"set"`
}

// ── AddRoute Tests ──────────────────────────────────────────────────

func TestCaddyAddRoute_SendsCorrectJSON(t *testing.T) {
	var gotBody caddyRouteJSON
	var gotMethod, gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Fatalf("unmarshal body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewCaddyClient(srv.URL)
	err := client.AddRoute(context.Background(), Route{
		AppName:   "my-app",
		SandboxID: "sbx-123",
		Upstream:  "100.64.0.1:8081",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/config/apps/http/servers/srv0/routes" {
		t.Errorf("path = %q, want /config/apps/http/servers/srv0/routes", gotPath)
	}
	if gotBody.ID != "route-my-app" {
		t.Errorf("@id = %q, want route-my-app", gotBody.ID)
	}
	if len(gotBody.Match) != 1 || len(gotBody.Match[0].Host) != 1 || gotBody.Match[0].Host[0] != "my-app.myappx.live" {
		t.Errorf("match host = %v, want [my-app.myappx.live]", gotBody.Match)
	}
	if len(gotBody.Handle) != 1 {
		t.Fatalf("handle count = %d, want 1", len(gotBody.Handle))
	}
	h := gotBody.Handle[0]
	if h.Handler != "reverse_proxy" {
		t.Errorf("handler = %q, want reverse_proxy", h.Handler)
	}
	if len(h.Upstreams) != 1 || h.Upstreams[0].Dial != "100.64.0.1:8081" {
		t.Errorf("upstream dial = %v, want 100.64.0.1:8081", h.Upstreams)
	}
	if h.Transport == nil || h.Transport.Protocol != "http" {
		t.Errorf("transport protocol = %v, want http", h.Transport)
	}
	// Explicit generous timeouts (defense-in-depth) so a future Caddy default
	// change / HTTP/2 transport can't silently cap entry.bundle streaming.
	if h.Transport.DialTimeout != "10s" {
		t.Errorf("transport dial_timeout = %q, want 10s", h.Transport.DialTimeout)
	}
	if h.Transport.ResponseHeaderTimeout != "300s" {
		t.Errorf("transport response_header_timeout = %q, want 300s", h.Transport.ResponseHeaderTimeout)
	}
	if h.Transport.ReadTimeout != "300s" {
		t.Errorf("transport read_timeout = %q, want 300s", h.Transport.ReadTimeout)
	}
	if h.Transport.WriteTimeout != "300s" {
		t.Errorf("transport write_timeout = %q, want 300s", h.Transport.WriteTimeout)
	}
	if h.Headers == nil || h.Headers.Request == nil {
		t.Fatal("headers.request is nil")
	}
	if v := h.Headers.Request.Set["X-Forwarded-Host"]; len(v) != 1 || v[0] != "{http.request.host}" {
		t.Errorf("X-Forwarded-Host = %v, want [{http.request.host}]", v)
	}
	if v := h.Headers.Request.Set["X-Sandbox-ID"]; len(v) != 1 || v[0] != "sbx-123" {
		t.Errorf("X-Sandbox-ID = %v, want [sbx-123]", v)
	}
}

func TestCaddyAddRoute_ReturnsNilOn200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewCaddyClient(srv.URL)
	err := client.AddRoute(context.Background(), Route{
		AppName:   "test-app",
		SandboxID: "sbx-456",
		Upstream:  "100.64.0.2:8081",
	})
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
}

func TestCaddyAddRoute_ReturnsErrorOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("caddy internal error"))
	}))
	defer srv.Close()

	client := NewCaddyClient(srv.URL)
	err := client.AddRoute(context.Background(), Route{
		AppName:   "fail-app",
		SandboxID: "sbx-789",
		Upstream:  "100.64.0.3:8081",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// Error should include status code and body
	errMsg := err.Error()
	if !contains(errMsg, "500") {
		t.Errorf("error should contain status code 500, got: %s", errMsg)
	}
	if !contains(errMsg, "caddy internal error") {
		t.Errorf("error should contain response body, got: %s", errMsg)
	}
}

// ── RemoveRoute Tests ───────────────────────────────────────────────

func TestCaddyRemoveRoute_SendsDeleteToCorrectPath(t *testing.T) {
	var gotMethod, gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewCaddyClient(srv.URL)
	err := client.RemoveRoute(context.Background(), "my-app")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", gotMethod)
	}
	if gotPath != "/id/route-my-app" {
		t.Errorf("path = %q, want /id/route-my-app", gotPath)
	}
}

func TestCaddyRemoveRoute_ReturnsNilOn200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewCaddyClient(srv.URL)
	err := client.RemoveRoute(context.Background(), "ok-app")
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
}

func TestCaddyRemoveRoute_ReturnsNilOn404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := NewCaddyClient(srv.URL)
	err := client.RemoveRoute(context.Background(), "gone-app")
	if err != nil {
		t.Fatalf("expected nil error on 404 (idempotent), got: %v", err)
	}
}

func TestCaddyRemoveRoute_ReturnsErrorOn500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("server error"))
	}))
	defer srv.Close()

	client := NewCaddyClient(srv.URL)
	err := client.RemoveRoute(context.Background(), "bad-app")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ── ListRoutes Tests ────────────────────────────────────────────────

func TestCaddyListRoutes_ParsesResponseIntoRoutes(t *testing.T) {
	routes := []caddyRouteJSON{
		{
			ID:    "route-app1",
			Match: []matchJSON{{Host: []string{"app1.myappx.live"}}},
			Handle: []handlerJSON{{
				Handler:   "reverse_proxy",
				Upstreams: []upstreamJSON{{Dial: "100.64.0.1:8081"}},
			}},
		},
		{
			ID:    "route-app2",
			Match: []matchJSON{{Host: []string{"app2.myappx.live"}}},
			Handle: []handlerJSON{{
				Handler:   "reverse_proxy",
				Upstreams: []upstreamJSON{{Dial: "100.64.0.2:9090"}},
			}},
		},
	}
	body, _ := json.Marshal(routes)

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	defer srv.Close()

	client := NewCaddyClient(srv.URL)
	result, err := client.ListRoutes(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/config/apps/http/servers/srv0/routes" {
		t.Errorf("path = %q, want /config/apps/http/servers/srv0/routes", gotPath)
	}
	if len(result) != 2 {
		t.Fatalf("route count = %d, want 2", len(result))
	}
	if result[0].AppName != "app1" {
		t.Errorf("route[0].AppName = %q, want app1", result[0].AppName)
	}
	if result[0].Upstream != "100.64.0.1:8081" {
		t.Errorf("route[0].Upstream = %q, want 100.64.0.1:8081", result[0].Upstream)
	}
	if result[1].AppName != "app2" {
		t.Errorf("route[1].AppName = %q, want app2", result[1].AppName)
	}
	if result[1].Upstream != "100.64.0.2:9090" {
		t.Errorf("route[1].Upstream = %q, want 100.64.0.2:9090", result[1].Upstream)
	}
}

func TestCaddyListRoutes_ReturnsEmptySliceOnEmptyArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
	}))
	defer srv.Close()

	client := NewCaddyClient(srv.URL)
	result, err := client.ListRoutes(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("route count = %d, want 0", len(result))
	}
}

func TestCaddyListRoutes_ReturnsErrorOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("unavailable"))
	}))
	defer srv.Close()

	client := NewCaddyClient(srv.URL)
	_, err := client.ListRoutes(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ── Helpers ─────────────────────────────────────────────────────────

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
