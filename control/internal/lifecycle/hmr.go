// Package lifecycle — hmr.go adds DispatchStartHmr / DispatchStopHmr for the
// per-turn ephemeral HMR tier (Step 3). It mirrors DispatchBuildExport's
// command/ack plumbing but issues distinct command types (start_hmr / stop_hmr)
// so the agent runs a LIVE dev Metro (`expo start`) bound to the project's live
// code dir, in its own box, for the lifetime of a turn. Like build_export it
// does NOT transition sandbox state — it piggybacks the command pipeline and is
// keyed to the dev sandbox's node so the box lands on the host holding the code
// dir.
package lifecycle

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/appx/forge/control/internal/store"
	"github.com/appx/forge/shared-go/models"
)

// hmrCommandTimeoutSeconds bounds how long a start_hmr / stop_hmr command may
// sit pending before the agent skips it as expired (sqlc passes the value
// explicitly, so omitting it would store 0 = never-expire rather than the
// column default). Generous enough to cover a cold image pull on the box.
const hmrCommandTimeoutSeconds = 120

// StartHmrRequest contains the parameters for starting a per-turn HMR box. The
// dev sandbox's image is resolved from the row by DispatchStartHmr — the caller
// need not set it.
type StartHmrRequest struct {
	// TurnID names + labels the box and is the join key for the eventual
	// stop_hmr. Required.
	TurnID string `json:"turn_id"`
	// Env is passed through to the HMR container at create time (backend owns
	// HMR env semantics, e.g. APP_NAME / EXPO_PACKAGER_PROXY_URL).
	Env map[string]string `json:"env,omitempty"`
	// Image is filled in from the dev sandbox row by DispatchStartHmr.
	Image string `json:"image,omitempty"`
}

// StopHmrRequest contains the parameters for tearing a per-turn HMR box down.
type StopHmrRequest struct {
	// TurnID identifies the box to remove (forge-hmr-<TurnID>). Required.
	TurnID string `json:"turn_id"`
}

// DispatchStartHmr creates a start_hmr command targeted at the sandbox's node
// and returns the new command ID. The agent receives it via the next poll cycle
// and acks with {hmr_container_id, host_port}; the backend then adds the
// ephemeral Caddy route and emits preview:hmr-ready.
//
// Validations:
//   - turn_id required
//   - sandbox must exist (else ErrNotFound)
//   - sandbox must have node_id assigned (else ErrSandboxNotAssigned)
//
// Like DispatchBuildExport, this does NOT require the sandbox to be `running`:
// the box binds the on-disk live code dir (which persists across a slept
// sandbox) and runs in its own container. It is keyed to the dev sandbox's node
// so the box lands on the same host as the code dir.
func (ls *LifecycleService) DispatchStartHmr(ctx context.Context, sandboxID uuid.UUID, req StartHmrRequest) (string, error) {
	if req.TurnID == "" {
		return "", fmt.Errorf("start_hmr: turn_id required")
	}

	pgSandboxID := pgtype.UUID{Bytes: sandboxID, Valid: true}

	sandbox, err := ls.store.GetSandbox(ctx, pgSandboxID)
	if err != nil {
		return "", ErrNotFound
	}
	if !sandbox.NodeID.Valid {
		return "", ErrSandboxNotAssigned
	}

	// Reuse the dev sandbox's EXACT image for the box (never the agent default).
	// The agent falls back to inspecting the live container if empty, but
	// passing it from the row is the authoritative source.
	if req.Image == "" {
		req.Image = sandbox.Image
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal start_hmr payload: %w", err)
	}

	cmdID := uuid.New()
	_, err = ls.store.CreateCommand(ctx, store.CreateCommandParams{
		ID:             pgtype.UUID{Bytes: cmdID, Valid: true},
		NodeID:         sandbox.NodeID,
		SandboxID:      pgSandboxID,
		CommandType:    string(models.CmdStartHmr),
		Payload:        payload,
		TimeoutSeconds: hmrCommandTimeoutSeconds,
	})
	if err != nil {
		return "", fmt.Errorf("create start_hmr command: %w", err)
	}

	ls.logger.Info("start_hmr command dispatched",
		"sandbox_id", sandboxID,
		"turn_id", req.TurnID,
		"cmd_id", cmdID,
	)

	return cmdID.String(), nil
}

// DispatchStopHmr creates a stop_hmr command targeted at the sandbox's node and
// returns the new command ID. The agent force-removes forge-hmr-<TurnID> and
// releases its host port. Idempotent agent-side: a turn whose box is already
// gone acks success.
func (ls *LifecycleService) DispatchStopHmr(ctx context.Context, sandboxID uuid.UUID, req StopHmrRequest) (string, error) {
	if req.TurnID == "" {
		return "", fmt.Errorf("stop_hmr: turn_id required")
	}

	pgSandboxID := pgtype.UUID{Bytes: sandboxID, Valid: true}

	sandbox, err := ls.store.GetSandbox(ctx, pgSandboxID)
	if err != nil {
		return "", ErrNotFound
	}
	if !sandbox.NodeID.Valid {
		return "", ErrSandboxNotAssigned
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal stop_hmr payload: %w", err)
	}

	cmdID := uuid.New()
	_, err = ls.store.CreateCommand(ctx, store.CreateCommandParams{
		ID:             pgtype.UUID{Bytes: cmdID, Valid: true},
		NodeID:         sandbox.NodeID,
		SandboxID:      pgSandboxID,
		CommandType:    string(models.CmdStopHmr),
		Payload:        payload,
		TimeoutSeconds: hmrCommandTimeoutSeconds,
	})
	if err != nil {
		return "", fmt.Errorf("create stop_hmr command: %w", err)
	}

	ls.logger.Info("stop_hmr command dispatched",
		"sandbox_id", sandboxID,
		"turn_id", req.TurnID,
		"cmd_id", cmdID,
	)

	return cmdID.String(), nil
}
