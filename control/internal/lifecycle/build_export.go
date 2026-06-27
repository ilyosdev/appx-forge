// Package lifecycle — build_export.go adds DispatchBuildExport for the isolated
// cold web export. It mirrors DispatchExec's command/ack plumbing but issues a
// distinct command type (build_export) so the agent runs the export in a
// SEPARATE ephemeral build-worker container (never sharing the dev sandbox's
// cgroup) against a snapshot of the project code. Like exec, it does not
// transition sandbox state — it piggybacks on the existing command pipeline.
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

// BuildExportRequest contains the parameters for an isolated build export. It
// reuses the exec payload shape (the export semantics — coldCommand string +
// APPX_BASE_URL env — are owned by the backend) and carries the dev sandbox's
// image so the worker is built from the exact same image, never a default.
type BuildExportRequest struct {
	Command        string            `json:"command"`
	Cwd            string            `json:"cwd,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	CPUBurst       bool              `json:"cpu_burst,omitempty"`
	User           string            `json:"user,omitempty"`
	// Image is filled in from the dev sandbox row by DispatchBuildExport — the
	// caller need not set it.
	Image string `json:"image,omitempty"`
}

// DispatchBuildExport creates a build_export command targeted at the sandbox's
// node and returns the new command ID. The agent receives it via the next poll
// cycle and acks with exit_code + build_id; the backend then fetches the build's
// dist/ via the build-scoped dist endpoint.
//
// Validations:
//   - sandbox must exist (else ErrNotFound)
//   - sandbox must have node_id assigned (else ErrSandboxNotAssigned)
//
// Unlike DispatchExec, this does NOT require the sandbox to be `running`: the
// build worker snapshots the on-disk code dir (which persists across a slept
// sandbox) and runs in its own container, so a slept-but-assigned sandbox can
// still build. The command is keyed to the dev sandbox's node so the worker
// lands on the same host as the code dir.
//
// Defaults: TimeoutSeconds == 0 → 120s; > 300 → clamped to 300s.
func (ls *LifecycleService) DispatchBuildExport(ctx context.Context, sandboxID uuid.UUID, req BuildExportRequest) (string, error) {
	pgSandboxID := pgtype.UUID{Bytes: sandboxID, Valid: true}

	sandbox, err := ls.store.GetSandbox(ctx, pgSandboxID)
	if err != nil {
		return "", ErrNotFound
	}

	if !sandbox.NodeID.Valid {
		return "", ErrSandboxNotAssigned
	}

	// Reuse the dev sandbox's EXACT image for the worker (never the agent
	// default). The agent falls back to inspecting the live container if empty,
	// but passing it from the row is the authoritative source.
	if req.Image == "" {
		req.Image = sandbox.Image
	}

	timeoutSeconds := req.TimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = 120
	}
	if timeoutSeconds > 300 {
		timeoutSeconds = 300
	}
	req.TimeoutSeconds = timeoutSeconds

	payload, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal build_export payload: %w", err)
	}

	cmdID := uuid.New()
	_, err = ls.store.CreateCommand(ctx, store.CreateCommandParams{
		ID:             pgtype.UUID{Bytes: cmdID, Valid: true},
		NodeID:         sandbox.NodeID,
		SandboxID:      pgSandboxID,
		CommandType:    string(models.CmdBuildExport),
		Payload:        payload,
		TimeoutSeconds: int32(timeoutSeconds),
	})
	if err != nil {
		return "", fmt.Errorf("create build_export command: %w", err)
	}

	ls.logger.Info("build_export command dispatched",
		"sandbox_id", sandboxID,
		"cmd_id", cmdID,
		"timeout_seconds", timeoutSeconds,
	)

	return cmdID.String(), nil
}
