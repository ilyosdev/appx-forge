// Package main is the entry point for the forge-control service.
// It wires config, Postgres, migrations, lifecycle, HTTP server, and a
// background heartbeat monitor into a single binary with graceful shutdown.
package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // Register "pgx" driver for database/sql
	"github.com/pressly/goose/v3"

	"github.com/appx/forge/control/internal/api"
	"github.com/appx/forge/control/internal/config"
	"github.com/appx/forge/control/internal/lifecycle"
	"github.com/appx/forge/control/internal/store"
)

func main() {
	// ── Config ──────────────────────────────────────────────────────────
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// ── Logger ─────────────────────────────────────────────────────────
	var level slog.Level
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	var handler slog.Handler
	if cfg.LogLevel == "debug" {
		handler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	} else {
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	}
	logger := slog.New(handler)

	logger.Info("forge-control starting", "listen_addr", cfg.ListenAddr)

	// ── Context with signal handling ───────────────────────────────────
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// ── Postgres ───────────────────────────────────────────────────────
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("failed to create connection pool", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		logger.Error("failed to ping database", "error", err)
		os.Exit(1)
	}
	logger.Info("database connected")

	// ── Migrations ─────────────────────────────────────────────────────
	if err := runMigrations(cfg.DatabaseURL); err != nil {
		logger.Error("failed to run migrations", "error", err)
		os.Exit(1)
	}
	logger.Info("migrations complete")

	// ── Store + Lifecycle ──────────────────────────────────────────────
	queries := store.New(pool)
	adapter := &storeAdapter{q: queries}

	lc := lifecycle.New(adapter, logger)

	// ── HTTP Server ────────────────────────────────────────────────────
	srv := api.NewServer(
		api.NewServerConfig(cfg.APIToken, cfg.HMACSecret),
		pool,
		logger,
		adapter,         // NodeStore
		lc,              // SandboxLifecycle
		adapter,         // SandboxReader
		cfg.HeartbeatIntervalSeconds,
	)
	srv.SetAgentDeps(adapter, lc)
	srv.SetFilePushStore(&filePushAdapter{q: queries})

	httpSrv := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: srv,
		BaseContext: func(_ net.Listener) context.Context {
			return ctx
		},
	}

	// ── Background heartbeat monitor ───────────────────────────────────
	go monitorHeartbeats(ctx, queries, cfg, logger)

	// ── Start HTTP server ──────────────────────────────────────────────
	errCh := make(chan error, 1)
	go func() {
		logger.Info("forge-control listening", "addr", cfg.ListenAddr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	// ── Wait for shutdown signal or error ──────────────────────────────
	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-errCh:
		if err != nil {
			logger.Error("http server error", "error", err)
		}
	}

	// ── Graceful shutdown ──────────────────────────────────────────────
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		logger.Error("http server shutdown error", "error", err)
	}

	// Cancel context stops heartbeat monitor goroutine
	cancel()
	logger.Info("forge-control shutdown complete")
}

// ── Migrations ─────────────────────────────────────────────────────────

// runMigrations opens a database/sql connection (required by goose) and runs
// all pending migrations from the filesystem.
func runMigrations(databaseURL string) error {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	goose.SetBaseFS(nil)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}

	// Locate migrations directory relative to the binary.
	// In production and docker-compose, we compile from module root so
	// the migrations path is relative to the working directory.
	migrationsDir := "migrations"
	if _, err := os.Stat(migrationsDir); os.IsNotExist(err) {
		// Fallback: when run from repo root (go run ./control/cmd/forge-control/)
		migrationsDir = "control/migrations"
	}

	return goose.Up(db, migrationsDir)
}

// ── Heartbeat Monitor ──────────────────────────────────────────────────

// monitorHeartbeats runs a background loop that marks nodes as unhealthy when
// they miss too many heartbeats. It checks all nodes every HeartbeatIntervalSeconds.
// This is NOT a reconciliation loop -- it only marks nodes unhealthy; it never
// restarts containers or changes sandbox state.
func monitorHeartbeats(ctx context.Context, q *store.Queries, cfg *config.Config, logger *slog.Logger) {
	interval := time.Duration(cfg.HeartbeatIntervalSeconds) * time.Second
	threshold := time.Duration(cfg.HeartbeatIntervalSeconds*cfg.HeartbeatMissThreshold) * time.Second

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Debug("heartbeat monitor stopped")
			return
		case <-ticker.C:
			nodes, err := q.ListNodes(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return // shutting down
				}
				logger.Warn("heartbeat monitor: failed to list nodes", "error", err)
				continue
			}

			now := time.Now()
			for _, n := range nodes {
				if n.Status != "healthy" {
					continue
				}
				if !n.LastSeenAt.Valid {
					continue
				}

				elapsed := now.Sub(n.LastSeenAt.Time)
				if elapsed > threshold {
					missedCount := int(elapsed.Seconds()) / cfg.HeartbeatIntervalSeconds
					logger.Warn("node marked unhealthy",
						"node_id", formatNodeID(n.ID),
						"hostname", n.Hostname,
						"missed_heartbeats", missedCount,
						"last_seen", n.LastSeenAt.Time.Format(time.RFC3339),
					)

					if err := q.UpdateNodeStatus(ctx, store.UpdateNodeStatusParams{
						ID:     n.ID,
						Status: "unhealthy",
					}); err != nil {
						logger.Error("heartbeat monitor: failed to update node status",
							"error", err,
							"node_id", formatNodeID(n.ID),
						)
					}
				}
			}
		}
	}
}

// formatNodeID converts a pgtype.UUID to its string representation.
func formatNodeID(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return uuid.UUID(u.Bytes).String()
}

// ── Store Adapter ──────────────────────────────────────────────────────

// storeAdapter bridges the sqlc-generated store.Queries to the interface types
// expected by the HTTP handlers and lifecycle service. It implements:
//   - api.NodeStore
//   - api.SandboxReader
//   - api.AgentStore
//   - api.FilePushStore
//   - lifecycle.Store
type storeAdapter struct {
	q *store.Queries
}

// ── NodeStore interface ────────────────────────────────────────────────

func (a *storeAdapter) GetNodeByHostnameAndIP(ctx context.Context, hostname string, ip netip.Addr) (api.NodeRecord, error) {
	n, err := a.q.GetNodeByHostnameAndIP(ctx, store.GetNodeByHostnameAndIPParams{
		Hostname:    hostname,
		TailscaleIp: ip,
	})
	if err != nil {
		return api.NodeRecord{}, err
	}
	return api.NodeRecord{ID: n.ID, Hostname: n.Hostname}, nil
}

func (a *storeAdapter) CreateNode(ctx context.Context, arg api.CreateNodeArgs) (api.NodeRecord, error) {
	cpuNum := pgtype.Numeric{}
	_ = cpuNum.Scan(arg.CapacityCPU)

	n, err := a.q.CreateNode(ctx, store.CreateNodeParams{
		ID:              arg.ID,
		Hostname:        arg.Hostname,
		TailscaleIp:     arg.TailscaleIP,
		AgentListenPort: arg.AgentListenPort,
		CapacityMb:      arg.CapacityMb,
		CapacityCpu:     cpuNum,
		AgentVersion:    arg.AgentVersion,
		Metadata:        arg.Metadata,
	})
	if err != nil {
		return api.NodeRecord{}, err
	}

	// Set agent_token via separate update (CreateNode SQL does not include it in INSERT)
	if arg.AgentToken != "" {
		if err := a.q.UpdateNodeToken(ctx, store.UpdateNodeTokenParams{
			AgentToken:   arg.AgentToken,
			AgentVersion: arg.AgentVersion,
			ID:           arg.ID,
		}); err != nil {
			return api.NodeRecord{}, err
		}
	}

	return api.NodeRecord{ID: n.ID, Hostname: n.Hostname}, nil
}

func (a *storeAdapter) UpdateNodeToken(ctx context.Context, token string, agentVersion string, id pgtype.UUID) error {
	return a.q.UpdateNodeToken(ctx, store.UpdateNodeTokenParams{
		AgentToken:   token,
		AgentVersion: agentVersion,
		ID:           id,
	})
}

func (a *storeAdapter) GetNode(ctx context.Context, id pgtype.UUID) (api.NodeRecord, error) {
	n, err := a.q.GetNode(ctx, id)
	if err != nil {
		return api.NodeRecord{}, err
	}
	return api.NodeRecord{ID: n.ID, Hostname: n.Hostname}, nil
}

func (a *storeAdapter) UpdateNodeHeartbeat(ctx context.Context, id pgtype.UUID, usedMb int32, runningContainers int32) error {
	return a.q.UpdateNodeHeartbeat(ctx, store.UpdateNodeHeartbeatParams{
		ID:                id,
		UsedMb:            usedMb,
		RunningContainers: runningContainers,
	})
}

// ── SandboxReader interface ────────────────────────────────────────────

func (a *storeAdapter) GetSandbox(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
	return a.q.GetSandbox(ctx, id)
}

func (a *storeAdapter) GetSandboxByAppName(ctx context.Context, appName string) (store.Sandbox, error) {
	return a.q.GetSandboxByAppName(ctx, appName)
}

func (a *storeAdapter) ListSandboxes(ctx context.Context, limit int32) ([]store.Sandbox, error) {
	return a.q.ListSandboxes(ctx, limit)
}

func (a *storeAdapter) ListSandboxesByState(ctx context.Context, state string) ([]store.Sandbox, error) {
	return a.q.ListSandboxesByState(ctx, state)
}

func (a *storeAdapter) ListSandboxesByNode(ctx context.Context, nodeID pgtype.UUID) ([]store.Sandbox, error) {
	return a.q.ListSandboxesByNode(ctx, nodeID)
}

func (a *storeAdapter) ListSandboxesByUser(ctx context.Context, userID string) ([]store.Sandbox, error) {
	return a.q.ListSandboxesByUser(ctx, userID)
}

// ── AgentStore interface ───────────────────────────────────────────────

func (a *storeAdapter) PollPendingCommands(ctx context.Context, nodeID pgtype.UUID) ([]store.Command, error) {
	return a.q.PollPendingCommands(ctx, nodeID)
}

func (a *storeAdapter) GetCommand(ctx context.Context, id pgtype.UUID) (store.Command, error) {
	return a.q.GetCommand(ctx, id)
}

// ── filePushAdapter ───────────────────────────────────────────────────

// filePushAdapter is a separate adapter for FilePushStore because its GetNode
// returns store.Node (full record with TailscaleIp, AgentListenPort) while
// NodeStore.GetNode returns the minimal NodeRecord. Go does not allow two
// methods with the same name and different return types on one struct.
type filePushAdapter struct {
	q *store.Queries
}

func (a *filePushAdapter) GetSandbox(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
	return a.q.GetSandbox(ctx, id)
}

func (a *filePushAdapter) GetNode(ctx context.Context, id pgtype.UUID) (store.Node, error) {
	return a.q.GetNode(ctx, id)
}

func (a *filePushAdapter) UpdateSandboxLastActive(ctx context.Context, id pgtype.UUID) error {
	return a.q.UpdateSandboxLastActive(ctx, id)
}

// ── lifecycle.Store interface ──────────────────────────────────────────

func (a *storeAdapter) CreateSandbox(ctx context.Context, arg store.CreateSandboxParams) (store.Sandbox, error) {
	return a.q.CreateSandbox(ctx, arg)
}

func (a *storeAdapter) ListHealthyNodes(ctx context.Context) ([]store.Node, error) {
	return a.q.ListHealthyNodes(ctx)
}

func (a *storeAdapter) AssignSandboxToNode(ctx context.Context, arg store.AssignSandboxToNodeParams) (store.Sandbox, error) {
	return a.q.AssignSandboxToNode(ctx, arg)
}

func (a *storeAdapter) TransitionSandboxState(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
	return a.q.TransitionSandboxState(ctx, arg)
}

func (a *storeAdapter) CreateCommand(ctx context.Context, arg store.CreateCommandParams) (store.Command, error) {
	return a.q.CreateCommand(ctx, arg)
}

func (a *storeAdapter) AckCommand(ctx context.Context, arg store.AckCommandParams) error {
	return a.q.AckCommand(ctx, arg)
}

func (a *storeAdapter) RecordEvent(ctx context.Context, arg store.RecordEventParams) (store.Event, error) {
	return a.q.RecordEvent(ctx, arg)
}
