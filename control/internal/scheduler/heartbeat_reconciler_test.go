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
	listSandboxes   []SandboxRow
	listErr         error
	markedVerified  []verifiedCall
	markedAgentLost []agentLostCall
}

type verifiedCall struct{ AppName, State string }
type agentLostCall struct{ AppName string }

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
