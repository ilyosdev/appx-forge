package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/appx/forge/control/internal/store"
)

// ── Mock Store ───────────────────────────────────────────────────────

type mockStore struct {
	createSandboxFn            func(ctx context.Context, arg store.CreateSandboxParams) (store.Sandbox, error)
	deleteSandboxFn            func(ctx context.Context, id pgtype.UUID) error
	getSandboxFn               func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error)
	getSandboxByAppNameFn      func(ctx context.Context, appName string) (store.Sandbox, error)
	listHealthyNodesFn         func(ctx context.Context) ([]store.Node, error)
	assignSandboxFn            func(ctx context.Context, arg store.AssignSandboxToNodeParams) (store.Sandbox, error)
	transitionStateFn          func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error)
	updateSandboxRuntimeFn     func(ctx context.Context, arg store.UpdateSandboxRuntimeParams) error
	updateSandboxLastActiveFn  func(ctx context.Context, id pgtype.UUID) error
	createCommandFn            func(ctx context.Context, arg store.CreateCommandParams) (store.Command, error)
	ackCommandFn               func(ctx context.Context, arg store.AckCommandParams) error
	recordEventFn              func(ctx context.Context, arg store.RecordEventParams) (store.Event, error)
	getNodeByIDFn              func(ctx context.Context, id pgtype.UUID) (store.Node, error)
	deleteCommandsForSandboxFn func(ctx context.Context, sandboxID pgtype.UUID) error
	countSchedulableFn         func(ctx context.Context, nodeID pgtype.UUID) (int32, error)
	assignUnderCapFn           func(ctx context.Context, nodeID, sandboxID pgtype.UUID, cap int32) (bool, store.Sandbox, error)
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

// Phase 33-Real-8 — mock the new commands purge.
func (m *mockStore) DeleteCommandsForSandbox(ctx context.Context, sandboxID pgtype.UUID) error {
	if m.deleteCommandsForSandboxFn != nil {
		return m.deleteCommandsForSandboxFn(ctx, sandboxID)
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

func (m *mockStore) UpdateSandboxLastActive(ctx context.Context, id pgtype.UUID) error {
	if m.updateSandboxLastActiveFn != nil {
		return m.updateSandboxLastActiveFn(ctx, id)
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

func (m *mockStore) CountSchedulableSandboxesByNode(ctx context.Context, nodeID pgtype.UUID) (int32, error) {
	if m.countSchedulableFn != nil {
		return m.countSchedulableFn(ctx, nodeID)
	}
	return 0, nil
}

func (m *mockStore) AssignSandboxToNodeUnderCap(ctx context.Context, nodeID, sandboxID pgtype.UUID, cap int32) (bool, store.Sandbox, error) {
	if m.assignUnderCapFn != nil {
		return m.assignUnderCapFn(ctx, nodeID, sandboxID, cap)
	}
	// Default: behave like the real query with no contention — assign and
	// return the starting row (cap never exceeded in the default path).
	return true, store.Sandbox{ID: sandboxID, State: "starting", NodeID: nodeID}, nil
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

// TestCreateSandbox_CountCap_SequentialUsesLiveDBCount proves the per-node cap
// holds across SEQUENTIAL creates even when the heartbeat count is stale. The
// single node reports running_containers=0 from the heartbeat AND has abundant
// free RAM — so the heartbeat-derived cap would NEVER trip. The fix sources the
// count from the authoritative DB count (checked atomically inside the
// conditional assign), which rises as each create assigns a row. With the cap
// at 3, the 4th create must be rejected.
//
// NOTE: this test exercises the SERIAL/heartbeat-staleness path only — each
// create fully completes before the next begins, so it does NOT cover the
// concurrent-burst race. That is covered by
// TestCreateSandbox_CountCap_ConcurrentBurstHoldsCap below, which fires creates
// in parallel against a mutex-guarded model of the atomic advisory-locked
// assign.
func TestCreateSandbox_CountCap_SequentialUsesLiveDBCount(t *testing.T) {
	nodeID := uuid.New()

	// liveCount models the DB's authoritative non-terminal sandbox count for
	// the node — it does NOT depend on the heartbeat. The atomic conditional
	// assign both checks it against the cap and increments it on success,
	// exactly as the real advisory-locked UPDATE ... WHERE (count) < cap does.
	var liveCount int32

	ms := &mockStore{
		listHealthyNodesFn: func(ctx context.Context) ([]store.Node, error) {
			// Heartbeat says 0 running and tons of free RAM — neither the
			// stale count nor the RAM check would ever reject. Only the
			// DB-derived count cap can.
			n := makeNode(nodeID, 64000, 0)
			n.RunningContainers = 0
			return []store.Node{n}, nil
		},
		countSchedulableFn: func(ctx context.Context, _ pgtype.UUID) (int32, error) {
			return liveCount, nil
		},
		assignUnderCapFn: func(ctx context.Context, nID, sID pgtype.UUID, cap int32) (bool, store.Sandbox, error) {
			if liveCount >= cap {
				return false, store.Sandbox{}, nil // node at/over cap
			}
			liveCount++ // row committed to the node
			return true, store.Sandbox{ID: sID, State: "starting", NodeID: nID}, nil
		},
	}

	svc := New(ms, nil)
	svc.SetMaxSandboxesPerNode(3)

	// First 3 creates must succeed.
	for i := 0; i < 3; i++ {
		_, err := svc.CreateSandbox(context.Background(), CreateRequest{
			AppName:   "burst-app",
			UserID:    "user-1",
			Image:     "appx/sandbox:v1",
			Resources: Resources{MemoryMB: 512},
		})
		if err != nil {
			t.Fatalf("create %d should succeed under cap (live count rising), got %v", i, err)
		}
	}

	// 4th create: DB count is now 3 == cap. Must be rejected even though the
	// heartbeat still reports 0 running and RAM is plentiful.
	_, err := svc.CreateSandbox(context.Background(), CreateRequest{
		AppName:   "burst-app",
		UserID:    "user-1",
		Image:     "appx/sandbox:v1",
		Resources: Resources{MemoryMB: 512},
	})
	if err != ErrNoCapacity {
		t.Fatalf("4th create should be rejected with ErrNoCapacity at cap, got %v", err)
	}
}

// TestCreateSandbox_CountCap_ConcurrentBurstHoldsCap proves the per-node cap
// holds under a genuinely CONCURRENT provision burst — the exact TOCTOU window
// the plain check-then-act count cap leaves open (N goroutines each read
// count=cap-1 before any assigns, all pass, all overshoot).
//
// The mock models the real defense: the atomic advisory-locked assign
// (storeAdapter.AssignSandboxToNodeUnderCap) serializes the count→assign per
// node and re-checks the live count under that lock. Here a sync.Mutex stands
// in for the per-node advisory lock + the conditional UPDATE's same-snapshot
// recount. With the cap at 5 and 50 concurrent creates, EXACTLY 5 must be
// admitted and the rest rejected with ErrNoCapacity — never an overshoot. Run
// under `go test -race` to catch any unsynchronised access.
func TestCreateSandbox_CountCap_ConcurrentBurstHoldsCap(t *testing.T) {
	const cap = 5
	const creates = 50

	nodeID := uuid.New()

	var mu sync.Mutex // models the per-node advisory lock
	var liveCount int32

	ms := &mockStore{
		listHealthyNodesFn: func(ctx context.Context) ([]store.Node, error) {
			// Heartbeat is stale (0 running) and RAM is plentiful, so only the
			// atomic cap re-check can prevent overshoot.
			n := makeNode(nodeID, 256000, 0)
			n.RunningContainers = 0
			return []store.Node{n}, nil
		},
		countSchedulableFn: func(ctx context.Context, _ pgtype.UUID) (int32, error) {
			// Best-effort pre-check count used only to build candidates; the
			// authoritative decision is made atomically in assignUnderCapFn.
			mu.Lock()
			defer mu.Unlock()
			return liveCount, nil
		},
		assignUnderCapFn: func(ctx context.Context, nID, sID pgtype.UUID, c int32) (bool, store.Sandbox, error) {
			// Critical section = advisory lock held across recount + assign.
			mu.Lock()
			defer mu.Unlock()
			if liveCount >= c {
				return false, store.Sandbox{}, nil
			}
			liveCount++
			return true, store.Sandbox{ID: sID, State: "starting", NodeID: nID}, nil
		},
	}

	svc := New(ms, nil)
	svc.SetMaxSandboxesPerNode(cap)

	var (
		wg        sync.WaitGroup
		successes int32
		resultMu  sync.Mutex
	)
	for i := 0; i < creates; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.CreateSandbox(context.Background(), CreateRequest{
				AppName:   "burst-app",
				UserID:    "user-1",
				Image:     "appx/sandbox:v1",
				Resources: Resources{MemoryMB: 512},
			})
			if err == nil {
				resultMu.Lock()
				successes++
				resultMu.Unlock()
			} else if err != ErrNoCapacity {
				t.Errorf("unexpected error from concurrent create: %v", err)
			}
		}()
	}
	wg.Wait()

	if successes != cap {
		t.Fatalf("cap overshoot/undershoot: admitted %d, want exactly %d", successes, cap)
	}
	mu.Lock()
	final := liveCount
	mu.Unlock()
	if final != cap {
		t.Fatalf("live count after burst = %d, want %d (cap must hold)", final, cap)
	}
}

// TestCreateSandbox_CountCap_DisabledSkipsDBCount proves that when the cap is
// off (the default for a freshly-constructed service), CreateSandbox does NOT
// query the authoritative count at all — no extra DB round-trip on the hot path.
func TestCreateSandbox_CountCap_DisabledSkipsDBCount(t *testing.T) {
	nodeID := uuid.New()
	countCalled := false

	ms := &mockStore{
		listHealthyNodesFn: func(ctx context.Context) ([]store.Node, error) {
			return []store.Node{makeNode(nodeID, 24000, 0)}, nil
		},
		countSchedulableFn: func(ctx context.Context, _ pgtype.UUID) (int32, error) {
			countCalled = true
			return 0, nil
		},
	}

	svc := New(ms, nil) // cap defaults to 0 (off)
	_, err := svc.CreateSandbox(context.Background(), CreateRequest{
		AppName:   "no-cap-app",
		UserID:    "user-1",
		Image:     "appx/sandbox:v1",
		Resources: Resources{MemoryMB: 512},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if countCalled {
		t.Error("CountSchedulableSandboxesByNode must NOT be called when cap is disabled")
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
		AppName:   "recycled-app",
		UserID:    "user-123",
		Image:     "appx/sandbox:v1",
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
		AppName:   "failed-app",
		UserID:    "user-123",
		Image:     "appx/sandbox:v1",
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
		AppName:   "fresh-app",
		UserID:    "user-123",
		Image:     "appx/sandbox:v1",
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

// Phase 32 Wave 2 Bug 6 — start_sandbox+success ack arrives in StateDestroying.
// Race: backend's eager verify-after-create marks sandbox ERROR before agent
// has executed the start; control transitions row to destroying. The start
// ack (with valid container_id) lands in destroying. Without the bug6
// branch, HandleAck would return ErrInvalidTransition (HTTP 500) and the
// cmd row stays "dispatched" forever while a container leaks on the agent.
//
// Expectation: HandleAck returns nil, persists runtime info, dispatches a
// cleanup stop_sandbox carrying the just-revealed container_id, and acks
// the original cmd as completed (no state change — sandbox stays
// destroying).
func TestHandleAck_StartSandboxSuccess_DuringDestroying_Bug6(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)
	nodeID := uuid.New()
	pgNodeID := pgtype.UUID{Bytes: nodeID, Valid: true}

	// First GetSandbox: row is destroying without container_id (because the
	// original stop dispatched while container_id was empty). Second
	// GetSandbox (after persistRuntimeInfo): row now has container_id.
	getCalls := 0
	var capturedRuntime store.UpdateSandboxRuntimeParams
	transitionCalled := false
	var capturedCleanupCmd store.CreateCommandParams
	cleanupCmdCalls := 0
	var capturedAck store.AckCommandParams

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			getCalls++
			if getCalls == 1 {
				return store.Sandbox{
					ID: pgSandboxID, State: "destroying", AppName: "test-app",
					NodeID: pgNodeID,
				}, nil
			}
			return store.Sandbox{
				ID: pgSandboxID, State: "destroying", AppName: "test-app",
				NodeID:      pgNodeID,
				ContainerID: pgtype.Text{String: "ctr-late-success", Valid: true},
				HostPort:    pgtype.Int4{Int32: 41234, Valid: true},
			}, nil
		},
		updateSandboxRuntimeFn: func(ctx context.Context, arg store.UpdateSandboxRuntimeParams) error {
			capturedRuntime = arg
			return nil
		},
		transitionStateFn: func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
			transitionCalled = true
			return store.Sandbox{}, nil
		},
		createCommandFn: func(ctx context.Context, arg store.CreateCommandParams) (store.Command, error) {
			capturedCleanupCmd = arg
			cleanupCmdCalls++
			return store.Command{ID: arg.ID, CommandType: arg.CommandType}, nil
		},
		ackCommandFn: func(ctx context.Context, arg store.AckCommandParams) error {
			capturedAck = arg
			return nil
		},
	}

	svc := New(ms, nil)
	err := svc.HandleAck(context.Background(), uuid.New(), sandboxID,
		"start_sandbox", "success",
		json.RawMessage(`{"container_id":"ctr-late-success","host_port":41234}`))

	if err != nil {
		t.Fatalf("expected nil error (bug6 tolerance), got: %v", err)
	}
	if transitionCalled {
		t.Fatal("expected no state transition (sandbox stays destroying)")
	}
	if capturedRuntime.ContainerID.String != "ctr-late-success" {
		t.Fatalf("expected runtime container_id 'ctr-late-success', got %q", capturedRuntime.ContainerID.String)
	}
	if cleanupCmdCalls != 1 {
		t.Fatalf("expected 1 cleanup stop_sandbox dispatched, got %d", cleanupCmdCalls)
	}
	if capturedCleanupCmd.CommandType != "stop_sandbox" {
		t.Fatalf("expected cleanup CommandType 'stop_sandbox', got %q", capturedCleanupCmd.CommandType)
	}
	if capturedCleanupCmd.NodeID != pgNodeID {
		t.Fatal("expected cleanup cmd targeted at sandbox's node")
	}
	var payload struct {
		ContainerID string `json:"container_id"`
	}
	if err := json.Unmarshal(capturedCleanupCmd.Payload, &payload); err != nil {
		t.Fatalf("cleanup cmd payload unmarshal: %v", err)
	}
	if payload.ContainerID != "ctr-late-success" {
		t.Fatalf("expected cleanup payload container_id 'ctr-late-success', got %q", payload.ContainerID)
	}
	if capturedAck.Status != "completed" {
		t.Fatalf("expected original cmd ack status 'completed', got %q", capturedAck.Status)
	}
}

// Bug6 sibling — start_sandbox+success arriving in destroyed (terminal)
// should also tolerate without 500. No cleanup cmd needed if container_id
// already empty (sandbox was destroyed before the start could complete on
// any container).
func TestHandleAck_StartSandboxSuccess_DuringDestroyed_Bug6(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)

	transitionCalled := false
	cleanupDispatched := false
	var capturedAck store.AckCommandParams

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{
				ID: pgSandboxID, State: "destroyed", AppName: "test-app",
				ContainerID: pgtype.Text{String: "ctr-final", Valid: true},
				NodeID:      pgtype.UUID{Bytes: uuid.New(), Valid: true},
			}, nil
		},
		transitionStateFn: func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
			transitionCalled = true
			return store.Sandbox{}, nil
		},
		createCommandFn: func(ctx context.Context, arg store.CreateCommandParams) (store.Command, error) {
			cleanupDispatched = true
			return store.Command{}, nil
		},
		ackCommandFn: func(ctx context.Context, arg store.AckCommandParams) error {
			capturedAck = arg
			return nil
		},
	}

	svc := New(ms, nil)
	err := svc.HandleAck(context.Background(), uuid.New(), sandboxID,
		"start_sandbox", "success",
		json.RawMessage(`{"container_id":"ctr-final","host_port":40000}`))

	if err != nil {
		t.Fatalf("expected nil error in destroyed state (bug6), got: %v", err)
	}
	if transitionCalled {
		t.Fatal("expected no state transition from destroyed")
	}
	if !cleanupDispatched {
		t.Fatal("expected cleanup stop_sandbox dispatched (container_id known)")
	}
	if capturedAck.Status != "completed" {
		t.Fatalf("expected ack 'completed', got %q", capturedAck.Status)
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

// Phase 32 Wave 2 Bug 2 — orphan sandbox short-circuit.
//
// When a sandbox has no node_id (Valid=false), DestroySandbox previously
// passed pgtype.UUID{Valid:false} into commands.node_id, which is
// declared NOT NULL → Postgres rejected with
// "null value in column \"node_id\"". Each rejection bubbled up as a
// 500 to the agent ack loop. Today's storm produced 32+ such errors.
//
// The fix short-circuits: if NodeID is invalid, skip CreateCommand
// entirely and mark state=destroyed directly (no node exists to
// receive the command anyway).
func TestDestroySandbox_OrphanShortCircuit(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)

	var capturedTransition store.TransitionSandboxStateParams
	createCommandCalled := false

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			// Orphan sandbox: NodeID.Valid == false.
			return store.Sandbox{
				ID:      pgSandboxID,
				State:   "pending",
				AppName: "orphan-app",
				NodeID:  pgtype.UUID{Valid: false},
			}, nil
		},
		transitionStateFn: func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
			capturedTransition = arg
			return store.Sandbox{ID: pgSandboxID, State: arg.State}, nil
		},
		createCommandFn: func(ctx context.Context, arg store.CreateCommandParams) (store.Command, error) {
			createCommandCalled = true
			return store.Command{ID: arg.ID}, nil
		},
	}

	svc := New(ms, nil)
	err := svc.DestroySandbox(context.Background(), sandboxID)

	if err != nil {
		t.Fatalf("expected success on orphan, got %v", err)
	}

	// Must NOT issue a stop_sandbox command — no node to receive it.
	if createCommandCalled {
		t.Fatal("expected CreateCommand NOT to be called on orphan sandbox")
	}

	// Must transition directly to destroyed.
	if capturedTransition.State != "destroyed" {
		t.Fatalf("expected transition to 'destroyed', got %q", capturedTransition.State)
	}
	// Guard: the transition must be from pending (the orphan's prior state)
	// so Postgres' optimistic-concurrency WHERE state = $3 still applies.
	if capturedTransition.State_2 != "pending" {
		t.Fatalf("expected transition guard from 'pending', got %q", capturedTransition.State_2)
	}
}

// ── SleepSandbox Tests ──────────────────────────────────────────────

// SleepSandbox stops a running sandbox without destroying it: running→stopped
// (NOT destroying) + a stop_sandbox command, so a later wake can revive it.
func TestSleepSandbox_HappyPath(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)
	nodeID := uuid.New()
	pgNodeID := makePgUUID(nodeID)

	var capturedTransition store.TransitionSandboxStateParams
	var capturedCmd store.CreateCommandParams

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{
				ID:          pgSandboxID,
				State:       "running",
				AppName:     "test-app",
				NodeID:      pgNodeID,
				ContainerID: pgtype.Text{String: "container-abc", Valid: true},
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
	if err := svc.SleepSandbox(context.Background(), sandboxID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Lands in stopped (wake-able), NOT destroying/destroyed.
	if capturedTransition.State != "stopped" {
		t.Fatalf("expected transition to 'stopped', got %q", capturedTransition.State)
	}
	if capturedTransition.State_2 != "running" {
		t.Fatalf("expected transition guard from 'running', got %q", capturedTransition.State_2)
	}
	// Stop command issued to the sandbox's node.
	if capturedCmd.CommandType != "stop_sandbox" {
		t.Fatalf("expected command type 'stop_sandbox', got %q", capturedCmd.CommandType)
	}
	if capturedCmd.NodeID != pgNodeID {
		t.Fatal("expected stop command to target the sandbox's node")
	}
	// Sleep must carry mode=stop — a mode-less payload makes the agent
	// DESTROY the container (rm + workdir + port release) while the row
	// stays 'stopped', so the later wake can only cold-create.
	var payload map[string]interface{}
	if err := json.Unmarshal(capturedCmd.Payload, &payload); err != nil {
		t.Fatalf("bad payload: %v", err)
	}
	if payload["mode"] != "stop" {
		t.Fatalf("expected sleep payload mode=stop, got %s", capturedCmd.Payload)
	}
}

// Idempotent: sleeping an already-stopped sandbox is a no-op success (no
// transition, no command).
func TestSleepSandbox_AlreadyStopped_NoOp(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)

	transitionCalled := false
	createCommandCalled := false

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{ID: pgSandboxID, State: "stopped", AppName: "asleep-app"}, nil
		},
		transitionStateFn: func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
			transitionCalled = true
			return store.Sandbox{}, nil
		},
		createCommandFn: func(ctx context.Context, arg store.CreateCommandParams) (store.Command, error) {
			createCommandCalled = true
			return store.Command{ID: arg.ID}, nil
		},
	}

	svc := New(ms, nil)
	if err := svc.SleepSandbox(context.Background(), sandboxID); err != nil {
		t.Fatalf("expected no-op success on already-stopped, got %v", err)
	}
	if transitionCalled {
		t.Fatal("expected NO state transition on already-stopped sandbox")
	}
	if createCommandCalled {
		t.Fatal("expected NO stop command on already-stopped sandbox")
	}
}

// A non-running, non-stopped sandbox (e.g. starting) cannot be slept — returns
// ErrInvalidState so the API surfaces a 409 rather than corrupting state.
func TestSleepSandbox_NotRunning_InvalidState(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{ID: pgSandboxID, State: "starting", AppName: "booting-app"}, nil
		},
	}

	svc := New(ms, nil)
	err := svc.SleepSandbox(context.Background(), sandboxID)
	if err == nil {
		t.Fatal("expected error sleeping a non-running sandbox")
	}
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("expected ErrInvalidState, got %v", err)
	}
}

// ── Sleep-not-destroy fixes (2026-06-12) ────────────────────────────

// Wake must bump last_active_at atomically with the start dispatch —
// otherwise the idle reaper re-selects the woken sandbox on its next tick
// when the post-wake file push fails (the only other bump site).
func TestWakeSandbox_BumpsLastActive(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)
	pgNodeID := makePgUUID(uuid.New())

	bumpedID := pgtype.UUID{}
	bumpCalls := 0

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{
				ID:      pgSandboxID,
				State:   "stopped",
				AppName: "slept-app",
				NodeID:  pgNodeID,
				Image:   "appx/sandbox:v12",
			}, nil
		},
		updateSandboxLastActiveFn: func(ctx context.Context, id pgtype.UUID) error {
			bumpCalls++
			bumpedID = id
			return nil
		},
	}

	svc := New(ms, nil)
	if err := svc.WakeSandbox(context.Background(), sandboxID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bumpCalls != 1 {
		t.Fatalf("expected exactly 1 last_active_at bump on wake, got %d", bumpCalls)
	}
	if bumpedID != pgSandboxID {
		t.Fatal("expected bump to target the woken sandbox")
	}
}

// A failed bump must never fail the wake — the start-ack bump is the
// second chance.
func TestWakeSandbox_BumpFailure_StillWakes(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)
	pgNodeID := makePgUUID(uuid.New())

	var capturedCmd store.CreateCommandParams

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{
				ID: pgSandboxID, State: "stopped", AppName: "slept-app", NodeID: pgNodeID,
			}, nil
		},
		updateSandboxLastActiveFn: func(ctx context.Context, id pgtype.UUID) error {
			return errors.New("pg hiccup")
		},
		createCommandFn: func(ctx context.Context, arg store.CreateCommandParams) (store.Command, error) {
			capturedCmd = arg
			return store.Command{ID: arg.ID}, nil
		},
	}

	svc := New(ms, nil)
	if err := svc.WakeSandbox(context.Background(), sandboxID); err != nil {
		t.Fatalf("wake must tolerate a failed activity bump, got: %v", err)
	}
	if capturedCmd.CommandType != "start_sandbox" {
		t.Fatalf("expected start_sandbox dispatched despite bump failure, got %q", capturedCmd.CommandType)
	}
}

// ── Wake idempotency (concurrent racers: Caddy / sync / auto-revive) ──

// A slept sandbox is woken by several racers at once. The first flips
// stopped→starting and dispatches the start command; a second racing wake
// arriving on an already-starting/running/restarting row must be a no-op
// success (mirrors SleepSandbox no-op'ing already-stopped) — never a 500 —
// and must NOT dispatch a second start_sandbox command.
func TestWakeSandbox_AlreadyWaking_NoOp(t *testing.T) {
	for _, state := range []string{"starting", "running", "restarting"} {
		t.Run(state, func(t *testing.T) {
			sandboxID := uuid.New()
			pgSandboxID := makePgUUID(sandboxID)

			transitionCalls := 0
			commandCalls := 0
			bumpCalls := 0

			ms := &mockStore{
				getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
					return store.Sandbox{ID: pgSandboxID, State: state, AppName: "raced-app"}, nil
				},
				transitionStateFn: func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
					transitionCalls++
					return store.Sandbox{ID: pgSandboxID, State: arg.State}, nil
				},
				createCommandFn: func(ctx context.Context, arg store.CreateCommandParams) (store.Command, error) {
					commandCalls++
					return store.Command{ID: arg.ID}, nil
				},
				updateSandboxLastActiveFn: func(ctx context.Context, id pgtype.UUID) error {
					bumpCalls++
					return nil
				},
			}

			svc := New(ms, nil)
			if err := svc.WakeSandbox(context.Background(), sandboxID); err != nil {
				t.Fatalf("racing wake on %s must be a no-op success, got: %v", state, err)
			}
			if transitionCalls != 0 {
				t.Fatalf("no-op wake must not transition state, got %d transitions", transitionCalls)
			}
			if commandCalls != 0 {
				t.Fatalf("no-op wake must not double-dispatch start_sandbox, got %d commands", commandCalls)
			}
			// Still refresh activity so a wake landing on a running sandbox
			// keeps it off the reaper's next tick.
			if bumpCalls != 1 {
				t.Fatalf("expected 1 last_active_at bump on idempotent wake, got %d", bumpCalls)
			}
		})
	}
}

// Truly-unwakeable states (destroyed/destroying/failed/pending) must still
// reject with ErrInvalidState — idempotency only covers wake-equivalent states.
func TestWakeSandbox_IllegalState_StillErrors(t *testing.T) {
	for _, state := range []string{"destroyed", "destroying", "failed", "pending"} {
		t.Run(state, func(t *testing.T) {
			sandboxID := uuid.New()
			pgSandboxID := makePgUUID(sandboxID)

			ms := &mockStore{
				getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
					return store.Sandbox{ID: pgSandboxID, State: state, AppName: "dead-app"}, nil
				},
			}

			svc := New(ms, nil)
			err := svc.WakeSandbox(context.Background(), sandboxID)
			if !errors.Is(err, ErrInvalidState) {
				t.Fatalf("wake on %s must return ErrInvalidState, got: %v", state, err)
			}
		})
	}
}

// The stopped→starting transition can lose the optimistic-lock race to a
// concurrent wake writer (pgx.ErrNoRows). Mirror HandleAck: re-read, and if
// the row already advanced into a wake-equivalent state, treat as success.
func TestWakeSandbox_TransitionRaceLost_TreatedAsSuccess(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)

	getCalls := 0
	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			getCalls++
			// First read sees stopped; the re-read after the lost CAS sees the
			// concurrent writer's advance to running.
			if getCalls == 1 {
				return store.Sandbox{ID: pgSandboxID, State: "stopped", AppName: "raced-app"}, nil
			}
			return store.Sandbox{ID: pgSandboxID, State: "running", AppName: "raced-app"}, nil
		},
		transitionStateFn: func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
			return store.Sandbox{}, pgx.ErrNoRows
		},
		createCommandFn: func(ctx context.Context, arg store.CreateCommandParams) (store.Command, error) {
			t.Fatal("lost-race wake must not dispatch a start command")
			return store.Command{}, nil
		},
	}

	svc := New(ms, nil)
	if err := svc.WakeSandbox(context.Background(), sandboxID); err != nil {
		t.Fatalf("lost CAS with concurrent advance to running must be success, got: %v", err)
	}
}

// A lost CAS whose re-read lands somewhere genuinely wrong (e.g. destroyed)
// must still surface the transition error rather than swallow it.
func TestWakeSandbox_TransitionRaceLost_UnexpectedState_Errors(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)

	getCalls := 0
	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			getCalls++
			if getCalls == 1 {
				return store.Sandbox{ID: pgSandboxID, State: "stopped", AppName: "raced-app"}, nil
			}
			return store.Sandbox{ID: pgSandboxID, State: "destroyed", AppName: "raced-app"}, nil
		},
		transitionStateFn: func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
			return store.Sandbox{}, pgx.ErrNoRows
		},
	}

	svc := New(ms, nil)
	if err := svc.WakeSandbox(context.Background(), sandboxID); err == nil {
		t.Fatal("lost CAS landing on destroyed must surface an error")
	}
}

// Start acks refresh activity (covers crash-restarts and the wake's
// stopped→starting→running tail); stop acks must NOT.
func TestHandleAck_StartBumpsLastActive_StopDoesNot(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)

	state := "starting"
	bumpCalls := 0

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{ID: pgSandboxID, State: state, AppName: "test-app"}, nil
		},
		transitionStateFn: func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
			return store.Sandbox{ID: pgSandboxID, State: arg.State}, nil
		},
		updateSandboxLastActiveFn: func(ctx context.Context, id pgtype.UUID) error {
			bumpCalls++
			return nil
		},
	}

	svc := New(ms, nil)
	if err := svc.HandleAck(context.Background(), uuid.New(), sandboxID, "start_sandbox", "success",
		json.RawMessage(`{"container_id":"abc","host_port":40001}`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bumpCalls != 1 {
		t.Fatalf("expected 1 last_active_at bump on start ack, got %d", bumpCalls)
	}

	// stop_sandbox ack on a slept row: NO bump (a stop must never extend
	// the idle window or look like activity).
	state = "stopped"
	if err := svc.HandleAck(context.Background(), uuid.New(), sandboxID, "stop_sandbox", "success",
		json.RawMessage(`{"slept":true}`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bumpCalls != 1 {
		t.Fatalf("stop ack must not bump last_active_at (got %d bumps)", bumpCalls)
	}
}

// Real-8 inline row delete must SKIP slept containers: the row is the only
// control-plane handle to the kept, wakeable container.
func TestHandleAck_StopSandboxSuccess_SleptAck_KeepsRow(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)

	deleteCalled := false
	deleteCmdsCalled := false

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{
				ID:      pgSandboxID,
				State:   "stopped",
				AppName: "pool-slept",
				// Even a POSITIVELY pool-classified row survives when the
				// agent says it slept the container.
				Metadata: []byte(`{"appx.pool":"true"}`),
			}, nil
		},
		deleteSandboxFn: func(ctx context.Context, id pgtype.UUID) error {
			deleteCalled = true
			return nil
		},
		deleteCommandsForSandboxFn: func(ctx context.Context, sandboxID pgtype.UUID) error {
			deleteCmdsCalled = true
			return nil
		},
	}

	svc := New(ms, nil)
	if err := svc.HandleAck(context.Background(), uuid.New(), sandboxID, "stop_sandbox", "success",
		json.RawMessage(`{"slept":true}`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deleteCalled || deleteCmdsCalled {
		t.Fatal("slept sandbox row must NOT be deleted on stop ack (it is wakeable)")
	}
}

// The Real-8 inline cleanup still fires for true pool destroys (mode-less
// stop, ack without slept:true) so warm-rotation rows don't accumulate.
func TestHandleAck_StopSandboxSuccess_PoolDestroyAck_DeletesRow(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)

	deleteCalled := false

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{
				ID:       pgSandboxID,
				State:    "stopped",
				AppName:  "pool-fungible",
				Metadata: []byte(`{"appx.pool":"true"}`),
			}, nil
		},
		deleteSandboxFn: func(ctx context.Context, id pgtype.UUID) error {
			deleteCalled = true
			return nil
		},
	}

	svc := New(ms, nil)
	if err := svc.HandleAck(context.Background(), uuid.New(), sandboxID, "stop_sandbox", "success",
		json.RawMessage(`{}`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !deleteCalled {
		t.Fatal("expected Real-8 inline delete for a destroyed pool sandbox")
	}
}

// Claimed-before-tag-at-claim rows (no appx.projectId, no appx.pool) must
// never be row-deleted on a stop ack — fail-safe classification.
func TestHandleAck_StopSandboxSuccess_UntaggedClaimed_KeepsRow(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)

	deleteCalled := false

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{
				ID:       pgSandboxID,
				State:    "stopped",
				AppName:  "pool-claimed-pre-fix",
				Metadata: []byte(`{"appx.managed":"true","appx.appName":"pool-claimed-pre-fix"}`),
			}, nil
		},
		deleteSandboxFn: func(ctx context.Context, id pgtype.UUID) error {
			deleteCalled = true
			return nil
		},
	}

	svc := New(ms, nil)
	if err := svc.HandleAck(context.Background(), uuid.New(), sandboxID, "stop_sandbox", "success",
		json.RawMessage(`{}`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deleteCalled {
		t.Fatal("untagged (claimed-pre-fix) sandbox row must NOT be deleted on stop ack")
	}
}

// container_exited arriving while the row is stopped/destroying/destroyed is
// the expected tail of an agent-initiated docker stop: legal no-op, audit
// event recorded, NO TransitionSandboxState call (a self-loop would bump
// updated_at and reset the stopped-retention clock).
func TestHandleEvent_ContainerExitedAfterStop_NoOp(t *testing.T) {
	for _, state := range []string{"stopped", "destroying", "destroyed"} {
		t.Run(state, func(t *testing.T) {
			sandboxID := uuid.New()
			pgSandboxID := makePgUUID(sandboxID)

			transitionCalled := false
			var recorded store.RecordEventParams

			ms := &mockStore{
				getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
					return store.Sandbox{ID: pgSandboxID, State: state, AppName: "slept-app"}, nil
				},
				transitionStateFn: func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
					transitionCalled = true
					return store.Sandbox{}, nil
				},
				recordEventFn: func(ctx context.Context, arg store.RecordEventParams) (store.Event, error) {
					recorded = arg
					return store.Event{}, nil
				},
			}

			svc := New(ms, nil)
			err := svc.HandleEvent(context.Background(), uuid.New(), sandboxID, "container_exited", json.RawMessage(`{}`))
			if err != nil {
				t.Fatalf("expected legal no-op, got: %v", err)
			}
			if transitionCalled {
				t.Fatalf("container_exited on %s must NOT transition state (retention clock reset)", state)
			}
			if recorded.EventType != "container_exited" {
				t.Fatalf("expected audit event recorded, got %+v", recorded)
			}
			if recorded.PrevState.String != state || recorded.NextState.String != state {
				t.Fatalf("audit event must record %s→%s, got %s→%s",
					state, state, recorded.PrevState.String, recorded.NextState.String)
			}
		})
	}
}

// container_started observed via the event channel refreshes activity
// (belt-and-braces for a lost start ack).
func TestHandleEvent_ContainerStarted_BumpsLastActive(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)

	bumpCalls := 0

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{ID: pgSandboxID, State: "starting", AppName: "test-app"}, nil
		},
		transitionStateFn: func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
			return store.Sandbox{ID: pgSandboxID, State: arg.State}, nil
		},
		updateSandboxLastActiveFn: func(ctx context.Context, id pgtype.UUID) error {
			bumpCalls++
			return nil
		},
	}

	svc := New(ms, nil)
	if err := svc.HandleEvent(context.Background(), uuid.New(), sandboxID, "container_started", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bumpCalls != 1 {
		t.Fatalf("expected 1 last_active_at bump on container_started, got %d", bumpCalls)
	}
}

// isPoolSandbox is fail-SAFE: only a positive appx.pool marker without an
// owning appx.projectId classifies as pool (destroy + inline row delete).
func TestIsPoolSandbox_Classification(t *testing.T) {
	cases := []struct {
		name     string
		metadata []byte
		want     bool
	}{
		{"nil metadata — unknown provenance, treat as claimed", nil, false},
		{"empty object — pre-marker rows, treat as claimed", []byte(`{}`), false},
		{"malformed json — leave row alone", []byte(`{not-json`), false},
		{"managed-only (claimed pre-fix kill class)", []byte(`{"appx.managed":"true","appx.appName":"pool-x"}`), false},
		{"project tag", []byte(`{"appx.projectId":"p-1"}`), false},
		{"empty project tag, no pool marker", []byte(`{"appx.projectId":""}`), false},
		{"pool marker", []byte(`{"appx.pool":"true"}`), true},
		{"pool marker boolean", []byte(`{"appx.pool":true}`), true},
		{"pool marker false", []byte(`{"appx.pool":"false"}`), false},
		{"claimed pool sandbox — projectId wins over pool marker", []byte(`{"appx.pool":"true","appx.projectId":"p-2"}`), false},
		{"pool marker with empty projectId", []byte(`{"appx.pool":"true","appx.projectId":""}`), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPoolSandbox(tc.metadata); got != tc.want {
				t.Errorf("isPoolSandbox(%s) = %v, want %v", tc.metadata, got, tc.want)
			}
		})
	}
}

// isSleptAck tolerates empty and garbage payloads.
func TestIsSleptAck(t *testing.T) {
	cases := []struct {
		name string
		ack  json.RawMessage
		want bool
	}{
		{"nil", nil, false},
		{"empty object", json.RawMessage(`{}`), false},
		{"garbage", json.RawMessage(`not-json`), false},
		{"slept true", json.RawMessage(`{"slept":true}`), true},
		{"slept false", json.RawMessage(`{"slept":false}`), false},
		{"unrelated fields", json.RawMessage(`{"error":"x"}`), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSleptAck(tc.ack); got != tc.want {
				t.Errorf("isSleptAck(%s) = %v, want %v", tc.ack, got, tc.want)
			}
		})
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

// ── Mock StateWebhookNotifier (Phase 33-B) ────────────────────────

type mockStateWebhookNotifier struct {
	mu        sync.Mutex
	calls     []StateChangePayload
	execCalls []ExecCompletedPayload
}

func (m *mockStateWebhookNotifier) OnSandboxStateChanged(_ context.Context, payload StateChangePayload) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, payload)
	return nil
}

// OnExecCompleted satisfies the StateWebhookNotifier interface for the
// exec ack hook. Captures payloads so exec-specific tests can assert on
// them; existing state-change tests remain untouched.
func (m *mockStateWebhookNotifier) OnExecCompleted(_ context.Context, payload ExecCompletedPayload) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.execCalls = append(m.execCalls, payload)
	return nil
}

func (m *mockStateWebhookNotifier) snapshot() []StateChangePayload {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]StateChangePayload, len(m.calls))
	copy(out, m.calls)
	return out
}

// waitForCalls blocks until len(snapshot()) >= want or timeout. The
// webhook fires inside a goroutine inside notifyStateWebhook, so plain
// assertions right after HandleAck race the goroutine — wait until the
// expected delivery count lands or the test's deadline is hit.
func (m *mockStateWebhookNotifier) waitForCalls(t *testing.T, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(m.snapshot()) >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d webhook calls; got %d", want, len(m.snapshot()))
}

// Phase 33-B Bug A — HandleAck on start_sandbox success transitioning
// starting → running fires the state webhook with the freshly-persisted
// container_id and host_port from the ack payload.
func TestLifecycle_Webhook_HandleAck_StartSandboxSuccess_FiresOnRunning(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)
	nodeID := uuid.New()
	pgNodeID := makePgUUID(nodeID)

	// Two GetSandbox calls — first sees `starting`, second (after
	// persistRuntimeInfo refresh) sees the row with container_id+host_port.
	getCalls := 0
	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			getCalls++
			if getCalls == 1 {
				return store.Sandbox{
					ID: pgSandboxID, State: "starting", AppName: "pool-1",
					UserID: "user-1", NodeID: pgNodeID,
				}, nil
			}
			return store.Sandbox{
				ID: pgSandboxID, State: "starting", AppName: "pool-1",
				UserID: "user-1", NodeID: pgNodeID,
				ContainerID: pgtype.Text{String: "ctr-9999", Valid: true},
				HostPort:    pgtype.Int4{Int32: 41234, Valid: true},
			}, nil
		},
		getNodeByIDFn: func(ctx context.Context, id pgtype.UUID) (store.Node, error) {
			return store.Node{ID: pgNodeID, TailscaleIp: netip.MustParseAddr("100.64.0.1")}, nil
		},
	}

	wn := &mockStateWebhookNotifier{}
	svc := New(ms, nil)
	svc.SetStateWebhookNotifier(wn)

	err := svc.HandleAck(context.Background(), uuid.New(), sandboxID,
		"start_sandbox", "success",
		json.RawMessage(`{"container_id":"ctr-9999","host_port":41234}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wn.waitForCalls(t, 1, 2*time.Second)
	calls := wn.snapshot()

	if calls[0].State != "running" {
		t.Errorf("state = %q, want running", calls[0].State)
	}
	if calls[0].PrevState != "starting" {
		t.Errorf("prev_state = %q, want starting", calls[0].PrevState)
	}
	if calls[0].AppName != "pool-1" {
		t.Errorf("app_name = %q, want pool-1", calls[0].AppName)
	}
	if calls[0].SandboxID != sandboxID.String() {
		t.Errorf("sandbox_id = %q, want %s", calls[0].SandboxID, sandboxID.String())
	}
	if calls[0].ContainerID != "ctr-9999" {
		t.Errorf("container_id = %q, want ctr-9999", calls[0].ContainerID)
	}
	if calls[0].HostPort != 41234 {
		t.Errorf("host_port = %d, want 41234", calls[0].HostPort)
	}
}

// Bug B — HandleAck where state already advanced (alreadyAtTarget escape
// hatch) should NOT fire a duplicate webhook. The lifecycle's nextState
// equals currentState in that branch, so the webhook helper's running
// condition wouldn't even be checked, but the explicit assertion guards
// against future refactors that might leak a second delivery.
func TestLifecycle_Webhook_HandleAck_AlreadyRunning_NoDuplicateFire(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{
				ID: pgSandboxID, State: "running", AppName: "pool-2",
				UserID: "user-2",
			}, nil
		},
	}

	wn := &mockStateWebhookNotifier{}
	svc := New(ms, nil)
	svc.SetStateWebhookNotifier(wn)

	err := svc.HandleAck(context.Background(), uuid.New(), sandboxID,
		"start_sandbox", "success",
		json.RawMessage(`{"container_id":"ctr-existing","host_port":40000}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Brief pause to let any goroutine settle. State is already running,
	// so we expect zero webhook calls — currentState == nextState in the
	// alreadyAtTarget branch suppresses the running-transition trigger.
	time.Sleep(50 * time.Millisecond)
	if got := len(wn.snapshot()); got != 0 {
		t.Errorf("expected 0 webhook calls (state already running), got %d", got)
	}
}

// Bug C — HandleEvent on container_started with the row in `starting`
// transitions to running and fires the webhook. Both HandleAck and
// HandleEvent paths can produce the same transition; either path winning
// emits a webhook.
func TestLifecycle_Webhook_HandleEvent_ContainerStarted_FiresOnRunning(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)
	nodeID := uuid.New()
	pgNodeID := makePgUUID(nodeID)

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{
				ID: pgSandboxID, State: "starting", AppName: "pool-3",
				UserID: "user-3", NodeID: pgNodeID,
				ContainerID: pgtype.Text{String: "ctr-event", Valid: true},
				HostPort:    pgtype.Int4{Int32: 42000, Valid: true},
			}, nil
		},
	}

	wn := &mockStateWebhookNotifier{}
	svc := New(ms, nil)
	svc.SetStateWebhookNotifier(wn)

	err := svc.HandleEvent(context.Background(), nodeID, sandboxID, "container_started", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wn.waitForCalls(t, 1, 2*time.Second)
	calls := wn.snapshot()
	if calls[0].State != "running" {
		t.Errorf("state = %q, want running", calls[0].State)
	}
	if calls[0].AppName != "pool-3" {
		t.Errorf("app_name = %q, want pool-3", calls[0].AppName)
	}
}

// Bug D — webhook is fully optional. With no SetStateWebhookNotifier
// call (notifier nil), HandleAck must not panic and must not block.
func TestLifecycle_Webhook_NilNotifier_DoesNotPanic(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{
				ID: pgSandboxID, State: "starting", AppName: "pool-4",
				UserID: "user-4",
			}, nil
		},
	}

	svc := New(ms, nil)
	// No SetStateWebhookNotifier call.

	err := svc.HandleAck(context.Background(), uuid.New(), sandboxID,
		"start_sandbox", "success",
		json.RawMessage(`{"container_id":"ctr","host_port":1}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Phase 33-E — start_sandbox + failure ack on a sandbox in starting
// state transitions to failed. The webhook must fire so backend's
// listener can stop polling and react immediately to the failure.
func TestLifecycle_Webhook_HandleAck_StartSandboxFailure_FiresOnFailed(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)
	nodeID := uuid.New()
	pgNodeID := makePgUUID(nodeID)

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{
				ID: pgSandboxID, State: "starting", AppName: "pool-fail",
				UserID: "user-fail", NodeID: pgNodeID,
			}, nil
		},
	}

	wn := &mockStateWebhookNotifier{}
	svc := New(ms, nil)
	svc.SetStateWebhookNotifier(wn)

	err := svc.HandleAck(context.Background(), uuid.New(), sandboxID,
		"start_sandbox", "failure",
		json.RawMessage(`{"error":"container start failed"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wn.waitForCalls(t, 1, 2*time.Second)
	calls := wn.snapshot()
	if calls[0].State != "failed" {
		t.Errorf("state = %q, want failed", calls[0].State)
	}
	if calls[0].PrevState != "starting" {
		t.Errorf("prev_state = %q, want starting", calls[0].PrevState)
	}
}

// Phase 33-E — orphan DestroySandbox fires the destroyed webhook
// (orphan short-circuit transitions row directly to destroyed without
// dispatching stop_sandbox). Backend's listener uses this to drop the
// app_deployments row even when no agent ack ever lands.
func TestLifecycle_Webhook_DestroySandbox_OrphanFiresOnDestroyed(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			// node_id NULL → orphan short-circuit path
			return store.Sandbox{
				ID: pgSandboxID, State: "pending", AppName: "pool-orphan",
				UserID: "user-orphan",
				NodeID: pgtype.UUID{Valid: false},
			}, nil
		},
	}

	wn := &mockStateWebhookNotifier{}
	svc := New(ms, nil)
	svc.SetStateWebhookNotifier(wn)

	err := svc.DestroySandbox(context.Background(), sandboxID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wn.waitForCalls(t, 1, 2*time.Second)
	calls := wn.snapshot()
	if calls[0].State != "destroyed" {
		t.Errorf("state = %q, want destroyed", calls[0].State)
	}
	if calls[0].AppName != "pool-orphan" {
		t.Errorf("app_name = %q, want pool-orphan", calls[0].AppName)
	}
}

// ── 2026-05-17 — stop_sandbox+failure missing-container force-destroy ──
//
// Idle-reaper fires stop_sandbox against a stale container_id every 60s.
// The agent returns "No such container: <id>" since the container was
// recreated/pruned out-of-band. Pre-fix: ack hit the `default:` branch
// ("unhandled ack combination"); the row stayed alive; DriftDetector
// re-found the replacement container 20s later and flipped state to
// running, causing an infinite reap loop. Post-fix: HandleAck force-
// transitions the row to StateDestroyed (bypassing the state machine
// since no valid running→destroyed event exists, but the container is
// provably gone). Other generic stop_sandbox failures (timeout etc.)
// stay a no-op so the reaper can retry on the next tick.

func TestHandleAck_StopSandboxFailure_MissingContainer_ForcesDestroyed(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)

	var capturedTransition store.TransitionSandboxStateParams
	transitionCalls := 0

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{
				ID: pgSandboxID, State: "running", AppName: "stuck-app",
				ContainerID: pgtype.Text{String: "7ece1234abcd", Valid: true},
			}, nil
		},
		transitionStateFn: func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
			capturedTransition = arg
			transitionCalls++
			return store.Sandbox{ID: pgSandboxID, State: arg.State}, nil
		},
	}

	svc := New(ms, nil)
	err := svc.HandleAck(context.Background(), uuid.New(), sandboxID,
		"stop_sandbox", "failure",
		json.RawMessage(`{"error":"No such container: 7ece1234abcd"}`))

	if err != nil {
		t.Fatalf("expected nil error (force-destroy path), got: %v", err)
	}
	if transitionCalls != 1 {
		t.Fatalf("expected exactly 1 transition call, got %d", transitionCalls)
	}
	if capturedTransition.State != "destroyed" {
		t.Fatalf("expected force-transition to 'destroyed', got %q", capturedTransition.State)
	}
	// Optimistic-concurrency guard must reflect the actual prior state
	// (running) so the WHERE state = $3 clause matches.
	if capturedTransition.State_2 != "running" {
		t.Fatalf("expected transition guard from 'running', got %q", capturedTransition.State_2)
	}
}

func TestHandleAck_StopSandboxFailure_GenericFailure_NoOp(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)

	transitionCalls := 0

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{
				ID: pgSandboxID, State: "running", AppName: "stuck-app",
				ContainerID: pgtype.Text{String: "7ece1234abcd", Valid: true},
			}, nil
		},
		transitionStateFn: func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
			transitionCalls++
			return store.Sandbox{ID: pgSandboxID, State: arg.State}, nil
		},
	}

	svc := New(ms, nil)
	err := svc.HandleAck(context.Background(), uuid.New(), sandboxID,
		"stop_sandbox", "failure",
		json.RawMessage(`{"error":"timeout while stopping container"}`))

	if err != nil {
		t.Fatalf("expected nil error (no-op path), got: %v", err)
	}
	if transitionCalls != 0 {
		t.Fatalf("expected no state transition for generic stop failure, got %d transition calls", transitionCalls)
	}
}

func TestHandleAck_StopSandboxFailure_AlreadyDestroyed_NoOp(t *testing.T) {
	sandboxID := uuid.New()
	pgSandboxID := makePgUUID(sandboxID)

	transitionCalls := 0

	ms := &mockStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{
				ID: pgSandboxID, State: "destroyed", AppName: "stuck-app",
				ContainerID: pgtype.Text{String: "7ece1234abcd", Valid: true},
			}, nil
		},
		transitionStateFn: func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
			transitionCalls++
			return store.Sandbox{ID: pgSandboxID, State: arg.State}, nil
		},
	}

	svc := New(ms, nil)
	err := svc.HandleAck(context.Background(), uuid.New(), sandboxID,
		"stop_sandbox", "failure",
		json.RawMessage(`{"error":"No such container: 7ece1234abcd"}`))

	if err != nil {
		t.Fatalf("expected nil error when already destroyed, got: %v", err)
	}
	// alreadyAtTarget escape hatch must suppress the transition — sandbox
	// is already in the destination state.
	if transitionCalls != 0 {
		t.Fatalf("expected no state transition when already destroyed, got %d transition calls", transitionCalls)
	}
}

func TestIsMissingContainerErr_MatchesVariants(t *testing.T) {
	cases := []struct {
		name    string
		payload json.RawMessage
		want    bool
	}{
		{"docker NotFound with id", json.RawMessage(`{"error":"No such container: 7ece1234abcd"}`), true},
		{"docker daemon prefix", json.RawMessage(`{"error":"Error response from daemon: No such container: abc"}`), true},
		{"bare phrase", json.RawMessage(`{"error":"No such container"}`), true},
		{"lowercase variant — case-sensitive substring match", json.RawMessage(`{"error":"no such container"}`), false},
		{"generic timeout", json.RawMessage(`{"error":"timeout while stopping container"}`), false},
		{"empty ack result", json.RawMessage(``), false},
		{"empty error field", json.RawMessage(`{"error":""}`), false},
		{"malformed json", json.RawMessage(`not-json`), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isMissingContainerErr(tc.payload)
			if got != tc.want {
				t.Errorf("isMissingContainerErr(%q) = %v, want %v", string(tc.payload), got, tc.want)
			}
		})
	}
}
