package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// newPgUUID returns a fresh pgtype.UUID. Tests use it as a placeholder node_id;
// the reconciler passes it through to the store opaquely.
func newPgUUID() pgtype.UUID {
	u := uuid.New()
	return pgtype.UUID{Bytes: u, Valid: true}
}

type fakeStore struct {
	listSandboxes    []SandboxRow
	listErr          error
	markedVerified   []verifiedCall
	markedAgentLost  []agentLostCall
	terminalRows     []TerminalSandboxRow
	terminalErr      error
	dispatchedStops  []stopDispatchCall
}

type verifiedCall struct{ AppName, State string }
type agentLostCall struct{ AppName string }
type stopDispatchCall struct {
	AppName     string
	ContainerID string
	Reason      string
}

func (f *fakeStore) ListSandboxesForNode(ctx context.Context, nodeID pgtype.UUID) ([]SandboxRow, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.listSandboxes, nil
}

func (f *fakeStore) MarkSandboxVerified(ctx context.Context, appName, state string) error {
	f.markedVerified = append(f.markedVerified, verifiedCall{appName, state})
	return nil
}

func (f *fakeStore) MarkSandboxAgentLost(ctx context.Context, appName string, nodeID pgtype.UUID) error {
	f.markedAgentLost = append(f.markedAgentLost, agentLostCall{appName})
	return nil
}

// Phase 33-Real-7 — fakeStore implementations for orphan stop dispatch.
func (f *fakeStore) ListTerminalSandboxesForNode(ctx context.Context, nodeID pgtype.UUID) ([]TerminalSandboxRow, error) {
	if f.terminalErr != nil {
		return nil, f.terminalErr
	}
	return f.terminalRows, nil
}

func (f *fakeStore) DispatchStopSandbox(ctx context.Context, sandboxID, nodeID pgtype.UUID, containerID, reason string) error {
	// Find app_name by sandboxID for the test assertions.
	appName := ""
	for _, row := range f.terminalRows {
		if row.ID == sandboxID {
			appName = row.AppName
			break
		}
	}
	f.dispatchedStops = append(f.dispatchedStops, stopDispatchCall{
		AppName:     appName,
		ContainerID: containerID,
		Reason:      reason,
	})
	return nil
}

func TestReconcile_BumpsVerifiedAtForPresent(t *testing.T) {
	store := &fakeStore{
		listSandboxes: []SandboxRow{
			{AppName: "pool-X", State: "running", CreatedAt: time.Now().Add(-5 * time.Minute)},
		},
	}
	r := NewHeartbeatReconciler(store, nil)

	err := r.Reconcile(context.Background(), newPgUUID(), []ContainerInfo{
		{AppName: "pool-X", State: "running", HostPort: 8081, ContainerID: "c1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.markedVerified) != 1 || store.markedVerified[0].AppName != "pool-X" {
		t.Errorf("expected verified pool-X, got %+v", store.markedVerified)
	}
	if store.markedVerified[0].State != "running" {
		t.Errorf("expected state=running, got %q", store.markedVerified[0].State)
	}
	if len(store.markedAgentLost) != 0 {
		t.Errorf("expected no agent-lost marks, got %+v", store.markedAgentLost)
	}
}

func TestReconcile_MarksAgentLostForMissingOldRow(t *testing.T) {
	store := &fakeStore{
		listSandboxes: []SandboxRow{
			{AppName: "pool-LOST", State: "running", CreatedAt: time.Now().Add(-5 * time.Minute)},
		},
	}
	r := NewHeartbeatReconciler(store, nil)

	err := r.Reconcile(context.Background(), newPgUUID(), []ContainerInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.markedAgentLost) != 1 || store.markedAgentLost[0].AppName != "pool-LOST" {
		t.Errorf("expected agent-lost pool-LOST, got %+v", store.markedAgentLost)
	}
	if len(store.markedVerified) != 0 {
		t.Errorf("expected no verified marks (agent reported nothing), got %+v", store.markedVerified)
	}
}

func TestReconcile_RespectsGraceWindowForRecentRows(t *testing.T) {
	store := &fakeStore{
		listSandboxes: []SandboxRow{
			{AppName: "pool-NEW", State: "starting", CreatedAt: time.Now().Add(-10 * time.Second)},
		},
	}
	r := NewHeartbeatReconciler(store, nil)

	err := r.Reconcile(context.Background(), newPgUUID(), []ContainerInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.markedAgentLost) != 0 {
		t.Errorf("grace window violated: %+v", store.markedAgentLost)
	}
}

// Phase 33-Real-7 — when a row is in terminal state but the agent still
// observes the underlying container, the reconciler must dispatch a
// stop_sandbox so the orphan container is destroyed. Replaces the prior
// silent skip behavior that left orphan containers running forever.
func TestReconcile_DispatchesStopForOrphanOnTerminalRow(t *testing.T) {
	terminalID := newPgUUID()
	store := &fakeStore{
		// dbRows is the non-terminal working set; empty here so the
		// agent-lost reverse loop has nothing to chew on.
		listSandboxes: []SandboxRow{},
		// Terminal-state row whose container the agent reports below.
		terminalRows: []TerminalSandboxRow{
			{ID: terminalID, AppName: "app-orphan", ContainerID: "ctn-abc123"},
		},
	}
	r := NewHeartbeatReconciler(store, nil)

	// Agent observes the container — orphan condition.
	err := r.Reconcile(context.Background(), newPgUUID(), []ContainerInfo{
		{AppName: "app-orphan", State: "running", ContainerID: "ctn-abc123"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(store.dispatchedStops) != 1 {
		t.Fatalf("expected 1 stop dispatch, got %d (%+v)",
			len(store.dispatchedStops), store.dispatchedStops)
	}
	got := store.dispatchedStops[0]
	if got.AppName != "app-orphan" {
		t.Errorf("dispatched for %q, expected app-orphan", got.AppName)
	}
	if got.ContainerID != "ctn-abc123" {
		t.Errorf("dispatched container_id=%q, expected ctn-abc123", got.ContainerID)
	}
	if got.Reason != "orphan_terminal_row" {
		t.Errorf("dispatched reason=%q, expected orphan_terminal_row", got.Reason)
	}
}

// 2026-05-17 — agent reports a container whose app_name has NO matching
// DB row at all. Reconciler must dispatch stop_sandbox with NULL
// sandbox_id so the orphan container is destroyed without a state
// transition. Mirror of TestReconcile_DispatchesStopForOrphanOnTerminalRow
// but with empty `terminalRows` instead of empty `listSandboxes`.
func TestReconcile_DispatchesStopForOrphanWithNoDBRow(t *testing.T) {
	store := &fakeStore{
		listSandboxes: []SandboxRow{},
		terminalRows:  []TerminalSandboxRow{},
	}
	r := NewHeartbeatReconciler(store, nil)

	err := r.Reconcile(context.Background(), newPgUUID(), []ContainerInfo{
		{AppName: "pool-ghost", State: "running", ContainerID: "ctn-ghost-1"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(store.dispatchedStops) != 1 {
		t.Fatalf("expected 1 stop dispatch, got %d (%+v)",
			len(store.dispatchedStops), store.dispatchedStops)
	}
	got := store.dispatchedStops[0]
	if got.ContainerID != "ctn-ghost-1" {
		t.Errorf("dispatched container_id=%q, expected ctn-ghost-1", got.ContainerID)
	}
	if got.Reason != "orphan_no_db_row" {
		t.Errorf("dispatched reason=%q, expected orphan_no_db_row", got.Reason)
	}
	// AppName comes from fakeStore reverse-lookup which only knows
	// terminalRows; orphan-no-db-row dispatch carries no sandbox row so
	// the fake records empty AppName. Fine for this assertion.
}

// 2026-05-17 — agent reports an empty AppName (corrupt/missing label).
// Reconciler must skip dispatch defensively rather than dispatching a
// stop for "" with a real container_id (which would catch any container
// without app_name label across the host).
func TestReconcile_SkipsOrphanDispatchOnEmptyAppName(t *testing.T) {
	store := &fakeStore{
		listSandboxes: []SandboxRow{},
		terminalRows:  []TerminalSandboxRow{},
	}
	r := NewHeartbeatReconciler(store, nil)

	err := r.Reconcile(context.Background(), newPgUUID(), []ContainerInfo{
		{AppName: "", State: "running", ContainerID: "ctn-unlabeled"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(store.dispatchedStops) != 0 {
		t.Errorf("expected 0 dispatches for empty app_name, got %+v", store.dispatchedStops)
	}
}

// Phase 33-Real-7 — when the row is terminal but the agent does NOT
// report the container (already destroyed), no dispatch should fire.
func TestReconcile_SkipsStopWhenTerminalContainerAbsent(t *testing.T) {
	store := &fakeStore{
		listSandboxes: []SandboxRow{},
		terminalRows: []TerminalSandboxRow{
			{ID: newPgUUID(), AppName: "app-gone", ContainerID: "ctn-old"},
		},
	}
	r := NewHeartbeatReconciler(store, nil)

	// Agent does not see the container — it's already gone.
	err := r.Reconcile(context.Background(), newPgUUID(), []ContainerInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.dispatchedStops) != 0 {
		t.Errorf("expected 0 dispatches when container absent, got %+v",
			store.dispatchedStops)
	}
}
