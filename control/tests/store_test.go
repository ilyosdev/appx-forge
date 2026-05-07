package tests

import (
	"context"
	"errors"
	"math/big"
	"net/netip"
	"testing"

	"github.com/appx/forge/control/internal/store"
	"github.com/appx/forge/control/tests/testhelpers"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestCreateSandbox(t *testing.T) {
	connStr, ctr := testhelpers.SetupTestDB(t)
	ctx := context.Background()

	if err := ctr.Restore(ctx); err != nil {
		t.Fatalf("restoring snapshot: %v", err)
	}

	pool := testhelpers.ConnectPool(t, connStr)
	q := store.New(pool)

	sbID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	sb, err := q.CreateSandbox(ctx, store.CreateSandboxParams{
		ID:                 sbID,
		AppName:            "test-app-create",
		UserID:             "user-1",
		Image:              "appx/sandbox:v1",
		Resources:          []byte(`{"cpu_cores": 0.5, "memory_mb": 512}`),
		Env:                []byte(`{}`),
		IdleTimeoutSeconds: 1800,
		Metadata:           []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("creating sandbox: %v", err)
	}
	if sb.State != "pending" {
		t.Errorf("expected state 'pending', got %q", sb.State)
	}
	if sb.StateVersion != 0 {
		t.Errorf("expected state_version 0, got %d", sb.StateVersion)
	}
}

func TestTransitionSandboxState_CASSuccess(t *testing.T) {
	connStr, ctr := testhelpers.SetupTestDB(t)
	ctx := context.Background()

	if err := ctr.Restore(ctx); err != nil {
		t.Fatalf("restoring snapshot: %v", err)
	}

	pool := testhelpers.ConnectPool(t, connStr)
	q := store.New(pool)

	sbID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	_, err := q.CreateSandbox(ctx, store.CreateSandboxParams{
		ID:                 sbID,
		AppName:            "test-cas-success",
		UserID:             "user-1",
		Image:              "appx/sandbox:v1",
		Resources:          []byte(`{"cpu_cores": 0.5, "memory_mb": 512}`),
		Env:                []byte(`{}`),
		IdleTimeoutSeconds: 1800,
		Metadata:           []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("creating sandbox: %v", err)
	}

	// Transition pending -> starting
	updated, err := q.TransitionSandboxState(ctx, store.TransitionSandboxStateParams{
		State:   "starting",
		ID:      sbID,
		State_2: "pending",
	})
	if err != nil {
		t.Fatalf("transitioning state: %v", err)
	}
	if updated.State != "starting" {
		t.Errorf("expected state 'starting', got %q", updated.State)
	}
	if updated.StateVersion != 1 {
		t.Errorf("expected state_version 1, got %d", updated.StateVersion)
	}
}

func TestTransitionSandboxState_CASRejectsConcurrentWrite(t *testing.T) {
	connStr, ctr := testhelpers.SetupTestDB(t)
	ctx := context.Background()

	if err := ctr.Restore(ctx); err != nil {
		t.Fatalf("restoring snapshot: %v", err)
	}

	pool := testhelpers.ConnectPool(t, connStr)
	q := store.New(pool)

	sbID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	_, err := q.CreateSandbox(ctx, store.CreateSandboxParams{
		ID:                 sbID,
		AppName:            "test-cas-reject",
		UserID:             "user-1",
		Image:              "appx/sandbox:v1",
		Resources:          []byte(`{"cpu_cores": 0.5, "memory_mb": 512}`),
		Env:                []byte(`{}`),
		IdleTimeoutSeconds: 1800,
		Metadata:           []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("creating sandbox: %v", err)
	}

	// First transition: pending -> starting (succeeds)
	_, err = q.TransitionSandboxState(ctx, store.TransitionSandboxStateParams{
		State:   "starting",
		ID:      sbID,
		State_2: "pending",
	})
	if err != nil {
		t.Fatalf("first transition: %v", err)
	}

	// Second transition: attempt from stale "pending" state (should fail -- CAS rejects)
	_, err = q.TransitionSandboxState(ctx, store.TransitionSandboxStateParams{
		State:   "running",
		ID:      sbID,
		State_2: "pending", // stale: actual state is now "starting"
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected pgx.ErrNoRows for stale CAS, got: %v", err)
	}
}

func TestTransitionSandboxState_CASRejectsInvalidState(t *testing.T) {
	connStr, ctr := testhelpers.SetupTestDB(t)
	ctx := context.Background()

	if err := ctr.Restore(ctx); err != nil {
		t.Fatalf("restoring snapshot: %v", err)
	}

	pool := testhelpers.ConnectPool(t, connStr)
	q := store.New(pool)

	sbID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	_, err := q.CreateSandbox(ctx, store.CreateSandboxParams{
		ID:                 sbID,
		AppName:            "test-cas-invalid",
		UserID:             "user-1",
		Image:              "appx/sandbox:v1",
		Resources:          []byte(`{"cpu_cores": 0.5, "memory_mb": 512}`),
		Env:                []byte(`{}`),
		IdleTimeoutSeconds: 1800,
		Metadata:           []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("creating sandbox: %v", err)
	}

	// Attempt transition from a state that doesn't match
	_, err = q.TransitionSandboxState(ctx, store.TransitionSandboxStateParams{
		State:   "running",
		ID:      sbID,
		State_2: "nonexistent_state",
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected pgx.ErrNoRows for invalid expected state, got: %v", err)
	}
}

func TestAssignSandboxToNode(t *testing.T) {
	connStr, ctr := testhelpers.SetupTestDB(t)
	ctx := context.Background()

	if err := ctr.Restore(ctx); err != nil {
		t.Fatalf("restoring snapshot: %v", err)
	}

	pool := testhelpers.ConnectPool(t, connStr)
	q := store.New(pool)

	// Create a node first.
	nodeID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	_, err := q.CreateNode(ctx, store.CreateNodeParams{
		ID:              nodeID,
		Hostname:        "node-assign-test",
		TailscaleIp:     mustParseAddr("100.64.1.5"),
		AgentListenPort: 8090,
		CapacityMb:      24000,
		CapacityCpu:     pgtype.Numeric{Int: mustBigInt(4), Exp: 0, Valid: true},
		AgentVersion:    "v0.1.0",
		Metadata:        []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("creating node: %v", err)
	}

	// Create a sandbox.
	sbID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	_, err = q.CreateSandbox(ctx, store.CreateSandboxParams{
		ID:                 sbID,
		AppName:            "test-assign",
		UserID:             "user-1",
		Image:              "appx/sandbox:v1",
		Resources:          []byte(`{"cpu_cores": 0.5, "memory_mb": 512}`),
		Env:                []byte(`{}`),
		IdleTimeoutSeconds: 1800,
		Metadata:           []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("creating sandbox: %v", err)
	}

	// Assign sandbox to node.
	assigned, err := q.AssignSandboxToNode(ctx, store.AssignSandboxToNodeParams{
		NodeID:      nodeID,
		HostPort:    pgtype.Int4{Int32: 43210, Valid: true},
		ContainerID: pgtype.Text{String: "abc123container", Valid: true},
		ID:          sbID,
	})
	if err != nil {
		t.Fatalf("assigning sandbox to node: %v", err)
	}
	if assigned.State != "starting" {
		t.Errorf("expected state 'starting', got %q", assigned.State)
	}
	if assigned.NodeID != nodeID {
		t.Errorf("expected node_id to match")
	}
	if assigned.HostPort.Int32 != 43210 {
		t.Errorf("expected host_port 43210, got %d", assigned.HostPort.Int32)
	}
	if assigned.ContainerID.String != "abc123container" {
		t.Errorf("expected container_id 'abc123container', got %q", assigned.ContainerID.String)
	}
}

func TestPollPendingCommands(t *testing.T) {
	connStr, ctr := testhelpers.SetupTestDB(t)
	ctx := context.Background()

	if err := ctr.Restore(ctx); err != nil {
		t.Fatalf("restoring snapshot: %v", err)
	}

	pool := testhelpers.ConnectPool(t, connStr)
	q := store.New(pool)

	// Create a node.
	nodeID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	_, err := q.CreateNode(ctx, store.CreateNodeParams{
		ID:              nodeID,
		Hostname:        "node-poll-test",
		TailscaleIp:     mustParseAddr("100.64.1.6"),
		AgentListenPort: 8090,
		CapacityMb:      24000,
		CapacityCpu:     pgtype.Numeric{Int: mustBigInt(4), Exp: 0, Valid: true},
		AgentVersion:    "v0.1.0",
		Metadata:        []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("creating node: %v", err)
	}

	// Create 2 pending commands.
	for i := 0; i < 2; i++ {
		_, err := q.CreateCommand(ctx, store.CreateCommandParams{
			ID:             pgtype.UUID{Bytes: uuid.New(), Valid: true},
			NodeID:         nodeID,
			SandboxID:      pgtype.UUID{Valid: false}, // nullable
			CommandType:    "start_sandbox",
			Payload:        []byte(`{}`),
			TimeoutSeconds: 60,
		})
		if err != nil {
			t.Fatalf("creating pending command %d: %v", i, err)
		}
	}

	// Create 1 already-dispatched command via raw SQL.
	dispatchedID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO commands (id, node_id, command_type, payload, status, timeout_seconds)
		VALUES ($1, $2, 'prune', '{}', 'dispatched', 60)
	`, dispatchedID, nodeID.Bytes)
	if err != nil {
		t.Fatalf("creating dispatched command: %v", err)
	}

	// Poll -- should return only the 2 pending commands.
	polled, err := q.PollPendingCommands(ctx, nodeID)
	if err != nil {
		t.Fatalf("polling commands: %v", err)
	}
	if len(polled) != 2 {
		t.Fatalf("expected 2 polled commands, got %d", len(polled))
	}
	for _, cmd := range polled {
		if cmd.Status != "dispatched" {
			t.Errorf("expected polled command status 'dispatched', got %q", cmd.Status)
		}
	}

	// Poll again -- should return 0 (all already dispatched).
	polled2, err := q.PollPendingCommands(ctx, nodeID)
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if len(polled2) != 0 {
		t.Fatalf("expected 0 commands on second poll, got %d", len(polled2))
	}
}

func TestRecordEvent(t *testing.T) {
	connStr, ctr := testhelpers.SetupTestDB(t)
	ctx := context.Background()

	if err := ctr.Restore(ctx); err != nil {
		t.Fatalf("restoring snapshot: %v", err)
	}

	pool := testhelpers.ConnectPool(t, connStr)
	q := store.New(pool)

	sbID := pgtype.UUID{Bytes: uuid.New(), Valid: true}

	evt, err := q.RecordEvent(ctx, store.RecordEventParams{
		SandboxID: sbID,
		NodeID:    pgtype.UUID{Valid: false},
		EventType: "scheduled",
		Actor:     "control",
		PrevState: pgtype.Text{String: "pending", Valid: true},
		NextState: pgtype.Text{String: "starting", Valid: true},
		Payload:   []byte(`{"reason": "test"}`),
	})
	if err != nil {
		t.Fatalf("recording event: %v", err)
	}
	if evt.EventType != "scheduled" {
		t.Errorf("expected event_type 'scheduled', got %q", evt.EventType)
	}
	if evt.PrevState.String != "pending" {
		t.Errorf("expected prev_state 'pending', got %q", evt.PrevState.String)
	}
	if evt.NextState.String != "starting" {
		t.Errorf("expected next_state 'starting', got %q", evt.NextState.String)
	}

	// List events by sandbox -- should find the one we just recorded.
	events, err := q.ListEventsBySandbox(ctx, store.ListEventsBySandboxParams{
		SandboxID: sbID,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("listing events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].ID != evt.ID {
		t.Errorf("event ID mismatch")
	}
}

// --- helpers ---

func mustParseAddr(s string) netip.Addr {
	addr, err := netip.ParseAddr(s)
	if err != nil {
		panic(err)
	}
	return addr
}

func mustBigInt(n int64) *big.Int {
	return big.NewInt(n)
}
