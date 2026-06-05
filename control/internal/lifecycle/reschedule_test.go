package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/appx/forge/control/internal/store"
	"github.com/appx/forge/shared-go/models"
)

// ── Mock RescheduleStore ──────────────────────────────────────────────

type mockRescheduleStore struct {
	listRunningSandboxesByNodeFn func(ctx context.Context, nodeID pgtype.UUID) ([]store.Sandbox, error)
	listHealthyNodesFn           func(ctx context.Context) ([]store.Node, error)
	transitionStateFn            func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error)
	assignSandboxToNodeFn        func(ctx context.Context, arg store.AssignSandboxToNodeParams) (store.Sandbox, error)
	createCommandFn              func(ctx context.Context, arg store.CreateCommandParams) (store.Command, error)
	recordEventFn                func(ctx context.Context, arg store.RecordEventParams) (store.Event, error)
	resetSandboxFailureCountFn   func(ctx context.Context, id pgtype.UUID) error
	countSchedulableFn           func(ctx context.Context, nodeID pgtype.UUID) (int32, error)
}

func (m *mockRescheduleStore) ListRunningSandboxesByNode(ctx context.Context, nodeID pgtype.UUID) ([]store.Sandbox, error) {
	if m.listRunningSandboxesByNodeFn != nil {
		return m.listRunningSandboxesByNodeFn(ctx, nodeID)
	}
	return nil, nil
}

func (m *mockRescheduleStore) ListHealthyNodes(ctx context.Context) ([]store.Node, error) {
	if m.listHealthyNodesFn != nil {
		return m.listHealthyNodesFn(ctx)
	}
	return nil, nil
}

func (m *mockRescheduleStore) TransitionSandboxState(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
	if m.transitionStateFn != nil {
		return m.transitionStateFn(ctx, arg)
	}
	return store.Sandbox{State: arg.State}, nil
}

func (m *mockRescheduleStore) AssignSandboxToNode(ctx context.Context, arg store.AssignSandboxToNodeParams) (store.Sandbox, error) {
	if m.assignSandboxToNodeFn != nil {
		return m.assignSandboxToNodeFn(ctx, arg)
	}
	return store.Sandbox{State: "starting"}, nil
}

func (m *mockRescheduleStore) CreateCommand(ctx context.Context, arg store.CreateCommandParams) (store.Command, error) {
	if m.createCommandFn != nil {
		return m.createCommandFn(ctx, arg)
	}
	return store.Command{ID: arg.ID, CommandType: arg.CommandType}, nil
}

func (m *mockRescheduleStore) RecordEvent(ctx context.Context, arg store.RecordEventParams) (store.Event, error) {
	if m.recordEventFn != nil {
		return m.recordEventFn(ctx, arg)
	}
	return store.Event{}, nil
}

func (m *mockRescheduleStore) ResetSandboxFailureCount(ctx context.Context, id pgtype.UUID) error {
	if m.resetSandboxFailureCountFn != nil {
		return m.resetSandboxFailureCountFn(ctx, id)
	}
	return nil
}

func (m *mockRescheduleStore) CountSchedulableSandboxesByNode(ctx context.Context, nodeID pgtype.UUID) (int32, error) {
	if m.countSchedulableFn != nil {
		return m.countSchedulableFn(ctx, nodeID)
	}
	return 0, nil
}

// ── Mock RouteNotifier ────────────────────────────────────────────────

type mockRescheduleRouteNotifier struct {
	onSandboxStoppedFn func(ctx context.Context, appName string) error
}

func (m *mockRescheduleRouteNotifier) OnSandboxRunning(ctx context.Context, appName string, sandboxID string, upstream string) error {
	return nil
}

func (m *mockRescheduleRouteNotifier) OnSandboxStopped(ctx context.Context, appName string) error {
	if m.onSandboxStoppedFn != nil {
		return m.onSandboxStoppedFn(ctx, appName)
	}
	return nil
}

// ── Test Helpers ──────────────────────────────────────────────────────

func makeRunningSandbox(id uuid.UUID, nodeID uuid.UUID, appName string) store.Sandbox {
	return store.Sandbox{
		ID:           makePgUUID(id),
		AppName:      appName,
		UserID:       "user-123",
		Image:        "appx/sandbox:v1",
		State:        string(models.StateRunning),
		NodeID:       makePgUUID(nodeID),
		ContainerID:  pgtype.Text{String: "container-abc", Valid: true},
		HostPort:     pgtype.Int4{Int32: 8080, Valid: true},
		Resources:    []byte(`{"cpu_cores":0.5,"memory_mb":512}`),
		Env:          []byte(`{"APP_PORT":"8080"}`),
		FailureCount: 0,
	}
}

// ── Reschedule Tests ──────────────────────────────────────────────────

func TestRescheduleNode_TwoRunningSandboxes_BothRescheduled(t *testing.T) {
	failedNodeID := uuid.New()
	sandbox1ID := uuid.New()
	sandbox2ID := uuid.New()
	healthyNode1 := uuid.New()
	healthyNode2 := uuid.New()

	sandbox1 := makeRunningSandbox(sandbox1ID, failedNodeID, "app-one")
	sandbox2 := makeRunningSandbox(sandbox2ID, failedNodeID, "app-two")

	var transitions []store.TransitionSandboxStateParams
	var commands []store.CreateCommandParams
	var assigns []store.AssignSandboxToNodeParams

	ms := &mockRescheduleStore{
		listRunningSandboxesByNodeFn: func(ctx context.Context, nodeID pgtype.UUID) ([]store.Sandbox, error) {
			return []store.Sandbox{sandbox1, sandbox2}, nil
		},
		listHealthyNodesFn: func(ctx context.Context) ([]store.Node, error) {
			return []store.Node{
				makeNode(healthyNode1, 4096, 512),
				makeNode(healthyNode2, 4096, 1024),
			}, nil
		},
		transitionStateFn: func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
			transitions = append(transitions, arg)
			return store.Sandbox{State: arg.State}, nil
		},
		assignSandboxToNodeFn: func(ctx context.Context, arg store.AssignSandboxToNodeParams) (store.Sandbox, error) {
			assigns = append(assigns, arg)
			return store.Sandbox{State: "starting"}, nil
		},
		createCommandFn: func(ctx context.Context, arg store.CreateCommandParams) (store.Command, error) {
			commands = append(commands, arg)
			return store.Command{ID: arg.ID, CommandType: arg.CommandType}, nil
		},
	}

	notifier := &mockRescheduleRouteNotifier{}

	r := NewRescheduler(ms, notifier, nil)
	result, err := r.RescheduleNode(context.Background(), makePgUUID(failedNodeID))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Count != 2 {
		t.Fatalf("expected Count=2, got %d", result.Count)
	}
	if result.Failed != 0 {
		t.Fatalf("expected Failed=0, got %d", result.Failed)
	}

	// Should have 2 running->pending transitions
	if len(transitions) < 2 {
		t.Fatalf("expected at least 2 transitions, got %d", len(transitions))
	}
	if transitions[0].State != string(models.StatePending) || transitions[0].State_2 != string(models.StateRunning) {
		t.Fatalf("first transition should be running->pending, got %s->%s", transitions[0].State_2, transitions[0].State)
	}

	// Should have 2 commands dispatched
	if len(commands) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(commands))
	}
	for _, cmd := range commands {
		if cmd.CommandType != string(models.CmdStartSandbox) {
			t.Fatalf("expected start_sandbox command, got %s", cmd.CommandType)
		}
	}

	// Should have 2 assignments
	if len(assigns) != 2 {
		t.Fatalf("expected 2 assigns, got %d", len(assigns))
	}

	// Both should be assigned to healthyNode1 (most free RAM: 4096-512=3584 > 4096-1024=3072)
	for _, a := range assigns {
		if a.NodeID != makePgUUID(healthyNode1) {
			t.Fatalf("expected assignment to node with most free RAM, got %v", a.NodeID)
		}
	}
}

// TestRescheduleNode_CountCap_FailoverHoldsViaLiveDBCount proves the cap holds
// during a failover pass — the path where OOM risk is worst. A failed node's
// sandboxes are moved sequentially onto a single surviving node that reports
// running_containers=0 (heartbeat-stale) and abundant free RAM, so neither the
// stale count nor RAM would ever reject. With the cap at 2 and the DB count
// rising as each AssignSandboxToNode commits, only 2 sandboxes may land; the
// rest must transition to FAILED rather than pile on past the ceiling.
func TestRescheduleNode_CountCap_FailoverHoldsViaLiveDBCount(t *testing.T) {
	failedNodeID := uuid.New()
	survivorID := uuid.New()

	// 4 running sandboxes to move; survivor cap is 2.
	sandboxes := []store.Sandbox{
		makeRunningSandbox(uuid.New(), failedNodeID, "fo-1"),
		makeRunningSandbox(uuid.New(), failedNodeID, "fo-2"),
		makeRunningSandbox(uuid.New(), failedNodeID, "fo-3"),
		makeRunningSandbox(uuid.New(), failedNodeID, "fo-4"),
	}

	var liveCount int32 // authoritative DB count on the survivor
	var assigns []store.AssignSandboxToNodeParams
	var failedTransitions int

	ms := &mockRescheduleStore{
		listRunningSandboxesByNodeFn: func(ctx context.Context, nodeID pgtype.UUID) ([]store.Sandbox, error) {
			return sandboxes, nil
		},
		listHealthyNodesFn: func(ctx context.Context) ([]store.Node, error) {
			// Single survivor: 0 running per heartbeat, huge free RAM.
			n := makeNode(survivorID, 64000, 0)
			n.RunningContainers = 0
			return []store.Node{n}, nil
		},
		countSchedulableFn: func(ctx context.Context, _ pgtype.UUID) (int32, error) {
			return liveCount, nil
		},
		assignSandboxToNodeFn: func(ctx context.Context, arg store.AssignSandboxToNodeParams) (store.Sandbox, error) {
			liveCount++ // row committed to survivor — DB count rises mid-pass
			assigns = append(assigns, arg)
			return store.Sandbox{State: "starting"}, nil
		},
		transitionStateFn: func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
			if arg.State == string(models.StateFailed) {
				failedTransitions++
			}
			return store.Sandbox{State: arg.State}, nil
		},
	}

	r := NewRescheduler(ms, &mockRescheduleRouteNotifier{}, nil)
	r.SetMaxSandboxesPerNode(2)

	result, err := r.RescheduleNode(context.Background(), makePgUUID(failedNodeID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Exactly 2 should be placed (cap), the other 2 failed — NOT all 4 piled on.
	if len(assigns) != 2 {
		t.Fatalf("expected exactly 2 assigns under cap, got %d (cap defeated — OOM risk)", len(assigns))
	}
	if result.Count != 2 {
		t.Fatalf("expected Count=2, got %d", result.Count)
	}
	if result.Failed != 2 {
		t.Fatalf("expected Failed=2 (over-cap rejected), got %d", result.Failed)
	}
	if failedTransitions != 2 {
		t.Fatalf("expected 2 pending->failed transitions, got %d", failedTransitions)
	}
}

func TestRescheduleNode_NoRunningSandboxes_ReturnsZero(t *testing.T) {
	failedNodeID := uuid.New()

	ms := &mockRescheduleStore{
		listRunningSandboxesByNodeFn: func(ctx context.Context, nodeID pgtype.UUID) ([]store.Sandbox, error) {
			return nil, nil
		},
	}

	r := NewRescheduler(ms, nil, nil)
	result, err := r.RescheduleNode(context.Background(), makePgUUID(failedNodeID))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Count != 0 {
		t.Fatalf("expected Count=0, got %d", result.Count)
	}
	if result.Failed != 0 {
		t.Fatalf("expected Failed=0, got %d", result.Failed)
	}
}

func TestRescheduleNode_NoCapacity_SandboxTransitionsToFailed(t *testing.T) {
	failedNodeID := uuid.New()
	sandboxID := uuid.New()
	sandbox := makeRunningSandbox(sandboxID, failedNodeID, "app-stuck")

	var transitions []store.TransitionSandboxStateParams

	ms := &mockRescheduleStore{
		listRunningSandboxesByNodeFn: func(ctx context.Context, nodeID pgtype.UUID) ([]store.Sandbox, error) {
			return []store.Sandbox{sandbox}, nil
		},
		listHealthyNodesFn: func(ctx context.Context) ([]store.Node, error) {
			// All nodes are full -- no capacity
			return nil, nil
		},
		transitionStateFn: func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
			transitions = append(transitions, arg)
			return store.Sandbox{State: arg.State}, nil
		},
	}

	notifier := &mockRescheduleRouteNotifier{}

	r := NewRescheduler(ms, notifier, nil)
	result, err := r.RescheduleNode(context.Background(), makePgUUID(failedNodeID))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Count != 0 {
		t.Fatalf("expected Count=0, got %d", result.Count)
	}
	if result.Failed != 1 {
		t.Fatalf("expected Failed=1, got %d", result.Failed)
	}

	// Should have: running->pending, then pending->failed
	foundPendingTransition := false
	foundFailedTransition := false
	for _, tr := range transitions {
		if tr.State == string(models.StatePending) && tr.State_2 == string(models.StateRunning) {
			foundPendingTransition = true
		}
		if tr.State == string(models.StateFailed) && tr.State_2 == string(models.StatePending) {
			foundFailedTransition = true
		}
	}
	if !foundPendingTransition {
		t.Fatal("expected running->pending transition")
	}
	if !foundFailedTransition {
		t.Fatal("expected pending->failed transition when no capacity")
	}
}

func TestRescheduleNode_RemovesCaddyRoutes(t *testing.T) {
	failedNodeID := uuid.New()
	sandboxID := uuid.New()
	sandbox := makeRunningSandbox(sandboxID, failedNodeID, "app-routed")
	healthyNodeID := uuid.New()

	var removedRoutes []string

	ms := &mockRescheduleStore{
		listRunningSandboxesByNodeFn: func(ctx context.Context, nodeID pgtype.UUID) ([]store.Sandbox, error) {
			return []store.Sandbox{sandbox}, nil
		},
		listHealthyNodesFn: func(ctx context.Context) ([]store.Node, error) {
			return []store.Node{makeNode(healthyNodeID, 4096, 0)}, nil
		},
	}

	notifier := &mockRescheduleRouteNotifier{
		onSandboxStoppedFn: func(ctx context.Context, appName string) error {
			removedRoutes = append(removedRoutes, appName)
			return nil
		},
	}

	r := NewRescheduler(ms, notifier, nil)
	_, err := r.RescheduleNode(context.Background(), makePgUUID(failedNodeID))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(removedRoutes) != 1 {
		t.Fatalf("expected 1 route removal, got %d", len(removedRoutes))
	}
	if removedRoutes[0] != "app-routed" {
		t.Fatalf("expected route removal for 'app-routed', got %q", removedRoutes[0])
	}
}

func TestRescheduleNode_RecordsNodeFailedEvents(t *testing.T) {
	failedNodeID := uuid.New()
	sandboxID := uuid.New()
	sandbox := makeRunningSandbox(sandboxID, failedNodeID, "app-events")
	healthyNodeID := uuid.New()

	var events []store.RecordEventParams

	ms := &mockRescheduleStore{
		listRunningSandboxesByNodeFn: func(ctx context.Context, nodeID pgtype.UUID) ([]store.Sandbox, error) {
			return []store.Sandbox{sandbox}, nil
		},
		listHealthyNodesFn: func(ctx context.Context) ([]store.Node, error) {
			return []store.Node{makeNode(healthyNodeID, 4096, 0)}, nil
		},
		recordEventFn: func(ctx context.Context, arg store.RecordEventParams) (store.Event, error) {
			events = append(events, arg)
			return store.Event{}, nil
		},
	}

	notifier := &mockRescheduleRouteNotifier{}

	r := NewRescheduler(ms, notifier, nil)
	_, err := r.RescheduleNode(context.Background(), makePgUUID(failedNodeID))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have at least a node_failed event and a scheduled event
	foundNodeFailed := false
	foundScheduled := false
	for _, ev := range events {
		if ev.EventType == string(models.EventNodeFailed) {
			foundNodeFailed = true
			if ev.Actor != "rescheduler" {
				t.Fatalf("expected actor 'rescheduler', got %q", ev.Actor)
			}
		}
		if ev.EventType == string(models.EventScheduled) {
			foundScheduled = true
		}
	}
	if !foundNodeFailed {
		t.Fatal("expected node_failed event to be recorded")
	}
	if !foundScheduled {
		t.Fatal("expected scheduled event to be recorded")
	}
}

func TestRescheduleNode_UsesSchedulerForNodeSelection(t *testing.T) {
	failedNodeID := uuid.New()
	sandboxID := uuid.New()
	sandbox := makeRunningSandbox(sandboxID, failedNodeID, "app-scheduled")
	bigNode := uuid.New()
	smallNode := uuid.New()

	var assignedNodeID pgtype.UUID

	ms := &mockRescheduleStore{
		listRunningSandboxesByNodeFn: func(ctx context.Context, nodeID pgtype.UUID) ([]store.Sandbox, error) {
			return []store.Sandbox{sandbox}, nil
		},
		listHealthyNodesFn: func(ctx context.Context) ([]store.Node, error) {
			return []store.Node{
				makeNode(smallNode, 2048, 1024),  // 1024 free
				makeNode(bigNode, 8192, 0),        // 8192 free (most)
			}, nil
		},
		assignSandboxToNodeFn: func(ctx context.Context, arg store.AssignSandboxToNodeParams) (store.Sandbox, error) {
			assignedNodeID = arg.NodeID
			return store.Sandbox{State: "starting"}, nil
		},
	}

	notifier := &mockRescheduleRouteNotifier{}

	r := NewRescheduler(ms, notifier, nil)
	result, err := r.RescheduleNode(context.Background(), makePgUUID(failedNodeID))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Count != 1 {
		t.Fatalf("expected Count=1, got %d", result.Count)
	}

	// Should pick bigNode (most free RAM: 8192 vs 1024)
	if assignedNodeID != makePgUUID(bigNode) {
		t.Fatalf("expected assignment to big node (most free RAM), got %v", assignedNodeID)
	}
}

func TestRescheduleNode_PartialFailure_OthersShouldProceed(t *testing.T) {
	failedNodeID := uuid.New()
	sandbox1ID := uuid.New()
	sandbox2ID := uuid.New()
	healthyNodeID := uuid.New()

	sandbox1 := makeRunningSandbox(sandbox1ID, failedNodeID, "app-fail")
	sandbox2 := makeRunningSandbox(sandbox2ID, failedNodeID, "app-ok")

	transitionCallCount := 0

	ms := &mockRescheduleStore{
		listRunningSandboxesByNodeFn: func(ctx context.Context, nodeID pgtype.UUID) ([]store.Sandbox, error) {
			return []store.Sandbox{sandbox1, sandbox2}, nil
		},
		listHealthyNodesFn: func(ctx context.Context) ([]store.Node, error) {
			return []store.Node{makeNode(healthyNodeID, 4096, 0)}, nil
		},
		transitionStateFn: func(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
			transitionCallCount++
			// Fail the first running->pending transition (sandbox1)
			if transitionCallCount == 1 && arg.State == string(models.StatePending) {
				return store.Sandbox{}, errors.New("db error: connection reset")
			}
			return store.Sandbox{State: arg.State}, nil
		},
	}

	notifier := &mockRescheduleRouteNotifier{}

	r := NewRescheduler(ms, notifier, nil)
	result, err := r.RescheduleNode(context.Background(), makePgUUID(failedNodeID))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// sandbox1 failed, sandbox2 succeeded
	if result.Count != 1 {
		t.Fatalf("expected Count=1 (one succeeded), got %d", result.Count)
	}
	if result.Failed != 1 {
		t.Fatalf("expected Failed=1 (one failed), got %d", result.Failed)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("expected 1 error collected, got %d", len(result.Errors))
	}
}
