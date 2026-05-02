package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/appx/forge/control/internal/lifecycle"
	"github.com/appx/forge/control/internal/scheduler"
	"github.com/appx/forge/control/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
)

// ── Mock Lifecycle Service ──────────────────────────────────────────

type mockLifecycle struct {
	createFn  func(ctx context.Context, req lifecycle.CreateRequest) (*lifecycle.SandboxResult, error)
	destroyFn func(ctx context.Context, id uuid.UUID) error
	restartFn func(ctx context.Context, id uuid.UUID) error
}

func (m *mockLifecycle) CreateSandbox(ctx context.Context, req lifecycle.CreateRequest) (*lifecycle.SandboxResult, error) {
	if m.createFn != nil {
		return m.createFn(ctx, req)
	}
	return &lifecycle.SandboxResult{}, nil
}

func (m *mockLifecycle) DestroySandbox(ctx context.Context, id uuid.UUID) error {
	if m.destroyFn != nil {
		return m.destroyFn(ctx, id)
	}
	return nil
}

func (m *mockLifecycle) RestartSandbox(ctx context.Context, id uuid.UUID) error {
	if m.restartFn != nil {
		return m.restartFn(ctx, id)
	}
	return nil
}

// WakeSandbox satisfies the SandboxLifecycle interface (added in b90f6a6).
// Stub returns nil — no Phase 30 test exercises wake semantics.
func (m *mockLifecycle) WakeSandbox(ctx context.Context, id uuid.UUID) error {
	return nil
}

// ── Mock Sandbox Reader ─────────────────────────────────────────────

type mockSandboxReader struct {
	getSandboxFn          func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error)
	getSandboxByAppNameFn func(ctx context.Context, appName string) (store.Sandbox, error)
	listSandboxesFn       func(ctx context.Context, limit int32) ([]store.Sandbox, error)
	listByStateFn         func(ctx context.Context, state string) ([]store.Sandbox, error)
	listByNodeFn          func(ctx context.Context, nodeID pgtype.UUID) ([]store.Sandbox, error)
	listByUserFn          func(ctx context.Context, userID string) ([]store.Sandbox, error)
}

func (m *mockSandboxReader) GetSandbox(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
	if m.getSandboxFn != nil {
		return m.getSandboxFn(ctx, id)
	}
	return store.Sandbox{}, errors.New("not found")
}

func (m *mockSandboxReader) GetSandboxByAppName(ctx context.Context, appName string) (store.Sandbox, error) {
	if m.getSandboxByAppNameFn != nil {
		return m.getSandboxByAppNameFn(ctx, appName)
	}
	return store.Sandbox{}, errors.New("not found")
}

func (m *mockSandboxReader) ListSandboxes(ctx context.Context, limit int32) ([]store.Sandbox, error) {
	if m.listSandboxesFn != nil {
		return m.listSandboxesFn(ctx, limit)
	}
	return []store.Sandbox{}, nil
}

func (m *mockSandboxReader) ListSandboxesByState(ctx context.Context, state string) ([]store.Sandbox, error) {
	if m.listByStateFn != nil {
		return m.listByStateFn(ctx, state)
	}
	return []store.Sandbox{}, nil
}

func (m *mockSandboxReader) ListSandboxesByNode(ctx context.Context, nodeID pgtype.UUID) ([]store.Sandbox, error) {
	if m.listByNodeFn != nil {
		return m.listByNodeFn(ctx, nodeID)
	}
	return []store.Sandbox{}, nil
}

func (m *mockSandboxReader) ListSandboxesByUser(ctx context.Context, userID string) ([]store.Sandbox, error) {
	if m.listByUserFn != nil {
		return m.listByUserFn(ctx, userID)
	}
	return []store.Sandbox{}, nil
}

// ── Test Helpers ─────────────────────────────────────────────────────

func newSandboxTestServer(lc SandboxLifecycle, reader SandboxReader) *Server {
	r := chi.NewRouter()
	s := &Server{
		router:        r,
		config:        &serverConfig{apiToken: "test-token"},
		logger:        testLogger(),
		lifecycle:     lc,
		sandboxReader: reader,
	}

	r.Group(func(r chi.Router) {
		r.Use(BearerAuth("test-token"))
		r.Route("/v1", func(r chi.Router) {
			r.Post("/sandboxes", s.handleCreateSandbox)
			r.Get("/sandboxes", s.handleListSandboxes)
			r.Get("/sandboxes/{id}", s.handleGetSandbox)
			r.Delete("/sandboxes/{id}", s.handleDestroySandbox)
			r.Post("/sandboxes/{id}/restart", s.handleRestartSandbox)
		})
	})

	return s
}

func makeSandbox(id uuid.UUID, appName, state string) store.Sandbox {
	return store.Sandbox{
		ID:        pgtype.UUID{Bytes: id, Valid: true},
		AppName:   appName,
		UserID:    "user-123",
		Image:     "appx/sandbox:v1",
		State:     state,
		Resources: []byte(`{"cpu_cores":0.5,"memory_mb":512}`),
	}
}

// ── POST /v1/sandboxes Tests ────────────────────────────────────────

func TestSandboxCreate_ValidRequest(t *testing.T) {
	sandboxID := uuid.New()
	lc := &mockLifecycle{
		createFn: func(ctx context.Context, req lifecycle.CreateRequest) (*lifecycle.SandboxResult, error) {
			if req.AppName != "my-cool-app" {
				t.Fatalf("expected app_name 'my-cool-app', got %q", req.AppName)
			}
			if req.UserID != "user-123" {
				t.Fatalf("expected user_id 'user-123', got %q", req.UserID)
			}
			return &lifecycle.SandboxResult{
				ID:      sandboxID,
				AppName: "my-cool-app",
				UserID:  "user-123",
				State:   "starting",
				URL:     "https://my-cool-app.myappx.live",
				Image:   "appx/sandbox:v1",
			}, nil
		},
	}

	srv := newSandboxTestServer(lc, &mockSandboxReader{})
	body := `{"app_name":"my-cool-app","user_id":"user-123","image":"appx/sandbox:v1","resources":{"cpu_cores":0.5,"memory_mb":512}}`

	req := httptest.NewRequest(http.MethodPost, "/v1/sandboxes", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp SandboxResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.AppName != "my-cool-app" {
		t.Fatalf("expected app_name 'my-cool-app', got %q", resp.AppName)
	}
	if resp.URL != "https://my-cool-app.myappx.live" {
		t.Fatalf("expected url, got %q", resp.URL)
	}
	if resp.State != "starting" {
		t.Fatalf("expected state 'starting', got %q", resp.State)
	}
}

func TestSandboxCreate_DuplicateAppName(t *testing.T) {
	lc := &mockLifecycle{
		createFn: func(ctx context.Context, req lifecycle.CreateRequest) (*lifecycle.SandboxResult, error) {
			return nil, lifecycle.ErrConflict
		},
	}

	srv := newSandboxTestServer(lc, &mockSandboxReader{})
	body := `{"app_name":"existing-app","user_id":"user-123","image":"appx/sandbox:v1"}`

	req := httptest.NewRequest(http.MethodPost, "/v1/sandboxes", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSandboxCreate_InvalidBody(t *testing.T) {
	lc := &mockLifecycle{}

	srv := newSandboxTestServer(lc, &mockSandboxReader{})

	// Missing required app_name
	body := `{"user_id":"user-123","image":"appx/sandbox:v1"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/sandboxes", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSandboxCreate_InvalidAppName(t *testing.T) {
	lc := &mockLifecycle{}
	srv := newSandboxTestServer(lc, &mockSandboxReader{})

	// Invalid app_name (uppercase)
	body := `{"app_name":"My-App","user_id":"user-123","image":"appx/sandbox:v1"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/sandboxes", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid app_name, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSandboxCreate_NoCapacity(t *testing.T) {
	lc := &mockLifecycle{
		createFn: func(ctx context.Context, req lifecycle.CreateRequest) (*lifecycle.SandboxResult, error) {
			return nil, lifecycle.ErrNoCapacity
		},
	}

	srv := newSandboxTestServer(lc, &mockSandboxReader{})
	body := `{"app_name":"my-app","user_id":"user-123","image":"appx/sandbox:v1"}`

	req := httptest.NewRequest(http.MethodPost, "/v1/sandboxes", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

// ── GET /v1/sandboxes/{id} Tests ────────────────────────────────────

func TestSandboxGet_ByUUID(t *testing.T) {
	sandboxID := uuid.New()
	reader := &mockSandboxReader{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return makeSandbox(sandboxID, "test-app", "running"), nil
		},
	}

	srv := newSandboxTestServer(&mockLifecycle{}, reader)
	req := httptest.NewRequest(http.MethodGet, "/v1/sandboxes/"+sandboxID.String(), nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp SandboxResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.AppName != "test-app" {
		t.Fatalf("expected app_name 'test-app', got %q", resp.AppName)
	}
}

func TestSandboxGet_ByAppName(t *testing.T) {
	sandboxID := uuid.New()
	reader := &mockSandboxReader{
		getSandboxByAppNameFn: func(ctx context.Context, appName string) (store.Sandbox, error) {
			if appName != "my-cool-app" {
				t.Fatalf("expected app_name 'my-cool-app', got %q", appName)
			}
			return makeSandbox(sandboxID, "my-cool-app", "running"), nil
		},
	}

	srv := newSandboxTestServer(&mockLifecycle{}, reader)
	req := httptest.NewRequest(http.MethodGet, "/v1/sandboxes/app:my-cool-app", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp SandboxResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.AppName != "my-cool-app" {
		t.Fatalf("expected app_name 'my-cool-app', got %q", resp.AppName)
	}
}

func TestSandboxGet_NotFound(t *testing.T) {
	reader := &mockSandboxReader{} // default returns error
	srv := newSandboxTestServer(&mockLifecycle{}, reader)

	req := httptest.NewRequest(http.MethodGet, "/v1/sandboxes/"+uuid.New().String(), nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// ── GET /v1/sandboxes Tests ─────────────────────────────────────────

func TestSandboxList_WithLimit(t *testing.T) {
	sandboxID := uuid.New()
	reader := &mockSandboxReader{
		listSandboxesFn: func(ctx context.Context, limit int32) ([]store.Sandbox, error) {
			if limit != 50 {
				t.Fatalf("expected default limit 50, got %d", limit)
			}
			return []store.Sandbox{makeSandbox(sandboxID, "app-1", "running")}, nil
		},
	}

	srv := newSandboxTestServer(&mockLifecycle{}, reader)
	req := httptest.NewRequest(http.MethodGet, "/v1/sandboxes", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp sandboxListResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Sandboxes) != 1 {
		t.Fatalf("expected 1 sandbox, got %d", len(resp.Sandboxes))
	}
}

// ── DELETE /v1/sandboxes/{id} Tests ─────────────────────────────────

func TestSandboxDestroy_Success(t *testing.T) {
	sandboxID := uuid.New()
	lc := &mockLifecycle{
		destroyFn: func(ctx context.Context, id uuid.UUID) error {
			if id != sandboxID {
				t.Fatalf("expected sandbox ID %s, got %s", sandboxID, id)
			}
			return nil
		},
	}

	srv := newSandboxTestServer(lc, &mockSandboxReader{})
	req := httptest.NewRequest(http.MethodDelete, "/v1/sandboxes/"+sandboxID.String(), nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSandboxDestroy_NotFound(t *testing.T) {
	lc := &mockLifecycle{
		destroyFn: func(ctx context.Context, id uuid.UUID) error {
			return lifecycle.ErrNotFound
		},
	}

	srv := newSandboxTestServer(lc, &mockSandboxReader{})
	req := httptest.NewRequest(http.MethodDelete, "/v1/sandboxes/"+uuid.New().String(), nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// ── POST /v1/sandboxes/{id}/restart Tests ───────────────────────────

// ── Phase 30: verified_at + freshness wiring ─────────────────────────

// fakeFreshness lets tests stub api.Freshness without spinning up a real
// SandboxFreshnessService. The fields capture the most recent call so
// assertions can confirm the handler forwarded the right name + force flag.
type fakeFreshness struct {
	row          *scheduler.SandboxRow
	verifiedAt   time.Time
	err          error
	calledName   string
	calledForce  bool
	calledCount  int
}

func (f *fakeFreshness) GetSandbox(ctx context.Context, name string, forceRefresh bool) (*scheduler.SandboxRow, time.Time, error) {
	f.calledName = name
	f.calledForce = forceRefresh
	f.calledCount++
	return f.row, f.verifiedAt, f.err
}

func makeSandboxWithVerified(id uuid.UUID, appName, state string, verifiedAt time.Time) store.Sandbox {
	sb := makeSandbox(id, appName, state)
	if !verifiedAt.IsZero() {
		sb.VerifiedAt = pgtype.Timestamptz{Time: verifiedAt, Valid: true}
	}
	return sb
}

func TestSandboxGet_IncludesVerifiedAtFromStore(t *testing.T) {
	sandboxID := uuid.New()
	stored := time.Now().Add(-30 * time.Second).UTC().Truncate(time.Second)
	reader := &mockSandboxReader{
		getSandboxByAppNameFn: func(ctx context.Context, appName string) (store.Sandbox, error) {
			return makeSandboxWithVerified(sandboxID, appName, "running", stored), nil
		},
	}

	srv := newSandboxTestServer(&mockLifecycle{}, reader)
	req := httptest.NewRequest(http.MethodGet, "/v1/sandboxes/app:pool-X", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp SandboxResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.VerifiedAt == "" {
		t.Errorf("expected verified_at populated from stored row, got empty")
	}
}

func TestSandboxGet_FreshnessRefreshUpdatesVerifiedAt(t *testing.T) {
	sandboxID := uuid.New()
	stored := time.Now().Add(-30 * time.Second)
	refreshed := time.Now().UTC().Truncate(time.Second)

	reader := &mockSandboxReader{
		getSandboxByAppNameFn: func(ctx context.Context, appName string) (store.Sandbox, error) {
			return makeSandboxWithVerified(sandboxID, appName, "running", stored), nil
		},
	}
	freshness := &fakeFreshness{
		row:        &scheduler.SandboxRow{AppName: "pool-X", State: "running"},
		verifiedAt: refreshed,
	}

	srv := newSandboxTestServer(&mockLifecycle{}, reader)
	srv.SetFreshness(freshness)

	req := httptest.NewRequest(http.MethodGet, "/v1/sandboxes/app:pool-X?force_refresh=true", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if freshness.calledCount != 1 {
		t.Errorf("freshness should have been called once, got %d", freshness.calledCount)
	}
	if freshness.calledName != "pool-X" {
		t.Errorf("freshness called with wrong name: %q", freshness.calledName)
	}
	if !freshness.calledForce {
		t.Errorf("force_refresh=true query should have propagated")
	}
	var resp SandboxResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.VerifiedAt != refreshed.Format(time.RFC3339) {
		t.Errorf("expected verified_at=%q, got %q", refreshed.Format(time.RFC3339), resp.VerifiedAt)
	}
}

func TestSandboxGet_FreshnessReturns404OnAgentMiss(t *testing.T) {
	sandboxID := uuid.New()
	reader := &mockSandboxReader{
		getSandboxByAppNameFn: func(ctx context.Context, appName string) (store.Sandbox, error) {
			return makeSandboxWithVerified(sandboxID, appName, "running",
				time.Now().Add(-30*time.Second)), nil
		},
	}
	freshness := &fakeFreshness{err: scheduler.ErrSandboxNotFound}

	srv := newSandboxTestServer(&mockLifecycle{}, reader)
	srv.SetFreshness(freshness)

	req := httptest.NewRequest(http.MethodGet, "/v1/sandboxes/app:pool-X", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
	if freshness.calledCount != 1 {
		t.Errorf("freshness should have been called once")
	}
}

func TestSandboxGet_FreshnessUnreachable_ReturnsCachedRow(t *testing.T) {
	sandboxID := uuid.New()
	stored := time.Now().Add(-30 * time.Second).UTC().Truncate(time.Second)
	reader := &mockSandboxReader{
		getSandboxByAppNameFn: func(ctx context.Context, appName string) (store.Sandbox, error) {
			return makeSandboxWithVerified(sandboxID, appName, "running", stored), nil
		},
	}
	freshness := &fakeFreshness{err: errors.New("connection refused")}

	srv := newSandboxTestServer(&mockLifecycle{}, reader)
	srv.SetFreshness(freshness)

	req := httptest.NewRequest(http.MethodGet, "/v1/sandboxes/app:pool-X", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (degrade to cached on agent error), got %d: %s", w.Code, w.Body.String())
	}
	var resp SandboxResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// VerifiedAt should still reflect the stored row (handler did not overwrite on err).
	if resp.VerifiedAt == "" {
		t.Errorf("expected stored verified_at to remain populated, got empty")
	}
}

func TestSandboxList_FleetVerifiedAtIsOldest(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	old := now.Add(-30 * time.Second)
	mid := now.Add(-10 * time.Second)
	new := now.Add(-5 * time.Second)

	reader := &mockSandboxReader{
		listSandboxesFn: func(ctx context.Context, limit int32) ([]store.Sandbox, error) {
			return []store.Sandbox{
				makeSandboxWithVerified(uuid.New(), "app-1", "running", new),
				makeSandboxWithVerified(uuid.New(), "app-2", "running", old),
				makeSandboxWithVerified(uuid.New(), "app-3", "running", mid),
			}, nil
		},
	}

	srv := newSandboxTestServer(&mockLifecycle{}, reader)
	req := httptest.NewRequest(http.MethodGet, "/v1/sandboxes", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp sandboxListResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.FleetVerifiedAt != old.Format(time.RFC3339) {
		t.Errorf("expected fleet_verified_at=%q (oldest), got %q",
			old.Format(time.RFC3339), resp.FleetVerifiedAt)
	}
}

func TestSandboxRestart_Success(t *testing.T) {
	sandboxID := uuid.New()
	lc := &mockLifecycle{
		restartFn: func(ctx context.Context, id uuid.UUID) error {
			if id != sandboxID {
				t.Fatalf("expected sandbox ID %s, got %s", sandboxID, id)
			}
			return nil
		},
	}

	srv := newSandboxTestServer(lc, &mockSandboxReader{})
	req := httptest.NewRequest(http.MethodPost, "/v1/sandboxes/"+sandboxID.String()+"/restart", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
}
