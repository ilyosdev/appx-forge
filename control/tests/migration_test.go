package tests

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/appx/forge/control/tests/testhelpers"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"
)

func TestMigrationUpDownUp(t *testing.T) {
	connStr, ctr := testhelpers.SetupTestDB(t)
	ctx := context.Background()

	// After SetupTestDB, migrations are already up. Verify 4 user tables exist.
	pool := testhelpers.ConnectPool(t, connStr)

	tables := filterGooseTables(queryPublicTables(t, ctx, pool))
	if len(tables) != 4 {
		t.Fatalf("expected 4 tables after up, got %d: %v", len(tables), tables)
	}
	assertContains(t, tables, "nodes")
	assertContains(t, tables, "sandboxes")
	assertContains(t, tables, "events")
	assertContains(t, tables, "commands")

	// Run goose down to 0.
	if err := ctr.Restore(ctx); err != nil {
		t.Fatalf("restoring snapshot: %v", err)
	}

	db, err := sql.Open("pgx", connStr)
	if err != nil {
		t.Fatalf("opening sql connection: %v", err)
	}
	defer db.Close()

	migrationsDir := testhelpers.MigrationsDir()
	goose.SetBaseFS(nil)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("setting goose dialect: %v", err)
	}

	if err := goose.DownTo(db, migrationsDir, 0); err != nil {
		t.Fatalf("goose down to 0: %v", err)
	}

	// Reconnect pool after schema change.
	pool2 := testhelpers.ConnectPool(t, connStr)
	tablesAfterDown := queryPublicTables(t, ctx, pool2)
	// goose_db_version table may remain; filter it out.
	userTables := filterGooseTables(tablesAfterDown)
	if len(userTables) != 0 {
		t.Fatalf("expected 0 user tables after down, got %d: %v", len(userTables), userTables)
	}

	// Run goose up again.
	if err := goose.Up(db, migrationsDir); err != nil {
		t.Fatalf("goose up (second time): %v", err)
	}

	pool3 := testhelpers.ConnectPool(t, connStr)
	tablesAfterReup := filterGooseTables(queryPublicTables(t, ctx, pool3))
	if len(tablesAfterReup) != 4 {
		t.Fatalf("expected 4 tables after re-up, got %d: %v", len(tablesAfterReup), tablesAfterReup)
	}
}

func TestMigrationNodesTable(t *testing.T) {
	connStr, ctr := testhelpers.SetupTestDB(t)
	ctx := context.Background()

	if err := ctr.Restore(ctx); err != nil {
		t.Fatalf("restoring snapshot: %v", err)
	}

	pool := testhelpers.ConnectPool(t, connStr)

	nodeID := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO nodes (id, hostname, tailscale_ip, agent_listen_port, capacity_mb, capacity_cpu, agent_version, metadata)
		VALUES ($1, 'node-1', '100.64.1.1', 8090, 24000, 4.0, 'v0.1.0', '{}')
	`, nodeID)
	if err != nil {
		t.Fatalf("inserting node: %v", err)
	}

	var hostname string
	var capacityMb int32
	err = pool.QueryRow(ctx, `SELECT hostname, capacity_mb FROM nodes WHERE id = $1`, nodeID).Scan(&hostname, &capacityMb)
	if err != nil {
		t.Fatalf("reading node: %v", err)
	}
	if hostname != "node-1" {
		t.Errorf("expected hostname 'node-1', got %q", hostname)
	}
	if capacityMb != 24000 {
		t.Errorf("expected capacity_mb 24000, got %d", capacityMb)
	}
}

func TestSandboxStateConstraint(t *testing.T) {
	connStr, ctr := testhelpers.SetupTestDB(t)
	ctx := context.Background()

	if err := ctr.Restore(ctx); err != nil {
		t.Fatalf("restoring snapshot: %v", err)
	}

	pool := testhelpers.ConnectPool(t, connStr)

	sandboxID := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO sandboxes (id, app_name, user_id, image, state)
		VALUES ($1, 'test-app', 'user-1', 'appx/sandbox:v1', 'invalid_state')
	`, sandboxID)
	if err == nil {
		t.Fatal("expected CHECK constraint error for invalid state, got nil")
	}

	// Verify the error mentions the constraint.
	errMsg := err.Error()
	if !strings.Contains(errMsg, "sandboxes_state_check") && !strings.Contains(errMsg, "check") {
		t.Errorf("expected error to mention CHECK constraint, got: %s", errMsg)
	}
}

// --- helpers ---

func queryPublicTables(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(ctx,
		`SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' ORDER BY table_name`)
	if err != nil {
		t.Fatalf("querying tables: %v", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scanning table name: %v", err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows error: %v", err)
	}
	return tables
}

func assertContains(t *testing.T, items []string, want string) {
	t.Helper()
	for _, item := range items {
		if item == want {
			return
		}
	}
	t.Errorf("expected %v to contain %q", items, want)
}

func filterGooseTables(tables []string) []string {
	var result []string
	for _, t := range tables {
		if t != "goose_db_version" {
			result = append(result, t)
		}
	}
	return result
}
