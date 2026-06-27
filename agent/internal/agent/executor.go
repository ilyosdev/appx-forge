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

	// Debounce timers for structural-push restarts, keyed by sandboxID. A real
	// AI gen pushes in CLUSTERS (provision sync, per-phase sync, heal sync), so a
	// restart-per-structural-push would cycle the container several times in
	// seconds — thrashing Metro into back-to-back cold rebundles and racing the
	// control plane's own start_sandbox acks. RestartSandbox instead (re)arms a
	// short timer per sandbox; only the last push in a burst actually restarts.
	restartTimers map[string]*time.Timer
	restartMu     sync.Mutex

	// buildDirs maps a build ID to its on-disk snapshot CODE directory
	// (.../builds/<buildID>/code) for the isolated cold export. The dist-out
	// handler resolves CodeDir(buildID) through this map so the build-scoped
	// dist fetch streams the snapshot's dist/ rather than the live sandbox's.
	// Process-local: an agent restart loses it (the dist fetch then 404s,
	// which the backend treats as a failed/superseded build) and the build
	// reaper sweeps the leaked snapshot dir + worker container.
	buildDirs map[string]string
	buildMu   sync.RWMutex
}

// restartDebounce is how long a sandbox must go without a new structural push
// before its coalesced Metro restart fires. Long enough to swallow a push burst
// (the observed clusters span ~2s), short enough that the preview's fresh crawl
// is not meaningfully delayed. A var (not const) so tests can shorten it.
var restartDebounce = 4 * time.Second

// NewCommandExecutor creates a CommandExecutor with the given dependencies.
func NewCommandExecutor(
	dockerClient docker.Client,
	portAlloc *ports.Allocator,
	ctrlClient ackReporter,
	sandboxDir string,
	logger *slog.Logger,
) *CommandExecutor {
	return &CommandExecutor{
		docker:        dockerClient,
		ports:         portAlloc,
		ctrlClient:    ctrlClient,
		sandboxDir:    sandboxDir,
		logger:        logger,
		sandboxes:     make(map[string]*sandboxInfo),
		restartTimers: make(map[string]*time.Timer),
		buildDirs:     make(map[string]string),
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
	// Mode selects what "stop" means (sleep-not-destroy, 2026-06-11):
	//   ""        — destroy (docker stop + rm + workdir RemoveAll + port
	//               release). The default, so a control plane that predates
	//               this field keeps today's behavior unchanged.
	//   "stop"    — sleep: docker stop ONLY. Container, workdir, port
	//               reservation and the in-memory entry are all KEPT so a
	//               later start_sandbox can docker-start the same container
	//               (wake ≈ sub-second; the built bundle survives on the
	//               container fs).
	//   "destroy" — explicit destroy (same as "").
	Mode string `json:"mode,omitempty"`
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
	// CPUBurst, when true, asks the agent to temporarily raise the sandbox's
	// CPU cap for the duration of this exec (then restore it). Optional;
	// omitted = false = no burst. Set by the web export to speed cold builds.
	CPUBurst bool `json:"cpu_burst,omitempty"`
	// User runs the exec as a specific user (empty = image default appuser).
	// Optional pass-through. The web export sets "root" to rewrite the
	// root-owned synced app.json before dropping to appuser via `su`.
	User string `json:"user,omitempty"`
}

// buildExportPayload is the payload for build_export commands. It mirrors
// execPayload (the export semantics — coldCommand + APPX_BASE_URL env — stay in
// the backend as the single source of truth) and adds an optional Image: the
// dev sandbox's EXACT image, so the worker is never built from the agent's
// stale default. When Image is empty the agent falls back to inspecting the
// live dev container's image.
type buildExportPayload struct {
	Command        string            `json:"command"`
	Cwd            string            `json:"cwd"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	CPUBurst       bool              `json:"cpu_burst,omitempty"`
	User           string            `json:"user,omitempty"`
	// Image is the dev sandbox's image (e.g. appx/sandbox:v19). Optional —
	// the agent inspects the live dev container when empty.
	Image string `json:"image,omitempty"`
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
	case "build_export":
		return e.executeBuildExport(ctx, cmd)
	case "prune":
		return e.executePrune(ctx, cmd)
	default:
		return e.ackFailure(ctx, cmd.ID, fmt.Sprintf("unknown command type: %s", cmd.Type))
	}
}

// ── Command Handlers ────────────────────────────────────────────────────

// executeStartSandbox starts a sandbox. Wake path (sleep-not-destroy,
// 2026-06-11): if a stopped container for this app already exists,
// docker-start it (sub-second — the built bundle survives on the container
// fs) instead of remove-and-recreate. Falls through to the create path on
// any reuse failure, so the cold path remains the safety net.
func (e *CommandExecutor) executeStartSandbox(ctx context.Context, cmd controlclient.Command) error {
	var payload startSandboxPayload
	if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
		return e.ackFailure(ctx, cmd.ID, fmt.Sprintf("invalid start_sandbox payload: %v", err))
	}

	// Wake fast-path: reuse an existing container for this app when present.
	if reused, containerID, hostPort := e.tryStartExisting(ctx, cmd.SandboxID, payload.AppName); reused {
		return e.ackSuccess(ctx, cmd.ID, map[string]interface{}{
			"container_id": containerID,
			"host_port":    hostPort,
		})
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

// tryStartExisting docker-starts a kept (slept) container for appName when
// one exists. Returns (true, containerID, hostPort) on success; (false, …)
// when there is no reusable container or the start failed — callers fall
// through to the remove-and-recreate cold path (CreateContainer already
// force-removes a same-name leftover, so a half-dead container can't block
// the wake). Best-effort port re-reservation: the port may already be
// reserved (slept in this process / boot adoption) — that's fine.
func (e *CommandExecutor) tryStartExisting(
	ctx context.Context,
	sandboxID string,
	appName string,
) (bool, string, int) {
	if appName == "" {
		return false, "", 0
	}
	snaps, err := e.docker.ListContainers(ctx)
	if err != nil {
		e.logger.Warn("wake reuse: ListContainers failed — falling back to create",
			"app_name", appName, "error", err)
		return false, "", 0
	}
	for _, snap := range snaps {
		if snap.AppName != appName {
			continue
		}
		switch snap.State {
		case "running":
			// Idempotent: already up (e.g. duplicate start command).
			e.adoptEntry(sandboxID, snap)
			e.logger.Info("wake reuse: container already running",
				"sandbox_id", sandboxID, "app_name", appName,
				"container_id", snap.ContainerID, "host_port", snap.HostPort)
			return true, snap.ContainerID, snap.HostPort
		case "stopped":
			// ContainerList reports NO ports for stopped containers — the
			// binding only shows in the list while bound. Recover the
			// configured port from HostConfig.PortBindings via inspect; a
			// port-less ack would make control write a dead ip:0 route.
			if snap.HostPort == 0 {
				if info, ierr := e.docker.InspectContainer(ctx, snap.ContainerID); ierr == nil && info.HostPort > 0 {
					snap.HostPort = info.HostPort
				}
			}
			if snap.HostPort == 0 {
				e.logger.Warn("wake reuse: no host port resolvable — falling back to create",
					"app_name", appName, "container_id", snap.ContainerID)
				return false, "", 0
			}
			if err := e.docker.StartContainer(ctx, snap.ContainerID); err != nil {
				e.logger.Warn("wake reuse: docker start failed — falling back to create",
					"app_name", appName, "container_id", snap.ContainerID, "error", err)
				return false, "", 0
			}
			e.adoptEntry(sandboxID, snap)
			e.logger.Info("sandbox woken (existing container started)",
				"sandbox_id", sandboxID, "app_name", appName,
				"container_id", snap.ContainerID, "host_port", snap.HostPort)
			return true, snap.ContainerID, snap.HostPort
		default:
			// starting/restarting/failed — let the cold path sort it out.
			return false, "", 0
		}
	}
	return false, "", 0
}

// adoptEntry records a reused container in the in-memory map and re-reserves
// its host port (idempotent — AllocateSpecific on an already-reserved port
// just errors, which we ignore).
func (e *CommandExecutor) adoptEntry(sandboxID string, snap docker.ContainerSnapshot) {
	if snap.HostPort > 0 {
		_ = e.ports.AllocateSpecific(snap.HostPort)
	}
	e.mu.Lock()
	e.sandboxes[sandboxID] = &sandboxInfo{
		ContainerID: snap.ContainerID,
		HostPort:    snap.HostPort,
		AppName:     snap.AppName,
	}
	e.mu.Unlock()
}

// AdoptBootSnapshot re-reserves the host ports of every container Docker
// already knows about (running AND stopped). Called once at agent startup:
// the port allocator is in-memory only, so without this a restarted agent
// could hand a slept container's port to a new sandbox and the eventual
// docker-start would fail with "address already in use". (The in-memory
// sandbox map itself is rebuilt lazily — the wake fast-path resolves
// containers by app name from Docker truth, not from the map.)
func (e *CommandExecutor) AdoptBootSnapshot(snaps []docker.ContainerSnapshot) {
	reserved := 0
	adopted := 0
	for _, snap := range snaps {
		if snap.HostPort > 0 {
			if err := e.ports.AllocateSpecific(snap.HostPort); err == nil {
				reserved++
			}
		}
		// Containers labeled forge.sandbox_id rebuild the in-memory sandbox
		// map directly — without this, every push/exec against a sandbox
		// created before the restart 404s until the container cycles.
		if snap.SandboxID != "" && snap.AppName != "" {
			e.adoptEntry(snap.SandboxID, snap)
			adopted++
		}
	}
	if reserved > 0 || adopted > 0 {
		e.logger.Info("boot adoption: rebuilt state from docker truth",
			"reserved_ports", reserved, "adopted_sandboxes", adopted,
			"total_containers", len(snaps))
	}
}

// executeStopSandbox stops a container. mode="stop" sleeps it (docker stop
// only — container/workdir/port/in-memory entry kept for a sub-second
// docker-start wake); any other mode destroys it (stop + rm + workdir
// RemoveAll + port release), which is the pre-2026-06-11 behavior and the
// default for payloads that don't carry the field.
func (e *CommandExecutor) executeStopSandbox(ctx context.Context, cmd controlclient.Command) error {
	var payload stopSandboxPayload
	if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
		return e.ackFailure(ctx, cmd.ID, fmt.Sprintf("invalid stop_sandbox payload: %v", err))
	}

	// Stop container with 10s timeout
	if err := e.docker.StopContainer(ctx, payload.ContainerID, 10*time.Second); err != nil {
		return e.ackFailure(ctx, cmd.ID, fmt.Sprintf("stop container failed: %v", err))
	}

	// Sleep-not-destroy: keep everything for a fast docker-start wake.
	if payload.Mode == "stop" {
		e.logger.Info("sandbox slept (container kept for wake)",
			"sandbox_id", cmd.SandboxID,
			"container_id", payload.ContainerID,
		)
		return e.ackSuccess(ctx, cmd.ID, map[string]interface{}{
			"slept": true,
		})
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
	info, ok := e.resolveSandbox(cmd.SandboxID)
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
		CPUBurst:       payload.CPUBurst,
		User:           payload.User,
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

// ── Build Export (isolated cold web export) ──────────────────────────────

// buildWorkerMemoryMB / buildWorkerCPUCores are the build worker's own cgroup
// (proven sufficient: an export-alone v19 box peaks ~303MiB; 2GiB is ample).
// CPUBurst on the export exec lifts CPU to 4 cores for the export's duration.
const (
	buildWorkerMemoryMB int64   = 2048
	buildWorkerCPUCores float64 = 2.0
)

// executeBuildExport runs a cold `expo export` in a SEPARATE ephemeral
// build-worker container against a SNAPSHOT of the project code, so the
// exporter never shares a cgroup with the running dev Metro (the dual-Metro
// OOM fix). Flow: resolve dev sandbox -> resolve image -> snapshot code ->
// create worker -> ExecRun export -> ack with build_id; the backend then
// fetches the snapshot's dist/ via the build-scoped dist endpoint.
//
// Cleanup is defer-guaranteed on every exit path: the worker container is
// ALWAYS force-removed; the snapshot dir is removed on any failure and RETAINED
// on success (it holds dist/ for the fetch) where the build reaper is the
// backstop that bounds disk if the backend never fetches.
func (e *CommandExecutor) executeBuildExport(ctx context.Context, cmd controlclient.Command) error {
	var payload buildExportPayload
	if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
		return e.ackFailure(ctx, cmd.ID, fmt.Sprintf("invalid build_export payload: %v", err))
	}
	if payload.Command == "" {
		return e.ackFailure(ctx, cmd.ID, "empty command")
	}
	if payload.Cwd == "" {
		// Match the exec default so the SAME coldCommand string behaves
		// identically to the in-container path.
		payload.Cwd = "/app"
	}
	if payload.TimeoutSeconds <= 0 {
		payload.TimeoutSeconds = 120
	}
	if payload.TimeoutSeconds > 300 {
		payload.TimeoutSeconds = 300
	}

	// Resolve the dev sandbox so we can locate its code dir and (if needed)
	// its image. The worker runs on the SAME node — the command was dispatched
	// to the dev sandbox's node.
	info, ok := e.resolveSandbox(cmd.SandboxID)
	if !ok {
		return e.ackFailure(ctx, cmd.ID, fmt.Sprintf("sandbox %s not running on this node", cmd.SandboxID))
	}

	// Resolve the image: prefer the payload (dev sandbox's exact image), fall
	// back to inspecting the live dev container. NEVER the agent default.
	image := payload.Image
	if image == "" {
		if dev, ierr := e.docker.InspectContainer(ctx, info.ContainerID); ierr == nil && dev.Image != "" {
			image = dev.Image
		}
	}
	if image == "" {
		return e.ackFailure(ctx, cmd.ID, "could not resolve dev sandbox image for build worker")
	}

	buildID := cmd.ID // reuse the command ID as the build ID (deterministic)
	srcCodeDir := fmt.Sprintf("%s/%s/code", e.sandboxDir, info.AppName)

	// Snapshot the live code dir into an isolated builds tree (outside the
	// per-app sandbox tree so a dev-sandbox destroy/GC can't wipe it).
	snapshotCode, err := docker.SnapshotCodeDir(ctx, e.buildsDir(), buildID, srcCodeDir)
	if err != nil {
		return e.ackFailure(ctx, cmd.ID, fmt.Sprintf("snapshot code dir failed: %v", err))
	}

	// success gates snapshot retention: on any error path below the deferred
	// cleanup removes the snapshot; only a clean export keeps it for the fetch.
	success := false
	defer func() {
		if !success {
			e.unregisterBuild(buildID)
			if rmErr := os.RemoveAll(filepath.Join(e.buildsDir(), buildID)); rmErr != nil {
				e.logger.Warn("build export: failed to remove snapshot after failure",
					"build_id", buildID, "error", rmErr)
			}
		}
	}()

	// Create the ephemeral worker (no dev Metro, own cgroup, snapshot bind).
	workerID, err := e.docker.CreateBuildWorker(ctx, &docker.BuildWorkerSpec{
		BuildID:  buildID,
		Image:    image,
		CodePath: snapshotCode,
		MemoryMB: buildWorkerMemoryMB,
		CPUCores: buildWorkerCPUCores,
		// SeccompPath intentionally matches the live sandbox path (empty
		// today) — build workers must not diverge from sandbox hardening.
	})
	if err != nil {
		return e.ackFailure(ctx, cmd.ID, fmt.Sprintf("create build worker failed: %v", err))
	}
	// ALWAYS tear the worker down (force remove also clears its CPU-cap entry).
	defer func() {
		rmCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if rmErr := e.docker.RemoveContainer(rmCtx, workerID); rmErr != nil {
			e.logger.Warn("build export: failed to remove build worker",
				"build_id", buildID, "container_id", workerID, "error", rmErr)
		}
	}()

	// Register the snapshot so the dist-out handler can resolve CodeDir(buildID)
	// -> snapshot code dir for the build-scoped fetch.
	e.registerBuild(buildID, snapshotCode)

	// Run the export inside the worker — the proven export-alone path.
	cmdArgs := []string{"sh", "-c", payload.Command}
	envSlice := make([]string, 0, len(payload.Env))
	for k, v := range payload.Env {
		envSlice = append(envSlice, k+"="+v)
	}

	result, err := e.docker.ExecRun(ctx, workerID, docker.ExecSpec{
		Cmd:            cmdArgs,
		Env:            envSlice,
		WorkingDir:     payload.Cwd,
		TimeoutSeconds: payload.TimeoutSeconds,
		CPUBurst:       payload.CPUBurst,
		User:           payload.User,
	})
	if err != nil {
		return e.ackFailure(ctx, cmd.ID, fmt.Sprintf("build export exec error: %v", err))
	}

	// Measure the produced artifact. For build_export, success MUST mean
	// "artifact produced" — not merely "the exporter process returned". ExecRun
	// returns err==nil for a timed-out export (ExitCode=-1) and for a non-zero /
	// OOM-killed export (ExitCode=137, etc.), so neither the err==nil check above
	// nor a plain ackSuccess is sufficient. A dist-size read failure (missing
	// dist/) is therefore treated as a zero-byte artifact, i.e. a failed export.
	var distBytes int64
	if size, derr := dirSize(filepath.Join(snapshotCode, "dist")); derr == nil {
		distBytes = size
	}

	// Only a clean exit (0) that actually wrote a non-empty dist/ counts as a
	// successful build. Anything else acks failure with the exit code so the
	// deferred cleanup drops the useless/partial snapshot and the backend gets
	// an unambiguous failure rather than fetching an empty/partial tar.
	if result.ExitCode != 0 || distBytes == 0 {
		var reason string
		switch {
		case result.ExitCode == -1:
			reason = fmt.Sprintf("export timed out after %ds", payload.TimeoutSeconds)
		case result.ExitCode == 137:
			reason = "export OOM-killed (exit 137)"
		case result.ExitCode != 0:
			reason = fmt.Sprintf("export exited non-zero (exit %d)", result.ExitCode)
		default:
			reason = "export produced empty dist/ (0 bytes)"
		}
		e.logger.Warn("build export failed",
			"build_id", buildID, "sandbox_id", cmd.SandboxID,
			"exit_code", result.ExitCode, "duration_ms", result.DurationMs,
			"dist_bytes", distBytes, "reason", reason,
		)
		return e.ackFailure(ctx, cmd.ID, fmt.Sprintf(
			"build export failed: %s (exit_code=%d, dist_bytes=%d)",
			reason, result.ExitCode, distBytes,
		))
	}

	// Retain the snapshot for the dist fetch; the build reaper bounds disk.
	success = true

	e.logger.Info("build export complete",
		"build_id", buildID, "sandbox_id", cmd.SandboxID,
		"exit_code", result.ExitCode, "duration_ms", result.DurationMs,
		"dist_bytes", distBytes,
	)

	return e.ackSuccess(ctx, cmd.ID, map[string]interface{}{
		"exit_code":   result.ExitCode,
		"stdout":      string(result.Stdout),
		"stderr":      string(result.Stderr),
		"duration_ms": result.DurationMs,
		"build_id":    buildID,
		"dist_bytes":  distBytes,
	})
}

// buildsDir is the isolated root for build snapshots, a SIBLING of the sandbox
// tree (e.g. sandboxDir=/var/lib/forge/sandboxes -> /var/lib/forge/builds).
// Kept outside the per-app tree so a dev-sandbox destroy / idle-reap / workdir
// GC can never delete an in-flight build's snapshot.
func (e *CommandExecutor) buildsDir() string {
	return filepath.Join(filepath.Dir(e.sandboxDir), "builds")
}

// registerBuild records a build ID -> snapshot code dir mapping.
func (e *CommandExecutor) registerBuild(buildID, codeDir string) {
	e.buildMu.Lock()
	e.buildDirs[buildID] = codeDir
	e.buildMu.Unlock()
}

// unregisterBuild drops a build ID from the resolver map.
func (e *CommandExecutor) unregisterBuild(buildID string) {
	e.buildMu.Lock()
	delete(e.buildDirs, buildID)
	e.buildMu.Unlock()
}

// ReapBuilds is the backstop that bounds build-snapshot disk + leaked worker
// containers (covers an agent crash/restart mid-build, or a backend that never
// fetched the dist). It force-removes build-worker containers older than
// retention and removes snapshot dirs older than retention. Safe against
// in-flight builds: a cold export + fetch completes in well under retention.
func (e *CommandExecutor) ReapBuilds(ctx context.Context, retention time.Duration) {
	cutoff := time.Now().Add(-retention)

	// 1. Leaked worker containers (invisible to the heartbeat reconciler).
	workers, err := e.docker.ListBuildWorkers(ctx)
	if err != nil {
		e.logger.Warn("build reaper: list build workers failed", "error", err)
	} else {
		for _, wkr := range workers {
			if wkr.CreatedUnix > 0 && time.Unix(wkr.CreatedUnix, 0).After(cutoff) {
				continue // still young — likely in-flight
			}
			rmCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if rmErr := e.docker.RemoveContainer(rmCtx, wkr.ContainerID); rmErr != nil {
				e.logger.Warn("build reaper: failed to remove leaked worker",
					"build_id", wkr.BuildID, "container_id", wkr.ContainerID, "error", rmErr)
			} else {
				e.logger.Info("build reaper: removed leaked worker",
					"build_id", wkr.BuildID, "container_id", wkr.ContainerID)
			}
			cancel()
		}
	}

	// 2. Leaked snapshot dirs.
	dir := e.buildsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			e.logger.Warn("build reaper: read builds dir failed", "dir", dir, "error", err)
		}
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		fi, ierr := entry.Info()
		if ierr != nil {
			continue
		}
		if fi.ModTime().After(cutoff) {
			continue // young — likely in-flight or awaiting fetch
		}
		buildID := entry.Name()
		e.unregisterBuild(buildID)
		path := filepath.Join(dir, buildID)
		if rmErr := os.RemoveAll(path); rmErr != nil {
			e.logger.Warn("build reaper: failed to remove snapshot", "path", path, "error", rmErr)
		} else {
			e.logger.Info("build reaper: removed stale snapshot", "build_id", buildID)
		}
	}
}

// dirSize returns the cumulative size of regular files under root. Symlinks are
// not followed (Walk uses Lstat). Used as a best-effort dist artifact gauge.
func dirSize(root string) (int64, error) {
	var total int64
	err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total, err
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

// CodeDir returns the code directory path for a sandbox OR a build snapshot.
// This implements the filepush.SandboxResolver / distout.codeDirResolver
// interfaces.
//
// Build-scoped resolution comes first: when id matches a registered build (the
// dist-out handler passes ?build=<id>), it returns that build's snapshot CODE
// dir (.../builds/<id>/code) so the build-scoped dist fetch streams the
// snapshot's dist/, never the live sandbox's. Otherwise id is treated as a
// sandbox ID and resolved to <sandboxDir>/<app>/code.
func (e *CommandExecutor) CodeDir(id string) (string, error) {
	e.buildMu.RLock()
	dir, isBuild := e.buildDirs[id]
	e.buildMu.RUnlock()
	if isBuild {
		return dir, nil
	}

	info, ok := e.resolveSandbox(id)
	if !ok {
		return "", fmt.Errorf("sandbox %s not found on this node", id)
	}

	return fmt.Sprintf("%s/%s/code", e.sandboxDir, info.AppName), nil
}

// ResolveContainerID resolves a sandbox ID to its container ID via the same
// map+docker-label lookup the exec/filepush paths use. Exported for the logs
// HTTP handler (logs_handler.go) so a sandbox created before the agent's last
// restart still resolves instead of 404'ing the Logs pane.
func (e *CommandExecutor) ResolveContainerID(sandboxID string) (string, bool) {
	info, ok := e.resolveSandbox(sandboxID)
	if !ok {
		return "", false
	}
	return info.ContainerID, true
}

// RestartSandbox restarts the container backing a sandbox so its Metro dev
// server re-crawls the project from scratch. Implements filepush.SandboxRestarter.
//
// Why this exists: the baked Metro config disables Watchman to save 300MB-1GB
// per container, so the running `expo start` relies on inotify. A file push that
// CREATES new paths (a fresh gen's screens/components, a newly-added module) is
// written straight to the bind mount and is NOT reliably picked up by the running
// Metro's file map — only a fresh crawl sees it, which is why the preview shows
// "Unable to resolve module …" for files that are physically on disk. The file
// push handler calls this after a STRUCTURAL push (new path or delete); a
// content-only edit keeps the cheaper mtime-nudge so HMR stays fast.
//
// DEBOUNCED + ASYNC: a real gen pushes in clusters, so this (re)arms a per-sandbox
// timer (restartDebounce) and returns immediately; the actual restart fires only
// once the pushes stop. This trades the old synchronous "stale Metro gone before
// the push returns" guarantee for not thrashing the container — acceptable because
// the backend's readiness poll plus the frontend's never-blank hold ride out the
// few-second window, whereas back-to-back restarts cold-rebundled Metro repeatedly
// and raced the control-plane acks. Always returns nil (scheduling can't fail);
// the real restart's outcome is logged inside doRestart, fail-open.
func (e *CommandExecutor) RestartSandbox(sandboxID string) error {
	e.restartMu.Lock()
	defer e.restartMu.Unlock()
	if t, ok := e.restartTimers[sandboxID]; ok {
		t.Stop() // a newer push arrived within the window — slide the deadline
	}
	e.restartTimers[sandboxID] = time.AfterFunc(restartDebounce, func() {
		e.restartMu.Lock()
		delete(e.restartTimers, sandboxID)
		e.restartMu.Unlock()
		e.doRestart(sandboxID)
	})
	return nil
}

// doRestart performs the coalesced Metro restart for a sandbox. Best-effort: any
// failure is logged but never propagated (the files are already on disk; worst
// case the preview needs a manual refresh).
//
// A plain docker restart preserves the /app/code bind mount — only the process is
// cycled, no code is lost (the entrypoint re-seeds idempotently). The 3s argument
// is the Docker STOP grace (Metro is stateless dev tooling with nothing to flush)
// — deliberately shorter than the 10s used by the control-plane restart_sandbox
// command, which has no latency-sensitive caller. The whole Docker call is bounded
// by a 30s deadline so a wedged daemon (the risk is highest exactly when this fires
// — a memory-pressured node) cannot hang a goroutine indefinitely; 30s sits well
// above the 3s grace + container start. Concurrent restarts of the same container
// (e.g. a control-plane restart_sandbox racing this) are serialized by the Docker
// daemon's per-container lock — a loser just errors and is logged. A heartbeat
// landing inside the ~3-5s window briefly reports the row 'restarting'/'stopped';
// the next heartbeat (≤15s) flips it back and no reap/sleep side-effect fires.
func (e *CommandExecutor) doRestart(sandboxID string) {
	containerID, ok := e.ResolveContainerID(sandboxID)
	if !ok {
		e.logger.Warn("debounced metro restart skipped: sandbox not found", "sandbox_id", sandboxID)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := e.docker.RestartContainer(ctx, containerID, 3*time.Second); err != nil {
		e.logger.Warn("debounced metro restart failed (preview may need a refresh)",
			"sandbox_id", sandboxID, "container_id", containerID, "error", err)
		return
	}
	e.logger.Info("sandbox restarted after structural file push (debounced)",
		"sandbox_id", sandboxID, "container_id", containerID)
}

// resolveSandbox looks a sandbox up in the in-memory map, falling back to
// Docker truth via the forge.sandbox_id label on a miss (restart-survivor:
// the map is process-local, the containers are not). Adopt-on-hit so the
// next lookup is map-fast.
func (e *CommandExecutor) resolveSandbox(sandboxID string) (*sandboxInfo, bool) {
	e.mu.RLock()
	info, ok := e.sandboxes[sandboxID]
	e.mu.RUnlock()
	if ok {
		return info, true
	}

	snaps, err := e.docker.ListContainers(context.Background())
	if err != nil {
		return nil, false
	}
	for _, snap := range snaps {
		if snap.SandboxID != sandboxID {
			continue
		}
		e.adoptEntry(sandboxID, snap)
		e.logger.Info("lazy adoption: resolved sandbox from docker label",
			"sandbox_id", sandboxID, "app_name", snap.AppName)
		e.mu.RLock()
		info = e.sandboxes[sandboxID]
		e.mu.RUnlock()
		return info, info != nil
	}
	return nil, false
}
