// Package main is the entry point for the forge-control service.
// It wires config, Postgres, migrations, lifecycle, HTTP server, and a
// background heartbeat monitor into a single binary with graceful shutdown.
package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"math/big"
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
	"github.com/appx/forge/control/internal/routing"
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

	// Route manager (Caddy proxy integration)
	caddyClient := routing.NewCaddyClient(cfg.CaddyAdminURL)
	batcher := routing.NewBatcher(caddyClient, logger)
	defer batcher.Stop()
	routeManager := routing.NewRouteManager(batcher, logger)
	lc.SetRouteNotifier(routeManager)
	logger.Info("route manager wired", "caddy_admin_url", cfg.CaddyAdminURL)

	// Restart manager (auto-restart with exponential backoff)
	restartMgr := lifecycle.NewRestartManager(adapter, logger)
	lc.SetRestartManager(restartMgr)
	logger.Info("restart manager wired")

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
	srv.SetMetricsStore(adapter)
	srv.SetRouteFetcher(caddyClient)
	srv.SetEventStore(&eventStoreAdapter{q: queries})
	srv.SetLogProxyStore(&filePushAdapter{q: queries}, nil)

	httpSrv := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: srv,
		BaseContext: func(_ net.Listener) context.Context {
			return ctx
		},
	}

	// ── Rescheduler (node failover) ───────────────────────────────────
	rescheduler := lifecycle.NewRescheduler(adapter, routeManager, logger)
	logger.Info("rescheduler wired")

	// ── Background heartbeat monitor ───────────────────────────────────
	go monitorHeartbeats(ctx, queries, cfg, logger, rescheduler)

	// ── Idle reaper (background goroutine) ─────────────────────────────
	idleReaper := lifecycle.NewIdleReaper(adapter, routeManager, logger,
		time.Duration(cfg.IdleReaperIntervalSeconds)*time.Second)
	go idleReaper.Run(ctx)
	logger.Info("idle reaper started", "interval_seconds", cfg.IdleReaperIntervalSeconds)

	// ── Drift detector (background goroutine) ──────────────────────────
	driftStore := &driftStoreAdapter{q: queries}
	driftDetector := routing.NewDriftDetector(caddyClient, driftStore, logger,
		time.Duration(cfg.DriftDetectorIntervalSeconds)*time.Second)
	go driftDetector.Run(ctx)
	logger.Info("drift detector started", "interval_seconds", cfg.DriftDetectorIntervalSeconds)

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

	// Cancel context stops background goroutines (heartbeat monitor,
	// idle reaper, drift detector)
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
// When a node is marked unhealthy, it triggers the rescheduler to move running
// sandboxes to healthy nodes.
func monitorHeartbeats(ctx context.Context, q *store.Queries, cfg *config.Config, logger *slog.Logger, rescheduler *lifecycle.Rescheduler) {
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
					} else {
						// Trigger reschedule for the newly-unhealthy node
						result, rErr := rescheduler.RescheduleNode(ctx, n.ID)
						if rErr != nil {
							logger.Error("reschedule failed for unhealthy node",
								"error", rErr,
								"node_id", formatNodeID(n.ID),
							)
						} else if result.Count > 0 {
							logger.Info("rescheduled sandboxes from unhealthy node",
								"node_id", formatNodeID(n.ID),
								"rescheduled", result.Count,
								"failed", result.Failed,
							)
						}
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

// float64ToNumeric converts a float64 to pgtype.Numeric.
// pgtype.Numeric.Scan does not accept float64, so we convert via
// big.Int with integer and fractional parts.
func float64ToNumeric(f float64) pgtype.Numeric {
	// Multiply by 1000 to preserve 3 decimal places, store with Exp=-3
	scaled := int64(f * 1000)
	return pgtype.Numeric{
		Int:   big.NewInt(scaled),
		Exp:   -3,
		Valid: true,
	}
}

// ── Store Adapter ──────────────────────────────────────────────────────

// storeAdapter bridges the sqlc-generated store.Queries to the interface types
// expected by the HTTP handlers and lifecycle service. It implements:
//   - api.NodeStore
//   - api.SandboxReader
//   - api.AgentStore
//   - api.MetricsStore
//   - lifecycle.Store
//   - lifecycle.RestartStore
//   - lifecycle.RescheduleStore
//   - lifecycle.IdleReaperStore
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
	cpuNum := float64ToNumeric(arg.CapacityCPU)

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

func (a *storeAdapter) UpdateNodeStatus(ctx context.Context, id pgtype.UUID, status string) error {
	return a.q.UpdateNodeStatus(ctx, store.UpdateNodeStatusParams{
		ID:     id,
		Status: status,
	})
}

func (a *storeAdapter) CountActiveSandboxesByNode(ctx context.Context, nodeID pgtype.UUID) (int32, error) {
	return a.q.CountActiveSandboxesByNode(ctx, nodeID)
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

func (a *storeAdapter) UpdateSandboxRuntime(ctx context.Context, arg store.UpdateSandboxRuntimeParams) error {
	return a.q.UpdateSandboxRuntime(ctx, arg)
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

func (a *storeAdapter) GetNodeByID(ctx context.Context, id pgtype.UUID) (store.Node, error) {
	return a.q.GetNode(ctx, id)
}

// ── lifecycle.RestartStore interface ──────────────────────────────────

func (a *storeAdapter) IncrementSandboxFailureCount(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
	return a.q.IncrementSandboxFailureCount(ctx, id)
}

func (a *storeAdapter) ResetSandboxFailureCount(ctx context.Context, id pgtype.UUID) error {
	return a.q.ResetSandboxFailureCount(ctx, id)
}

// ── lifecycle.RescheduleStore interface ──────────────────────────────

func (a *storeAdapter) ListRunningSandboxesByNode(ctx context.Context, nodeID pgtype.UUID) ([]store.Sandbox, error) {
	return a.q.ListRunningSandboxesByNode(ctx, nodeID)
}

// ── lifecycle.IdleReaperStore interface ───────────────────────────────

func (a *storeAdapter) ListIdleSandboxes(ctx context.Context) ([]store.Sandbox, error) {
	return a.q.ListIdleSandboxes(ctx)
}

// ── api.MetricsStore interface ───────────────────────────────────────

func (a *storeAdapter) CountSandboxesByState(ctx context.Context) ([]store.CountSandboxesByStateRow, error) {
	return a.q.CountSandboxesByState(ctx)
}

func (a *storeAdapter) ListNodes(ctx context.Context) ([]store.Node, error) {
	return a.q.ListNodes(ctx)
}

// ── driftStoreAdapter ────────────────────────────────────────────────

// driftStoreAdapter is a separate adapter for routing.DriftStore because its
// GetNode returns store.Node while the main storeAdapter.GetNode returns
// api.NodeRecord. Go does not allow two methods with the same name and
// different return types on one struct.
type driftStoreAdapter struct {
	q *store.Queries
}

func (a *driftStoreAdapter) ListSandboxesByState(ctx context.Context, state string) ([]store.Sandbox, error) {
	return a.q.ListSandboxesByState(ctx, state)
}

func (a *driftStoreAdapter) GetNode(ctx context.Context, id pgtype.UUID) (store.Node, error) {
	return a.q.GetNode(ctx, id)
}

// ── eventStoreAdapter ───────────────────────────────────────────────

// eventStoreAdapter bridges store.Queries to api.EventStore.
type eventStoreAdapter struct {
	q *store.Queries
}

func (a *eventStoreAdapter) ListEventsBySandbox(ctx context.Context, arg store.ListEventsBySandboxParams) ([]store.Event, error) {
	return a.q.ListEventsBySandbox(ctx, arg)
}

func (a *eventStoreAdapter) ListEventsByType(ctx context.Context, arg store.ListEventsByTypeParams) ([]store.Event, error) {
	return a.q.ListEventsByType(ctx, arg)
}

func (a *eventStoreAdapter) ListRecentEvents(ctx context.Context, limit int32) ([]store.Event, error) {
	return a.q.ListRecentEvents(ctx, limit)
}
