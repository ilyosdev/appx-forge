package routing

import (
	"context"
	"log/slog"
	"testing"
)

// ── Spy Batcher ─────────────────────────────────────────────────────

// spyBatcher records Enqueue calls for assertion.
type spyBatcher struct {
	calls []RouteChange
}

func (s *spyBatcher) Enqueue(change RouteChange) {
	s.calls = append(s.calls, change)
}

// ── RouteManager Tests ──────────────────────────────────────────────

func TestRouteManager_OnSandboxRunning_EnqueuesAdd(t *testing.T) {
	spy := &spyBatcher{}
	rm := NewRouteManager(spy, slog.Default())

	err := rm.OnSandboxRunning(context.Background(), "my-app", "sbx-123", "100.64.0.1:8081")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(spy.calls) != 1 {
		t.Fatalf("expected 1 enqueue call, got %d", len(spy.calls))
	}

	change := spy.calls[0]
	if change.Action != "add" {
		t.Errorf("action = %q, want add", change.Action)
	}
	if change.Route.AppName != "my-app" {
		t.Errorf("route.AppName = %q, want my-app", change.Route.AppName)
	}
	if change.Route.SandboxID != "sbx-123" {
		t.Errorf("route.SandboxID = %q, want sbx-123", change.Route.SandboxID)
	}
	if change.Route.Upstream != "100.64.0.1:8081" {
		t.Errorf("route.Upstream = %q, want 100.64.0.1:8081", change.Route.Upstream)
	}
}

func TestRouteManager_OnSandboxStopped_EnqueuesRemove(t *testing.T) {
	spy := &spyBatcher{}
	rm := NewRouteManager(spy, slog.Default())

	err := rm.OnSandboxStopped(context.Background(), "my-app")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(spy.calls) != 1 {
		t.Fatalf("expected 1 enqueue call, got %d", len(spy.calls))
	}

	change := spy.calls[0]
	if change.Action != "remove" {
		t.Errorf("action = %q, want remove", change.Action)
	}
	if change.AppName != "my-app" {
		t.Errorf("appName = %q, want my-app", change.AppName)
	}
}

func TestRouteManager_OnSandboxRunning_EmptyUpstream_ReturnsError(t *testing.T) {
	spy := &spyBatcher{}
	rm := NewRouteManager(spy, slog.Default())

	err := rm.OnSandboxRunning(context.Background(), "my-app", "sbx-123", "")
	if err == nil {
		t.Fatal("expected error for empty upstream, got nil")
	}

	if len(spy.calls) != 0 {
		t.Fatalf("expected 0 enqueue calls on error, got %d", len(spy.calls))
	}
}
