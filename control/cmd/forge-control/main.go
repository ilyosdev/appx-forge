// Package main is the entry point for the forge-control service.
// It wires config, Postgres, migrations, lifecycle, HTTP server, and a
// background heartbeat monitor into a single binary with graceful shutdown.
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"github.com/appx/forge/control/internal/scheduler"
	"github.com/appx/forge/control/internal/store"
	"github.com/appx/forge/control/migrations"
	"github.com/appx/forge/shared-go/models"
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

	// Phase 33-B — state-change webhook notifier. Posts JSON to the
	// configured backend URL on every transition into running so the
	// pool service can flip provisioning rows to warm without polling.
	// HMAC-signed via FORGE_WEBHOOK_SECRET. Skipped entirely when
	// FORGE_WEBHOOK_URL is empty (backwards compat for environments
	// without a listener).
	if cfg.WebhookURL != "" {
		webhookClient := newStateWebhookClient(
			cfg.WebhookURL,
			cfg.WebhookSecret,
			time.Duration(cfg.WebhookTimeoutSeconds)*time.Second,
			logger,
		)
		lc.SetStateWebhookNotifier(webhookClient)
		logger.Info("state webhook notifier wired",
			"url", cfg.WebhookURL,
			"timeout_seconds", cfg.WebhookTimeoutSeconds,
		)

		// Phase 33-H2 — Startup state replay. forge-control's webhook
		// delivery is fire-and-forget (lifecycle.go:notifyStateWebhook
		// spawns a goroutine and never persists). If the process crashes
		// between sandbox state transition and goroutine completion, the
		// event is lost and backend's app_deployments row stays in the
		// pre-transition state until sweepStuckProvisioning or the user
		// retries.
		//
		// On startup, replay the same three states the runtime emits:
		// running (so PROVISIONING→WARM and ERROR→RUNNING recover),
		// failed (so PROVISIONING/WARM/RUNNING→ERROR), and destroyed
		// (so any non-terminal row→DESTROYED). Backend's listener is
		// idempotent: already-terminal rows early-return.
		//
		// Phase 33-Audit-3: extended from running-only to {running,
		// failed, destroyed} so crashes during failed/destroyed
		// transitions don't permanently strand backend state.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			states := []models.SandboxState{
				models.StateRunning,
				models.StateFailed,
				models.StateDestroyed,
			}
			totalDelivered := 0
			totalCount := 0
			for _, st := range states {
				rows, err := queries.ListSandboxesByState(ctx, string(st))
				if err != nil {
					logger.Warn("startup webhook replay: list failed",
						"state", string(st), "error", err)
					continue
				}
				logger.Info("startup webhook replay batch starting",
					"state", string(st), "count", len(rows))
				delivered := 0
				for _, sb := range rows {
					payload := lifecycle.StateChangePayload{
						SandboxID: uuid.UUID(sb.ID.Bytes).String(),
						AppName:   sb.AppName,
						UserID:    sb.UserID,
						State:     string(st),
						// Self-replay marker; backend filters on State only.
						PrevState: string(st),
						Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
					}
					if sb.ContainerID.Valid {
						payload.ContainerID = sb.ContainerID.String
					}
					if sb.HostPort.Valid {
						payload.HostPort = sb.HostPort.Int32
					}
					if sb.NodeID.Valid {
						payload.NodeID = uuid.UUID(sb.NodeID.Bytes).String()
					}
					if err := webhookClient.OnSandboxStateChanged(ctx, payload); err != nil {
						logger.Warn("startup webhook replay: delivery failed",
							"state", string(st), "error", err, "app_name", payload.AppName)
						continue
					}
					delivered++
					// Phase 33-Audit-3 — small inter-request delay so a
					// large terminal-state backlog (forge-db cleanup
					// retains 6h of destroyed/failed) doesn't crash into
					// downstream rate limits as a thundering herd. 20ms
					// between requests caps replay throughput at ~50/s,
					// well under any reasonable receiver rate limit.
					time.Sleep(20 * time.Millisecond)
				}
				logger.Info("startup webhook replay batch complete",
					"state", string(st), "delivered", delivered, "total", len(rows))
				totalDelivered += delivered
				totalCount += len(rows)
			}
			logger.Info("startup webhook replay all batches complete",
				"delivered", totalDelivered, "total", totalCount)
		}()
	} else {
		logger.Info("state webhook notifier skipped (FORGE_WEBHOOK_URL unset)")
	}

	// ── HTTP Server ────────────────────────────────────────────────────
	srvCfg := api.NewServerConfig(cfg.APIToken, cfg.HMACSecret)
	if cfg.ExecJWTSecret != "" {
		srvCfg.SetExecJWTSecret(cfg.ExecJWTSecret)
		logger.Info("exec JWT secret configured — /sandboxes/{id}/exec accepts X-Exec-Token")
	} else {
		logger.Warn("FORGE_EXEC_JWT_SECRET unset — /sandboxes/{id}/exec only accepts Bearer FORGE_API_TOKEN")
	}
	srv := api.NewServer(
		srvCfg,
		pool,
		logger,
		adapter, // NodeStore
		lc,      // SandboxLifecycle
		adapter, // SandboxReader
		cfg.HeartbeatIntervalSeconds,
	)
	srv.SetAgentDeps(adapter, lc)
	srv.SetFilePushStore(&filePushAdapter{q: queries})
	srv.SetMetricsStore(adapter)
	srv.SetRouteFetcher(caddyClient)
	srv.SetEventStore(&eventStoreAdapter{q: queries})
	srv.SetLogProxyStore(&filePushAdapter{q: queries}, nil)

	// Phase 30 — heartbeat reconciler. Diffs each rich heartbeat against
	// the per-node DB working set: bumps verified_at for confirmed rows,
	// marks agent-lost for DB rows missing past the 60s grace window.
	heartbeatReconciler := scheduler.NewHeartbeatReconciler(
		&reconcilerStoreAdapter{q: queries}, logger)
	srv.SetReconciler(&reconcilerAdapter{r: heartbeatReconciler})
	logger.Info("heartbeat reconciler wired")

	// Phase 30 — read-through freshness service. On a stale GetSandbox
	// (verified_at older than the configured window) or ?force_refresh,
	// the service synchronously calls agent's GET /v1/containers/{name}
	// to confirm container existence; on miss the row is marked destroyed.
	agentClient := &agentHTTPClient{
		store:   &agentLookupAdapter{q: queries},
		http:    &http.Client{Timeout: time.Duration(cfg.AgentRequestTimeoutSeconds) * time.Second},
		logger:  logger,
	}
	freshnessSvc := scheduler.NewSandboxFreshnessService(
		&freshnessStoreAdapter{q: queries},
		agentClient,
		time.Duration(cfg.FreshnessWindowSeconds)*time.Second,
		logger,
	)
	srv.SetFreshness(freshnessSvc)
	logger.Info("freshness service wired",
		"window_seconds", cfg.FreshnessWindowSeconds,
		"agent_timeout_seconds", cfg.AgentRequestTimeoutSeconds)

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
// all pending migrations from the embedded migrations FS.
func runMigrations(databaseURL string) error {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}

	return goose.Up(db, ".")
}

// ── Heartbeat Monitor ──────────────────────────────────────────────────

// Phase 32 Wave 2 Bug 4 — flap debounce.
//
// Before this fix, monitorHeartbeats marked a node unhealthy and called
// RescheduleNode on the FIRST tick where last_seen exceeded the threshold.
// That made any single skipped heartbeat (agent under load running a slow
// docker list_containers, transient network blip, host CPU saturation)
// cascade into a reschedule storm. Production logs on 2026-05-07 showed
// nodes flapping healthy↔unhealthy every 2-3 minutes.
//
// We now require defaultUnhealthyConfirmTicks consecutive over-threshold
// ticks before triggering reschedule. At interval=15s and confirm=4 that's
// roughly a 1-minute floor on top of the existing 45s threshold (~3 min
// total since the last good heartbeat). A single healthy heartbeat at any
// point clears the streak.
const (
	// defaultUnhealthyConfirmTicks is the number of consecutive
	// over-threshold ticks required before a node is flipped to unhealthy
	// and its sandboxes are rescheduled.
	defaultUnhealthyConfirmTicks = 4
)

// heartbeatDecision is the per-node verdict produced by evaluateHeartbeats
// for a single tick. The monitor goroutine consumes these to drive the
// store update + reschedule. shouldMarkUnhealthy is the only field the
// caller acts on; the rest exists for structured logging.
type heartbeatDecision struct {
	nodeID              pgtype.UUID
	hostname            string
	elapsed             time.Duration
	missedCount         int
	streak              int
	shouldMarkUnhealthy bool
}

// evaluateHeartbeats is a pure function: given the current node list and an
// in-memory streak map, it returns one decision per node that is currently
// `healthy` and whose last_seen has crossed the threshold. The streaks map
// is mutated in place — increment on miss, reset on healthy heartbeat,
// cleared on the tick that flags the node for reschedule (so the monitor
// doesn't keep re-firing while the DB flip / reschedule call is in flight).
//
// Returning the decisions instead of doing the I/O lets us unit-test the
// debounce logic without spinning up Postgres.
func evaluateHeartbeats(
	nodes []store.Node,
	streaks map[[16]byte]int,
	now time.Time,
	threshold time.Duration,
	intervalSeconds int,
	confirmTicks int,
) []heartbeatDecision {
	if confirmTicks < 1 {
		confirmTicks = 1 // safety: never disable debounce entirely
	}
	if intervalSeconds < 1 {
		intervalSeconds = 1 // avoid div-by-zero in missedCount
	}

	var decisions []heartbeatDecision
	for _, n := range nodes {
		if n.Status != "healthy" {
			continue
		}
		if !n.LastSeenAt.Valid {
			continue
		}

		elapsed := now.Sub(n.LastSeenAt.Time)
		if elapsed <= threshold {
			// Healthy heartbeat landed within the window — clear any
			// accumulated streak so a future flap starts from zero.
			delete(streaks, n.ID.Bytes)
			continue
		}

		streak := streaks[n.ID.Bytes] + 1
		streaks[n.ID.Bytes] = streak

		d := heartbeatDecision{
			nodeID:      n.ID,
			hostname:    n.Hostname,
			elapsed:     elapsed,
			missedCount: int(elapsed.Seconds()) / intervalSeconds,
			streak:      streak,
		}
		if streak >= confirmTicks {
			d.shouldMarkUnhealthy = true
			// Wipe the streak so the next tick doesn't immediately
			// re-trigger reschedule against the now-unhealthy row.
			delete(streaks, n.ID.Bytes)
		}
		decisions = append(decisions, d)
	}
	return decisions
}

// monitorHeartbeats runs a background loop that marks nodes as unhealthy when
// they miss too many heartbeats. It checks all nodes every HeartbeatIntervalSeconds.
// When a node is marked unhealthy, it triggers the rescheduler to move running
// sandboxes to healthy nodes.
//
// Per-node unhealthy streaks are kept in an in-memory map keyed by node UUID
// bytes (process-local, lifecycle matches the goroutine — see Bug 4 above).
func monitorHeartbeats(ctx context.Context, q *store.Queries, cfg *config.Config, logger *slog.Logger, rescheduler *lifecycle.Rescheduler) {
	interval := time.Duration(cfg.HeartbeatIntervalSeconds) * time.Second
	threshold := time.Duration(cfg.HeartbeatIntervalSeconds*cfg.HeartbeatMissThreshold) * time.Second

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	streaks := make(map[[16]byte]int)

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

			decisions := evaluateHeartbeats(
				nodes, streaks, time.Now(),
				threshold, cfg.HeartbeatIntervalSeconds, defaultUnhealthyConfirmTicks,
			)

			for _, d := range decisions {
				if !d.shouldMarkUnhealthy {
					logger.Debug("node over heartbeat threshold but within debounce window",
						"node_id", formatNodeID(d.nodeID),
						"hostname", d.hostname,
						"missed_heartbeats", d.missedCount,
						"streak", d.streak,
						"confirm_ticks", defaultUnhealthyConfirmTicks,
					)
					continue
				}

				logger.Warn("node marked unhealthy",
					"node_id", formatNodeID(d.nodeID),
					"hostname", d.hostname,
					"missed_heartbeats", d.missedCount,
					"streak", d.streak,
				)

				if err := q.UpdateNodeStatus(ctx, store.UpdateNodeStatusParams{
					ID:     d.nodeID,
					Status: "unhealthy",
				}); err != nil {
					logger.Error("heartbeat monitor: failed to update node status",
						"error", err,
						"node_id", formatNodeID(d.nodeID),
					)
					continue
				}

				// Trigger reschedule for the newly-unhealthy node.
				result, rErr := rescheduler.RescheduleNode(ctx, d.nodeID)
				if rErr != nil {
					logger.Error("reschedule failed for unhealthy node",
						"error", rErr,
						"node_id", formatNodeID(d.nodeID),
					)
				} else if result.Count > 0 {
					logger.Info("rescheduled sandboxes from unhealthy node",
						"node_id", formatNodeID(d.nodeID),
						"rescheduled", result.Count,
						"failed", result.Failed,
					)
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

func (a *storeAdapter) DeleteSandbox(ctx context.Context, id pgtype.UUID) error {
	return a.q.DeleteSandbox(ctx, id)
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

// Phase 33-Real-8 — purge command rows referencing a sandbox so the
// row can be deleted without violating commands_sandbox_id_fkey.
func (a *storeAdapter) DeleteCommandsForSandbox(ctx context.Context, sandboxID pgtype.UUID) error {
	return a.q.DeleteCommandsForSandbox(ctx, sandboxID)
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

// ── reconcilerStoreAdapter ─────────────────────────────────────────────

// reconcilerStoreAdapter implements scheduler.SandboxStore. Translates
// sqlc rows into the package-local SandboxRow shape (which carries only
// the three fields the reconciler needs).
type reconcilerStoreAdapter struct {
	q *store.Queries
}

func (a *reconcilerStoreAdapter) ListSandboxesForNode(ctx context.Context, nodeID pgtype.UUID) ([]scheduler.SandboxRow, error) {
	rows, err := a.q.ListSandboxesForNode(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	out := make([]scheduler.SandboxRow, len(rows))
	for i, r := range rows {
		out[i] = scheduler.SandboxRow{
			AppName:   r.AppName,
			State:     r.State,
			CreatedAt: r.CreatedAt.Time,
		}
	}
	return out, nil
}

func (a *reconcilerStoreAdapter) MarkSandboxVerified(ctx context.Context, appName, state string) error {
	return a.q.MarkSandboxVerified(ctx, store.MarkSandboxVerifiedParams{
		AppName: appName,
		State:   state,
	})
}

func (a *reconcilerStoreAdapter) MarkSandboxAgentLost(ctx context.Context, appName string, nodeID pgtype.UUID) error {
	return a.q.MarkSandboxAgentLost(ctx, store.MarkSandboxAgentLostParams{
		AppName: appName,
		NodeID:  nodeID,
	})
}

// Phase 33-Real-7 — terminal-row containers the agent still observes.
func (a *reconcilerStoreAdapter) ListTerminalSandboxesForNode(ctx context.Context, nodeID pgtype.UUID) ([]scheduler.TerminalSandboxRow, error) {
	rows, err := a.q.ListTerminalSandboxesForNode(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	out := make([]scheduler.TerminalSandboxRow, 0, len(rows))
	for _, r := range rows {
		if !r.ContainerID.Valid || r.ContainerID.String == "" {
			continue
		}
		out = append(out, scheduler.TerminalSandboxRow{
			ID:          r.ID,
			AppName:     r.AppName,
			ContainerID: r.ContainerID.String,
		})
	}
	return out, nil
}

// DispatchStopSandbox creates a stop_sandbox command targeted at the
// node so the agent destroys the orphan container. Mirrors idle_reaper's
// dispatch pattern (lifecycle/idle_reaper.go) — same payload shape and
// timeout. Reason is recorded on the command for audit.
func (a *reconcilerStoreAdapter) DispatchStopSandbox(ctx context.Context, sandboxID, nodeID pgtype.UUID, containerID, reason string) error {
	cmdPayload, err := json.Marshal(map[string]interface{}{
		"container_id": containerID,
		"reason":       reason,
	})
	if err != nil {
		return err
	}
	cmdID := uuid.New()
	_, err = a.q.CreateCommand(ctx, store.CreateCommandParams{
		ID:             pgtype.UUID{Bytes: cmdID, Valid: true},
		NodeID:         nodeID,
		SandboxID:      sandboxID,
		CommandType:    string(models.CmdStopSandbox),
		Payload:        cmdPayload,
		TimeoutSeconds: 60,
	})
	return err
}

// ── reconcilerAdapter ──────────────────────────────────────────────────

// reconcilerAdapter satisfies api.Reconciler by translating api.ContainerInfo
// to scheduler.ContainerInfo. The duplicate types exist because scheduler
// cannot import api without closing an import cycle (api → lifecycle →
// scheduler).
type reconcilerAdapter struct {
	r *scheduler.HeartbeatReconciler
}

func (a *reconcilerAdapter) Reconcile(ctx context.Context, nodeID pgtype.UUID, containers []api.ContainerInfo) error {
	out := make([]scheduler.ContainerInfo, len(containers))
	for i, c := range containers {
		out[i] = scheduler.ContainerInfo{
			AppName:     c.AppName,
			State:       c.State,
			HostPort:    c.HostPort,
			ContainerID: c.ContainerID,
		}
	}
	return a.r.Reconcile(ctx, nodeID, out)
}

// ── freshnessStoreAdapter ──────────────────────────────────────────────

// freshnessStoreAdapter implements scheduler.FreshnessStore by translating
// sqlc rows to scheduler.SandboxRow. Reuses the T7 MarkSandboxVerified
// query and the new (T9) MarkSandboxDestroyed query.
type freshnessStoreAdapter struct {
	q *store.Queries
}

func (a *freshnessStoreAdapter) GetSandboxByName(ctx context.Context, name string) (*scheduler.SandboxRow, time.Time, error) {
	sb, err := a.q.GetSandboxByAppName(ctx, name)
	if err != nil {
		return nil, time.Time{}, err
	}
	row := &scheduler.SandboxRow{
		AppName:   sb.AppName,
		State:     sb.State,
		CreatedAt: sb.CreatedAt.Time,
	}
	var verifiedAt time.Time
	if sb.VerifiedAt.Valid {
		verifiedAt = sb.VerifiedAt.Time
	}
	return row, verifiedAt, nil
}

func (a *freshnessStoreAdapter) MarkSandboxVerified(ctx context.Context, appName, state string) error {
	return a.q.MarkSandboxVerified(ctx, store.MarkSandboxVerifiedParams{
		AppName: appName,
		State:   state,
	})
}

func (a *freshnessStoreAdapter) MarkSandboxDestroyed(ctx context.Context, appName, reason string) error {
	return a.q.MarkSandboxDestroyed(ctx, store.MarkSandboxDestroyedParams{
		AppName: appName,
		Reason:  reason,
	})
}

// ── agentHTTPClient ────────────────────────────────────────────────────

// agentLookupStore is the read surface agentHTTPClient needs to resolve
// app_name → node tailscale_ip + agent_listen_port (where the GET goes).
type agentLookupStore interface {
	GetSandboxByAppName(ctx context.Context, appName string) (store.Sandbox, error)
	GetNode(ctx context.Context, id pgtype.UUID) (store.Node, error)
}

type agentLookupAdapter struct {
	q *store.Queries
}

func (a *agentLookupAdapter) GetSandboxByAppName(ctx context.Context, appName string) (store.Sandbox, error) {
	return a.q.GetSandboxByAppName(ctx, appName)
}

func (a *agentLookupAdapter) GetNode(ctx context.Context, id pgtype.UUID) (store.Node, error) {
	return a.q.GetNode(ctx, id)
}

// agentHTTPClient satisfies scheduler.AgentClient by calling the agent's
// GET /v1/containers/{name} endpoint (added in T5). The endpoint is
// unauthenticated — it is only reachable via Tailscale. Network failure
// or non-2xx/404 status surfaces as err so the freshness service falls
// through to the cached row instead of declaring the container missing.
type agentHTTPClient struct {
	store  agentLookupStore
	http   *http.Client
	logger *slog.Logger
}

// containerExistsResponse mirrors the agent-side response shape
// (agent/internal/agent/containers_handler.go). Duplicated here to keep
// control free of any agent-package dependency.
type containerExistsResponse struct {
	Exists      bool   `json:"exists"`
	AppName     string `json:"app_name"`
	State       string `json:"state,omitempty"`
	HostPort    int    `json:"host_port,omitempty"`
	ContainerID string `json:"container_id,omitempty"`
}

func (c *agentHTTPClient) ContainerExists(ctx context.Context, name string) (bool, string, error) {
	sb, err := c.store.GetSandboxByAppName(ctx, name)
	if err != nil {
		// Sandbox doesn't exist in DB — agent can't tell us anything new.
		return false, "", err
	}
	if !sb.NodeID.Valid {
		// Not yet scheduled to a node; treat as agent-unreachable rather
		// than a confirmed miss so we don't spuriously mark destroyed.
		return false, "", errors.New("sandbox not assigned to a node")
	}
	node, err := c.store.GetNode(ctx, sb.NodeID)
	if err != nil {
		return false, "", err
	}

	url := fmt.Sprintf("http://%s:%d/v1/containers/%s",
		node.TailscaleIp.String(), node.AgentListenPort, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false, "", err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var body containerExistsResponse
		if decErr := json.NewDecoder(resp.Body).Decode(&body); decErr != nil {
			return false, "", decErr
		}
		return body.Exists, body.State, nil
	case http.StatusNotFound:
		// Agent confirms container is absent.
		return false, "", nil
	default:
		return false, "", fmt.Errorf("agent returned status %d", resp.StatusCode)
	}
}

// ── stateWebhookClient ───────────────────────────────────────────────
//
// Phase 33-B implementation of lifecycle.StateWebhookNotifier. POSTs the
// state-change payload as JSON to the configured backend URL with an
// HMAC-SHA256 signature header so the listener can verify authenticity.
// Errors are returned to the caller (lifecycle.notifyStateWebhook) which
// already runs us in a goroutine and logs warnings — we don't add our
// own retry / backoff here.

type stateWebhookClient struct {
	url     string
	secret  []byte
	client  *http.Client
	logger  *slog.Logger
}

func newStateWebhookClient(
	url, secret string,
	timeout time.Duration,
	logger *slog.Logger,
) *stateWebhookClient {
	return &stateWebhookClient{
		url:    url,
		secret: []byte(secret),
		client: &http.Client{Timeout: timeout},
		logger: logger,
	}
}

// OnSandboxStateChanged satisfies lifecycle.StateWebhookNotifier.
func (c *stateWebhookClient) OnSandboxStateChanged(
	ctx context.Context,
	payload lifecycle.StateChangePayload,
) error {
	return c.postSigned(ctx, payload)
}

// OnExecCompleted satisfies lifecycle.StateWebhookNotifier for exec acks.
// Same URL + HMAC scheme as state-change; receiver discriminates on
// payload.type ("exec_completed").
func (c *stateWebhookClient) OnExecCompleted(
	ctx context.Context,
	payload lifecycle.ExecCompletedPayload,
) error {
	return c.postSigned(ctx, payload)
}

// postSigned marshals the payload, posts it to the configured URL with the
// shared HMAC-SHA256 signature header, and validates the listener's response
// status. Used by both OnSandboxStateChanged and OnExecCompleted — the body
// is opaque to this helper, only its marshaling matters.
func (c *stateWebhookClient) postSigned(ctx context.Context, payload interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "forge-control/1.0")

	// HMAC-SHA256 over the raw body. Listener computes the same digest
	// from the request body and constant-time compares.
	if len(c.secret) > 0 {
		mac := hmac.New(sha256.New, c.secret)
		mac.Write(body)
		req.Header.Set("X-Forge-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()

	// Drain the body so the connection is reusable. We don't care about
	// content (listener returns 204 on success).
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("listener returned status %d", resp.StatusCode)
}
