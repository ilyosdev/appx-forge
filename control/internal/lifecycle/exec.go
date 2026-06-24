// Package lifecycle — exec.go adds DispatchExec for ad-hoc shell-command
// dispatch to a running sandbox. Mirrors CreateSandbox / RestartSandbox cmd
// dispatch shape but does not transition sandbox state — exec is a read-side
// concern that piggybacks on the existing command/ack pipeline.
package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/appx/forge/control/internal/store"
	"github.com/appx/forge/shared-go/models"
)

// ErrSandboxNotAssigned is returned by DispatchExec when the sandbox row
// exists but has no node assigned (NodeID.Valid == false). There is no
// agent to receive the exec command in this case.
var ErrSandboxNotAssigned = errors.New("lifecycle: sandbox not assigned to a node")

// ErrSandboxNotRunning is returned by DispatchExec when the sandbox state
// is not `running`. Exec only makes sense against a live container.
var ErrSandboxNotRunning = errors.New("lifecycle: sandbox not in running state")

// ExecRequest contains the parameters for a sandbox exec call. JSON tags
// match the on-the-wire shape the agent receives in command payload.
type ExecRequest struct {
	Command        string            `json:"command"`
	Cwd            string            `json:"cwd,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	// CPUBurst is an optional pass-through flag (omitted = false). When true,
	// the agent temporarily raises the sandbox CPU cap for this exec. Control
	// does not interpret it — it is re-marshalled verbatim into the agent
	// command payload alongside the other exec fields.
	CPUBurst bool `json:"cpu_burst,omitempty"`
	// User is an optional pass-through (empty = agent default appuser). The
	// web export sets "root"; control forwards it verbatim to the agent.
	User string `json:"user,omitempty"`
}

// DispatchExec creates an exec command targeted at the sandbox's node and
// returns the new command ID. The agent will receive it via the next
// PollPendingCommands cycle and ack with stdout/stderr/exit_code in result.
//
// Validations:
//   - sandbox must exist (else ErrNotFound)
//   - sandbox must have node_id assigned (else ErrSandboxNotAssigned)
//   - sandbox state must be running (else ErrSandboxNotRunning)
//
// Defaults:
//   - TimeoutSeconds == 0 → 120s
//   - TimeoutSeconds > 300 → clamped to 300s
func (ls *LifecycleService) DispatchExec(ctx context.Context, sandboxID uuid.UUID, req ExecRequest) (string, error) {
	pgSandboxID := pgtype.UUID{Bytes: sandboxID, Valid: true}

	sandbox, err := ls.store.GetSandbox(ctx, pgSandboxID)
	if err != nil {
		return "", ErrNotFound
	}

	if !sandbox.NodeID.Valid {
		return "", ErrSandboxNotAssigned
	}

	if models.SandboxState(sandbox.State) != models.StateRunning {
		return "", fmt.Errorf("%w: current state %s", ErrSandboxNotRunning, sandbox.State)
	}

	// Clamp timeout into [1, 300]; 120s default when caller passed zero.
	timeoutSeconds := req.TimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = 120
	}
	if timeoutSeconds > 300 {
		timeoutSeconds = 300
	}

	// Persist the bounded timeout back into the payload so the agent sees
	// the same value the control plane is using to time out the command row.
	req.TimeoutSeconds = timeoutSeconds

	payload, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal exec payload: %w", err)
	}

	cmdID := uuid.New()
	_, err = ls.store.CreateCommand(ctx, store.CreateCommandParams{
		ID:             pgtype.UUID{Bytes: cmdID, Valid: true},
		NodeID:         sandbox.NodeID,
		SandboxID:      pgSandboxID,
		CommandType:    string(models.CmdExec),
		Payload:        payload,
		TimeoutSeconds: int32(timeoutSeconds),
	})
	if err != nil {
		return "", fmt.Errorf("create exec command: %w", err)
	}

	ls.logger.Info("exec command dispatched",
		"sandbox_id", sandboxID,
		"cmd_id", cmdID,
		"timeout_seconds", timeoutSeconds,
	)

	return cmdID.String(), nil
}
