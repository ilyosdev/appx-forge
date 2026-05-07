package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// ContainerInfo is the protocol-side representation of a single container
// reported by the agent in its rich heartbeat (Phase 30).
//
// Mirrors the shape of api.ContainerInfo, agent's docker.ContainerSnapshot,
// and controlclient.ContainerInfo. Defined here in the scheduler package
// because importing api would close an import cycle (api → lifecycle →
// scheduler). Wiring at main.go converts api.ContainerInfo → this type
// before invoking Reconcile, then exposes the reconciler via an adapter
// satisfying api.Reconciler.
type ContainerInfo struct {
	AppName     string
	State       string
	HostPort    int
	ContainerID string
}

// SandboxRow is the per-sandbox shape HeartbeatReconciler needs from the
// store: the per-node working set used for drift detection. Exported so
// the wiring adapter in main.go can construct rows from sqlc results.
type SandboxRow struct {
	AppName   string
	State     string
	CreatedAt time.Time
}

// SandboxStore is the interface HeartbeatReconciler needs from the store.
type SandboxStore interface {
	ListSandboxesForNode(ctx context.Context, nodeID pgtype.UUID) ([]SandboxRow, error)
	MarkSandboxVerified(ctx context.Context, appName, state string) error
	MarkSandboxAgentLost(ctx context.Context, appName string, nodeID pgtype.UUID) error
}

// reconcilerGraceWindow is how long a freshly-created DB row is allowed to
// be missing from the agent's container list before the reconciler marks
// it agent-lost. Matches the SQL filter `created_at < NOW() - INTERVAL '60 seconds'`
// in MarkSandboxAgentLost — the SQL is the authoritative gate; this Go
// short-circuit just avoids the extra UPDATE round-trip.
const reconcilerGraceWindow = 60 * time.Second

// HeartbeatReconciler diffs each rich heartbeat against the per-node DB
// working set: bumps verified_at + state for confirmed containers, marks
// agent-lost for DB rows missing from the agent's report past the grace
// window. Replaces the periodic OrphanHunter as primary drift catcher.
type HeartbeatReconciler struct {
	store  SandboxStore
	logger *slog.Logger
}

// NewHeartbeatReconciler constructs a HeartbeatReconciler. Pass nil for
// logger to use slog.Default().
func NewHeartbeatReconciler(store SandboxStore, logger *slog.Logger) *HeartbeatReconciler {
	if logger == nil {
		logger = slog.Default()
	}
	return &HeartbeatReconciler{store: store, logger: logger}
}

// Reconcile diffs the agent's reported container list against the DB rows
// for this node. For each agent-reported row, bumps verified_at + state.
// For each DB row not in agent's list AND older than the grace window,
// marks state='destroyed' with reason='agent_lost_at_heartbeat'.
//
// Per-row failures are logged and do not abort the reconcile pass — one
// row's UPDATE failure must not block drift detection on its peers.
func (r *HeartbeatReconciler) Reconcile(ctx context.Context, nodeID pgtype.UUID, containers []ContainerInfo) error {
	dbRows, err := r.store.ListSandboxesForNode(ctx, nodeID)
	if err != nil {
		return err
	}

	agentSet := make(map[string]ContainerInfo, len(containers))
	for _, c := range containers {
		agentSet[c.AppName] = c
	}

	// Bump verified_at for confirmed rows.
	for _, c := range containers {
		if err := r.store.MarkSandboxVerified(ctx, c.AppName, c.State); err != nil {
			r.logger.Warn("MarkSandboxVerified failed", "app_name", c.AppName, "err", err)
		}
	}

	// Mark agent-lost for DB rows missing from agent list AND past grace window.
	driftCount := 0
	now := time.Now()
	for _, row := range dbRows {
		if _, present := agentSet[row.AppName]; present {
			continue
		}
		if now.Sub(row.CreatedAt) < reconcilerGraceWindow {
			continue
		}
		if err := r.store.MarkSandboxAgentLost(ctx, row.AppName, nodeID); err != nil {
			r.logger.Warn("MarkSandboxAgentLost failed", "app_name", row.AppName, "err", err)
			continue
		}
		driftCount++
	}

	if driftCount > 0 {
		r.logger.Info("reconcile drift", "agent_lost", driftCount)
	}
	return nil
}
