package tests

import (
	"context"
	"testing"

	"github.com/appx/forge/control/internal/store"
	"github.com/appx/forge/control/tests/testhelpers"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// createCapTestNode inserts a healthy node and returns its UUID.
func createCapTestNode(t *testing.T, ctx context.Context, q *store.Queries, hostname, ip string) pgtype.UUID {
	t.Helper()
	nodeID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	_, err := q.CreateNode(ctx, store.CreateNodeParams{
		ID:              nodeID,
		Hostname:        hostname,
		TailscaleIp:     mustParseAddr(ip),
		AgentListenPort: 8090,
		CapacityMb:      64000,
		CapacityCpu:     pgtype.Numeric{Int: mustBigInt(8), Exp: 0, Valid: true},
		AgentVersion:    "v0.1.0",
		Metadata:        []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("creating node %s: %v", hostname, err)
	}
	return nodeID
}

// createPendingSandbox inserts a PENDING sandbox row and returns its UUID.
func createPendingSandbox(t *testing.T, ctx context.Context, q *store.Queries, appName string) pgtype.UUID {
	t.Helper()
	sbID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	_, err := q.CreateSandbox(ctx, store.CreateSandboxParams{
		ID:                 sbID,
		AppName:            appName,
		UserID:             "user-cap",
		Image:              "appx/sandbox:v1",
		Resources:          []byte(`{"cpu_cores": 0.5, "memory_mb": 512}`),
		Env:                []byte(`{}`),
		IdleTimeoutSeconds: 1800,
		Metadata:           []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("creating sandbox %s: %v", appName, err)
	}
	return sbID
}

// TestAssignPendingSandboxUnderCap_EnforcesCap exercises the REAL cap-aware
// assignment query end-to-end against Postgres. This is the regression test for
// the prior broken attempt where the @cap param was mis-bound to a UUID
// (generated as NodeID_2 pgtype.UUID), so every capped assign failed at runtime
// with "operator does not exist: bigint < uuid". Here the Cap param MUST be a
// real integer reaching the `count(*) < $3::int` comparison.
//
// With cap=N on a single node: the first N pending sandboxes must all be
// admitted (assigned=true, state -> starting, bound to the node); the (N+1)th
// must be rejected (assigned=false, nil error) because the node is at cap.
func TestAssignPendingSandboxUnderCap_EnforcesCap(t *testing.T) {
	connStr, ctr := testhelpers.SetupTestDB(t)
	ctx := context.Background()

	if err := ctr.Restore(ctx); err != nil {
		t.Fatalf("restoring snapshot: %v", err)
	}

	pool := testhelpers.ConnectPool(t, connStr)
	q := store.New(pool)

	const cap int32 = 3
	nodeID := createCapTestNode(t, ctx, q, "node-cap-enforce", "100.64.2.1")

	// First N=cap assigns must all admit.
	for i := int32(0); i < cap; i++ {
		sbID := createPendingSandbox(t, ctx, q, "cap-app-"+string(rune('a'+i)))
		assigned, sb, err := store.AssignPendingSandboxUnderCap(ctx, pool, nodeID, sbID, cap)
		if err != nil {
			t.Fatalf("assign %d under cap: unexpected error (binding/type bug?): %v", i, err)
		}
		if !assigned {
			t.Fatalf("assign %d under cap: expected admitted (live count %d < cap %d)", i, i, cap)
		}
		if sb.State != "starting" {
			t.Fatalf("assign %d: expected state 'starting', got %q", i, sb.State)
		}
		if sb.NodeID != nodeID {
			t.Fatalf("assign %d: sandbox not bound to node", i)
		}
	}

	// The (N+1)th assign must be REJECTED — node is now at cap.
	overflowID := createPendingSandbox(t, ctx, q, "cap-app-overflow")
	assigned, _, err := store.AssignPendingSandboxUnderCap(ctx, pool, nodeID, overflowID, cap)
	if err != nil {
		t.Fatalf("overflow assign: unexpected error (must be nil when at cap): %v", err)
	}
	if assigned {
		t.Fatalf("overflow assign: cap defeated — %dth sandbox admitted past cap %d (OOM risk)", cap+1, cap)
	}

	// The rejected sandbox must remain PENDING (untouched), not stuck in starting.
	overflowSB, err := q.GetSandbox(ctx, overflowID)
	if err != nil {
		t.Fatalf("re-reading overflow sandbox: %v", err)
	}
	if overflowSB.State != "pending" {
		t.Fatalf("rejected sandbox should remain 'pending', got %q", overflowSB.State)
	}
	if overflowSB.NodeID.Valid {
		t.Fatalf("rejected sandbox should not be bound to a node, got node_id valid")
	}

	// The authoritative live count must equal the cap exactly — no overshoot.
	live, err := q.CountSchedulableSandboxesByNode(ctx, nodeID)
	if err != nil {
		t.Fatalf("counting schedulable: %v", err)
	}
	if live != cap {
		t.Fatalf("live schedulable count = %d, want exactly %d (cap must hold)", live, cap)
	}
}

// TestAssignPendingSandboxUnderCap_CapParamIsHonored proves the Cap argument is
// actually threaded into the query (not ignored / not a constant). Lowering the
// cap below the current live count must reject; a generous cap must admit. This
// would fail outright if Cap were still mis-typed as a UUID (the query would
// error rather than compare), and would also catch a constant-folded cap.
func TestAssignPendingSandboxUnderCap_CapParamIsHonored(t *testing.T) {
	connStr, ctr := testhelpers.SetupTestDB(t)
	ctx := context.Background()

	if err := ctr.Restore(ctx); err != nil {
		t.Fatalf("restoring snapshot: %v", err)
	}

	pool := testhelpers.ConnectPool(t, connStr)
	q := store.New(pool)

	nodeID := createCapTestNode(t, ctx, q, "node-cap-honor", "100.64.2.2")

	// Seed 2 already-running sandboxes on the node (live count = 2).
	for i := 0; i < 2; i++ {
		sbID := createPendingSandbox(t, ctx, q, "seed-app-"+string(rune('a'+i)))
		ok, _, err := store.AssignPendingSandboxUnderCap(ctx, pool, nodeID, sbID, 10)
		if err != nil || !ok {
			t.Fatalf("seeding sandbox %d: ok=%v err=%v", i, ok, err)
		}
	}

	// cap=2 with live count already 2 -> must reject (2 < 2 is false).
	tightID := createPendingSandbox(t, ctx, q, "tight-app")
	assigned, _, err := store.AssignPendingSandboxUnderCap(ctx, pool, nodeID, tightID, 2)
	if err != nil {
		t.Fatalf("tight-cap assign: unexpected error: %v", err)
	}
	if assigned {
		t.Fatalf("tight-cap assign: expected rejection at cap=2 with live count 2")
	}

	// cap=3 with live count still 2 -> must admit (2 < 3 is true). Reuse the
	// same still-pending sandbox.
	assigned, sb, err := store.AssignPendingSandboxUnderCap(ctx, pool, nodeID, tightID, 3)
	if err != nil {
		t.Fatalf("loose-cap assign: unexpected error: %v", err)
	}
	if !assigned {
		t.Fatalf("loose-cap assign: expected admission at cap=3 with live count 2")
	}
	if sb.State != "starting" {
		t.Fatalf("loose-cap assign: expected state 'starting', got %q", sb.State)
	}
}
