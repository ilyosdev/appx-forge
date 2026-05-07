package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/appx/forge/control/internal/store"
)

// ── Mock Store ───────────────────────────────────────────────────────

type mockStore struct {
	createSandboxFn         func(ctx context.Context, arg store.CreateSandboxParams) (store.Sandbox, error)
	deleteSandboxFn         func(ctx context.Context, id pgtype.UUID) error
	getSandboxFn            func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error)
	getSandboxByAppNameFn   func(ctx context.Context, appName string) (store.Sandbox, error)
	listHealthyNodesFn      func(ctx context.Context) ([]store.Node, error)
	assignSandboxFn         func(ctx context.Context, arg store.AssignSandboxToNodeParams) (store.Sandbox, error)
	transitionStateFn       func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error)
	updateSandboxRuntimeFn  func(ctx context.Context, arg store.UpdateSandboxRuntimeParams) error
	createCommandFn         func(ctx context.Context, arg store.CreateCommandParams) (store.Command, error)
	ackCommandFn            func(ctx context.Context, arg store.AckCommandParams) error
	recordEventFn           func(ctx context.Context, arg store.RecordEventParams) (store.Event, error)
	getNodeByIDFn           func(ctx context.Context, id pgtype.UUID) (store.Node, error)
}

func (m *mockStore) CreateSandbox(ctx context.Context, arg store.CreateSandboxParams) (store.Sandbox, error) {
	if m.createSandboxFn != nil {
		return m.createSandboxFn(ctx, arg)
	}
	return store.Sandbox{ID: arg.ID, AppName: arg.AppName, UserID: arg.UserID, Image: arg.Image, State: "pending"}, nil
}

func (m *mockStore) DeleteSandbox(ctx context.Context, id pgtype.UUID) error {
	if m.deleteSandboxFn != nil {
		return m.deleteSandboxFn(ctx, id)
	}
	return nil
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
	// Default: "no prior row" — matches production pgx behavior and lets
	// CreateSandbox's soft-recycle pre-check proceed to the normal INSERT path.
	return store.Sandbox{}, pgx.ErrNoRows
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

func (m *mockStore) UpdateSandboxRuntime(ctx context.Context, arg store.UpdateSandboxRuntimeParams) error {
	if m.updateSandboxRuntimeFn != nil {
		return m.updateSandboxRuntimeFn(ctx, arg)
	}
	return nil
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

func (m *mockStore) GetNodeByID(ctx context.Context, id pgtype.UUID) (store.Node, error) {
	if m.getNodeByIDFn != nil {
		return m.getNodeByIDFn(ctx, id)
	}
	return store.Node{}, errors.New("not found")
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
	// Primary rejection path: pre-check sees an existing row in an active state (running).
	existingID := makePgUUID(uuid.New())
	ms := &mockStore{
		getSandboxByAppNameFn: func(ctx context.Context, appName string) (store.Sandbox, error) {
			return store.Sandbox{ID: existingID, AppName: appName, State: "running"}, nil
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

func TestCreateSandbox_DuplicateAppName_RaceOnInsert(t *testing.T) {
	// Fallback path: two concurrent callers pass the pre-check (both see pgx.ErrNoRows),
	// and one of them loses the race on INSERT. The unique-constraint error path still
	// maps to ErrConflict.
	ms := &mockStore{
		getSandboxByAppNameFn: func(ctx context.Context, appName string) (store.Sandbox, error) {
			return store.Sandbox{}, pgx.ErrNoRows
		},
		createSandboxFn: func(ctx context.Context, arg store.CreateSandboxParams) (store.Sandbox, error) {
			return store.Sandbox{}, errors.New("duplicate key value violates unique constraint")
		},
	}

	svc := New(ms, nil)
	_, err := svc.CreateSandbox(context.Background(), CreateRequest{
		AppName: "race-app",
		UserID:  "user-123",
		Image:   "appx/sandbox:v1",
	})

	if err == nil {
		t.Fatal("expected error for race-on-insert duplicate")
	}
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestCreateSandbox_RecyclesDestroyed(t *testing.T) {
	existingUUID := uuid.New()
	existingID := makePgUUID(existingUUID)
	nodeID := uuid.New()

	var deleteCalls int
	var deleteCalledWith pgtype.UUID
	var capturedCreate store.CreateSandboxParams
	var createCalls int

	ms := &mockStore{
		getSandboxByAppNameFn: func(ctx context.Context, appName string) (store.Sandbox, error) {
			return store.Sandbox{ID: existingID, AppName: appName, State: "destroyed"}, nil
		},
		deleteSandboxFn: func(ctx context.Context, id pgtype.UUID) error {
			deleteCalls++
			deleteCalledWith = id
			return nil
		},
		createSandboxFn: func(ctx context.Context, arg store.CreateSandboxParams) (store.Sandbox, error) {
			createCalls++
			capturedCreate = arg
			return store.Sandbox{ID: arg.ID, AppName: arg.AppName, UserID: arg.UserID, State: "pending"}, nil
		},
		listHealthyNodesFn: func(ctx context.Context) ([]store.Node, error) {
			return []store.Node{makeNode(nodeID, 24000, 0)}, nil
		},
		assignSandboxFn: func(ctx context.Context, arg store.AssignSandboxToNodeParams) (store.Sandbox, error) {
			return store.Sandbox{ID: arg.ID, AppName: "recycled-app", State: "starting", NodeID: arg.NodeID}, nil
		},
	}

	svc := New(ms, nil)
	result, err := svc.CreateSandbox(context.Background(), CreateRequest{
		AppName: "recycled-app",
		UserID:  "user-123",
		Image:   "appx/sandbox:v1",
		Resources: Resources{CPUCores: 0.5, MemoryMB: 512},
	})

	if err != nil {
		t.Fatalf("expected recycle to succeed, got error: %v", err)
	}
	if errors.Is(err, ErrConflict) {
		t.Fatal("expected no ErrConflict when prior row is destroyed")
	}
	if deleteCalls != 1 {
		t.Fatalf("expected DeleteSandbox to be called exactly once, got %d", deleteCalls)
	}
	if deleteCalledWith != existingID {
		t.Fatalf("expected DeleteSandbox called with existing ID %v, got %v", existingID, deleteCalledWith)
	}
	if createCalls != 1 {
		t.Fatalf("expected CreateSandbox to be called exactly once, got %d", createCalls)
	}
	if capturedCreate.ID == existingID {
		t.Fatal("expected CreateSandbox to use a fresh UUID, not the recycled one")
	}
	if result == nil || result.AppName != "recycled-app" {
		t.Fatalf("expected result with app_name 'recycled-app', got %+v", result)
	}
}

func TestCreateSandbox_RecyclesFailed(t *testing.T) {
	existingUUID := uuid.New()
	existingID := makePgUUID(existingUUID)
	nodeID := uuid.New()

	var deleteCalls int
	var createCalls int

	ms := &mockStore{
		getSandboxByAppNameFn: func(ctx context.Context, appName string) (store.Sandbox, error) {
			return store.Sandbox{ID: existingID, AppName: appName, State: "failed"}, nil
		},
		deleteSandboxFn: func(ctx context.Context, id pgtype.UUID) error {
			deleteCalls++
			if id != existingID {
				t.Fatalf("expected DeleteSandbox to get existing ID %v, got %v", existingID, id)
			}
			return nil
		},
		createSandboxFn: func(ctx context.Context, arg store.CreateSandboxParams) (store.Sandbox, error) {
			createCalls++
			return store.Sandbox{ID: arg.ID, AppName: arg.AppName, State: "pending"}, nil
		},
		listHealthyNodesFn: func(ctx context.Context) ([]store.Node, error) {
			return []store.Node{makeNode(nodeID, 24000, 0)}, nil
		},
		assignSandboxFn: func(ctx context.Context, arg store.AssignSandboxToNodeParams) (store.Sandbox, error) {
			return store.Sandbox{ID: arg.ID, AppName: "failed-app", State: "starting", NodeID: arg.NodeID}, nil
		},
	}

	svc := New(ms, nil)
	_, err := svc.CreateSandbox(context.Background(), CreateRequest{
		AppName: "failed-app",
		UserID:  "user-123",
		Image:   "appx/sandbox:v1",
		Resources: Resources{CPUCores: 0.5, MemoryMB: 512},
	})

	if err != nil {
		t.Fatalf("expected recycle of failed row to succeed, got error: %v", err)
	}
	if deleteCalls != 1 {
		t.Fatalf("expected DeleteSandbox to be called exactly once, got %d", deleteCalls)
	}
	if createCalls != 1 {
		t.Fatalf("expected CreateSandbox to be called exactly once, got %d", createCalls)
	}
}

func TestCreateSandbox_RejectsActiveDuplicate_Running(t *testing.T) {
	var deleteCalls int
	ms := &mockStore{
		getSandboxByAppNameFn: func(ctx context.Context, appName string) (store.Sandbox, error) {
			return store.Sandbox{ID: makePgUUID(uuid.New()), AppName: appName, State: "running"}, nil
		},
		deleteSandboxFn: func(ctx context.Context, id pgtype.UUID) error {
			deleteCalls++
			return nil
		},
	}

	svc := New(ms, nil)
	_, err := svc.CreateSandbox(context.Background(), CreateRequest{
		AppName: "running-app",
		UserID:  "user-123",
		Image:   "appx/sandbox:v1",
	})

	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for active running duplicate, got %v", err)
	}
	if deleteCalls != 0 {
		t.Fatalf("expected DeleteSandbox NOT to be called, got %d calls", deleteCalls)
	}
}

func TestCreateSandbox_RejectsActiveDuplicate_Starting(t *testing.T) {
	var deleteCalls int
	ms := &mockStore{
		getSandboxByAppNameFn: func(ctx context.Context, appName string) (store.Sandbox, error) {
			return store.Sandbox{ID: makePgUUID(uuid.New()), AppName: appName, State: "starting"}, nil
		},
		deleteSandboxFn: func(ctx context.Context, id pgtype.UUID) error {
			deleteCalls++
			return nil
		},
	}

	svc := New(ms, nil)
	_, err := svc.CreateSandbox(context.Background(), CreateRequest{
		AppName: "starting-app",
		UserID:  "user-123",
		Image:   "appx/sandbox:v1",
	})

	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for active starting duplicate, got %v", err)
	}
	if deleteCalls != 0 {
		t.Fatalf("expected DeleteSandbox NOT to be called, got %d calls", deleteCalls)
	}
}

func TestCreateSandbox_NoPriorRow_HappyPath(t *testing.T) {
	nodeID := uuid.New()
	var deleteCalls int
	var createCalls int

	ms := &mockStore{
		getSandboxByAppNameFn: func(ctx context.Context, appName string) (store.Sandbox, error) {
			return store.Sandbox{}, pgx.ErrNoRows
		},
		deleteSandboxFn: func(ctx context.Context, id pgtype.UUID) error {
			deleteCalls++
			return nil
		},
		createSandboxFn: func(ctx context.Context, arg store.CreateSandboxParams) (store.Sandbox, error) {
			createCalls++
			return store.Sandbox{ID: arg.ID, AppName: arg.AppName, State: "pending"}, nil
		},
		listHealthyNodesFn: func(ctx context.Context) ([]store.Node, error) {
			return []store.Node{makeNode(nodeID, 24000, 0)}, nil
		},
		assignSandboxFn: func(ctx context.Context, arg store.AssignSandboxToNodeParams) (store.Sandbox, error) {
			return store.Sandbox{ID: arg.ID, AppName: "fresh-app", State: "starting", NodeID: arg.NodeID}, nil
		},
	}

	svc := New(ms, nil)
	_, err := svc.CreateSandbox(context.Background(), CreateRequest{
		AppName: "fresh-app",
		UserID:  "user-123",
		Image:   "appx/sandbox:v1",
		Resources: Resources{CPUCores: 0.5, MemoryMB: 512},
	})

	if err != nil {
		t.Fatalf("expected happy path to succeed, got error: %v", err)
	}
	if deleteCalls != 0 {
		t.Fatalf("expected DeleteSandbox NOT to be called when no prior row, got %d calls", deleteCalls)
	}
	if createCalls != 1 {
		t.Fatalf("expected CreateSandbox to be called exactly once, got %d", createCalls)
	}
}

func TestCreateSandbox_DeleteFails_ReturnsError(t *testing.T) {
	existingID := makePgUUID(uuid.New())
	var createCalls int

	ms := &mockStore{
		getSandboxByAppNameFn: func(ctx context.Context, appName string) (store.Sandbox, error) {
			return store.Sandbox{ID: existingID, AppName: appName, State: "destroyed"}, nil
		},
		deleteSandboxFn: func(ctx context.Context, id pgtype.UUID) error {
			return errors.New("simulated delete failure")
		},
		createSandboxFn: func(ctx context.Context, arg store.CreateSandboxParams) (store.Sandbox, error) {
			createCalls++
			return store.Sandbox{}, nil
		},
	}

	svc := New(ms, nil)
	_, err := svc.CreateSandbox(context.Background(), CreateRequest{
		AppName: "delete-fail-app",
		UserID:  "user-123",
		Image:   "appx/sandbox:v1",
	})

	if err == nil {
		t.Fatal("expected error when DeleteSandbox fails")
	}
	if createCalls != 0 {
		t.Fatalf("expected CreateSandbox NOT to be called when delete fails (fail fast), got %d calls", createCalls)
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

// ── Phase 32 Wave 2 Bug 1: orphan cleanup ──────────────────────────
//
// CreateSandbox previously inserted a PENDING sandbox row, then ran
// scheduler + AssignSandboxToNode as separate operations. If any step
// after CreateSandbox failed (no capacity, AssignSandboxToNode error,
// transient DB error), the row was left orphaned with node_id IS NULL.
// Production state on 2026-05-07 had 4233 such orphan rows.
//
// These tests lock in the cleanup contract: when CreateSandbox cannot
// finish the schedule + assign sequence, the PENDING row MUST be
// deleted before returning the error to the caller.

func TestCreateSandbox_NoOrphanOnAssignFailure(t *testing.T) {
	nodeID := uuid.New()
	var createdID pgtype.UUID
	var deletedID pgtype.UUID
	var deleteCalls int

	ms := &mockStore{
		getSandboxByAppNameFn: func(ctx context.Context, appName string) (store.Sandbox, error) {
			return store.Sandbox{}, pgx.ErrNoRows
		},
		createSandboxFn: func(ctx context.Context, arg store.CreateSandboxParams) (store.Sandbox, error) {
			createdID = arg.ID
			return store.Sandbox{ID: arg.ID, AppName: arg.AppName, State: "pending"}, nil
		},
		listHealthyNodesFn: func(ctx context.Context) ([]store.Node, error) {
			return []store.Node{makeNode(nodeID, 24000, 0)}, nil
		},
		assignSandboxFn: func(ctx context.Context, arg store.AssignSandboxToNodeParams) (store.Sandbox, error) {
			// Simulate node going unhealthy / DB transient error mid-scheduling.
			return store.Sandbox{}, errors.New("simulated assign failure")
		},
		deleteSandboxFn: func(ctx context.Context, id pgtype.UUID) error {
			deleteCalls++
			deletedID = id
			return nil
		},
	}

	svc := New(ms, nil)
	_, err := svc.CreateSandbox(context.Background(), CreateRequest{
		AppName:   "orphan-app",
		UserID:    "user-123",
		Image:     "appx/sandbox:v1",
		Resources: Resources{CPUCores: 0.5, MemoryMB: 512},
	})

	if err == nil {
		t.Fatal("expected error from AssignSandboxToNode failure")
	}
	if deleteCalls != 1 {
		t.Fatalf("expected DeleteSandbox to be called once to clean up the PENDING row, got %d calls", deleteCalls)
	}
	if deletedID != createdID {
		t.Fatalf("expected DeleteSandbox to be called with the freshly created sandbox ID %v, got %v", createdID, deletedID)
	}
}

func TestCreateSandbox_NoOrphanOnNoCapacity(t *testing.T) {
	// scheduler.Schedule returns ErrNoCapacity when no node has enough memory.
	// The PENDING row created moments earlier must be cleaned up so
	// node_id IS NULL orphans don't accumulate.
	var createdID pgtype.UUID
	var deletedID pgtype.UUID
	var deleteCalls int

	ms := &mockStore{
		getSandboxByAppNameFn: func(ctx context.Context, appName string) (store.Sandbox, error) {
			return store.Sandbox{}, pgx.ErrNoRows
		},
		createSandboxFn: func(ctx context.Context, arg store.CreateSandboxParams) (store.Sandbox, error) {
			createdID = arg.ID
			return store.Sandbox{ID: arg.ID, AppName: arg.AppName, State: "pending"}, nil
		},
		listHealthyNodesFn: func(ctx context.Context) ([]store.Node, error) {
			// Single node with no spare capacity → scheduler returns ErrNoCapacity.
			return []store.Node{makeNode(uuid.New(), 1024, 1024)}, nil
		},
		deleteSandboxFn: func(ctx context.Context, id pgtype.UUID) error {
			deleteCalls++
			deletedID = id
			return nil
		},
	}

	svc := New(ms, nil)
	_, err := svc.CreateSandbox(context.Background(), CreateRequest{
		AppName:   "no-capacity-app",
		UserID:    "user-123",
		Image:     "appx/sandbox:v1",
		Resources: Resources{CPUCores: 0.5, MemoryMB: 4096},
	})

	if !errors.Is(err, ErrNoCapacity) {
		t.Fatalf("expected ErrNoCapacity, got %v", err)
	}
	if deleteCalls != 1 {
		t.Fatalf("expected DeleteSandbox to be called once to clean up the PENDING row, got %d calls", deleteCalls)
	}
	if deletedID != createdID {
		t.Fatalf("expected DeleteSandbox to be called with the freshly created sandbox ID %v, got %v", createdID, deletedID)
	}
}

func TestCreateSandbox_NoOrphanOnListNodesFailure(t *testing.T) {
	// Even a transient ListHealthyNodes failure between CreateSandbox and
	// scheduling must not leave the PENDING row behind.
	var createdID pgtype.UUID
	var deletedID pgtype.UUID
	var deleteCalls int

	ms := &mockStore{
		getSandboxByAppNameFn: func(ctx context.Context, appName string) (store.Sandbox, error) {
			return store.Sandbox{}, pgx.ErrNoRows
		},
		createSandboxFn: func(ctx context.Context, arg store.CreateSandboxParams) (store.Sandbox, error) {
			createdID = arg.ID
			return store.Sandbox{ID: arg.ID, AppName: arg.AppName, State: "pending"}, nil
		},
		listHealthyNodesFn: func(ctx context.Context) ([]store.Node, error) {
			return nil, errors.New("simulated transient db error")
		},
		deleteSandboxFn: func(ctx context.Context, id pgtype.UUID) error {
			deleteCalls++
			deletedID = id
			return nil
		},
	}

	svc := New(ms, nil)
	_, err := svc.CreateSandbox(context.Background(), CreateRequest{
		AppName:   "list-fail-app",
		UserID:    "user-123",
		Image:     "appx/sandbox:v1",
		Resources: Resources{CPUCores: 0.5, MemoryMB: 512},
	})

	if err == nil {
		t.Fatal("expected error from ListHealthyNodes failure")
	}
	if deleteCalls != 1 {
		t.Fatalf("expected DeleteSandbox to be called once to clean up the PENDING row, got %d calls", deleteCalls)
	}
	if deletedID != createdID {
		t.Fatalf("expected DeleteSandbox to be called with the freshly created sandbox ID %v, got %v", createdID, deletedID)
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

func TestHandleAck_StartSandboxSuccess_AlreadyRunning(t *testing.T) {
	// Race condition: HandleEvent(container_started) already transitioned
	// starting→running before this ack arrives. HandleAck should succeed.
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)

	transitionCalled := false
	var capturedRuntime store.UpdateSandboxRuntimeParams

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{ID: pgSandboxID, State: "running", AppName: "test-app"}, nil
		},
		transitionStateFn: func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
			transitionCalled = true
			return store.Sandbox{}, nil
		},
		updateSandboxRuntimeFn: func(ctx context.Context, arg store.UpdateSandboxRuntimeParams) error {
			capturedRuntime = arg
			return nil
		},
	}

	svc := New(ms, nil)
	err := svc.HandleAck(context.Background(), uuid.New(), sandboxID, "start_sandbox", "success",
		json.RawMessage(`{"container_id":"abc123","host_port":43210}`))

	if err != nil {
		t.Fatalf("expected no error for already-running sandbox, got: %v", err)
	}
	if transitionCalled {
		t.Fatal("expected no state transition when already at target state")
	}
	// Runtime info should still be persisted even when transition is skipped
	if capturedRuntime.ContainerID.String != "abc123" {
		t.Fatalf("expected container_id 'abc123', got %q", capturedRuntime.ContainerID.String)
	}
	if capturedRuntime.HostPort.Int32 != 43210 {
		t.Fatalf("expected host_port 43210, got %d", capturedRuntime.HostPort.Int32)
	}
}

func TestHandleAck_StartSandboxSuccess_PersistsRuntimeInfo(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)

	var capturedRuntime store.UpdateSandboxRuntimeParams

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{ID: pgSandboxID, State: "starting", AppName: "test-app"}, nil
		},
		updateSandboxRuntimeFn: func(ctx context.Context, arg store.UpdateSandboxRuntimeParams) error {
			capturedRuntime = arg
			return nil
		},
	}

	svc := New(ms, nil)
	err := svc.HandleAck(context.Background(), uuid.New(), sandboxID, "start_sandbox", "success",
		json.RawMessage(`{"container_id":"ctr-xyz","host_port":8081}`))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedRuntime.ContainerID.String != "ctr-xyz" {
		t.Fatalf("expected container_id 'ctr-xyz', got %q", capturedRuntime.ContainerID.String)
	}
	if capturedRuntime.HostPort.Int32 != 8081 {
		t.Fatalf("expected host_port 8081, got %d", capturedRuntime.HostPort.Int32)
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

// ── Mock RouteNotifier ─────────────────────────────────────────────

type mockRouteNotifier struct {
	runningCalls []routeRunningCall
	stoppedCalls []string // app names
}

type routeRunningCall struct {
	AppName   string
	SandboxID string
	Upstream  string
}

func (m *mockRouteNotifier) OnSandboxRunning(_ context.Context, appName, sandboxID, upstream string) error {
	m.runningCalls = append(m.runningCalls, routeRunningCall{AppName: appName, SandboxID: sandboxID, Upstream: upstream})
	return nil
}

func (m *mockRouteNotifier) OnSandboxStopped(_ context.Context, appName string) error {
	m.stoppedCalls = append(m.stoppedCalls, appName)
	return nil
}

// ── Lifecycle Route Notification Tests ─────────────────────────────

func TestLifecycle_Route_HandleAck_StartSandboxSuccess_CallsOnSandboxRunning(t *testing.T) {
	sandboxID := uuid.New()
	nodeID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)
	pgNodeID := makePgUUID(nodeID)

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{
				ID:       pgSandboxID,
				State:    "starting",
				AppName:  "test-app",
				NodeID:   pgNodeID,
				HostPort: pgtype.Int4{Int32: 8081, Valid: true},
			}, nil
		},
		getNodeByIDFn: func(ctx context.Context, id pgtype.UUID) (store.Node, error) {
			return store.Node{
				ID:          pgNodeID,
				TailscaleIp: netip.MustParseAddr("100.64.0.1"),
			}, nil
		},
	}

	rn := &mockRouteNotifier{}
	svc := New(ms, nil)
	svc.SetRouteNotifier(rn)

	err := svc.HandleAck(context.Background(), uuid.New(), sandboxID, "start_sandbox", "success",
		json.RawMessage(`{"container_id":"abc123","host_port":8081}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(rn.runningCalls) != 1 {
		t.Fatalf("expected 1 OnSandboxRunning call, got %d", len(rn.runningCalls))
	}
	call := rn.runningCalls[0]
	if call.AppName != "test-app" {
		t.Errorf("appName = %q, want test-app", call.AppName)
	}
	if call.Upstream != "100.64.0.1:8081" {
		t.Errorf("upstream = %q, want 100.64.0.1:8081", call.Upstream)
	}
	if call.SandboxID != sandboxID.String() {
		t.Errorf("sandboxID = %q, want %s", call.SandboxID, sandboxID.String())
	}
}

func TestLifecycle_Route_HandleAck_StopSandboxSuccess_CallsOnSandboxStopped(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)
	nodeID := uuid.New()
	pgNodeID := makePgUUID(nodeID)

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{
				ID:      pgSandboxID,
				State:   "destroying",
				AppName: "test-app",
				NodeID:  pgNodeID,
			}, nil
		},
	}

	rn := &mockRouteNotifier{}
	svc := New(ms, nil)
	svc.SetRouteNotifier(rn)

	err := svc.HandleAck(context.Background(), uuid.New(), sandboxID, "stop_sandbox", "success", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(rn.stoppedCalls) != 1 {
		t.Fatalf("expected 1 OnSandboxStopped call, got %d", len(rn.stoppedCalls))
	}
	if rn.stoppedCalls[0] != "test-app" {
		t.Errorf("appName = %q, want test-app", rn.stoppedCalls[0])
	}
}

func TestLifecycle_Route_HandleEvent_ContainerExitedFromRunning_CallsOnSandboxStopped(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)
	nodeID := uuid.New()
	pgNodeID := makePgUUID(nodeID)

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{
				ID:      pgSandboxID,
				State:   "running",
				AppName: "test-app",
				NodeID:  pgNodeID,
			}, nil
		},
	}

	rn := &mockRouteNotifier{}
	svc := New(ms, nil)
	svc.SetRouteNotifier(rn)

	err := svc.HandleEvent(context.Background(), nodeID, sandboxID, "container_exited",
		json.RawMessage(`{"exit_code":137}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(rn.stoppedCalls) != 1 {
		t.Fatalf("expected 1 OnSandboxStopped call, got %d", len(rn.stoppedCalls))
	}
	if rn.stoppedCalls[0] != "test-app" {
		t.Errorf("appName = %q, want test-app", rn.stoppedCalls[0])
	}
}

func TestLifecycle_Route_HandleEvent_ContainerStarted_DoesNotCallOnSandboxStopped(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)
	nodeID := uuid.New()

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{
				ID:      pgSandboxID,
				State:   "starting",
				AppName: "test-app",
			}, nil
		},
	}

	rn := &mockRouteNotifier{}
	svc := New(ms, nil)
	svc.SetRouteNotifier(rn)

	err := svc.HandleEvent(context.Background(), nodeID, sandboxID, "container_started", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// container_started via HandleEvent should NOT trigger OnSandboxStopped
	if len(rn.stoppedCalls) != 0 {
		t.Fatalf("expected 0 OnSandboxStopped calls, got %d", len(rn.stoppedCalls))
	}
	// Also should NOT trigger OnSandboxRunning (only HandleAck triggers route adds)
	if len(rn.runningCalls) != 0 {
		t.Fatalf("expected 0 OnSandboxRunning calls, got %d", len(rn.runningCalls))
	}
}

func TestLifecycle_Route_NilRouteNotifier_DoesNotPanic(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)
	nodeID := uuid.New()
	pgNodeID := makePgUUID(nodeID)

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{
				ID:       pgSandboxID,
				State:    "starting",
				AppName:  "test-app",
				NodeID:   pgNodeID,
				HostPort: pgtype.Int4{Int32: 8081, Valid: true},
			}, nil
		},
	}

	// No SetRouteNotifier call -- routeNotifier is nil
	svc := New(ms, nil)

	// Should not panic
	err := svc.HandleAck(context.Background(), uuid.New(), sandboxID, "start_sandbox", "success",
		json.RawMessage(`{"container_id":"abc123","host_port":8081}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
