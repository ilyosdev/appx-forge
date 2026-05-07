package lifecycle

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/appx/forge/control/internal/store"
	"github.com/appx/forge/shared-go/models"
)

// ── Mock RestartStore ───────────────────────────────────────────────

type mockRestartStore struct {
	incrementFailureCountFn func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error)
	resetFailureCountFn     func(ctx context.Context, id pgtype.UUID) error
	transitionStateFn       func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error)
	createCommandFn         func(ctx context.Context, arg store.CreateCommandParams) (store.Command, error)
	recordEventFn           func(ctx context.Context, arg store.RecordEventParams) (store.Event, error)
	getSandboxFn            func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error)
}

func (m *mockRestartStore) IncrementSandboxFailureCount(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
	if m.incrementFailureCountFn != nil {
		return m.incrementFailureCountFn(ctx, id)
	}
	return store.Sandbox{}, nil
}

func (m *mockRestartStore) ResetSandboxFailureCount(ctx context.Context, id pgtype.UUID) error {
	if m.resetFailureCountFn != nil {
		return m.resetFailureCountFn(ctx, id)
	}
	return nil
}

func (m *mockRestartStore) TransitionSandboxState(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
	if m.transitionStateFn != nil {
		return m.transitionStateFn(ctx, arg)
	}
	return store.Sandbox{State: arg.State}, nil
}

func (m *mockRestartStore) CreateCommand(ctx context.Context, arg store.CreateCommandParams) (store.Command, error) {
	if m.createCommandFn != nil {
		return m.createCommandFn(ctx, arg)
	}
	return store.Command{ID: arg.ID, CommandType: arg.CommandType}, nil
}

func (m *mockRestartStore) RecordEvent(ctx context.Context, arg store.RecordEventParams) (store.Event, error) {
	if m.recordEventFn != nil {
		return m.recordEventFn(ctx, arg)
	}
	return store.Event{}, nil
}

func (m *mockRestartStore) GetSandbox(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
	if m.getSandboxFn != nil {
		return m.getSandboxFn(ctx, id)
	}
	return store.Sandbox{}, nil
}

// ── Test Helpers ────────────────────────────────────────────────────

func makeSandbox(id uuid.UUID, state models.SandboxState, failureCount int32) store.Sandbox {
	nodeID := uuid.New()
	return store.Sandbox{
		ID:           makePgUUID(id),
		AppName:      "test-app",
		UserID:       "user-123",
		Image:        "appx/sandbox:v1",
		State:        string(state),
		FailureCount: failureCount,
		NodeID:       makePgUUID(nodeID),
		ContainerID:  pgtype.Text{String: "container-abc", Valid: true},
	}
}

// ── HandleCrash Tests ──────────────────────────────────────────────

func TestRestart_HandleCrash_FailureCount0_ReturnsRestartWithDelay5s(t *testing.T) {
	sandboxID := uuid.New()
	sandbox := makeSandbox(sandboxID, models.StateRestarting, 0)

	ms := &mockRestartStore{
		incrementFailureCountFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			s := sandbox
			s.FailureCount = 1
			return s, nil
		},
	}

	rm := NewRestartManager(ms, nil)
	result, err := rm.HandleCrash(context.Background(), sandbox)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.ShouldRestart {
		t.Fatal("expected ShouldRestart=true for failure_count=0")
	}
	if result.Delay != 5*time.Second {
		t.Fatalf("expected delay=5s, got %v", result.Delay)
	}
}

func TestRestart_HandleCrash_FailureCount1_ReturnsRestartWithDelay10s(t *testing.T) {
	sandboxID := uuid.New()
	sandbox := makeSandbox(sandboxID, models.StateRestarting, 1)

	ms := &mockRestartStore{
		incrementFailureCountFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			s := sandbox
			s.FailureCount = 2
			return s, nil
		},
	}

	rm := NewRestartManager(ms, nil)
	result, err := rm.HandleCrash(context.Background(), sandbox)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.ShouldRestart {
		t.Fatal("expected ShouldRestart=true for failure_count=1")
	}
	if result.Delay != 10*time.Second {
		t.Fatalf("expected delay=10s, got %v", result.Delay)
	}
}

func TestRestart_HandleCrash_FailureCount2_ReturnsRestartWithDelay20s(t *testing.T) {
	sandboxID := uuid.New()
	sandbox := makeSandbox(sandboxID, models.StateRestarting, 2)

	ms := &mockRestartStore{
		incrementFailureCountFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			s := sandbox
			s.FailureCount = 3
			return s, nil
		},
	}

	rm := NewRestartManager(ms, nil)
	result, err := rm.HandleCrash(context.Background(), sandbox)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.ShouldRestart {
		t.Fatal("expected ShouldRestart=true for failure_count=2")
	}
	if result.Delay != 20*time.Second {
		t.Fatalf("expected delay=20s, got %v", result.Delay)
	}
}

func TestRestart_HandleCrash_FailureCount3_ReturnsShouldNotRestart(t *testing.T) {
	sandboxID := uuid.New()
	sandbox := makeSandbox(sandboxID, models.StateRestarting, 3)

	var capturedTransition store.TransitionSandboxStateParams

	ms := &mockRestartStore{
		incrementFailureCountFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			s := sandbox
			s.FailureCount = 4
			return s, nil
		},
		transitionStateFn: func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
			capturedTransition = arg
			return store.Sandbox{State: arg.State}, nil
		},
	}

	rm := NewRestartManager(ms, nil)
	result, err := rm.HandleCrash(context.Background(), sandbox)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ShouldRestart {
		t.Fatal("expected ShouldRestart=false for failure_count=3 (max retries exceeded)")
	}

	// Should transition to failed
	if capturedTransition.State != string(models.StateFailed) {
		t.Fatalf("expected transition to 'failed', got %q", capturedTransition.State)
	}
}

func TestRestart_HandleRestarted_ResetsFailureCount(t *testing.T) {
	sandboxID := uuid.New()
	pgID := makePgUUID(sandboxID)
	resetCalled := false
	var resetID pgtype.UUID

	ms := &mockRestartStore{
		resetFailureCountFn: func(ctx context.Context, id pgtype.UUID) error {
			resetCalled = true
			resetID = id
			return nil
		},
	}

	rm := NewRestartManager(ms, nil)
	err := rm.HandleRestarted(context.Background(), pgID)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resetCalled {
		t.Fatal("expected ResetSandboxFailureCount to be called")
	}
	if resetID != pgID {
		t.Fatal("expected reset to be called with correct sandbox ID")
	}
}

func TestRestart_HandleCrash_IncrementsFailureCountInStore(t *testing.T) {
	sandboxID := uuid.New()
	sandbox := makeSandbox(sandboxID, models.StateRestarting, 0)
	incrementCalled := false
	var incrementID pgtype.UUID

	ms := &mockRestartStore{
		incrementFailureCountFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			incrementCalled = true
			incrementID = id
			s := sandbox
			s.FailureCount = 1
			return s, nil
		},
	}

	rm := NewRestartManager(ms, nil)
	_, err := rm.HandleCrash(context.Background(), sandbox)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !incrementCalled {
		t.Fatal("expected IncrementSandboxFailureCount to be called")
	}
	if incrementID != sandbox.ID {
		t.Fatal("expected increment to be called with correct sandbox ID")
	}
}

func TestRestart_HandleCrash_DispatchesRestartCommand(t *testing.T) {
	sandboxID := uuid.New()
	sandbox := makeSandbox(sandboxID, models.StateRestarting, 0)

	var capturedCmd store.CreateCommandParams
	cmdCreated := false

	ms := &mockRestartStore{
		incrementFailureCountFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			s := sandbox
			s.FailureCount = 1
			return s, nil
		},
		createCommandFn: func(ctx context.Context, arg store.CreateCommandParams) (store.Command, error) {
			cmdCreated = true
			capturedCmd = arg
			return store.Command{ID: arg.ID, CommandType: arg.CommandType}, nil
		},
	}

	rm := NewRestartManager(ms, nil)
	_, err := rm.HandleCrash(context.Background(), sandbox)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cmdCreated {
		t.Fatal("expected CreateCommand to be called for restart")
	}
	if capturedCmd.CommandType != string(models.CmdStartSandbox) {
		t.Fatalf("expected command type %q, got %q", models.CmdStartSandbox, capturedCmd.CommandType)
	}
	if capturedCmd.NodeID != sandbox.NodeID {
		t.Fatal("expected command to target sandbox's node")
	}
	if capturedCmd.SandboxID != sandbox.ID {
		t.Fatal("expected command to reference correct sandbox")
	}
}

func TestRestart_HandleCrash_TransitionsRestartingToStarting(t *testing.T) {
	sandboxID := uuid.New()
	sandbox := makeSandbox(sandboxID, models.StateRestarting, 0)

	var capturedTransition store.TransitionSandboxStateParams
	transitionCalled := false

	ms := &mockRestartStore{
		incrementFailureCountFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			s := sandbox
			s.FailureCount = 1
			return s, nil
		},
		transitionStateFn: func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
			transitionCalled = true
			capturedTransition = arg
			return store.Sandbox{State: arg.State}, nil
		},
	}

	rm := NewRestartManager(ms, nil)
	_, err := rm.HandleCrash(context.Background(), sandbox)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !transitionCalled {
		t.Fatal("expected TransitionSandboxState to be called")
	}
	// restart_attempt transitions restarting -> starting
	if capturedTransition.State != string(models.StateStarting) {
		t.Fatalf("expected transition to %q, got %q", models.StateStarting, capturedTransition.State)
	}
	if capturedTransition.State_2 != string(models.StateRestarting) {
		t.Fatalf("expected from-state %q, got %q", models.StateRestarting, capturedTransition.State_2)
	}
}
