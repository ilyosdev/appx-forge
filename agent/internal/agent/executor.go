package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/appx/forge/agent/internal/controlclient"
	"github.com/appx/forge/agent/internal/docker"
	"github.com/appx/forge/agent/internal/ports"
)

// ackReporter is the interface for acknowledging commands with the control plane.
type ackReporter interface {
	AckCommand(ctx context.Context, cmdID string, ack controlclient.AckRequest) error
}

// sandboxInfo tracks running sandbox state in-memory.
type sandboxInfo struct {
	ContainerID string
	HostPort    int
	AppName     string
}

// CommandExecutor dispatches commands received from the control plane
// to the appropriate Docker operations and tracks sandbox state.
//
// Thread safety: the sandboxes map is protected by a RWMutex.
// Commands are executed sequentially (one at a time) per agent-protocol.md.
type CommandExecutor struct {
	docker     docker.Client
	ports      *ports.Allocator
	ctrlClient ackReporter
	sandboxDir string
	logger     *slog.Logger

	// In-memory map: sandboxID -> sandbox state
	sandboxes map[string]*sandboxInfo
	mu        sync.RWMutex
}

// NewCommandExecutor creates a CommandExecutor with the given dependencies.
func NewCommandExecutor(
	dockerClient docker.Client,
	portAlloc *ports.Allocator,
	ctrlClient ackReporter,
	sandboxDir string,
	logger *slog.Logger,
) *CommandExecutor {
	return &CommandExecutor{
		docker:     dockerClient,
		ports:      portAlloc,
		ctrlClient: ctrlClient,
		sandboxDir: sandboxDir,
		logger:     logger,
		sandboxes:  make(map[string]*sandboxInfo),
	}
}

// ── Command Payloads ────────────────────────────────────────────────────

// startSandboxPayload is the payload for start_sandbox commands.
type startSandboxPayload struct {
	AppName   string            `json:"app_name"`
	Image     string            `json:"image"`
	Resources resourceSpec      `json:"resources"`
	Env       map[string]string `json:"env"`
}

// resourceSpec defines CPU and memory limits for a sandbox.
type resourceSpec struct {
	CPUCores float64 `json:"cpu_cores"`
	MemoryMB int64   `json:"memory_mb"`
}

// stopSandboxPayload is the payload for stop_sandbox commands.
type stopSandboxPayload struct {
	ContainerID string `json:"container_id"`
}

// restartSandboxPayload is the payload for restart_sandbox commands.
type restartSandboxPayload struct {
	ContainerID string `json:"container_id"`
}

// getLogsPayload is the payload for get_logs commands.
type getLogsPayload struct {
	ContainerID string `json:"container_id"`
	Tail        int    `json:"tail"`
	Follow      bool   `json:"follow"`
}

// execPayload is the payload for exec commands. The control plane sends
// a shell command string, optional cwd / env overrides, and a wall-clock
// timeout (clamped server-side in ExecRun).
type execPayload struct {
	Command        string            `json:"command"`
	Cwd            string            `json:"cwd"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds"`
}

// ── Execute ─────────────────────────────────────────────────────────────

// Execute dispatches a command to the appropriate handler based on type.
// It checks for expired commands first, then switches on command type.
// On any error, it acks with failure rather than returning an error.
func (e *CommandExecutor) Execute(ctx context.Context, cmd controlclient.Command) error {
	e.logger.Info("executing command", "cmd_id", cmd.ID, "type", cmd.Type, "sandbox_id", cmd.SandboxID)

	// Check if command has expired
	if cmd.TimeoutSeconds > 0 {
		deadline := cmd.IssuedAt.Add(time.Duration(cmd.TimeoutSeconds) * time.Second)
		if time.Now().After(deadline) {
			e.logger.Warn("command expired, skipping", "cmd_id", cmd.ID, "issued_at", cmd.IssuedAt)
			return e.ackFailure(ctx, cmd.ID, "command timed out before execution")
		}
	}

	switch cmd.Type {
	case "start_sandbox":
		return e.executeStartSandbox(ctx, cmd)
	case "stop_sandbox":
		return e.executeStopSandbox(ctx, cmd)
	case "restart_sandbox":
		return e.executeRestartSandbox(ctx, cmd)
	case "get_logs":
		return e.executeGetLogs(ctx, cmd)
	case "exec":
		return e.executeExec(ctx, cmd)
	case "prune":
		return e.executePrune(ctx, cmd)
	default:
		return e.ackFailure(ctx, cmd.ID, fmt.Sprintf("unknown command type: %s", cmd.Type))
	}
}

// ── Command Handlers ────────────────────────────────────────────────────

// executeStartSandbox allocates a port, creates a container, and tracks it.
func (e *CommandExecutor) executeStartSandbox(ctx context.Context, cmd controlclient.Command) error {
	var payload startSandboxPayload
	if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
		return e.ackFailure(ctx, cmd.ID, fmt.Sprintf("invalid start_sandbox payload: %v", err))
	}

	// Allocate host port
	hostPort, err := e.ports.Allocate()
	if err != nil {
		return e.ackFailure(ctx, cmd.ID, fmt.Sprintf("port allocation failed: %v", err))
	}

	// Build sandbox spec
	spec := &docker.SandboxSpec{
		SandboxID:  cmd.SandboxID,
		AppName:    payload.AppName,
		Image:      payload.Image,
		HostPort:   hostPort,
		CPUCores:   payload.Resources.CPUCores,
		MemoryMB:   payload.Resources.MemoryMB,
		Env:        payload.Env,
		SandboxDir: e.sandboxDir,
	}

	// Create and start container
	containerID, err := e.docker.CreateContainer(ctx, spec)
	if err != nil {
		// Release port on failure
		if releaseErr := e.ports.Release(hostPort); releaseErr != nil {
			e.logger.Warn("failed to release port after container creation failure",
				"port", hostPort, "error", releaseErr)
		}
		return e.ackFailure(ctx, cmd.ID, fmt.Sprintf("container creation failed: %v", err))
	}

	// Track sandbox in memory
	e.mu.Lock()
	e.sandboxes[cmd.SandboxID] = &sandboxInfo{
		ContainerID: containerID,
		HostPort:    hostPort,
		AppName:     payload.AppName,
	}
	e.mu.Unlock()

	e.logger.Info("sandbox started",
		"sandbox_id", cmd.SandboxID,
		"container_id", containerID,
		"host_port", hostPort,
	)

	return e.ackSuccess(ctx, cmd.ID, map[string]interface{}{
		"container_id": containerID,
		"host_port":    hostPort,
	})
}

// executeStopSandbox stops and removes a container, releases its port.
func (e *CommandExecutor) executeStopSandbox(ctx context.Context, cmd controlclient.Command) error {
	var payload stopSandboxPayload
	if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
		return e.ackFailure(ctx, cmd.ID, fmt.Sprintf("invalid stop_sandbox payload: %v", err))
	}

	// Stop container with 10s timeout
	if err := e.docker.StopContainer(ctx, payload.ContainerID, 10*time.Second); err != nil {
		return e.ackFailure(ctx, cmd.ID, fmt.Sprintf("stop container failed: %v", err))
	}

	// Remove container
	if err := e.docker.RemoveContainer(ctx, payload.ContainerID); err != nil {
		return e.ackFailure(ctx, cmd.ID, fmt.Sprintf("remove container failed: %v", err))
	}

	// Release port and clean up sandbox tracking
	e.mu.Lock()
	var workdirToRemove string
	if info, ok := e.sandboxes[cmd.SandboxID]; ok {
		if releaseErr := e.ports.Release(info.HostPort); releaseErr != nil {
			e.logger.Warn("failed to release port on stop",
				"port", info.HostPort, "error", releaseErr)
		}
		// E3 dir-leak fix — capture the workdir to delete AFTER the container
		// is removed, so destroying a sandbox no longer leaks its bind-mount
		// directory under SandboxDir. Guard a non-empty AppName: an empty name
		// would join to SandboxDir itself and RemoveAll the whole tree.
		if info.AppName != "" {
			workdirToRemove = filepath.Join(e.sandboxDir, info.AppName)
		}
		delete(e.sandboxes, cmd.SandboxID)
	}
	e.mu.Unlock()

	// Remove the sandbox workdir outside the lock (filesystem I/O). Non-fatal:
	// a failure here must never fail the stop — the periodic GC is the backstop.
	if workdirToRemove != "" {
		if rmErr := os.RemoveAll(workdirToRemove); rmErr != nil {
			e.logger.Warn("failed to remove sandbox workdir",
				"path", workdirToRemove, "error", rmErr)
		} else {
			e.logger.Info("removed sandbox workdir", "path", workdirToRemove)
		}
	}

	e.logger.Info("sandbox stopped", "sandbox_id", cmd.SandboxID, "container_id", payload.ContainerID)

	return e.ackSuccess(ctx, cmd.ID, map[string]interface{}{})
}

// executeRestartSandbox restarts a running container.
func (e *CommandExecutor) executeRestartSandbox(ctx context.Context, cmd controlclient.Command) error {
	var payload restartSandboxPayload
	if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
		return e.ackFailure(ctx, cmd.ID, fmt.Sprintf("invalid restart_sandbox payload: %v", err))
	}

	if err := e.docker.RestartContainer(ctx, payload.ContainerID, 10*time.Second); err != nil {
		return e.ackFailure(ctx, cmd.ID, fmt.Sprintf("restart container failed: %v", err))
	}

	e.logger.Info("sandbox restarted", "sandbox_id", cmd.SandboxID, "container_id", payload.ContainerID)

	return e.ackSuccess(ctx, cmd.ID, map[string]interface{}{})
}

// executeGetLogs retrieves container logs and returns them in the ack.
func (e *CommandExecutor) executeGetLogs(ctx context.Context, cmd controlclient.Command) error {
	var payload getLogsPayload
	if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
		return e.ackFailure(ctx, cmd.ID, fmt.Sprintf("invalid get_logs payload: %v", err))
	}

	reader, err := e.docker.GetLogs(ctx, payload.ContainerID, payload.Tail, payload.Follow)
	if err != nil {
		return e.ackFailure(ctx, cmd.ID, fmt.Sprintf("get logs failed: %v", err))
	}
	defer reader.Close()

	logs, err := io.ReadAll(reader)
	if err != nil {
		return e.ackFailure(ctx, cmd.ID, fmt.Sprintf("read logs failed: %v", err))
	}

	return e.ackSuccess(ctx, cmd.ID, map[string]interface{}{
		"logs": string(logs),
	})
}

// executeExec runs a one-shot shell command inside a tracked sandbox
// container via the Docker exec API and returns the captured streams
// and exit code in the ack. Defaults: cwd=/app, timeout=120s (capped
// at 300s in docker.ExecRun). The command is invoked as `sh -c <command>`
// so the control plane can send shell snippets directly.
func (e *CommandExecutor) executeExec(ctx context.Context, cmd controlclient.Command) error {
	var payload execPayload
	if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
		return e.ackFailure(ctx, cmd.ID, fmt.Sprintf("invalid exec payload: %v", err))
	}
	if payload.Command == "" {
		return e.ackFailure(ctx, cmd.ID, "empty command")
	}
	if payload.Cwd == "" {
		payload.Cwd = "/app"
	}
	if payload.TimeoutSeconds <= 0 {
		payload.TimeoutSeconds = 120
	}
	if payload.TimeoutSeconds > 300 {
		payload.TimeoutSeconds = 300
	}

	// Resolve the sandbox to a container ID. The agent only execs into
	// sandboxes it currently tracks (no cross-node, no stale rows).
	e.mu.RLock()
	info, ok := e.sandboxes[cmd.SandboxID]
	e.mu.RUnlock()
	if !ok {
		return e.ackFailure(ctx, cmd.ID, fmt.Sprintf("sandbox %s not running on this node", cmd.SandboxID))
	}

	cmdArgs := []string{"sh", "-c", payload.Command}

	envSlice := make([]string, 0, len(payload.Env))
	for k, v := range payload.Env {
		envSlice = append(envSlice, k+"="+v)
	}

	result, err := e.docker.ExecRun(ctx, info.ContainerID, docker.ExecSpec{
		Cmd:            cmdArgs,
		Env:            envSlice,
		WorkingDir:     payload.Cwd,
		TimeoutSeconds: payload.TimeoutSeconds,
	})
	if err != nil {
		return e.ackFailure(ctx, cmd.ID, fmt.Sprintf("exec error: %v", err))
	}

	return e.ackSuccess(ctx, cmd.ID, map[string]interface{}{
		"exit_code":        result.ExitCode,
		"stdout":           string(result.Stdout),
		"stderr":           string(result.Stderr),
		"stdout_truncated": result.StdoutTruncated,
		"stderr_truncated": result.StderrTruncated,
		"duration_ms":      result.DurationMs,
	})
}

// executePrune is a placeholder for Docker container/image pruning.
func (e *CommandExecutor) executePrune(ctx context.Context, cmd controlclient.Command) error {
	e.logger.Info("prune command received (v1: no-op)")

	return e.ackSuccess(ctx, cmd.ID, map[string]interface{}{})
}

// ── Ack Helpers ─────────────────────────────────────────────────────────

// ackSuccess sends a success acknowledgment for a command.
func (e *CommandExecutor) ackSuccess(ctx context.Context, cmdID string, result interface{}) error {
	return e.ctrlClient.AckCommand(ctx, cmdID, controlclient.AckRequest{
		Status: "success",
		Result: result,
	})
}

// ackFailure sends a failure acknowledgment for a command.
func (e *CommandExecutor) ackFailure(ctx context.Context, cmdID string, errMsg string) error {
	e.logger.Warn("command failed", "cmd_id", cmdID, "error", errMsg)
	return e.ctrlClient.AckCommand(ctx, cmdID, controlclient.AckRequest{
		Status: "failure",
		Error:  errMsg,
	})
}

// ── Sandbox Lookup ──────────────────────────────────────────────────────

// CodeDir returns the code directory path for a sandbox.
// This implements the filepush.SandboxResolver interface.
func (e *CommandExecutor) CodeDir(sandboxID string) (string, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	info, ok := e.sandboxes[sandboxID]
	if !ok {
		return "", fmt.Errorf("sandbox %s not found on this node", sandboxID)
	}

	return fmt.Sprintf("%s/%s/code", e.sandboxDir, info.AppName), nil
}
