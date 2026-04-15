package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/appx/forge/control/internal/store"
)

// ── Mock Store ───────────────────────────────────────────────────────

type mockStore struct {
	createSandboxFn       func(ctx context.Context, arg store.CreateSandboxParams) (store.Sandbox, error)
	getSandboxFn          func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error)
	getSandboxByAppNameFn func(ctx context.Context, appName string) (store.Sandbox, error)
	listHealthyNodesFn    func(ctx context.Context) ([]store.Node, error)
	assignSandboxFn       func(ctx context.Context, arg store.AssignSandboxToNodeParams) (store.Sandbox, error)
	transitionStateFn     func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error)
	createCommandFn       func(ctx context.Context, arg store.CreateCommandParams) (store.Command, error)
	ackCommandFn          func(ctx context.Context, arg store.AckCommandParams) error
	recordEventFn         func(ctx context.Context, arg store.RecordEventParams) (store.Event, error)
}

func (m *mockStore) CreateSandbox(ctx context.Context, arg store.CreateSandboxParams) (store.Sandbox, error) {
	if m.createSandboxFn != nil {
		return m.createSandboxFn(ctx, arg)
	}
	return store.Sandbox{ID: arg.ID, AppName: arg.AppName, UserID: arg.UserID, Image: arg.Image, State: "pending"}, nil
}

func (m *mockStore) GetSandbox(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
	if m.getSandboxFn != nil {
		return m.getSandboxFn(ctx, id)
	}
	return store.Sandbox{}, errors.New("not found")
}

func (m *mockStore) GetSandboxByAppName(ctx context.Context, appName string) (store.Sandbox, error) {
	if m.getSandboxByAppNameFn != nil {
		return m.getSandboxByAppNameFn(ctx, appName)
	}
	return store.Sandbox{}, errors.New("not found")
}

func (m *mockStore) ListHealthyNodes(ctx context.Context) ([]store.Node, error) {
	if m.listHealthyNodesFn != nil {
		return m.listHealthyNodesFn(ctx)
	}
	return nil, nil
}

func (m *mockStore) AssignSandboxToNode(ctx context.Context, arg store.AssignSandboxToNodeParams) (store.Sandbox, error) {
	if m.assignSandboxFn != nil {
		return m.assignSandboxFn(ctx, arg)
	}
	return store.Sandbox{State: "starting"}, nil
}

func (m *mockStore) TransitionSandboxState(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
	if m.transitionStateFn != nil {
		return m.transitionStateFn(ctx, arg)
	}
	return store.Sandbox{State: arg.State}, nil
}

func (m *mockStore) CreateCommand(ctx context.Context, arg store.CreateCommandParams) (store.Command, error) {
	if m.createCommandFn != nil {
		return m.createCommandFn(ctx, arg)
	}
	return store.Command{ID: arg.ID, CommandType: arg.CommandType}, nil
}

func (m *mockStore) AckCommand(ctx context.Context, arg store.AckCommandParams) error {
	if m.ackCommandFn != nil {
		return m.ackCommandFn(ctx, arg)
	}
	return nil
}

func (m *mockStore) RecordEvent(ctx context.Context, arg store.RecordEventParams) (store.Event, error) {
	if m.recordEventFn != nil {
		return m.recordEventFn(ctx, arg)
	}
	return store.Event{}, nil
}

// ── Test Helpers ─────────────────────────────────────────────────────

func makePgUUID(u uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: u, Valid: true}
}

func makeNode(id uuid.UUID, capacityMB, usedMB int32) store.Node {
	return store.Node{
		ID:         makePgUUID(id),
		Hostname:   "test-node",
		CapacityMb: capacityMB,
		UsedMb:     usedMB,
		Status:     "healthy",
	}
}

// ── CreateSandbox Tests ─────────────────────────────────────────────

func TestCreateSandbox_HappyPath(t *testing.T) {
	nodeID := uuid.New()
	var capturedCmd store.CreateCommandParams
	var capturedEvent store.RecordEventParams
	var capturedAssign store.AssignSandboxToNodeParams

	ms := &mockStore{
		listHealthyNodesFn: func(ctx context.Context) ([]store.Node, error) {
			return []store.Node{makeNode(nodeID, 24000, 0)}, nil
		},
		assignSandboxFn: func(ctx context.Context, arg store.AssignSandboxToNodeParams) (store.Sandbox, error) {
			capturedAssign = arg
			return store.Sandbox{
				ID:      arg.ID,
				AppName: "my-app",
				State:   "starting",
				NodeID:  arg.NodeID,
			}, nil
		},
		createCommandFn: func(ctx context.Context, arg store.CreateCommandParams) (store.Command, error) {
			capturedCmd = arg
			return store.Command{ID: arg.ID, CommandType: arg.CommandType}, nil
		},
		recordEventFn: func(ctx context.Context, arg store.RecordEventParams) (store.Event, error) {
			capturedEvent = arg
			return store.Event{}, nil
		},
	}

	svc := New(ms, nil)
	result, err := svc.CreateSandbox(context.Background(), CreateRequest{
		AppName:            "my-app",
		UserID:             "user-123",
		Image:              "appx/sandbox:v1",
		Resources:          Resources{CPUCores: 0.5, MemoryMB: 512},
		Env:                map[string]string{"PORT": "8081"},
		IdleTimeoutSeconds: 1800,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.AppName != "my-app" {
		t.Fatalf("expected app_name 'my-app', got %q", result.AppName)
	}
	if result.State != "starting" {
		t.Fatalf("expected state 'starting', got %q", result.State)
	}
	if result.URL != "https://my-app.myappx.live" {
		t.Fatalf("expected URL 'https://my-app.myappx.live', got %q", result.URL)
	}

	// Verify node was assigned
	if capturedAssign.NodeID != makePgUUID(nodeID) {
		t.Fatalf("expected assign to node %s, got different", nodeID)
	}

	// Verify start_sandbox command was created
	if capturedCmd.CommandType != "start_sandbox" {
		t.Fatalf("expected command type 'start_sandbox', got %q", capturedCmd.CommandType)
	}

	// Verify command payload has app_name
	var payload map[string]interface{}
	if err := json.Unmarshal(capturedCmd.Payload, &payload); err != nil {
		t.Fatalf("unmarshal command payload: %v", err)
	}
	if payload["app_name"] != "my-app" {
		t.Fatalf("expected payload app_name='my-app', got %v", payload["app_name"])
	}

	// Verify event recorded
	if capturedEvent.EventType != "scheduled" {
		t.Fatalf("expected event type 'scheduled', got %q", capturedEvent.EventType)
	}
}

func TestCreateSandbox_DuplicateAppName(t *testing.T) {
	ms := &mockStore{
		createSandboxFn: func(ctx context.Context, arg store.CreateSandboxParams) (store.Sandbox, error) {
			return store.Sandbox{}, errors.New("duplicate key value violates unique constraint")
		},
	}

	svc := New(ms, nil)
	_, err := svc.CreateSandbox(context.Background(), CreateRequest{
		AppName: "existing-app",
		UserID:  "user-123",
		Image:   "appx/sandbox:v1",
	})

	if err == nil {
		t.Fatal("expected error for duplicate app_name")
	}
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestCreateSandbox_NoAvailableNodes(t *testing.T) {
	ms := &mockStore{
		listHealthyNodesFn: func(ctx context.Context) ([]store.Node, error) {
			return []store.Node{}, nil
		},
	}

	svc := New(ms, nil)
	_, err := svc.CreateSandbox(context.Background(), CreateRequest{
		AppName:   "my-app",
		UserID:    "user-123",
		Image:     "appx/sandbox:v1",
		Resources: Resources{MemoryMB: 512},
	})

	if err == nil {
		t.Fatal("expected error for no available nodes")
	}
	if !errors.Is(err, ErrNoCapacity) {
		t.Fatalf("expected ErrNoCapacity, got %v", err)
	}
}

// ── HandleAck Tests ─────────────────────────────────────────────────

func TestHandleAck_StartSandboxSuccess(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)

	var capturedTransition store.TransitionSandboxStateParams

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{ID: pgSandboxID, State: "starting", AppName: "test-app"}, nil
		},
		transitionStateFn: func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
			capturedTransition = arg
			return store.Sandbox{ID: pgSandboxID, State: arg.State}, nil
		},
	}

	svc := New(ms, nil)
	err := svc.HandleAck(context.Background(), uuid.New(), sandboxID, "start_sandbox", "success", json.RawMessage(`{"container_id":"abc123","host_port":43210}`))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify transition: starting -> running
	if capturedTransition.State != "running" {
		t.Fatalf("expected transition to 'running', got %q", capturedTransition.State)
	}
	if capturedTransition.State_2 != "starting" {
		t.Fatalf("expected from-state 'starting', got %q", capturedTransition.State_2)
	}
}

func TestHandleAck_StartSandboxFailure(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)

	var capturedTransition store.TransitionSandboxStateParams

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{ID: pgSandboxID, State: "starting", AppName: "test-app"}, nil
		},
		transitionStateFn: func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
			capturedTransition = arg
			return store.Sandbox{ID: pgSandboxID, State: arg.State}, nil
		},
	}

	svc := New(ms, nil)
	err := svc.HandleAck(context.Background(), uuid.New(), sandboxID, "start_sandbox", "failure", nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify transition: starting -> failed
	if capturedTransition.State != "failed" {
		t.Fatalf("expected transition to 'failed', got %q", capturedTransition.State)
	}
}

func TestHandleAck_StopSandboxSuccess(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)

	var capturedTransition store.TransitionSandboxStateParams

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{ID: pgSandboxID, State: "destroying", AppName: "test-app"}, nil
		},
		transitionStateFn: func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
			capturedTransition = arg
			return store.Sandbox{ID: pgSandboxID, State: arg.State}, nil
		},
	}

	svc := New(ms, nil)
	err := svc.HandleAck(context.Background(), uuid.New(), sandboxID, "stop_sandbox", "success", nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify transition: destroying -> destroyed
	if capturedTransition.State != "destroyed" {
		t.Fatalf("expected transition to 'destroyed', got %q", capturedTransition.State)
	}
}

// ── HandleEvent Tests ───────────────────────────────────────────────

func TestHandleEvent_ContainerExitedOnRunning(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)

	var capturedTransition store.TransitionSandboxStateParams

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{ID: pgSandboxID, State: "running", AppName: "test-app"}, nil
		},
		transitionStateFn: func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
			capturedTransition = arg
			return store.Sandbox{ID: pgSandboxID, State: arg.State}, nil
		},
	}

	svc := New(ms, nil)
	err := svc.HandleEvent(context.Background(), uuid.New(), sandboxID, "container_exited", json.RawMessage(`{"exit_code":137}`))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify transition: running -> restarting
	if capturedTransition.State != "restarting" {
		t.Fatalf("expected transition to 'restarting', got %q", capturedTransition.State)
	}
}

func TestHandleEvent_ContainerStarted(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)

	var capturedTransition store.TransitionSandboxStateParams

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{ID: pgSandboxID, State: "starting", AppName: "test-app"}, nil
		},
		transitionStateFn: func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
			capturedTransition = arg
			return store.Sandbox{ID: pgSandboxID, State: arg.State}, nil
		},
	}

	svc := New(ms, nil)
	err := svc.HandleEvent(context.Background(), uuid.New(), sandboxID, "container_started", nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify transition: starting -> running
	if capturedTransition.State != "running" {
		t.Fatalf("expected transition to 'running', got %q", capturedTransition.State)
	}
}

func TestHandleEvent_ContainerOOMOnRunning(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)

	var capturedTransition store.TransitionSandboxStateParams

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{ID: pgSandboxID, State: "running", AppName: "test-app"}, nil
		},
		transitionStateFn: func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
			capturedTransition = arg
			return store.Sandbox{ID: pgSandboxID, State: arg.State}, nil
		},
	}

	svc := New(ms, nil)
	err := svc.HandleEvent(context.Background(), uuid.New(), sandboxID, "container_oom", json.RawMessage(`{"oom_killed":true}`))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// container_oom maps to container_exited event -> running -> restarting
	if capturedTransition.State != "restarting" {
		t.Fatalf("expected transition to 'restarting', got %q", capturedTransition.State)
	}
}

// ── DestroySandbox Tests ────────────────────────────────────────────

func TestDestroySandbox_HappyPath(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)
	nodeID := uuid.New()
	pgNodeID := makePgUUID(nodeID)

	var capturedTransition store.TransitionSandboxStateParams
	var capturedCmd store.CreateCommandParams

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{
				ID:      pgSandboxID,
				State:   "running",
				AppName: "test-app",
				NodeID:  pgNodeID,
			}, nil
		},
		transitionStateFn: func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
			capturedTransition = arg
			return store.Sandbox{ID: pgSandboxID, State: arg.State, NodeID: pgNodeID}, nil
		},
		createCommandFn: func(ctx context.Context, arg store.CreateCommandParams) (store.Command, error) {
			capturedCmd = arg
			return store.Command{ID: arg.ID}, nil
		},
	}

	svc := New(ms, nil)
	err := svc.DestroySandbox(context.Background(), sandboxID)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify transition to destroying
	if capturedTransition.State != "destroying" {
		t.Fatalf("expected transition to 'destroying', got %q", capturedTransition.State)
	}

	// Verify stop_sandbox command created
	if capturedCmd.CommandType != "stop_sandbox" {
		t.Fatalf("expected command type 'stop_sandbox', got %q", capturedCmd.CommandType)
	}

	// Verify command targets correct node
	if capturedCmd.NodeID != pgNodeID {
		t.Fatal("expected command to target sandbox's node")
	}
}

// ── RestartSandbox Tests ────────────────────────────────────────────

func TestRestartSandbox_HappyPath(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)
	nodeID := uuid.New()
	pgNodeID := makePgUUID(nodeID)

	var capturedCmd store.CreateCommandParams
	transitionCalled := false

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{
				ID:          pgSandboxID,
				State:       "running",
				AppName:     "test-app",
				NodeID:      pgNodeID,
				ContainerID: pgtype.Text{String: "abc123", Valid: true},
			}, nil
		},
		transitionStateFn: func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
			transitionCalled = true
			return store.Sandbox{}, nil
		},
		createCommandFn: func(ctx context.Context, arg store.CreateCommandParams) (store.Command, error) {
			capturedCmd = arg
			return store.Command{ID: arg.ID}, nil
		},
	}

	svc := New(ms, nil)
	err := svc.RestartSandbox(context.Background(), sandboxID)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify restart_sandbox command created
	if capturedCmd.CommandType != "restart_sandbox" {
		t.Fatalf("expected command type 'restart_sandbox', got %q", capturedCmd.CommandType)
	}

	// No state transition for restart (stays running)
	if transitionCalled {
		t.Fatal("expected no state transition for restart")
	}
}
