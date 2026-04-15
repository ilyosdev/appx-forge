package testhelpers

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // Register "pgx" driver for database/sql
	"github.com/pressly/goose/v3"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// MigrationsDir returns the absolute path to the control/migrations/ directory.
// Uses runtime.Caller to locate relative to this source file.
func MigrationsDir() string {
	_, thisFile, _, _ := runtime.Caller(0)
	// thisFile = .../control/tests/testhelpers/postgres.go
	// migrations = .../control/migrations/
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "migrations")
}

// SetupTestDB starts a Postgres 16-alpine container, runs goose migrations,
// and creates a snapshot for fast restore between tests. Returns the connection
// string and the container (for Snapshot/Restore).
func SetupTestDB(t *testing.T) (string, *postgres.PostgresContainer) {
	t.Helper()
	ctx := context.Background()

	ctr, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("forge_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		postgres.WithSQLDriver("pgx"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2),
		),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatalf("starting postgres container: %v", err)
	}

	connStr, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("getting connection string: %v", err)
	}

	// Run goose migrations from the filesystem.
	migrationsDir := MigrationsDir()
	if _, err := os.Stat(migrationsDir); os.IsNotExist(err) {
		t.Fatalf("migrations directory not found: %s", migrationsDir)
	}

	db, err := sql.Open("pgx", connStr)
	if err != nil {
		t.Fatalf("opening sql connection for goose: %v", err)
	}

	goose.SetBaseFS(nil) // Use real filesystem, not embed.
	if err := goose.SetDialect("postgres"); err != nil {
		db.Close()
		t.Fatalf("setting goose dialect: %v", err)
	}
	if err := goose.Up(db, migrationsDir); err != nil {
		db.Close()
		t.Fatalf("running goose up: %v", err)
	}

	// Close the goose connection before snapshot -- Postgres requires no
	// active connections to the template database during CREATE DATABASE.
	db.Close()

	// Create snapshot for restore between tests.
	if err := ctr.Snapshot(ctx, postgres.WithSnapshotName("migrated")); err != nil {
		t.Fatalf("creating snapshot: %v", err)
	}

	return connStr, ctr
}

// ConnectPool creates a pgxpool connection pool from a connection string.
func ConnectPool(t *testing.T, connStr string) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatalf("creating pgxpool: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	return pool
}
