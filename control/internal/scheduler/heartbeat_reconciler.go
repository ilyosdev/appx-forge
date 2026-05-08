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

// TerminalSandboxRow is what the reconciler needs to dispatch a stop
// command for an orphan container that the agent still observes against
// a row in terminal state.
type TerminalSandboxRow struct {
	ID          pgtype.UUID
	AppName     string
	ContainerID string
}

// SandboxStore is the interface HeartbeatReconciler needs from the store.
type SandboxStore interface {
	ListSandboxesForNode(ctx context.Context, nodeID pgtype.UUID) ([]SandboxRow, error)
	MarkSandboxVerified(ctx context.Context, appName, state string) error
	MarkSandboxAgentLost(ctx context.Context, appName string, nodeID pgtype.UUID) error

	// Phase 33-Real-7 — terminal-state rows whose container the agent
	// still observes. Reconciler dispatches stop_sandbox so the orphan
	// container is destroyed, replacing the prior silent skip.
	ListTerminalSandboxesForNode(ctx context.Context, nodeID pgtype.UUID) ([]TerminalSandboxRow, error)
	DispatchStopSandbox(ctx context.Context, sandboxID pgtype.UUID, nodeID pgtype.UUID, containerID, reason string) error
}

// reconcilerGraceWindow is how long a freshly-created DB row is allowed to
// be missing from the agent's container list before the reconciler marks
// it agent-lost. Matches the SQL filter `created_at < NOW() - INTERVAL '60 seconds'`
// in MarkSandboxAgentLost — the SQL is the authoritative gate; this Go
// short-circuit just avoids the extra UPDATE round-trip.
const reconcilerGraceWindow = 60 * time.Second

// isTerminalState reports whether a sandbox state is a sink in the state
// machine — no further transitions are legal except DELETE. Used by the
// reconciler to skip MarkSandboxVerified UPDATEs that would violate the
// `sandboxes_state_check` constraint (e.g. failed → running is not legal).
func isTerminalState(s string) bool {
	switch s {
	case "failed", "destroyed", "destroying":
		return true
	}
	return false
}

// validPGState reports whether s is one of the eight values the
// `sandboxes_state_check` constraint accepts. Defense-in-depth guard for
// the reconciler's verify pass: agent translates Docker primitives at its
// own boundary (Phase 33-Real-9 — agent/internal/docker/client.go via
// models.FromDockerState), but a malformed agent payload should not
// produce a constraint-violation log spam, so we re-check here and skip
// the UPDATE rather than trust blindly.
func validPGState(s string) bool {
	switch s {
	case "pending", "starting", "running", "restarting",
		"stopped", "destroying", "destroyed", "failed":
		return true
	}
	return false
}

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

	// Phase 33-Audit-7 — build DB-state lookup so the verify pass can skip
	// rows that have already reached a terminal state. Without this guard,
	// agent-observed containers for failed/destroyed/destroying rows
	// trigger a state UPDATE that the sandboxes_state_check constraint
	// rejects (e.g. failed → running is not a legal transition), spamming
	// the log every reconcile tick. Beta incident 2026-05-08: 7 failed
	// rows produced thousands of "MarkSandboxVerified failed" warnings.
	dbState := make(map[string]string, len(dbRows))
	for _, row := range dbRows {
		dbState[row.AppName] = row.State
	}

	// Bump verified_at for confirmed rows.
	for _, c := range containers {
		if isTerminalState(dbState[c.AppName]) {
			// Container still exists at the agent but the row is terminal;
			// the lifecycle layer will eventually issue a destroy command.
			// Verifying back to 'running' here would violate the state
			// machine — drop silently.
			continue
		}
		// Phase 33-Real-9 — agent now translates Docker primitives to
		// canonical SandboxState at its own boundary (see
		// agent/internal/docker/client.go and models.FromDockerState).
		// Defense in depth: still validate the incoming state matches
		// the PG enum so a malformed payload skips the UPDATE rather
		// than crashing the constraint and spamming the log.
		if !validPGState(c.State) {
			r.logger.Warn("reconcile: agent reported non-canonical state, skipping verify",
				"app_name", c.AppName, "state", c.State)
			continue
		}
		if err := r.store.MarkSandboxVerified(ctx, c.AppName, c.State); err != nil {
			r.logger.Warn("MarkSandboxVerified failed",
				"app_name", c.AppName, "state", c.State, "err", err)
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

	// Phase 33-Real-7 — orphan-container cleanup. Replaces the prior
	// silent skip when the agent reported a container for a row already
	// in terminal state (failed / destroying / destroyed). Without this
	// dispatch, the underlying Docker container lived forever — the
	// MarkSandboxVerified guard (state IN clause) prevented row flap, but
	// nothing was actually destroying the orphan. Now: list terminal-row
	// containers on this node, intersect with agent's observed set, and
	// dispatch stop_sandbox per match. Idempotent on the agent side
	// (stop on already-stopped container is a no-op).
	terminalRows, err := r.store.ListTerminalSandboxesForNode(ctx, nodeID)
	if err != nil {
		r.logger.Warn("reconcile: list terminal rows failed", "err", err)
		return nil // non-fatal, drift catcher resumes next tick
	}
	dispatched := 0
	for _, row := range terminalRows {
		if _, present := agentSet[row.AppName]; !present {
			continue
		}
		if row.ContainerID == "" {
			continue
		}
		if err := r.store.DispatchStopSandbox(ctx, row.ID, nodeID, row.ContainerID, "orphan_terminal_row"); err != nil {
			r.logger.Warn("reconcile: orphan stop dispatch failed",
				"app_name", row.AppName, "err", err)
			continue
		}
		dispatched++
	}
	if dispatched > 0 {
		r.logger.Info("reconcile orphan cleanup", "stop_dispatched", dispatched)
	}
	return nil
}
