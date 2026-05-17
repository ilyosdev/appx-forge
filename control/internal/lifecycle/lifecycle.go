// Package lifecycle orchestrates sandbox lifecycle operations: create, destroy,
// restart, handle command acknowledgments, and process container events.
package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/appx/forge/control/internal/scheduler"
	"github.com/appx/forge/control/internal/store"
	"github.com/appx/forge/shared-go/models"
)

// Sentinel errors for lifecycle operations.
var (
	// ErrConflict is returned when a sandbox with the same app_name already exists.
	ErrConflict = errors.New("lifecycle: app_name already exists")

	// ErrNoCapacity is returned when no node has sufficient resources.
	ErrNoCapacity = errors.New("lifecycle: no node has sufficient capacity")

	// ErrNotFound is returned when a sandbox is not found.
	ErrNotFound = errors.New("lifecycle: sandbox not found")

	// ErrInvalidTransition is returned when a state transition is not valid.
	ErrInvalidTransition = errors.New("lifecycle: invalid state transition")

	// ErrInvalidState is returned when a sandbox is not in the expected state.
	ErrInvalidState = errors.New("lifecycle: sandbox not in expected state")
)

// Store abstracts the database operations needed by the lifecycle service.
type Store interface {
	CreateSandbox(ctx context.Context, arg store.CreateSandboxParams) (store.Sandbox, error)
	DeleteSandbox(ctx context.Context, id pgtype.UUID) error
	GetSandbox(ctx context.Context, id pgtype.UUID) (store.Sandbox, error)
	GetSandboxByAppName(ctx context.Context, appName string) (store.Sandbox, error)
	GetNodeByID(ctx context.Context, id pgtype.UUID) (store.Node, error)
	ListHealthyNodes(ctx context.Context) ([]store.Node, error)
	AssignSandboxToNode(ctx context.Context, arg store.AssignSandboxToNodeParams) (store.Sandbox, error)
	TransitionSandboxState(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error)
	UpdateSandboxRuntime(ctx context.Context, arg store.UpdateSandboxRuntimeParams) error
	CreateCommand(ctx context.Context, arg store.CreateCommandParams) (store.Command, error)
	AckCommand(ctx context.Context, arg store.AckCommandParams) error
	RecordEvent(ctx context.Context, arg store.RecordEventParams) (store.Event, error)

	// Phase 33-Real-8 — purge commands then row for inline pool cleanup
	// on stop_sandbox+success ack. Eliminates the cron-based stopped-row
	// accumulation for the dominant case (pool warm sandboxes that never
	// resume after idle reap).
	DeleteCommandsForSandbox(ctx context.Context, sandboxID pgtype.UUID) error
}

// RouteNotifier is called by the lifecycle service when sandbox state changes
// require proxy route updates. Errors are logged as warnings and never propagated
// -- routing is best-effort, corrected by the drift detector.
type RouteNotifier interface {
	OnSandboxRunning(ctx context.Context, appName string, sandboxID string, upstream string) error
	OnSandboxStopped(ctx context.Context, appName string) error
}

// Phase 33-B — StateWebhookNotifier is called when a sandbox transitions
// into a state that downstream consumers care about (currently: running).
// The implementation POSTs JSON to a configured URL with HMAC signing.
//
// Like RouteNotifier, this is best-effort — errors are logged but never
// propagated. Backend consumers MUST tolerate missed events (e.g. via
// existing polling fallbacks) until Phase 33-C drops the polling path.
//
// OnExecCompleted is fired once per exec command ack (success OR failure)
// so backend can correlate the per-command result without polling
// /sandboxes/{id}/exec/{cmd_id}. Same HMAC signing rules as state-change
// — listener verifies via X-Forge-Signature: sha256=<hex>.
type StateWebhookNotifier interface {
	OnSandboxStateChanged(ctx context.Context, payload StateChangePayload) error
	OnExecCompleted(ctx context.Context, payload ExecCompletedPayload) error
}

// StateChangePayload is the JSON body posted to the configured webhook URL.
// Field names use snake_case to match the rest of the Forge JSON API.
type StateChangePayload struct {
	SandboxID   string `json:"sandbox_id"`
	AppName     string `json:"app_name"`
	UserID      string `json:"user_id,omitempty"`
	State       string `json:"state"`
	PrevState   string `json:"prev_state"`
	HostPort    int32  `json:"host_port,omitempty"`
	ContainerID string `json:"container_id,omitempty"`
	NodeID      string `json:"node_id,omitempty"`
	Timestamp   string `json:"ts"`
}

// ExecCompletedPayload is the JSON body posted to the webhook URL when an
// exec command acks. The Type discriminator lets the receiver multiplex
// state vs exec payloads on the same endpoint without sniffing fields.
//
// Stdout/Stderr may be truncated by the agent at agent-configured caps;
// the *_truncated flags signal the cut so the caller knows the strings
// are not the full output.
type ExecCompletedPayload struct {
	Type            string `json:"type"` // always "exec_completed"
	SandboxID       string `json:"sandbox_id"`
	CommandID       string `json:"command_id"`
	ExitCode        int    `json:"exit_code"`
	Stdout          string `json:"stdout"`
	Stderr          string `json:"stderr"`
	StdoutTruncated bool   `json:"stdout_truncated"`
	StderrTruncated bool   `json:"stderr_truncated"`
	DurationMs      int64  `json:"duration_ms"`
	Ts              int64  `json:"ts"`
}

// ── Request/Result Types ─────────────────────────────────────────────

// CreateRequest contains the parameters for creating a new sandbox.
type CreateRequest struct {
	AppName            string
	UserID             string
	Image              string
	Resources          Resources
	Env                map[string]string
	IdleTimeoutSeconds int32
	Metadata           map[string]interface{}
}

// Resources specifies the resource limits for a sandbox.
type Resources struct {
	CPUCores float64 `json:"cpu_cores"`
	MemoryMB int32   `json:"memory_mb"`
}

// SandboxResult contains the result of a sandbox operation.
type SandboxResult struct {
	ID        uuid.UUID  `json:"id"`
	AppName   string     `json:"app_name"`
	UserID    string     `json:"user_id"`
	State     string     `json:"state"`
	URL       string     `json:"url"`
	NodeID    *uuid.UUID `json:"node_id,omitempty"`
	Image     string     `json:"image"`
	CreatedAt string     `json:"created_at,omitempty"`
}

// ── Service ──────────────────────────────────────────────────────────

// LifecycleService orchestrates sandbox lifecycle operations.
type LifecycleService struct {
	store           Store
	logger          *slog.Logger
	routeNotifier   RouteNotifier
	webhookNotifier StateWebhookNotifier
	restartMgr      *RestartManager
}

// New creates a new LifecycleService.
func New(s Store, logger *slog.Logger) *LifecycleService {
	if logger == nil {
		logger = slog.Default()
	}
	return &LifecycleService{store: s, logger: logger}
}

// SetRouteNotifier injects the route notifier after construction.
// This avoids widening the New() signature for optional dependencies.
func (ls *LifecycleService) SetRouteNotifier(rn RouteNotifier) {
	ls.routeNotifier = rn
}

// SetStateWebhookNotifier injects the Phase 33-B state-change webhook
// notifier after construction. Optional — when unset, lifecycle never
// posts state-change webhooks (backwards compat).
func (ls *LifecycleService) SetStateWebhookNotifier(wn StateWebhookNotifier) {
	ls.webhookNotifier = wn
}

// notifyStateWebhook is a fire-and-forget helper that POSTs the state
// change to the configured webhook (if any).
//
// Phase 33-B: emit on transitions INTO running so backend's
// ContainerPoolService can flip provisioning rows to warm without polling.
//
// Phase 33-E: also emit on transitions INTO failed and destroyed so
// backend learns about provisioning failures the same event-driven way.
// Closes the gap where backend's pool service had to poll
// forgeService.getSandbox to discover that a sandbox vanished or failed.
//
// The webhook runs in a goroutine so the lifecycle handler doesn't block
// on slow HTTP. Caller passes the freshly-loaded sandbox row (already
// has node_id / container_id / host_port for `running` transitions).
func (ls *LifecycleService) notifyStateWebhook(
	sandbox store.Sandbox,
	sandboxID uuid.UUID,
	prevState models.SandboxState,
	nextState models.SandboxState,
) {
	if ls.webhookNotifier == nil {
		return
	}
	// Phase 33-E — emit on running, failed, destroyed. These are the
	// states backend actually reacts to. Restarting / stopped happen
	// often during normal lifecycle and would just be noise.
	switch nextState {
	case models.StateRunning, models.StateFailed, models.StateDestroyed:
		// emit
	default:
		return
	}
	// Suppress no-op transitions (alreadyAtTarget escape hatch in
	// HandleAck sets nextState=currentState). Only fire on the actual
	// state transition.
	if prevState == nextState {
		return
	}
	payload := StateChangePayload{
		SandboxID: sandboxID.String(),
		AppName:   sandbox.AppName,
		UserID:    sandbox.UserID,
		State:     string(nextState),
		PrevState: string(prevState),
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if sandbox.ContainerID.Valid {
		payload.ContainerID = sandbox.ContainerID.String
	}
	if sandbox.HostPort.Valid {
		payload.HostPort = sandbox.HostPort.Int32
	}
	if sandbox.NodeID.Valid {
		payload.NodeID = uuid.UUID(sandbox.NodeID.Bytes).String()
	}

	go func(p StateChangePayload) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := ls.webhookNotifier.OnSandboxStateChanged(ctx, p); err != nil {
			ls.logger.Warn("state webhook delivery failed",
				"error", err,
				"sandbox_id", p.SandboxID,
				"app_name", p.AppName,
				"state", p.State,
			)
		}
	}(payload)
}

// notifyExecCompleted is a fire-and-forget helper that POSTs the exec ack
// payload to the configured webhook. Called from HandleAck after the agent
// reports exec result (success or failure). The agent's result map shape
// is documented in the agent exec executor — control plane just shuttles
// it into the typed payload here without re-validating fields.
func (ls *LifecycleService) notifyExecCompleted(
	sandboxID uuid.UUID,
	cmdID uuid.UUID,
	ackResult json.RawMessage,
) {
	if ls.webhookNotifier == nil {
		return
	}
	var resultMap map[string]interface{}
	if len(ackResult) > 0 {
		if err := json.Unmarshal(ackResult, &resultMap); err != nil {
			ls.logger.Warn("exec_completed: failed to parse ack result; emitting partial payload",
				"err", err, "sandbox_id", sandboxID, "cmd_id", cmdID)
			resultMap = nil
		}
	}

	payload := ExecCompletedPayload{
		Type:            "exec_completed",
		SandboxID:       sandboxID.String(),
		CommandID:       cmdID.String(),
		ExitCode:        int(asFloat(resultMap, "exit_code")),
		Stdout:          asString(resultMap, "stdout"),
		Stderr:          asString(resultMap, "stderr"),
		StdoutTruncated: asBool(resultMap, "stdout_truncated"),
		StderrTruncated: asBool(resultMap, "stderr_truncated"),
		DurationMs:      int64(asFloat(resultMap, "duration_ms")),
		Ts:              time.Now().UnixMilli(),
	}

	go func(p ExecCompletedPayload) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := ls.webhookNotifier.OnExecCompleted(ctx, p); err != nil {
			ls.logger.Warn("exec_completed webhook delivery failed",
				"error", err,
				"sandbox_id", p.SandboxID,
				"cmd_id", p.CommandID,
			)
		}
	}(payload)
}

// asFloat extracts a float64 from a parsed JSON map. JSON numbers decode
// to float64 by default via encoding/json; integer fields like exit_code
// or duration_ms come through this path. Returns 0 if missing or wrong type.
func asFloat(m map[string]interface{}, key string) float64 {
	if m == nil {
		return 0
	}
	v, ok := m[key]
	if !ok {
		return 0
	}
	f, _ := v.(float64)
	return f
}

// asString extracts a string field from the parsed JSON map. Returns ""
// if missing or wrong type.
func asString(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// asBool extracts a bool field from the parsed JSON map. Returns false
// if missing or wrong type.
func asBool(m map[string]interface{}, key string) bool {
	if m == nil {
		return false
	}
	v, ok := m[key]
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
}

// SetRestartManager injects the restart manager after construction.
// When set, HandleEvent delegates container_exited events in restarting state
// to the restart manager for exponential backoff recovery.
func (ls *LifecycleService) SetRestartManager(rm *RestartManager) {
	ls.restartMgr = rm
}

// ── CreateSandbox ────────────────────────────────────────────────────

// CreateSandbox creates a new sandbox, schedules it to a node, and dispatches
// a start_sandbox command. It returns the sandbox result with a computed URL.
//
// Phase 32 Wave 2 Bug 1: if the row is created but a subsequent step fails
// (no capacity, AssignSandboxToNode error, transient DB error during
// scheduling), we delete the freshly-inserted PENDING row before returning.
// Otherwise the row is left with node_id IS NULL and accumulates as garbage
// — production state on 2026-05-07 had 4233 such orphans.
func (ls *LifecycleService) CreateSandbox(ctx context.Context, req CreateRequest) (result *SandboxResult, retErr error) {
	// Generate sandbox UUID
	sandboxUUID := uuid.New()
	pgID := pgtype.UUID{Bytes: sandboxUUID, Valid: true}

	// rowCreated is flipped to true once we've inserted the PENDING row.
	// The deferred cleanup runs ONLY if (a) the row was actually inserted
	// and (b) we're returning a non-nil error. Successful returns and
	// pre-insert failures (recycle / pre-check / marshal errors) are skipped.
	rowCreated := false
	defer func() {
		if retErr == nil || !rowCreated {
			return
		}
		// Use a fresh context: the caller's ctx may have been canceled,
		// which would prevent the cleanup DELETE from running and leave
		// the orphan we're trying to avoid. The DB call is fast and
		// bounded — a brief background context is the right choice.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if delErr := ls.store.DeleteSandbox(cleanupCtx, pgID); delErr != nil {
			ls.logger.Warn("failed to clean up orphan PENDING sandbox row after create error",
				"error", delErr,
				"original_error", retErr,
				"sandbox_id", sandboxUUID,
				"app_name", req.AppName,
			)
		}
	}()

	// Marshal resources and env to JSON
	resourcesJSON, err := json.Marshal(req.Resources)
	if err != nil {
		return nil, fmt.Errorf("marshal resources: %w", err)
	}

	envJSON, err := json.Marshal(req.Env)
	if err != nil {
		return nil, fmt.Errorf("marshal env: %w", err)
	}

	metadataJSON := []byte(`{}`)
	if req.Metadata != nil {
		metadataJSON, err = json.Marshal(req.Metadata)
		if err != nil {
			return nil, fmt.Errorf("marshal metadata: %w", err)
		}
	}

	idleTimeout := req.IdleTimeoutSeconds
	if idleTimeout == 0 {
		idleTimeout = 1800
	}

	// Soft-recycle: if a prior row exists for this app_name and is in a terminal-ish
	// state (destroyed or failed), delete it so the unique constraint lets us recreate.
	// Active states (pending/starting/running/restarting/stopped/destroying) still return
	// ErrConflict — the caller must destroy first if they really want to replace a live sandbox.
	if existing, lookupErr := ls.store.GetSandboxByAppName(ctx, req.AppName); lookupErr == nil {
		state := models.SandboxState(existing.State)
		if state == models.StateDestroyed || state == models.StateFailed {
			if derr := ls.store.DeleteSandbox(ctx, existing.ID); derr != nil {
				return nil, fmt.Errorf("recycle destroyed sandbox: %w", derr)
			}
			ls.logger.Info("recycled prior sandbox row",
				"app_name", req.AppName,
				"prior_state", existing.State,
				"prior_id", uuid.UUID(existing.ID.Bytes).String(),
			)
		} else {
			return nil, ErrConflict
		}
	} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
		// Unknown lookup error — surface it, do NOT silently proceed.
		return nil, fmt.Errorf("lookup existing sandbox: %w", lookupErr)
	}

	// Create PENDING sandbox row
	_, err = ls.store.CreateSandbox(ctx, store.CreateSandboxParams{
		ID:                 pgID,
		AppName:            req.AppName,
		UserID:             req.UserID,
		Image:              req.Image,
		Resources:          resourcesJSON,
		Env:                envJSON,
		IdleTimeoutSeconds: idleTimeout,
		Metadata:           metadataJSON,
	})
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
			return nil, ErrConflict
		}
		return nil, fmt.Errorf("create sandbox: %w", err)
	}
	// Row is now in the DB. From this point on, any error return must
	// trigger orphan cleanup via the deferred handler at the top of the
	// function (Phase 32 Wave 2 Bug 1).
	rowCreated = true

	// List healthy nodes for scheduling
	nodes, err := ls.store.ListHealthyNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list healthy nodes: %w", err)
	}

	// Convert to scheduler candidates
	candidates := make([]scheduler.NodeCandidate, len(nodes))
	for i, n := range nodes {
		candidates[i] = scheduler.NodeCandidate{
			ID:         uuid.UUID(n.ID.Bytes),
			CapacityMB: n.CapacityMb,
			UsedMB:     n.UsedMb,
			Status:     n.Status,
		}
	}

	requiredMB := req.Resources.MemoryMB
	if requiredMB == 0 {
		requiredMB = 512
	}

	selectedNodeID, err := scheduler.Schedule(candidates, requiredMB)
	if err != nil {
		return nil, ErrNoCapacity
	}

	pgNodeID := pgtype.UUID{Bytes: selectedNodeID, Valid: true}

	// Assign sandbox to node (transitions pending -> starting)
	assigned, err := ls.store.AssignSandboxToNode(ctx, store.AssignSandboxToNodeParams{
		NodeID: pgNodeID,
		ID:     pgID,
	})
	if err != nil {
		return nil, fmt.Errorf("assign sandbox to node: %w", err)
	}

	// Create start_sandbox command
	cmdPayload, err := json.Marshal(map[string]interface{}{
		"app_name":  req.AppName,
		"image":     req.Image,
		"resources": req.Resources,
		"env":       req.Env,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal command payload: %w", err)
	}

	cmdID := uuid.New()
	_, err = ls.store.CreateCommand(ctx, store.CreateCommandParams{
		ID:             pgtype.UUID{Bytes: cmdID, Valid: true},
		NodeID:         pgNodeID,
		SandboxID:      pgID,
		CommandType:    string(models.CmdStartSandbox),
		Payload:        cmdPayload,
		TimeoutSeconds: 60,
	})
	if err != nil {
		return nil, fmt.Errorf("create start command: %w", err)
	}

	// Record scheduled event
	_, err = ls.store.RecordEvent(ctx, store.RecordEventParams{
		SandboxID: pgID,
		NodeID:    pgNodeID,
		EventType: string(models.EventScheduled),
		Actor:     "control-plane",
		PrevState: pgtype.Text{String: string(models.StatePending), Valid: true},
		NextState: pgtype.Text{String: string(models.StateStarting), Valid: true},
		Payload:   cmdPayload,
	})
	if err != nil {
		ls.logger.Warn("failed to record scheduled event", "error", err, "sandbox_id", sandboxUUID)
	}

	nodeIDVal := selectedNodeID
	return &SandboxResult{
		ID:      sandboxUUID,
		AppName: assigned.AppName,
		UserID:  assigned.UserID,
		State:   assigned.State,
		URL:     fmt.Sprintf("https://%s.myappx.live", req.AppName),
		NodeID:  &nodeIDVal,
		Image:   req.Image,
	}, nil
}

// ── HandleAck ────────────────────────────────────────────────────────

// HandleAck processes a command acknowledgment from an agent.
// It transitions the sandbox state based on the command type and ack status.
func (ls *LifecycleService) HandleAck(ctx context.Context, cmdID uuid.UUID, sandboxID uuid.UUID, cmdType string, status string, ackResult json.RawMessage) error {
	pgSandboxID := pgtype.UUID{Bytes: sandboxID, Valid: true}
	pgCmdID := pgtype.UUID{Bytes: cmdID, Valid: true}

	// Get current sandbox state
	sandbox, err := ls.store.GetSandbox(ctx, pgSandboxID)
	if err != nil {
		return fmt.Errorf("get sandbox: %w", err)
	}

	// Map (cmdType, status) to state machine event
	var event models.SandboxEvent
	needsTransition := true
	// forceDestroyed bypasses the state-machine and writes StateDestroyed
	// directly. Used when the agent reports "No such container" on a
	// stop_sandbox failure: the container is provably absent so the only
	// consistent DB state is destroyed, regardless of the current state.
	forceDestroyed := false

	switch {
	case cmdType == string(models.CmdStartSandbox) && status == "success":
		event = models.EventStarted
	case cmdType == string(models.CmdStartSandbox) && status == "failure":
		event = models.EventStartFailed
	case cmdType == string(models.CmdStopSandbox) && status == "success":
		// Idle-reaper already transitioned running→stopped before dispatching the
		// stop command. When the ack arrives the sandbox is in StateStopped, not
		// StateDestroying, so EventDestroyed is invalid. Only use EventDestroyed
		// for the explicit destroy flow (sandbox is StateDestroying).
		if models.SandboxState(sandbox.State) == models.StateDestroying {
			event = models.EventDestroyed
		} else {
			needsTransition = false
		}
	case cmdType == string(models.CmdStopSandbox) && status == "failure":
		// 2026-05-17 incident — stuck-sandbox infinite reap loop.
		//
		// Idle-reaper fires stop_sandbox with a stale container_id every
		// 60s. The container with that ID no longer exists on the host
		// (recreated/pruned out-of-band — agent returns
		// "No such container: <id>"). Previously this fell through to
		// `default:` ("unhandled ack combination") which did nothing, so
		// the row stayed alive; 20s later DriftDetector found the
		// replacement container by name and flipped state back to
		// running, and the cycle repeated forever.
		//
		// Recovery: when the agent confirms the container is missing,
		// force-transition the row to StateDestroyed (bypassing the
		// state machine — no valid running→destroyed event exists, but
		// the container is provably gone so the DB just needs to catch
		// up). Other stop_sandbox failures (container still exists but
		// stop call timed out etc.) fall through to a no-op so the
		// idle-reaper can retry on the next tick.
		if isMissingContainerErr(ackResult) {
			event = models.EventDestroyed
			forceDestroyed = true
		} else {
			needsTransition = false
		}
	case cmdType == string(models.CmdRestartSandbox) && status == "success":
		// Restart success: no state change, container restarted at Docker level
		needsTransition = false
	default:
		ls.logger.Warn("unhandled ack combination", "cmd_type", cmdType, "status", status)
		needsTransition = false
	}

	if needsTransition {
		currentState := models.SandboxState(sandbox.State)
		nextState, valid := models.NextState(currentState, event)

		// 2026-05-17 — stop_sandbox+failure with missing container.
		// Force StateDestroyed regardless of current state because the
		// container is provably gone. Treat current state as already
		// terminal so we never round-trip through StateDestroying.
		if forceDestroyed {
			nextState = models.StateDestroyed
			valid = currentState != models.StateDestroyed
			if !valid {
				ls.logger.Info("ack: stop_sandbox+failure on missing container; already destroyed",
					"sandbox_id", sandboxID, "current_state", currentState)
			} else {
				ls.logger.Warn("ack: stop_sandbox+failure on missing container; force-destroying",
					"sandbox_id", sandboxID, "current_state", currentState)
			}
		}

		if !valid {
			// Race: HandleEvent (e.g. container_started) may have already
			// advanced the state before this ack arrived. If we're already
			// at the target state, treat as a no-op instead of failing.
			alreadyAtTarget := false
			switch event {
			case models.EventStarted:
				alreadyAtTarget = currentState == models.StateRunning
			case models.EventStartFailed:
				alreadyAtTarget = currentState == models.StateFailed
			case models.EventDestroyed:
				alreadyAtTarget = currentState == models.StateDestroyed
			}

			// Phase 32 Wave 2 Bug 6 — tolerate start_sandbox+success
			// arriving when the sandbox is already in destroying/destroyed.
			//
			// Race: backend's eager verify-after-create marks the sandbox
			// ERROR before the agent's start_sandbox cmd has been picked up;
			// control transitions the row to destroying and dispatches
			// stop_sandbox. The original stop_sandbox fails because
			// container_id was empty at dispatch time. The start finally
			// completes on the agent and acks success — but the row is
			// already destroying. Without this branch we'd return 500 to
			// the agent (cmd row stays "dispatched" forever) and leak a
			// running container that nothing tracks.
			//
			// Tolerance: persist container_id from the ack so the orphan is
			// addressable, dispatch a fresh stop_sandbox with the real ID
			// so the agent can clean up, and soft-ack the original cmd as
			// completed (no state change — sandbox stays in
			// destroying/destroyed).
			tolerateStaleStart := event == models.EventStarted &&
				(currentState == models.StateDestroying || currentState == models.StateDestroyed)

			if !alreadyAtTarget && !tolerateStaleStart {
				return fmt.Errorf("%w: %s + %s from %s", ErrInvalidTransition, cmdType, status, sandbox.State)
			}

			if tolerateStaleStart {
				ls.persistRuntimeInfo(ctx, pgSandboxID, sandboxID, ackResult)
				if refreshed, ferr := ls.store.GetSandbox(ctx, pgSandboxID); ferr == nil &&
					refreshed.ContainerID.Valid && refreshed.ContainerID.String != "" {
					cleanupCmdID := uuid.New()
					cmdPayload, _ := json.Marshal(map[string]interface{}{
						"container_id": refreshed.ContainerID.String,
					})
					if _, cerr := ls.store.CreateCommand(ctx, store.CreateCommandParams{
						ID:             pgtype.UUID{Bytes: cleanupCmdID, Valid: true},
						NodeID:         refreshed.NodeID,
						SandboxID:      pgSandboxID,
						CommandType:    string(models.CmdStopSandbox),
						Payload:        cmdPayload,
						TimeoutSeconds: 60,
					}); cerr != nil {
						ls.logger.Warn("bug6: failed to dispatch cleanup stop_sandbox",
							"error", cerr,
							"sandbox_id", sandboxID,
						)
					} else {
						ls.logger.Info("bug6: dispatched cleanup stop_sandbox for orphan container",
							"sandbox_id", sandboxID,
							"container_id", refreshed.ContainerID.String,
							"cleanup_cmd_id", cleanupCmdID,
						)
					}
				}
				ls.logger.Info("ack tolerated: start_sandbox+success arrived during destroy (Phase 32 Wave 2 Bug 6)",
					"sandbox_id", sandboxID,
					"current_state", currentState,
				)
			} else {
				ls.logger.Info("ack transition skipped: state already advanced",
					"sandbox_id", sandboxID,
					"current_state", currentState,
					"cmd_type", cmdType,
				)
			}
			nextState = currentState
		} else {
			_, err = ls.store.TransitionSandboxState(ctx, store.TransitionSandboxStateParams{
				State:   string(nextState),
				ID:      pgSandboxID,
				State_2: sandbox.State,
			})
			if err != nil {
				// Optimistic-lock miss: another handler (typically the
				// container_started event from the same agent) advanced
				// the row between our read and write. Re-read; if the
				// row's current state is already what this ack would set
				// (or further along in the same direction), treat as
				// idempotent no-op instead of bubbling a 500 to the
				// agent — same shape as the `!valid` branch above.
				if errors.Is(err, pgx.ErrNoRows) {
					if refreshed, ferr := ls.store.GetSandbox(ctx, pgSandboxID); ferr == nil {
						currentNow := models.SandboxState(refreshed.State)
						if currentNow == nextState ||
							(event == models.EventStarted && currentNow == models.StateRunning) ||
							(event == models.EventStartFailed && currentNow == models.StateFailed) ||
							(event == models.EventDestroyed && currentNow == models.StateDestroyed) {
							ls.logger.Info("ack transition skipped: state already advanced (concurrent)",
								"sandbox_id", sandboxID,
								"current_state", currentNow,
								"cmd_type", cmdType,
							)
							sandbox = refreshed
							nextState = currentNow
						} else {
							return fmt.Errorf("transition state: %w", err)
						}
					} else {
						return fmt.Errorf("transition state: %w", err)
					}
				} else {
					return fmt.Errorf("transition state: %w", err)
				}
			}
		}

		// Persist container_id and host_port from agent ack result.
		// The agent sends these in the start_sandbox success ack payload.
		// Re-fetch the sandbox row afterwards so downstream notifications
		// (route upsert) see the updated host_port. Without this, restarts
		// push the STALE pre-restart port into Caddy and drift -- which only
		// compares app_names -- never heals it.
		if event == models.EventStarted {
			ls.persistRuntimeInfo(ctx, pgSandboxID, sandboxID, ackResult)
			if refreshed, err := ls.store.GetSandbox(ctx, pgSandboxID); err == nil {
				sandbox = refreshed
			}
		}

		// Record event
		_, err = ls.store.RecordEvent(ctx, store.RecordEventParams{
			SandboxID: pgSandboxID,
			NodeID:    sandbox.NodeID,
			EventType: string(event),
			Actor:     "agent",
			PrevState: pgtype.Text{String: sandbox.State, Valid: true},
			NextState: pgtype.Text{String: string(nextState), Valid: true},
			Payload:   safePayload(ackResult),
		})
		if err != nil {
			ls.logger.Warn("failed to record ack event", "error", err, "sandbox_id", sandboxID)
		}

		// Notify route manager of state changes (best-effort, errors logged)
		if ls.routeNotifier != nil {
			switch nextState {
			case models.StateRunning:
				ls.notifyRouteAdd(ctx, sandbox, sandboxID)
			case models.StateDestroyed:
				ls.notifyRouteRemove(ctx, sandbox.AppName, sandboxID)
			}
		}

		// Phase 33-B — emit state-change webhook for downstream consumers
		// (backend ContainerPoolService flips provisioning→warm on this).
		// Fire-and-forget; webhook impl runs the POST in its own goroutine.
		ls.notifyStateWebhook(sandbox, sandboxID, models.SandboxState(currentState), nextState)

		// Reset restart failure count on successful start (handles restart recovery).
		if ls.restartMgr != nil && nextState == models.StateRunning && sandbox.FailureCount > 0 {
			if err := ls.restartMgr.HandleRestarted(ctx, pgSandboxID); err != nil {
				ls.logger.Warn("restart manager HandleRestarted failed",
					"error", err,
					"sandbox_id", sandboxID,
				)
			}
		}
	}

	// Ack the command
	ackStatus := "completed"
	if status == "failure" {
		ackStatus = "failed"
	}
	err = ls.store.AckCommand(ctx, store.AckCommandParams{
		ID:     pgCmdID,
		Status: ackStatus,
		Result: ackResult,
	})
	if err != nil {
		return fmt.Errorf("ack command: %w", err)
	}

	// Fire exec_completed webhook for exec commands. Mirrors notifyStateWebhook:
	// fire-and-forget goroutine, best-effort. Listener URL & HMAC secret are
	// the same as the state-change channel (single webhook endpoint multiplexes
	// on payload.type). The receiver gets the parsed stdout/stderr/exit_code
	// from the agent's ack result map without re-polling /sandboxes/{id}/exec.
	if cmdType == string(models.CmdExec) && ls.webhookNotifier != nil {
		ls.notifyExecCompleted(sandboxID, cmdID, ackResult)
	}

	// Phase 33-Real-8 — inline pool cleanup. When a pool sandbox's
	// stop_sandbox command acks success, the row is now in `stopped`
	// state with no chance of resumption (pool sandboxes are not woken
	// — backend's container_pool service creates a fresh entry instead).
	// Without inline cleanup the row sits in PG forever waiting on the
	// daily-cron sweep, accumulating thousands of stopped rows that slow
	// every reconcile and inflate orphan-detection cost. Delete both
	// commands and the row inline; project sandboxes (with appx.projectId
	// metadata) keep the existing `stopped` behavior since they may be
	// woken later. Best-effort: cleanup failures don't unwind the ack.
	if cmdType == string(models.CmdStopSandbox) &&
		(status == "success" || forceDestroyed) &&
		isPoolSandbox(sandbox.Metadata) {
		// Re-fetch sandbox to confirm it's actually in stopped state
		// (HandleAck may have transitioned it above for destroying flow).
		fresh, ferr := ls.store.GetSandbox(ctx, pgSandboxID)
		if ferr == nil &&
			(models.SandboxState(fresh.State) == models.StateStopped ||
				models.SandboxState(fresh.State) == models.StateDestroyed) {
			if err := ls.store.DeleteCommandsForSandbox(ctx, pgSandboxID); err != nil {
				ls.logger.Warn("real-8 pool cleanup: delete commands failed",
					"sandbox_id", sandboxID, "err", err)
			} else if err := ls.store.DeleteSandbox(ctx, pgSandboxID); err != nil {
				ls.logger.Warn("real-8 pool cleanup: delete sandbox failed",
					"sandbox_id", sandboxID, "err", err)
			} else {
				ls.logger.Info("real-8 pool cleanup: row + commands deleted",
					"sandbox_id", sandboxID, "app_name", sandbox.AppName)
			}
		}
	}

	return nil
}

// isPoolSandbox returns true when the sandbox metadata lacks the
// appx.projectId label, i.e. it was created by backend's container_pool
// service for warm-cache rotation rather than by a user project. Pool
// sandboxes do not get woken after idle reap (backend's pool layer
// creates a fresh row instead), so their PG rows can be deleted inline
// on stop ack rather than waiting for the daily cleanup cron.
func isPoolSandbox(metadataJSON []byte) bool {
	if len(metadataJSON) == 0 {
		return true // no metadata at all = treat as pool (rare)
	}
	var md map[string]interface{}
	if err := json.Unmarshal(metadataJSON, &md); err != nil {
		return false // malformed metadata — be conservative, leave row alone
	}
	if v, ok := md["appx.projectId"]; ok {
		if s, isStr := v.(string); isStr && s != "" {
			return false
		}
	}
	return true
}

// isMissingContainerErr reports whether an agent failure ack indicates the
// target container no longer exists on the host. ackResult shape on failure
// is {"error": "<msg>"} (built in api.handleAckCommand). Docker's
// NotFound error message is "No such container: <id>" — substring-match
// keeps this resilient to Docker version drift.
//
// Wired into HandleAck's stop_sandbox+failure branch so a stale
// container_id reference cannot keep the idle-reaper retrying the same
// dead command forever (2026-05-17 incident).
func isMissingContainerErr(ackResult json.RawMessage) bool {
	if len(ackResult) == 0 {
		return false
	}
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(ackResult, &payload); err != nil {
		return false
	}
	return strings.Contains(payload.Error, "No such container")
}

// ── HandleEvent ──────────────────────────────────────────────────────

// HandleEvent processes a container event reported by an agent.
// It maps the agent event type to a state machine event and transitions accordingly.
func (ls *LifecycleService) HandleEvent(ctx context.Context, nodeID uuid.UUID, sandboxID uuid.UUID, eventType string, payload json.RawMessage) error {
	pgSandboxID := pgtype.UUID{Bytes: sandboxID, Valid: true}
	pgNodeID := pgtype.UUID{Bytes: nodeID, Valid: true}

	// Get current sandbox state
	sandbox, err := ls.store.GetSandbox(ctx, pgSandboxID)
	if err != nil {
		return fmt.Errorf("get sandbox: %w", err)
	}

	// Map agent event_type to state machine event
	var event models.SandboxEvent
	switch eventType {
	case "container_started":
		event = models.EventStarted
	case "container_exited":
		event = models.EventContainerExited
	case "container_oom":
		event = models.EventContainerExited // Same transition as exit
	default:
		ls.logger.Warn("unhandled event type", "event_type", eventType, "sandbox_id", sandboxID)
		return nil
	}

	// Validate transition via state machine
	currentState := models.SandboxState(sandbox.State)
	nextState, valid := models.NextState(currentState, event)
	if !valid {
		ls.logger.Warn("invalid state transition for event",
			"sandbox_id", sandboxID,
			"current_state", sandbox.State,
			"event", event,
		)
		return nil // Not an error -- just ignore invalid transitions
	}

	// Transition state
	_, err = ls.store.TransitionSandboxState(ctx, store.TransitionSandboxStateParams{
		State:   string(nextState),
		ID:      pgSandboxID,
		State_2: sandbox.State,
	})
	if err != nil {
		return fmt.Errorf("transition state: %w", err)
	}

	// Record event
	_, err = ls.store.RecordEvent(ctx, store.RecordEventParams{
		SandboxID: pgSandboxID,
		NodeID:    pgNodeID,
		EventType: eventType,
		Actor:     "agent",
		PrevState: pgtype.Text{String: sandbox.State, Valid: true},
		NextState: pgtype.Text{String: string(nextState), Valid: true},
		Payload:   safePayload(payload),
	})
	if err != nil {
		ls.logger.Warn("failed to record event", "error", err, "sandbox_id", sandboxID)
	}

	// Notify route manager when sandbox leaves running state (best-effort).
	// Only container_exited triggers route removal here -- container_started
	// does NOT trigger route add (only HandleAck for start_sandbox does).
	if ls.routeNotifier != nil && currentState == models.StateRunning {
		switch nextState {
		case models.StateRestarting, models.StateStopped, models.StateFailed:
			ls.notifyRouteRemove(ctx, sandbox.AppName, sandboxID)
		}
	}

	// Phase 33-B — emit state-change webhook on transition into running.
	// Phase 33-E — also emit on transitions into failed and destroyed
	// (the helper itself filters; we just call it on any transition).
	// Both HandleAck and HandleEvent can produce these transitions;
	// either path arriving first wins, the other becomes a no-op via
	// the prevState==nextState guard inside notifyStateWebhook. We
	// re-fetch the row so the webhook payload carries container_id and
	// host_port (HandleEvent itself doesn't update them — those flow in
	// via the start ack).
	if refreshed, err := ls.store.GetSandbox(ctx, pgSandboxID); err == nil {
		ls.notifyStateWebhook(refreshed, sandboxID, currentState, nextState)
	} else {
		ls.notifyStateWebhook(sandbox, sandboxID, currentState, nextState)
	}

	// Delegate to restart manager when transitioning to restarting state.
	// The restart manager handles failure counting and exponential backoff.
	if ls.restartMgr != nil && nextState == models.StateRestarting {
		result, err := ls.restartMgr.HandleCrash(ctx, sandbox)
		if err != nil {
			ls.logger.Warn("restart manager HandleCrash failed",
				"error", err,
				"sandbox_id", sandboxID,
			)
		} else if result.ShouldRestart {
			ls.logger.Info("restart scheduled",
				"sandbox_id", sandboxID,
				"delay", result.Delay,
			)
		}
	}

	return nil
}

// ── DestroySandbox ───────────────────────────────────────────────────

// DestroySandbox transitions a sandbox to DESTROYING and dispatches a stop_sandbox command.
func (ls *LifecycleService) DestroySandbox(ctx context.Context, sandboxID uuid.UUID) error {
	pgSandboxID := pgtype.UUID{Bytes: sandboxID, Valid: true}

	sandbox, err := ls.store.GetSandbox(ctx, pgSandboxID)
	if err != nil {
		return fmt.Errorf("get sandbox: %w", err)
	}

	// Phase 32 Wave 2 Bug 2 — orphan sandbox short-circuit.
	//
	// If the sandbox has no node assigned (node_id IS NULL), there is no
	// agent to receive a stop_sandbox command. The previous code path
	// passed pgtype.UUID{Valid: false} into commands.node_id which is
	// declared NOT NULL → Postgres rejected the INSERT and the failure
	// bubbled up as a 500 to the agent ack loop. Instead, mark the row
	// destroyed directly so the orphan reaper can garbage-collect it.
	if !sandbox.NodeID.Valid {
		_, err = ls.store.TransitionSandboxState(ctx, store.TransitionSandboxStateParams{
			State:   string(models.StateDestroyed),
			ID:      pgSandboxID,
			State_2: sandbox.State,
		})
		if err != nil {
			return fmt.Errorf("transition orphan to destroyed: %w", err)
		}
		// Best-effort event record for the orphan path (no node id available).
		_, evtErr := ls.store.RecordEvent(ctx, store.RecordEventParams{
			SandboxID: pgSandboxID,
			NodeID:    pgtype.UUID{Valid: false},
			EventType: string(models.EventDestroyRequest),
			Actor:     "control-plane",
			PrevState: pgtype.Text{String: sandbox.State, Valid: true},
			NextState: pgtype.Text{String: string(models.StateDestroyed), Valid: true},
			Payload:   []byte(`{"reason":"orphan_short_circuit"}`),
		})
		if evtErr != nil {
			ls.logger.Warn("failed to record orphan-destroy event",
				"error", evtErr, "sandbox_id", sandboxID)
		}
		ls.logger.Info("destroyed orphan sandbox without command dispatch",
			"sandbox_id", sandboxID, "prev_state", sandbox.State)
		// Phase 33-E — orphan short-circuit also fires the destroyed
		// webhook so backend learns the sandbox is gone the same way
		// it learns about running transitions.
		ls.notifyStateWebhook(sandbox, sandboxID, models.SandboxState(sandbox.State), models.StateDestroyed)
		return nil
	}

	// Validate transition via state machine
	currentState := models.SandboxState(sandbox.State)

	// Phase 32 Wave 2 Bug 3 — idempotent short-circuit. The state machine
	// permits destroy_request as a self-loop on destroying/destroyed (so
	// downstream callers using NextState directly don't see invalid-transition
	// errors), but the handler itself should skip the redundant DB UPDATE
	// and the spurious stop_sandbox command. The reschedule-on-node-flap
	// path was producing 24 errors/sec at 14:35:03-05 from re-attempted
	// destroys against terminal rows; this short-circuit silences them.
	if currentState == models.StateDestroying || currentState == models.StateDestroyed {
		ls.logger.Info("destroy skipped: sandbox already in terminal teardown",
			"sandbox_id", sandboxID,
			"current_state", currentState,
		)
		return nil
	}

	nextState, valid := models.NextState(currentState, models.EventDestroyRequest)
	if !valid {
		return fmt.Errorf("%w: cannot destroy sandbox in state %s", ErrInvalidTransition, sandbox.State)
	}

	// Transition to destroying
	_, err = ls.store.TransitionSandboxState(ctx, store.TransitionSandboxStateParams{
		State:   string(nextState),
		ID:      pgSandboxID,
		State_2: sandbox.State,
	})
	if err != nil {
		return fmt.Errorf("transition state: %w", err)
	}

	// Create stop_sandbox command targeted at sandbox's node
	cmdID := uuid.New()
	cmdPayload, _ := json.Marshal(map[string]interface{}{
		"container_id": sandbox.ContainerID.String,
	})

	_, err = ls.store.CreateCommand(ctx, store.CreateCommandParams{
		ID:             pgtype.UUID{Bytes: cmdID, Valid: true},
		NodeID:         sandbox.NodeID,
		SandboxID:      pgSandboxID,
		CommandType:    string(models.CmdStopSandbox),
		Payload:        cmdPayload,
		TimeoutSeconds: 60,
	})
	if err != nil {
		return fmt.Errorf("create stop command: %w", err)
	}

	// Record event
	_, err = ls.store.RecordEvent(ctx, store.RecordEventParams{
		SandboxID: pgSandboxID,
		NodeID:    sandbox.NodeID,
		EventType: string(models.EventDestroyRequest),
		Actor:     "control-plane",
		PrevState: pgtype.Text{String: sandbox.State, Valid: true},
		NextState: pgtype.Text{String: string(nextState), Valid: true},
		Payload:   cmdPayload,
	})
	if err != nil {
		ls.logger.Warn("failed to record destroy event", "error", err, "sandbox_id", sandboxID)
	}

	return nil
}

// ── WakeSandbox ──────────────────────────────────────────────────────

// WakeSandbox re-starts a stopped sandbox by firing EventScheduled and
// dispatching a new start_sandbox command. Only valid from StateStopped.
func (ls *LifecycleService) WakeSandbox(ctx context.Context, sandboxID uuid.UUID) error {
	pgSandboxID := pgtype.UUID{Bytes: sandboxID, Valid: true}

	sandbox, err := ls.store.GetSandbox(ctx, pgSandboxID)
	if err != nil {
		return fmt.Errorf("get sandbox: %w", err)
	}

	if sandbox.State != string(models.StateStopped) {
		return fmt.Errorf("%w: sandbox must be stopped to wake, current state: %s", ErrInvalidState, sandbox.State)
	}

	// Transition stopped → starting via EventScheduled
	_, err = ls.store.TransitionSandboxState(ctx, store.TransitionSandboxStateParams{
		State:   string(models.StateStarting),
		ID:      pgSandboxID,
		State_2: sandbox.State,
	})
	if err != nil {
		return fmt.Errorf("transition state: %w", err)
	}

	// Decode resources and env to re-issue the start command
	var resources Resources
	if len(sandbox.Resources) > 0 {
		_ = json.Unmarshal(sandbox.Resources, &resources)
	}
	var env map[string]string
	if len(sandbox.Env) > 0 {
		_ = json.Unmarshal(sandbox.Env, &env)
	}

	cmdPayload, err := json.Marshal(map[string]interface{}{
		"app_name":  sandbox.AppName,
		"image":     sandbox.Image,
		"resources": resources,
		"env":       env,
	})
	if err != nil {
		return fmt.Errorf("marshal command payload: %w", err)
	}

	cmdID := uuid.New()
	_, err = ls.store.CreateCommand(ctx, store.CreateCommandParams{
		ID:             pgtype.UUID{Bytes: cmdID, Valid: true},
		NodeID:         sandbox.NodeID,
		SandboxID:      pgSandboxID,
		CommandType:    string(models.CmdStartSandbox),
		Payload:        cmdPayload,
		TimeoutSeconds: 60,
	})
	if err != nil {
		return fmt.Errorf("create start command: %w", err)
	}

	_, err = ls.store.RecordEvent(ctx, store.RecordEventParams{
		SandboxID: pgSandboxID,
		NodeID:    sandbox.NodeID,
		EventType: string(models.EventScheduled),
		Actor:     "control-plane-wake",
		PrevState: pgtype.Text{String: string(models.StateStopped), Valid: true},
		NextState: pgtype.Text{String: string(models.StateStarting), Valid: true},
		Payload:   cmdPayload,
	})
	if err != nil {
		ls.logger.Warn("failed to record wake event", "error", err, "sandbox_id", sandboxID)
	}

	ls.logger.Info("waking stopped sandbox", "sandbox_id", sandboxID, "app_name", sandbox.AppName)
	return nil
}

// ── RestartSandbox ───────────────────────────────────────────────────

// RestartSandbox creates a restart_sandbox command for a running sandbox.
// No state transition occurs -- the restart is handled at the Docker level.
func (ls *LifecycleService) RestartSandbox(ctx context.Context, sandboxID uuid.UUID) error {
	pgSandboxID := pgtype.UUID{Bytes: sandboxID, Valid: true}

	sandbox, err := ls.store.GetSandbox(ctx, pgSandboxID)
	if err != nil {
		return fmt.Errorf("get sandbox: %w", err)
	}

	// Must be in running state
	if sandbox.State != string(models.StateRunning) {
		return fmt.Errorf("%w: sandbox must be running to restart, current state: %s", ErrInvalidState, sandbox.State)
	}

	// Create restart_sandbox command
	cmdID := uuid.New()
	cmdPayload, _ := json.Marshal(map[string]interface{}{
		"container_id": sandbox.ContainerID.String,
	})

	_, err = ls.store.CreateCommand(ctx, store.CreateCommandParams{
		ID:             pgtype.UUID{Bytes: cmdID, Valid: true},
		NodeID:         sandbox.NodeID,
		SandboxID:      pgSandboxID,
		CommandType:    string(models.CmdRestartSandbox),
		Payload:        cmdPayload,
		TimeoutSeconds: 60,
	})
	if err != nil {
		return fmt.Errorf("create restart command: %w", err)
	}

	// Record event (informational, no state change)
	_, err = ls.store.RecordEvent(ctx, store.RecordEventParams{
		SandboxID: pgSandboxID,
		NodeID:    sandbox.NodeID,
		EventType: "restart_requested",
		Actor:     "control-plane",
		PrevState: pgtype.Text{String: sandbox.State, Valid: true},
		NextState: pgtype.Text{String: sandbox.State, Valid: true},
		Payload:   cmdPayload,
	})
	if err != nil {
		ls.logger.Warn("failed to record restart event", "error", err, "sandbox_id", sandboxID)
	}

	return nil
}

// ── Route Notification Helpers ──────────────────────────────────────

// notifyRouteAdd builds the upstream address from node + sandbox data and
// calls OnSandboxRunning. Errors are logged as warnings, never propagated.
func (ls *LifecycleService) notifyRouteAdd(ctx context.Context, sandbox store.Sandbox, sandboxID uuid.UUID) {
	if !sandbox.NodeID.Valid || !sandbox.HostPort.Valid {
		ls.logger.Warn("cannot notify route add: missing node_id or host_port",
			"sandbox_id", sandboxID,
			"node_id_valid", sandbox.NodeID.Valid,
			"host_port_valid", sandbox.HostPort.Valid,
		)
		return
	}

	node, err := ls.store.GetNodeByID(ctx, sandbox.NodeID)
	if err != nil {
		ls.logger.Warn("failed to get node for route notification",
			"error", err,
			"sandbox_id", sandboxID,
			"node_id", uuid.UUID(sandbox.NodeID.Bytes).String(),
		)
		return
	}

	upstream := fmt.Sprintf("%s:%d", node.TailscaleIp.String(), sandbox.HostPort.Int32)
	if err := ls.routeNotifier.OnSandboxRunning(ctx, sandbox.AppName, sandboxID.String(), upstream); err != nil {
		ls.logger.Warn("route add notification failed",
			"error", err,
			"app_name", sandbox.AppName,
			"sandbox_id", sandboxID,
			"upstream", upstream,
		)
	}
}

// notifyRouteRemove calls OnSandboxStopped. Errors are logged, never propagated.
func (ls *LifecycleService) notifyRouteRemove(ctx context.Context, appName string, sandboxID uuid.UUID) {
	if err := ls.routeNotifier.OnSandboxStopped(ctx, appName); err != nil {
		ls.logger.Warn("route remove notification failed",
			"error", err,
			"app_name", appName,
			"sandbox_id", sandboxID,
		)
	}
}

// persistRuntimeInfo extracts container_id and host_port from an ack result
// and persists them to the sandbox row. Best-effort: errors are logged.
func (ls *LifecycleService) persistRuntimeInfo(ctx context.Context, pgSandboxID pgtype.UUID, sandboxID uuid.UUID, ackResult json.RawMessage) {
	if len(ackResult) == 0 {
		return
	}
	var info struct {
		ContainerID string `json:"container_id"`
		HostPort    int32  `json:"host_port"`
	}
	if err := json.Unmarshal(ackResult, &info); err != nil {
		ls.logger.Warn("failed to parse ack runtime info", "error", err, "sandbox_id", sandboxID)
		return
	}
	if info.ContainerID == "" && info.HostPort == 0 {
		return
	}
	if err := ls.store.UpdateSandboxRuntime(ctx, store.UpdateSandboxRuntimeParams{
		ContainerID: pgtype.Text{String: info.ContainerID, Valid: info.ContainerID != ""},
		HostPort:    pgtype.Int4{Int32: info.HostPort, Valid: info.HostPort != 0},
		ID:          pgSandboxID,
	}); err != nil {
		ls.logger.Warn("failed to persist sandbox runtime info",
			"error", err,
			"sandbox_id", sandboxID,
			"container_id", info.ContainerID,
			"host_port", info.HostPort,
		)
	}
}

// safePayload returns p if non-nil, or an empty JSON object otherwise.
// Prevents NOT NULL constraint violations on the events.payload column.
func safePayload(p []byte) []byte {
	if len(p) == 0 {
		return []byte(`{}`)
	}
	return p
}
