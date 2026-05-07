package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/appx/forge/control/internal/store"
)

// ── Mock IdleReaperStore ────────────────────────────────────────────

type mockIdleReaperStore struct {
	listIdleSandboxesFn   func(ctx context.Context) ([]store.Sandbox, error)
	transitionStateFn     func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error)
	recordEventFn         func(ctx context.Context, arg store.RecordEventParams) (store.Event, error)
	createCommandFn       func(ctx context.Context, arg store.CreateCommandParams) (store.Command, error)
}

func (m *mockIdleReaperStore) ListIdleSandboxes(ctx context.Context) ([]store.Sandbox, error) {
	if m.listIdleSandboxesFn != nil {
		return m.listIdleSandboxesFn(ctx)
	}
	return nil, nil
}

func (m *mockIdleReaperStore) TransitionSandboxState(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
	if m.transitionStateFn != nil {
		return m.transitionStateFn(ctx, arg)
	}
	return store.Sandbox{State: arg.State}, nil
}

func (m *mockIdleReaperStore) RecordEvent(ctx context.Context, arg store.RecordEventParams) (store.Event, error) {
	if m.recordEventFn != nil {
		return m.recordEventFn(ctx, arg)
	}
	return store.Event{}, nil
}

func (m *mockIdleReaperStore) CreateCommand(ctx context.Context, arg store.CreateCommandParams) (store.Command, error) {
	if m.createCommandFn != nil {
		return m.createCommandFn(ctx, arg)
	}
	return store.Command{ID: arg.ID}, nil
}

// ── Mock RouteNotifier for IdleReaper ─────────────────────────────

type mockReaperNotifier struct {
	stoppedCalls []string // app names
	stoppedErr   error
}

func (m *mockReaperNotifier) OnSandboxRunning(_ context.Context, _, _, _ string) error {
	return nil
}

func (m *mockReaperNotifier) OnSandboxStopped(_ context.Context, appName string) error {
	m.stoppedCalls = append(m.stoppedCalls, appName)
	return m.stoppedErr
}

// ── Test Helpers ────────────────────────────────────────────────────

func makeIdleSandbox(appName string) store.Sandbox {
	id := uuid.New()
	nodeID := uuid.New()
	return store.Sandbox{
		ID:      pgtype.UUID{Bytes: id, Valid: true},
		AppName: appName,
		State:   "running",
		NodeID:  pgtype.UUID{Bytes: nodeID, Valid: true},
	}
}

// ── Tests ───────────────────────────────────────────────────────────

func TestIdleReaper_NoIdleSandboxes(t *testing.T) {
	ms := &mockIdleReaperStore{
		listIdleSandboxesFn: func(ctx context.Context) ([]store.Sandbox, error) {
			return []store.Sandbox{}, nil
		},
	}

	transitionCalled := false
	ms.transitionStateFn = func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
		transitionCalled = true
		return store.Sandbox{}, nil
	}

	reaper := NewIdleReaper(ms, nil, nil, 0)
	err := reaper.reap(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if transitionCalled {
		t.Fatal("expected no transitions for empty idle list")
	}
}

func TestIdleReaper_TwoIdleSandboxes(t *testing.T) {
	sb1 := makeIdleSandbox("app-one")
	sb2 := makeIdleSandbox("app-two")

	ms := &mockIdleReaperStore{
		listIdleSandboxesFn: func(ctx context.Context) ([]store.Sandbox, error) {
			return []store.Sandbox{sb1, sb2}, nil
		},
	}

	var transitions []store.TransitionSandboxStateParams
	ms.transitionStateFn = func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
		transitions = append(transitions, arg)
		return store.Sandbox{State: arg.State}, nil
	}

	var events []store.RecordEventParams
	ms.recordEventFn = func(ctx context.Context, arg store.RecordEventParams) (store.Event, error) {
		events = append(events, arg)
		return store.Event{}, nil
	}

	var commands []store.CreateCommandParams
	ms.createCommandFn = func(ctx context.Context, arg store.CreateCommandParams) (store.Command, error) {
		commands = append(commands, arg)
		return store.Command{ID: arg.ID}, nil
	}

	notifier := &mockReaperNotifier{}
	reaper := NewIdleReaper(ms, notifier, nil, 0)
	err := reaper.reap(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Both sandboxes should be transitioned to stopped
	if len(transitions) != 2 {
		t.Fatalf("expected 2 transitions, got %d", len(transitions))
	}
	for i, tr := range transitions {
		if tr.State != "stopped" {
			t.Errorf("transition[%d]: expected state 'stopped', got %q", i, tr.State)
		}
		if tr.State_2 != "running" {
			t.Errorf("transition[%d]: expected from-state 'running', got %q", i, tr.State_2)
		}
	}

	// Both should have idle_timeout events recorded
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	for i, ev := range events {
		if ev.EventType != "idle_timeout" {
			t.Errorf("event[%d]: expected type 'idle_timeout', got %q", i, ev.EventType)
		}
	}

	// Both should have stop_sandbox commands dispatched
	if len(commands) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(commands))
	}
	for i, cmd := range commands {
		if cmd.CommandType != "stop_sandbox" {
			t.Errorf("command[%d]: expected type 'stop_sandbox', got %q", i, cmd.CommandType)
		}
	}

	// Notifier should be called for both
	if len(notifier.stoppedCalls) != 2 {
		t.Fatalf("expected 2 OnSandboxStopped calls, got %d", len(notifier.stoppedCalls))
	}
	if notifier.stoppedCalls[0] != "app-one" {
		t.Errorf("expected first stopped call for 'app-one', got %q", notifier.stoppedCalls[0])
	}
	if notifier.stoppedCalls[1] != "app-two" {
		t.Errorf("expected second stopped call for 'app-two', got %q", notifier.stoppedCalls[1])
	}
}

func TestIdleReaper_ContinuesOnTransitionFailure(t *testing.T) {
	sb1 := makeIdleSandbox("failing-app")
	sb2 := makeIdleSandbox("good-app")

	callCount := 0
	ms := &mockIdleReaperStore{
		listIdleSandboxesFn: func(ctx context.Context) ([]store.Sandbox, error) {
			return []store.Sandbox{sb1, sb2}, nil
		},
	}

	ms.transitionStateFn = func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
		callCount++
		if callCount == 1 {
			return store.Sandbox{}, errors.New("transition failed")
		}
		return store.Sandbox{State: arg.State}, nil
	}

	var commands []store.CreateCommandParams
	ms.createCommandFn = func(ctx context.Context, arg store.CreateCommandParams) (store.Command, error) {
		commands = append(commands, arg)
		return store.Command{ID: arg.ID}, nil
	}

	notifier := &mockReaperNotifier{}
	reaper := NewIdleReaper(ms, notifier, nil, 0)
	err := reaper.reap(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Despite first failure, second sandbox should still be processed
	if len(commands) != 1 {
		t.Fatalf("expected 1 command (only good-app), got %d", len(commands))
	}
	if len(notifier.stoppedCalls) != 1 {
		t.Fatalf("expected 1 OnSandboxStopped call (only good-app), got %d", len(notifier.stoppedCalls))
	}
	if notifier.stoppedCalls[0] != "good-app" {
		t.Errorf("expected stopped call for 'good-app', got %q", notifier.stoppedCalls[0])
	}
}

func TestIdleReaper_CallsRouteNotifier(t *testing.T) {
	sb := makeIdleSandbox("routed-app")

	ms := &mockIdleReaperStore{
		listIdleSandboxesFn: func(ctx context.Context) ([]store.Sandbox, error) {
			return []store.Sandbox{sb}, nil
		},
	}

	notifier := &mockReaperNotifier{}
	reaper := NewIdleReaper(ms, notifier, nil, 0)
	err := reaper.reap(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(notifier.stoppedCalls) != 1 {
		t.Fatalf("expected 1 OnSandboxStopped call, got %d", len(notifier.stoppedCalls))
	}
	if notifier.stoppedCalls[0] != "routed-app" {
		t.Errorf("expected stopped call for 'routed-app', got %q", notifier.stoppedCalls[0])
	}
}

func TestIdleReaper_RecordsIdleTimeoutEvent(t *testing.T) {
	sb := makeIdleSandbox("timeout-app")

	ms := &mockIdleReaperStore{
		listIdleSandboxesFn: func(ctx context.Context) ([]store.Sandbox, error) {
			return []store.Sandbox{sb}, nil
		},
	}

	var events []store.RecordEventParams
	ms.recordEventFn = func(ctx context.Context, arg store.RecordEventParams) (store.Event, error) {
		events = append(events, arg)
		return store.Event{}, nil
	}

	reaper := NewIdleReaper(ms, nil, nil, 0)
	err := reaper.reap(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.EventType != "idle_timeout" {
		t.Errorf("expected event type 'idle_timeout', got %q", ev.EventType)
	}
	if ev.Actor != "idle-reaper" {
		t.Errorf("expected actor 'idle-reaper', got %q", ev.Actor)
	}
	if ev.PrevState.String != "running" {
		t.Errorf("expected prev_state 'running', got %q", ev.PrevState.String)
	}
	if ev.NextState.String != "stopped" {
		t.Errorf("expected next_state 'stopped', got %q", ev.NextState.String)
	}
}
