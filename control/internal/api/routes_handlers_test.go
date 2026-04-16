package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/appx/forge/control/internal/routing"
)

// --- Mock RouteListFetcher ---

type mockRouteListFetcher struct {
	listRoutesFn func(ctx context.Context) ([]routing.Route, error)
}

func (m *mockRouteListFetcher) ListRoutes(ctx context.Context) ([]routing.Route, error) {
	if m.listRoutesFn != nil {
		return m.listRoutesFn(ctx)
	}
	return []routing.Route{}, nil
}

// --- Tests ---

func TestListRoutes_ReturnsRoutes(t *testing.T) {
	fetcher := &mockRouteListFetcher{
		listRoutesFn: func(ctx context.Context) ([]routing.Route, error) {
			return []routing.Route{
				{AppName: "my-app", SandboxID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", Upstream: "100.64.1.5:43210"},
				{AppName: "demo-app", SandboxID: "11111111-2222-3333-4444-555555555555", Upstream: "100.64.1.6:43211"},
			}, nil
		},
	}

	srv := newTestServerWithRouteFetcher(fetcher)
	req := httptest.NewRequest(http.MethodGet, "/v1/routes", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Routes []map[string]interface{} `json:"routes"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(resp.Routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(resp.Routes))
	}

	// Verify fields
	r := resp.Routes[0]
	for _, field := range []string{"app_name", "sandbox_id", "upstream"} {
		if _, ok := r[field]; !ok {
			t.Errorf("missing expected field %q in route response", field)
		}
	}

	if r["app_name"] != "my-app" {
		t.Errorf("expected app_name 'my-app', got %v", r["app_name"])
	}
}

func TestListRoutes_EmptyList(t *testing.T) {
	fetcher := &mockRouteListFetcher{
		listRoutesFn: func(ctx context.Context) ([]routing.Route, error) {
			return []routing.Route{}, nil
		},
	}

	srv := newTestServerWithRouteFetcher(fetcher)
	req := httptest.NewRequest(http.MethodGet, "/v1/routes", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Routes []map[string]interface{} `json:"routes"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(resp.Routes) != 0 {
		t.Fatalf("expected 0 routes, got %d", len(resp.Routes))
	}
}

func TestListRoutes_FetcherError(t *testing.T) {
	fetcher := &mockRouteListFetcher{
		listRoutesFn: func(ctx context.Context) ([]routing.Route, error) {
			return nil, errors.New("caddy unreachable")
		},
	}

	srv := newTestServerWithRouteFetcher(fetcher)
	req := httptest.NewRequest(http.MethodGet, "/v1/routes", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}
}

// --- Test helper ---

func newTestServerWithRouteFetcher(rf RouteListFetcher) *Server {
	r := chi.NewRouter()
	s := &Server{
		router:       r,
		config:       &serverConfig{apiToken: "test-token"},
		logger:       testLogger(),
		routeFetcher: rf,
	}

	r.Group(func(r chi.Router) {
		r.Use(BearerAuth("test-token"))
		r.Get("/v1/routes", s.handleListRoutes)
	})

	return s
}
