package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/appx/forge/control/internal/store"
)

// --- Mock EventStore ---

type mockEventStore struct {
	listBySandboxFn   func(ctx context.Context, arg store.ListEventsBySandboxParams) ([]store.Event, error)
	listByTypeFn      func(ctx context.Context, arg store.ListEventsByTypeParams) ([]store.Event, error)
	listRecentFn      func(ctx context.Context, limit int32) ([]store.Event, error)
}

func (m *mockEventStore) ListEventsBySandbox(ctx context.Context, arg store.ListEventsBySandboxParams) ([]store.Event, error) {
	if m.listBySandboxFn != nil {
		return m.listBySandboxFn(ctx, arg)
	}
	return []store.Event{}, nil
}

func (m *mockEventStore) ListEventsByType(ctx context.Context, arg store.ListEventsByTypeParams) ([]store.Event, error) {
	if m.listByTypeFn != nil {
		return m.listByTypeFn(ctx, arg)
	}
	return []store.Event{}, nil
}

func (m *mockEventStore) ListRecentEvents(ctx context.Context, limit int32) ([]store.Event, error) {
	if m.listRecentFn != nil {
		return m.listRecentFn(ctx, limit)
	}
	return []store.Event{}, nil
}

// --- Tests ---

func TestListEvents_BySandboxID(t *testing.T) {
	sandboxID := pgtype.UUID{Valid: true}
	copy(sandboxID.Bytes[:], []byte("sandboxuuid00001"))

	now := time.Now().UTC().Truncate(time.Microsecond)

	es := &mockEventStore{
		listBySandboxFn: func(ctx context.Context, arg store.ListEventsBySandboxParams) ([]store.Event, error) {
			if arg.SandboxID != sandboxID {
				t.Fatalf("unexpected sandbox ID filter")
			}
			return []store.Event{
				{
					ID:        1,
					SandboxID: sandboxID,
					EventType: "container_started",
					Actor:     "agent",
					CreatedAt: pgtype.Timestamptz{Time: now, Valid: true},
				},
			}, nil
		},
	}

	srv := newTestServerWithEventStore(es)
	req := httptest.NewRequest(http.MethodGet, "/v1/events?sandbox_id="+formatUUID(sandboxID), nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Events []map[string]interface{} `json:"events"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(resp.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(resp.Events))
	}

	if resp.Events[0]["event_type"] != "container_started" {
		t.Errorf("expected event_type 'container_started', got %v", resp.Events[0]["event_type"])
	}
}

func TestListEvents_ByEventType(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)

	es := &mockEventStore{
		listByTypeFn: func(ctx context.Context, arg store.ListEventsByTypeParams) ([]store.Event, error) {
			if arg.EventType != "container_exited" {
				t.Fatalf("expected event_type filter 'container_exited', got %q", arg.EventType)
			}
			return []store.Event{
				{
					ID:        2,
					EventType: "container_exited",
					Actor:     "agent",
					CreatedAt: pgtype.Timestamptz{Time: now, Valid: true},
				},
			}, nil
		},
	}

	srv := newTestServerWithEventStore(es)
	req := httptest.NewRequest(http.MethodGet, "/v1/events?event_type=container_exited", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Events []map[string]interface{} `json:"events"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(resp.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(resp.Events))
	}
}

func TestListEvents_NoFilters_DefaultLimit(t *testing.T) {
	var capturedLimit int32

	es := &mockEventStore{
		listRecentFn: func(ctx context.Context, limit int32) ([]store.Event, error) {
			capturedLimit = limit
			return []store.Event{}, nil
		},
	}

	srv := newTestServerWithEventStore(es)
	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if capturedLimit != 100 {
		t.Fatalf("expected default limit 100, got %d", capturedLimit)
	}
}

func TestListEvents_CustomLimit(t *testing.T) {
	var capturedLimit int32

	es := &mockEventStore{
		listRecentFn: func(ctx context.Context, limit int32) ([]store.Event, error) {
			capturedLimit = limit
			return []store.Event{}, nil
		},
	}

	srv := newTestServerWithEventStore(es)
	req := httptest.NewRequest(http.MethodGet, "/v1/events?limit=50", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if capturedLimit != 50 {
		t.Fatalf("expected limit 50, got %d", capturedLimit)
	}
}

func TestListEvents_StoreError(t *testing.T) {
	es := &mockEventStore{
		listRecentFn: func(ctx context.Context, limit int32) ([]store.Event, error) {
			return nil, errors.New("database error")
		},
	}

	srv := newTestServerWithEventStore(es)
	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}
}

// --- Test helper ---

func newTestServerWithEventStore(es EventStore) *Server {
	r := chi.NewRouter()
	s := &Server{
		router:     r,
		config:     &serverConfig{apiToken: "test-token"},
		logger:     testLogger(),
		eventStore: es,
	}

	r.Group(func(r chi.Router) {
		r.Use(BearerAuth("test-token"))
		r.Get("/v1/events", s.handleListEvents)
	})

	return s
}
