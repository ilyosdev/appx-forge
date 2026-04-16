package lifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/appx/forge/control/internal/scheduler"
	"github.com/appx/forge/control/internal/store"
	"github.com/appx/forge/shared-go/models"
)

// RescheduleStore abstracts the database operations needed by the Rescheduler.
type RescheduleStore interface {
	ListRunningSandboxesByNode(ctx context.Context, nodeID pgtype.UUID) ([]store.Sandbox, error)
	ListHealthyNodes(ctx context.Context) ([]store.Node, error)
	TransitionSandboxState(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error)
	AssignSandboxToNode(ctx context.Context, arg store.AssignSandboxToNodeParams) (store.Sandbox, error)
	CreateCommand(ctx context.Context, arg store.CreateCommandParams) (store.Command, error)
	RecordEvent(ctx context.Context, arg store.RecordEventParams) (store.Event, error)
	ResetSandboxFailureCount(ctx context.Context, id pgtype.UUID) error
}

// RescheduleResult contains the outcome of rescheduling a failed node's sandboxes.
type RescheduleResult struct {
	// Count is the number of sandboxes successfully rescheduled.
	Count int

	// Failed is the number of sandboxes that failed to reschedule.
	Failed int

	// Errors collects errors from individual sandbox reschedule failures.
	Errors []error
}

// Rescheduler moves RUNNING sandboxes from a failed node to healthy nodes.
// It is invoked by the heartbeat monitor when a node stops sending heartbeats.
type Rescheduler struct {
	store    RescheduleStore
	notifier RouteNotifier
	logger   *slog.Logger
}

// NewRescheduler creates a new Rescheduler.
func NewRescheduler(s RescheduleStore, notifier RouteNotifier, logger *slog.Logger) *Rescheduler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Rescheduler{store: s, notifier: notifier, logger: logger}
}

// RescheduleNode moves all RUNNING sandboxes on the failed node to healthy nodes.
// Processing is sequential per sandbox (no concurrent reschedules) to prevent
// thundering herd on the remaining nodes.
//
// If a single sandbox fails to reschedule, others still proceed. The aggregate
// result contains counts and collected errors for observability.
func (r *Rescheduler) RescheduleNode(ctx context.Context, failedNodeID pgtype.UUID) (RescheduleResult, error) {
	sandboxes, err := r.store.ListRunningSandboxesByNode(ctx, failedNodeID)
	if err != nil {
		return RescheduleResult{}, fmt.Errorf("list running sandboxes for node: %w", err)
	}

	if len(sandboxes) == 0 {
		r.logger.Info("no running sandboxes on failed node",
			"node_id", uuid.UUID(failedNodeID.Bytes),
		)
		return RescheduleResult{Count: 0}, nil
	}

	r.logger.Info("rescheduling sandboxes from failed node",
		"node_id", uuid.UUID(failedNodeID.Bytes),
		"sandbox_count", len(sandboxes),
	)

	var result RescheduleResult

	for _, sandbox := range sandboxes {
		if err := r.rescheduleSandbox(ctx, sandbox); err != nil {
			sandboxID := uuid.UUID(sandbox.ID.Bytes)
			r.logger.Warn("failed to reschedule sandbox",
				"sandbox_id", sandboxID,
				"app_name", sandbox.AppName,
				"error", err,
			)
			result.Failed++
			result.Errors = append(result.Errors, fmt.Errorf("sandbox %s (%s): %w", sandboxID, sandbox.AppName, err))
		} else {
			result.Count++
		}
	}

	return result, nil
}

// rescheduleSandbox moves a single sandbox from the failed node to a healthy one.
// Steps:
//  1. Transition running -> pending via EventNodeFailed
//  2. Record node_failed event for audit trail
//  3. Remove Caddy route (best-effort)
//  4. Reset failure count (fresh start on new node)
//  5. Pick new node via scheduler (bin-packing by most free RAM)
//  6. If no capacity: transition to FAILED
//  7. Assign to new node (pending -> starting)
//  8. Dispatch start_sandbox command
//  9. Record scheduled event
func (r *Rescheduler) rescheduleSandbox(ctx context.Context, sandbox store.Sandbox) error {
	sandboxID := uuid.UUID(sandbox.ID.Bytes)
	fromNodeID := uuid.UUID(sandbox.NodeID.Bytes)

	// 1. Transition running -> pending via EventNodeFailed (CAS: only succeeds if still running)
	_, err := r.store.TransitionSandboxState(ctx, store.TransitionSandboxStateParams{
		State:   string(models.StatePending),
		ID:      sandbox.ID,
		State_2: string(models.StateRunning),
	})
	if err != nil {
		return fmt.Errorf("transition running->pending: %w", err)
	}

	// 2. Record node_failed event for audit trail
	r.store.RecordEvent(ctx, store.RecordEventParams{
		SandboxID: sandbox.ID,
		NodeID:    sandbox.NodeID,
		EventType: string(models.EventNodeFailed),
		Actor:     "rescheduler",
		PrevState: pgtype.Text{String: string(models.StateRunning), Valid: true},
		NextState: pgtype.Text{String: string(models.StatePending), Valid: true},
		Payload:   []byte(`{}`),
	})

	// 3. Remove Caddy route (best-effort, errors logged)
	if r.notifier != nil {
		if err := r.notifier.OnSandboxStopped(ctx, sandbox.AppName); err != nil {
			r.logger.Warn("route removal failed during reschedule",
				"error", err,
				"sandbox_id", sandboxID,
				"app_name", sandbox.AppName,
			)
		}
	}

	// 4. Reset failure count (fresh start on new node)
	if err := r.store.ResetSandboxFailureCount(ctx, sandbox.ID); err != nil {
		r.logger.Warn("failed to reset failure count",
			"error", err,
			"sandbox_id", sandboxID,
		)
	}

	// 5. List healthy nodes and pick one via scheduler
	nodes, err := r.store.ListHealthyNodes(ctx)
	if err != nil {
		return fmt.Errorf("list healthy nodes: %w", err)
	}

	candidates := make([]scheduler.NodeCandidate, len(nodes))
	for i, n := range nodes {
		candidates[i] = scheduler.NodeCandidate{
			ID:         uuid.UUID(n.ID.Bytes),
			CapacityMB: n.CapacityMb,
			UsedMB:     n.UsedMb,
			Status:     n.Status,
		}
	}

	// Parse memory_mb from sandbox resources (default 512 if missing/malformed)
	requiredMB := int32(512)
	var res Resources
	if err := json.Unmarshal(sandbox.Resources, &res); err == nil && res.MemoryMB > 0 {
		requiredMB = res.MemoryMB
	}

	selectedNodeID, err := scheduler.Schedule(candidates, requiredMB)
	if err != nil {
		// 6. No capacity: transition sandbox to FAILED
		r.logger.Warn("no capacity for rescheduled sandbox, transitioning to failed",
			"sandbox_id", sandboxID,
			"error", err,
		)

		r.store.TransitionSandboxState(ctx, store.TransitionSandboxStateParams{
			State:   string(models.StateFailed),
			ID:      sandbox.ID,
			State_2: string(models.StatePending),
		})

		r.store.RecordEvent(ctx, store.RecordEventParams{
			SandboxID: sandbox.ID,
			NodeID:    sandbox.NodeID,
			EventType: "reschedule_failed",
			Actor:     "rescheduler",
			PrevState: pgtype.Text{String: string(models.StatePending), Valid: true},
			NextState: pgtype.Text{String: string(models.StateFailed), Valid: true},
			Payload:   []byte(`{}`),
		})

		return fmt.Errorf("schedule sandbox: %w", err)
	}

	pgNewNodeID := pgtype.UUID{Bytes: selectedNodeID, Valid: true}

	// 7. Assign sandbox to new node (pending -> starting)
	_, err = r.store.AssignSandboxToNode(ctx, store.AssignSandboxToNodeParams{
		NodeID: pgNewNodeID,
		ID:     sandbox.ID,
	})
	if err != nil {
		return fmt.Errorf("assign sandbox to node: %w", err)
	}

	// 8. Dispatch start_sandbox command to the new node
	cmdPayload, _ := json.Marshal(map[string]interface{}{
		"app_name":  sandbox.AppName,
		"image":     sandbox.Image,
		"resources": res,
		"env":       json.RawMessage(sandbox.Env),
	})

	cmdID := uuid.New()
	_, err = r.store.CreateCommand(ctx, store.CreateCommandParams{
		ID:             pgtype.UUID{Bytes: cmdID, Valid: true},
		NodeID:         pgNewNodeID,
		SandboxID:      sandbox.ID,
		CommandType:    string(models.CmdStartSandbox),
		Payload:        cmdPayload,
		TimeoutSeconds: 60,
	})
	if err != nil {
		return fmt.Errorf("create start command: %w", err)
	}

	// 9. Record scheduled event
	r.store.RecordEvent(ctx, store.RecordEventParams{
		SandboxID: sandbox.ID,
		NodeID:    pgNewNodeID,
		EventType: string(models.EventScheduled),
		Actor:     "rescheduler",
		PrevState: pgtype.Text{String: string(models.StatePending), Valid: true},
		NextState: pgtype.Text{String: string(models.StateStarting), Valid: true},
		Payload:   cmdPayload,
	})

	r.logger.Info("sandbox rescheduled",
		"sandbox_id", sandboxID,
		"app_name", sandbox.AppName,
		"from_node", fromNodeID,
		"to_node", selectedNodeID,
	)

	return nil
}
