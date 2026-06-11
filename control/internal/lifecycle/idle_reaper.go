package lifecycle

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/appx/forge/control/internal/store"
	"github.com/appx/forge/shared-go/models"
)

// IdleReaperStore abstracts the database operations needed by the idle reaper.
type IdleReaperStore interface {
	ListIdleSandboxes(ctx context.Context) ([]store.Sandbox, error)
	ListStoppedExpired(ctx context.Context, retentionSeconds int32) ([]store.Sandbox, error)
	TransitionSandboxState(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error)
	RecordEvent(ctx context.Context, arg store.RecordEventParams) (store.Event, error)
	CreateCommand(ctx context.Context, arg store.CreateCommandParams) (store.Command, error)
}

// IdleReaper periodically checks for idle sandboxes and stops them.
// Idle detection uses per-sandbox idle_timeout_seconds from Postgres, not a
// global hardcoded value. The ListIdleSandboxes query compares last_active_at
// against each sandbox's own timeout.
type IdleReaper struct {
	store    IdleReaperStore
	notifier RouteNotifier
	logger   *slog.Logger
	interval time.Duration
	// retention is how long a slept (stopped, kept-on-disk) sandbox may
	// exist before the second-tier pass destroys it for real.
	// 0 defaults to 24h.
	retention time.Duration
}

// NewIdleReaper creates a new IdleReaper.
// interval is how often reap() is called; 0 defaults to 60s.
// retention is how long slept sandboxes are kept before second-tier
// destruction; 0 defaults to 24h.
func NewIdleReaper(store IdleReaperStore, notifier RouteNotifier, logger *slog.Logger, interval time.Duration, retention time.Duration) *IdleReaper {
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = 60 * time.Second
	}
	if retention <= 0 {
		retention = 24 * time.Hour
	}
	return &IdleReaper{
		store:     store,
		notifier:  notifier,
		logger:    logger,
		interval:  interval,
		retention: retention,
	}
}

// Run starts the idle reaper ticker loop. It calls reap() on each tick and
// returns when ctx is cancelled.
func (r *IdleReaper) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	r.logger.Info("idle reaper started", "interval", r.interval)

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("idle reaper stopped")
			return
		case <-ticker.C:
			if err := r.reap(ctx); err != nil {
				r.logger.Error("idle reap cycle failed", "error", err)
			}
			if err := r.reapExpired(ctx); err != nil {
				r.logger.Error("second-tier reap cycle failed", "error", err)
			}
		}
	}
}

// reap queries for idle sandboxes and stops each one. Individual failures are
// logged and do not abort the batch.
func (r *IdleReaper) reap(ctx context.Context) error {
	sandboxes, err := r.store.ListIdleSandboxes(ctx)
	if err != nil {
		return err
	}

	if len(sandboxes) == 0 {
		return nil
	}

	reaped := 0
	for _, sb := range sandboxes {
		if err := r.reapOne(ctx, sb); err != nil {
			r.logger.Warn("failed to reap sandbox",
				"error", err,
				"sandbox_id", uuid.UUID(sb.ID.Bytes).String(),
				"app_name", sb.AppName,
			)
			continue
		}
		reaped++
	}

	if reaped > 0 {
		r.logger.Info("idle reap complete", "reaped", reaped, "total_idle", len(sandboxes))
	}
	return nil
}

// reapExpired is the second tier (sleep-not-destroy, 2026-06-11): slept
// sandboxes older than the retention window are destroyed for real. The
// stopped->destroyed transition is direct per the state machine; the
// dispatched stop command carries NO mode field, which the agent treats
// as destroy (rm + workdir removal + port release). The heartbeat
// reconciler's orphan pass is the backstop if the command is lost.
func (r *IdleReaper) reapExpired(ctx context.Context) error {
	expired, err := r.store.ListStoppedExpired(ctx, int32(r.retention.Seconds()))
	if err != nil {
		return err
	}
	destroyed := 0
	for _, sb := range expired {
		if err := r.destroyExpired(ctx, sb); err != nil {
			r.logger.Warn("failed to destroy expired slept sandbox",
				"error", err,
				"sandbox_id", uuid.UUID(sb.ID.Bytes).String(),
				"app_name", sb.AppName,
			)
			continue
		}
		destroyed++
	}
	if destroyed > 0 {
		r.logger.Info("second-tier reap complete", "destroyed", destroyed, "total_expired", len(expired))
	}
	return nil
}

func (r *IdleReaper) destroyExpired(ctx context.Context, sb store.Sandbox) error {
	sandboxID := uuid.UUID(sb.ID.Bytes)

	// stopped --destroy_requested--> destroyed (direct; container is not
	// running, the agent rm of an exited container is near-instant).
	_, err := r.store.TransitionSandboxState(ctx, store.TransitionSandboxStateParams{
		State:   string(models.StateDestroyed),
		ID:      sb.ID,
		State_2: string(models.StateStopped),
	})
	if err != nil {
		return err
	}

	cmdPayload, _ := json.Marshal(map[string]interface{}{
		"container_id": sb.ContainerID.String,
		"reason":       "stopped_retention_expired",
		// no mode -> agent destroys (rm + workdir + port release)
	})
	cmdID := uuid.New()
	if _, err := r.store.CreateCommand(ctx, store.CreateCommandParams{
		ID:             pgtype.UUID{Bytes: cmdID, Valid: true},
		NodeID:         sb.NodeID,
		SandboxID:      sb.ID,
		CommandType:    string(models.CmdStopSandbox),
		Payload:        cmdPayload,
		TimeoutSeconds: 60,
	}); err != nil {
		r.logger.Warn("failed to create destroy command for expired slept sandbox",
			"error", err, "sandbox_id", sandboxID)
		// Continue — reconciler orphan pass is the backstop.
	}

	if _, err := r.store.RecordEvent(ctx, store.RecordEventParams{
		SandboxID: sb.ID,
		NodeID:    sb.NodeID,
		EventType: string(models.EventDestroyRequest),
		Actor:     "idle-reaper",
		PrevState: pgtype.Text{String: string(models.StateStopped), Valid: true},
		NextState: pgtype.Text{String: string(models.StateDestroyed), Valid: true},
		Payload:   []byte(`{"reason":"stopped_retention_expired"}`),
	}); err != nil {
		r.logger.Warn("failed to record retention-destroy event",
			"error", err, "sandbox_id", sandboxID)
	}

	r.logger.Info("destroyed expired slept sandbox",
		"sandbox_id", sandboxID, "app_name", sb.AppName)
	return nil
}

// reapOne transitions a single sandbox from running to stopped, dispatches a
// stop command, records an event, and notifies the route manager.
func (r *IdleReaper) reapOne(ctx context.Context, sb store.Sandbox) error {
	sandboxID := uuid.UUID(sb.ID.Bytes)

	// 1. Transition running -> stopped via idle_timeout event
	_, err := r.store.TransitionSandboxState(ctx, store.TransitionSandboxStateParams{
		State:   string(models.StateStopped),
		ID:      sb.ID,
		State_2: string(models.StateRunning),
	})
	if err != nil {
		return err
	}

	// 2. Dispatch stop_sandbox command to agent.
	// Sleep-not-destroy (2026-06-11): PROJECT sandboxes sleep (mode=stop —
	// container kept for a sub-second docker-start wake; second-tier reap
	// destroys after the retention window). POOL sandboxes keep destroy
	// semantics: they are fungible and lifecycle.go's stop-ack handler
	// deletes their DB row inline — sleeping them would just hoard disk.
	payloadMap := map[string]interface{}{
		"container_id": sb.ContainerID.String,
		"reason":       "idle_timeout",
	}
	if !isPoolSandbox(sb.Metadata) {
		payloadMap["mode"] = "stop"
	}
	cmdPayload, _ := json.Marshal(payloadMap)
	cmdID := uuid.New()
	_, err = r.store.CreateCommand(ctx, store.CreateCommandParams{
		ID:             pgtype.UUID{Bytes: cmdID, Valid: true},
		NodeID:         sb.NodeID,
		SandboxID:      sb.ID,
		CommandType:    string(models.CmdStopSandbox),
		Payload:        cmdPayload,
		TimeoutSeconds: 60,
	})
	if err != nil {
		r.logger.Warn("failed to create stop command for reaped sandbox",
			"error", err,
			"sandbox_id", sandboxID,
		)
		// Continue -- state is already transitioned
	}

	// 3. Record idle_timeout event
	_, err = r.store.RecordEvent(ctx, store.RecordEventParams{
		SandboxID: sb.ID,
		NodeID:    sb.NodeID,
		EventType: string(models.EventIdleTimeout),
		Actor:     "idle-reaper",
		PrevState: pgtype.Text{String: string(models.StateRunning), Valid: true},
		NextState: pgtype.Text{String: string(models.StateStopped), Valid: true},
		Payload:   []byte(`{}`),
	})
	if err != nil {
		r.logger.Warn("failed to record idle_timeout event",
			"error", err,
			"sandbox_id", sandboxID,
		)
	}

	// 4. Notify route manager (best-effort)
	if r.notifier != nil {
		if err := r.notifier.OnSandboxStopped(ctx, sb.AppName); err != nil {
			r.logger.Warn("route remove notification failed for reaped sandbox",
				"error", err,
				"app_name", sb.AppName,
				"sandbox_id", sandboxID,
			)
		}
	}

	r.logger.Info("reaped idle sandbox",
		"sandbox_id", sandboxID,
		"app_name", sb.AppName,
	)
	return nil
}
