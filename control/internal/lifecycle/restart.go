package lifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/appx/forge/control/internal/store"
	"github.com/appx/forge/shared-go/models"
)

// RestartStore abstracts the database operations needed by the RestartManager.
type RestartStore interface {
	IncrementSandboxFailureCount(ctx context.Context, id pgtype.UUID) (store.Sandbox, error)
	ResetSandboxFailureCount(ctx context.Context, id pgtype.UUID) error
	TransitionSandboxState(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error)
	CreateCommand(ctx context.Context, arg store.CreateCommandParams) (store.Command, error)
	RecordEvent(ctx context.Context, arg store.RecordEventParams) (store.Event, error)
	GetSandbox(ctx context.Context, id pgtype.UUID) (store.Sandbox, error)
}

const (
	// maxRestartAttempts is the maximum number of restart attempts before
	// transitioning to FAILED state.
	maxRestartAttempts = 3

	// baseRestartDelay is the base delay for exponential backoff.
	// Delays: 5s, 10s, 20s (baseDelay * 2^attempt).
	baseRestartDelay = 5 * time.Second
)

// CrashResult contains the outcome of HandleCrash.
type CrashResult struct {
	// ShouldRestart indicates whether the sandbox should be restarted.
	ShouldRestart bool

	// Delay is the backoff delay before the restart command should be executed.
	// Only meaningful when ShouldRestart is true.
	Delay time.Duration
}

// RestartManager handles crash recovery with exponential backoff.
// It coordinates failure count tracking, state transitions, and restart
// command dispatch when a sandbox container exits unexpectedly.
type RestartManager struct {
	store  RestartStore
	logger *slog.Logger
}

// NewRestartManager creates a new RestartManager.
func NewRestartManager(s RestartStore, logger *slog.Logger) *RestartManager {
	if logger == nil {
		logger = slog.Default()
	}
	return &RestartManager{store: s, logger: logger}
}

// HandleCrash processes a container crash for a sandbox in the restarting state.
// It increments the failure count, and either dispatches a restart command with
// exponential backoff or transitions to FAILED if max attempts are exceeded.
//
// The backoff delay is recorded in the command payload -- the agent is responsible
// for waiting the specified delay before executing the restart.
func (rm *RestartManager) HandleCrash(ctx context.Context, sandbox store.Sandbox) (CrashResult, error) {
	sandboxID := uuid.UUID(sandbox.ID.Bytes)

	// Increment failure count atomically in the database.
	updated, err := rm.store.IncrementSandboxFailureCount(ctx, sandbox.ID)
	if err != nil {
		return CrashResult{}, fmt.Errorf("increment failure count: %w", err)
	}

	newCount := updated.FailureCount
	rm.logger.Info("sandbox crash detected",
		"sandbox_id", sandboxID,
		"failure_count", newCount,
		"max_attempts", maxRestartAttempts,
	)

	// If max retries exceeded, transition to FAILED.
	if newCount > int32(maxRestartAttempts) {
		rm.logger.Warn("max restart attempts exceeded, transitioning to failed",
			"sandbox_id", sandboxID,
			"failure_count", newCount,
		)

		_, err = rm.store.TransitionSandboxState(ctx, store.TransitionSandboxStateParams{
			State:   string(models.StateFailed),
			ID:      sandbox.ID,
			State_2: sandbox.State,
		})
		if err != nil {
			return CrashResult{}, fmt.Errorf("transition to failed: %w", err)
		}

		// Record the failure event.
		rm.store.RecordEvent(ctx, store.RecordEventParams{
			SandboxID: sandbox.ID,
			NodeID:    sandbox.NodeID,
			EventType: "restart_exhausted",
			Actor:     "control-plane",
			PrevState: pgtype.Text{String: sandbox.State, Valid: true},
			NextState: pgtype.Text{String: string(models.StateFailed), Valid: true},
		})

		return CrashResult{ShouldRestart: false}, nil
	}

	// Calculate exponential backoff delay: baseDelay * 2^(count-1).
	// count=1 -> 5s, count=2 -> 10s, count=3 -> 20s.
	delay := baseRestartDelay * (1 << (newCount - 1))

	// Transition restarting -> starting via restart_attempt event.
	_, err = rm.store.TransitionSandboxState(ctx, store.TransitionSandboxStateParams{
		State:   string(models.StateStarting),
		ID:      sandbox.ID,
		State_2: string(models.StateRestarting),
	})
	if err != nil {
		return CrashResult{}, fmt.Errorf("transition restarting->starting: %w", err)
	}

	// Dispatch start_sandbox command (the agent will use the delay before executing).
	cmdPayload, _ := json.Marshal(map[string]interface{}{
		"app_name":      sandbox.AppName,
		"image":         sandbox.Image,
		"container_id":  sandbox.ContainerID.String,
		"restart_delay": delay.Seconds(),
	})

	cmdID := uuid.New()
	_, err = rm.store.CreateCommand(ctx, store.CreateCommandParams{
		ID:             pgtype.UUID{Bytes: cmdID, Valid: true},
		NodeID:         sandbox.NodeID,
		SandboxID:      sandbox.ID,
		CommandType:    string(models.CmdStartSandbox),
		Payload:        cmdPayload,
		TimeoutSeconds: 60 + int32(delay.Seconds()),
	})
	if err != nil {
		return CrashResult{}, fmt.Errorf("create restart command: %w", err)
	}

	// Record restart_attempt event.
	rm.store.RecordEvent(ctx, store.RecordEventParams{
		SandboxID: sandbox.ID,
		NodeID:    sandbox.NodeID,
		EventType: string(models.EventRestartAttempt),
		Actor:     "control-plane",
		PrevState: pgtype.Text{String: string(models.StateRestarting), Valid: true},
		NextState: pgtype.Text{String: string(models.StateStarting), Valid: true},
		Payload:   cmdPayload,
	})

	rm.logger.Info("restart command dispatched",
		"sandbox_id", sandboxID,
		"attempt", newCount,
		"delay", delay,
	)

	return CrashResult{ShouldRestart: true, Delay: delay}, nil
}

// HandleRestarted resets the failure count to zero after a successful restart.
// This should be called when a sandbox transitions from starting to running
// after a restart cycle.
func (rm *RestartManager) HandleRestarted(ctx context.Context, sandboxID pgtype.UUID) error {
	err := rm.store.ResetSandboxFailureCount(ctx, sandboxID)
	if err != nil {
		return fmt.Errorf("reset failure count: %w", err)
	}

	rm.logger.Info("sandbox restart succeeded, failure count reset",
		"sandbox_id", uuid.UUID(sandboxID.Bytes),
	)

	return nil
}
