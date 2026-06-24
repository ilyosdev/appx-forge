// Package docker wraps the Docker Engine SDK with an interface-based design
// for testability. The Client interface enables mock-based testing in all
// packages that need to interact with Docker containers.
package docker

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	dockerclient "github.com/moby/moby/client"

	"github.com/appx/forge/shared-go/models"
)

// cpuBurstNanos is the CPU cap a container is raised to during a CPUBurst exec.
// 4 cores (4e9 NanoCPUs) is a safe, predictable ceiling on the 8-core Server-2
// fleet: it leaves headroom for the host + other sandboxes while cutting a cold
// web export's transform time well below the 0.5-core baseline. The cap is
// restored to the container's original value as soon as the exec returns.
const cpuBurstNanos int64 = 4e9

// cpuUpdateTimeout bounds each ContainerUpdate call (burst + restore) so a slow
// daemon can't stall the exec path. Burst failure is non-fatal (exec runs at the
// current cap); restore failure leaves the container bursted until its next
// start_sandbox re-applies the spec cap — both are safe.
const cpuUpdateTimeout = 5 * time.Second

// Client defines the interface for Docker container operations.
// All methods are context-aware and return errors on failure.
type Client interface {
	// CreateContainer creates and starts a sandbox container with the given spec.
	// Returns the Docker container ID on success.
	CreateContainer(ctx context.Context, spec *SandboxSpec) (string, error)

	// StartContainer starts a previously created container.
	StartContainer(ctx context.Context, containerID string) error

	// StopContainer gracefully stops a container with the given timeout.
	StopContainer(ctx context.Context, containerID string, timeout time.Duration) error

	// RemoveContainer removes a container. It does not stop it first.
	RemoveContainer(ctx context.Context, containerID string) error

	// RestartContainer restarts a running container with the given timeout.
	RestartContainer(ctx context.Context, containerID string, timeout time.Duration) error

	// InspectContainer returns information about a container.
	InspectContainer(ctx context.Context, containerID string) (*ContainerInfo, error)

	// GetLogs returns a reader for container logs.
	// tail specifies the number of lines from the end; 0 means all.
	// follow=true streams new log output.
	GetLogs(ctx context.Context, containerID string, tail int, follow bool) (io.ReadCloser, error)

	// PullImage pulls a Docker image from a registry.
	PullImage(ctx context.Context, imageRef string) error

	// EventsStream returns channels for Docker container events and errors.
	// Events are filtered to container-type events starting from the given time.
	EventsStream(ctx context.Context, since time.Time) (<-chan ContainerEvent, <-chan error)

	// ListContainers returns all Forge-managed containers (running + stopped)
	// as ContainerSnapshot entries. Phase 30 — used by Snapshotter to feed
	// the heartbeat protocol's container list.
	ListContainers(ctx context.Context) ([]ContainerSnapshot, error)

	// ExecRun runs a one-shot command inside the given container and
	// returns the captured stdout/stderr (each truncated at a fixed
	// budget) and exit code. Used by the `exec` command type so the
	// control plane can run sandboxed shell snippets on behalf of users.
	ExecRun(ctx context.Context, containerID string, spec ExecSpec) (*ExecResult, error)

	// Close releases the underlying Docker client resources.
	Close() error
}

// SandboxSpec defines the parameters for creating a sandbox container.
type SandboxSpec struct {
	// SandboxID is the unique identifier assigned by the control plane.
	SandboxID string

	// AppName is the application name, used for container naming and bind mount paths.
	AppName string

	// Image is the Docker image to run (e.g., "appx/sandbox:v1").
	Image string

	// HostPort is the host port to bind the container's internal port 8081 to.
	HostPort int

	// CPUCores is the CPU limit in cores (e.g., 0.5 = half a core).
	CPUCores float64

	// MemoryMB is the memory limit in megabytes.
	MemoryMB int64

	// Env is a map of environment variables to set in the container.
	Env map[string]string

	// SandboxDir is the base directory for sandbox bind mounts
	// (e.g., "/var/lib/forge/sandboxes").
	SandboxDir string

	// SeccompPath is the path to the seccomp profile JSON file
	// (e.g., "/etc/forge/seccomp-default.json").
	SeccompPath string
}

// ContainerInfo holds information about a running or stopped container.
type ContainerInfo struct {
	ID        string
	Name      string
	State     string // "running", "exited", "created", etc.
	Running   bool
	ExitCode  int
	StartedAt time.Time
	// HostPort is the host side of the 8081/tcp binding, extracted from
	// HostConfig.PortBindings — present even for STOPPED containers, unlike
	// the ContainerList Ports field (which is empty when nothing is bound).
	// 0 when no binding is configured. Sleep-not-destroy (2026-06-11): the
	// wake fast-path needs the kept container's port to ack a routable
	// upstream.
	HostPort int
}

// ContainerEvent represents a Docker container lifecycle event.
type ContainerEvent struct {
	ContainerID   string
	ContainerName string
	Action        string // "die", "oom", "start", "health_status"
	ExitCode      string
	Time          time.Time
}

// ── Docker Client Implementation ─────────────────────────────────────────

// rawDockerClient is the subset of the Docker SDK client used by dockerClient.
// It enables mock injection for testing container creation parameters without
// a running Docker daemon.
type rawDockerClient interface {
	ContainerCreate(ctx context.Context, opts dockerclient.ContainerCreateOptions) (dockerclient.ContainerCreateResult, error)
	ContainerStart(ctx context.Context, containerID string, opts dockerclient.ContainerStartOptions) (dockerclient.ContainerStartResult, error)
	ContainerStop(ctx context.Context, containerID string, opts dockerclient.ContainerStopOptions) (dockerclient.ContainerStopResult, error)
	ContainerRemove(ctx context.Context, containerID string, opts dockerclient.ContainerRemoveOptions) (dockerclient.ContainerRemoveResult, error)
	ContainerRestart(ctx context.Context, containerID string, opts dockerclient.ContainerRestartOptions) (dockerclient.ContainerRestartResult, error)
	ContainerInspect(ctx context.Context, containerID string, opts dockerclient.ContainerInspectOptions) (dockerclient.ContainerInspectResult, error)
	ContainerLogs(ctx context.Context, containerID string, opts dockerclient.ContainerLogsOptions) (dockerclient.ContainerLogsResult, error)
	ImagePull(ctx context.Context, ref string, opts dockerclient.ImagePullOptions) (dockerclient.ImagePullResponse, error)
	ContainerList(ctx context.Context, opts dockerclient.ContainerListOptions) (dockerclient.ContainerListResult, error)
	Events(ctx context.Context, opts dockerclient.EventsListOptions) dockerclient.EventsResult
	Close() error
}

// dockerClient wraps the Docker Engine SDK client.
// The rawClient field enables mock injection for testing.
type dockerClient struct {
	cli       *dockerclient.Client // nil when using rawClient directly
	rawClient rawDockerClient
	logger    *slog.Logger // optional; used by the CPU-burst exec path. Nil-safe.
}

// raw returns the underlying Docker SDK client, preferring rawClient if set.
func (d *dockerClient) raw() rawDockerClient {
	if d.rawClient != nil {
		return d.rawClient
	}
	return d.cli
}

// NewDockerClient creates a new Docker client using environment defaults.
// It negotiates the API version with the Docker daemon automatically.
//
// logger is optional (may be nil) and is used only by the CPU-burst exec path
// to record non-fatal burst/restore failures.
func NewDockerClient(logger *slog.Logger) (*dockerClient, error) {
	cli, err := dockerclient.NewClientWithOpts(
		dockerclient.FromEnv,
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("docker: create client: %w", err)
	}
	return &dockerClient{cli: cli, rawClient: cli, logger: logger}, nil
}

// StartContainer starts a previously created container.
//
// Defense-in-depth for the per-build CPU burst: if a burst's restore failed
// (a ContainerUpdate hiccup, or the agent was SIGKILL'd mid-export), the
// container is left pinned at cpuBurstNanos and Docker persists that HostConfig
// across stop→start. On every wake we re-assert the tracked original cap, so a
// leaked burst can never outlive a sleep→wake cycle (the realistic path: a
// bursted pool container goes idle, sleeps, and wakes). Best-effort + nil-safe;
// a failure here only means the (rare) leak persists until the next wake and
// never blocks the start. Untracked containers (e.g. after an agent restart
// that emptied the in-memory tracker) are skipped — those were never bursted by
// this process, so they sit at their create-time cap already.
func (d *dockerClient) StartContainer(ctx context.Context, containerID string) error {
	_, err := d.raw().ContainerStart(ctx, containerID, dockerclient.ContainerStartOptions{})
	if err != nil {
		return fmt.Errorf("docker: start container %s: %w", containerID, err)
	}
	if d.cli != nil {
		if original, ok := getOriginalCPUCap(containerID); ok {
			upCtx, cancel := context.WithTimeout(context.Background(), cpuUpdateTimeout)
			if _, uerr := d.cli.ContainerUpdate(upCtx, containerID, dockerclient.ContainerUpdateOptions{
				Resources: &container.Resources{NanoCPUs: original},
			}); uerr != nil {
				d.logBurst("cpu-burst: failed to re-assert cap on start",
					"container_id", containerID, "error", uerr)
			}
			cancel()
		}
	}
	return nil
}

// StopContainer gracefully stops a container with the given timeout.
func (d *dockerClient) StopContainer(ctx context.Context, containerID string, timeout time.Duration) error {
	timeoutSec := int(timeout.Seconds())
	_, err := d.raw().ContainerStop(ctx, containerID, dockerclient.ContainerStopOptions{
		Timeout: &timeoutSec,
	})
	if err != nil {
		return fmt.Errorf("docker: stop container %s: %w", containerID, err)
	}
	return nil
}

// RemoveContainer removes a container. Force=true to remove running containers.
func (d *dockerClient) RemoveContainer(ctx context.Context, containerID string) error {
	_, err := d.raw().ContainerRemove(ctx, containerID, dockerclient.ContainerRemoveOptions{
		Force: true,
	})
	if err != nil {
		return fmt.Errorf("docker: remove container %s: %w", containerID, err)
	}
	// Drop the CPU-burst cap entry so the tracker can't leak across churn.
	clearCPUCap(containerID)
	return nil
}

// RestartContainer restarts a running container with the given timeout.
func (d *dockerClient) RestartContainer(ctx context.Context, containerID string, timeout time.Duration) error {
	timeoutSec := int(timeout.Seconds())
	_, err := d.raw().ContainerRestart(ctx, containerID, dockerclient.ContainerRestartOptions{
		Timeout: &timeoutSec,
	})
	if err != nil {
		return fmt.Errorf("docker: restart container %s: %w", containerID, err)
	}
	return nil
}

// InspectContainer returns information about a container.
func (d *dockerClient) InspectContainer(ctx context.Context, containerID string) (*ContainerInfo, error) {
	result, err := d.raw().ContainerInspect(ctx, containerID, dockerclient.ContainerInspectOptions{})
	if err != nil {
		return nil, fmt.Errorf("docker: inspect container %s: %w", containerID, err)
	}

	info := &ContainerInfo{
		ID:   result.Container.ID,
		Name: result.Container.Name,
	}

	if result.Container.State != nil {
		info.State = string(result.Container.State.Status)
		info.Running = result.Container.State.Running
		info.ExitCode = result.Container.State.ExitCode
		if t, err := time.Parse(time.RFC3339Nano, result.Container.State.StartedAt); err == nil {
			info.StartedAt = t
		}
	}

	// Host port from the configured binding (survives docker stop, unlike
	// the ContainerList Ports field). Sleep-not-destroy (2026-06-11).
	if result.Container.HostConfig != nil {
		for port, bindings := range result.Container.HostConfig.PortBindings {
			if port.String() != "8081/tcp" {
				continue
			}
			for _, b := range bindings {
				if p, perr := strconv.Atoi(b.HostPort); perr == nil && p > 0 {
					info.HostPort = p
					break
				}
			}
		}
	}

	return info, nil
}

// GetLogs returns a reader for container logs.
func (d *dockerClient) GetLogs(ctx context.Context, containerID string, tail int, follow bool) (io.ReadCloser, error) {
	tailStr := "all"
	if tail > 0 {
		tailStr = strconv.Itoa(tail)
	}

	result, err := d.raw().ContainerLogs(ctx, containerID, dockerclient.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     follow,
		Tail:       tailStr,
	})
	if err != nil {
		return nil, fmt.Errorf("docker: get logs for container %s: %w", containerID, err)
	}
	return result, nil
}

// PullImage pulls a Docker image from a registry.
func (d *dockerClient) PullImage(ctx context.Context, imageRef string) error {
	resp, err := d.raw().ImagePull(ctx, imageRef, dockerclient.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("docker: pull image %s: %w", imageRef, err)
	}
	defer resp.Close()

	// Drain the response body to complete the pull.
	if _, err := io.Copy(io.Discard, resp); err != nil {
		return fmt.Errorf("docker: pull image %s: read response: %w", imageRef, err)
	}
	return nil
}

// EventsStream returns channels for Docker container events and errors.
func (d *dockerClient) EventsStream(ctx context.Context, since time.Time) (<-chan ContainerEvent, <-chan error) {
	eventCh := make(chan ContainerEvent)
	errCh := make(chan error, 1)

	result := d.raw().Events(ctx, dockerclient.EventsListOptions{
		Since: since.Format(time.RFC3339Nano),
		Filters: dockerclient.Filters{
			"type":  {"container": true},
			"event": {"die": true, "oom": true, "start": true, "health_status": true},
		},
	})

	go func() {
		defer close(eventCh)
		defer close(errCh)

		for msg := range result.Messages {
			event := ContainerEvent{
				ContainerID:   msg.Actor.ID,
				ContainerName: msg.Actor.Attributes["name"],
				Action:        string(msg.Action),
				ExitCode:      msg.Actor.Attributes["exitCode"],
				Time:          time.Unix(msg.Time, msg.TimeNano%int64(time.Second)),
			}
			select {
			case eventCh <- event:
			case <-ctx.Done():
				return
			}
		}

		// Check for errors after messages channel is drained
		for err := range result.Err {
			select {
			case errCh <- err:
			case <-ctx.Done():
				return
			}
		}
	}()

	return eventCh, errCh
}

// Close releases the underlying Docker client resources.
func (d *dockerClient) Close() error {
	return d.raw().Close()
}

// ExecRun delegates to the exec.go helper. It uses the moby SDK client
// directly (not d.raw()) because exec is not part of the rawClient
// mock surface — tests for exec live in exec_test.go and construct a
// fake execClient.
//
// When spec.CPUBurst is set, the container's CPU cap is raised to cpuBurstNanos
// for the duration of the exec and restored to its original value via a defer.
// The burst is entirely best-effort: a missing tracker entry, a nil SDK handle,
// or a ContainerUpdate failure all fall back to running the exec at the current
// cap. Restore is guaranteed on every exit path (return, error, panic) by defer.
func (d *dockerClient) ExecRun(ctx context.Context, containerID string, spec ExecSpec) (*ExecResult, error) {
	if spec.CPUBurst && d.cli != nil {
		if restore, ok := d.applyCPUBurst(containerID); ok {
			defer restore()
		}
	}
	return ExecRun(ctx, d.cli, containerID, spec)
}

// applyCPUBurst raises containerID's CPU cap to cpuBurstNanos and returns a
// restore closure plus whether the burst was applied. ok == false means no
// burst happened (no tracked cap, or the update failed) and the caller must NOT
// defer the returned closure. Errors are logged (nil-safe) and never surfaced —
// the exec must proceed regardless.
func (d *dockerClient) applyCPUBurst(containerID string) (restore func(), ok bool) {
	originalNanos, tracked := getOriginalCPUCap(containerID)
	if !tracked {
		d.logBurst("cpu-burst: container not tracked, skipping burst", "container_id", containerID)
		return nil, false
	}
	if originalNanos == cpuBurstNanos {
		// Already at (or above) the burst cap — nothing to do, nothing to restore.
		return nil, false
	}

	burstCtx, cancel := context.WithTimeout(context.Background(), cpuUpdateTimeout)
	_, err := d.cli.ContainerUpdate(burstCtx, containerID, dockerclient.ContainerUpdateOptions{
		Resources: &container.Resources{NanoCPUs: cpuBurstNanos},
	})
	cancel()
	if err != nil {
		d.logBurst("cpu-burst: failed to apply burst, running at current cap",
			"container_id", containerID, "error", err)
		return nil, false
	}

	return func() {
		restoreCtx, cancelRestore := context.WithTimeout(context.Background(), cpuUpdateTimeout)
		_, restoreErr := d.cli.ContainerUpdate(restoreCtx, containerID, dockerclient.ContainerUpdateOptions{
			Resources: &container.Resources{NanoCPUs: originalNanos},
		})
		cancelRestore()
		if restoreErr != nil {
			d.logBurst("cpu-burst: failed to restore original cap (will reset on next start_sandbox)",
				"container_id", containerID, "original_nanos", originalNanos, "error", restoreErr)
		}
	}, true
}

// logBurst logs a CPU-burst diagnostic at warn level, tolerating a nil logger.
func (d *dockerClient) logBurst(msg string, args ...any) {
	if d.logger != nil {
		d.logger.Warn(msg, args...)
	}
}

// ListContainers returns all Forge-managed containers (running + stopped)
// visible to the Docker daemon, mapped to ContainerSnapshot. Used by
// Snapshotter for the heartbeat container list (Phase 30) and by the
// agent's startup cache rebuild.
//
// Filters: only containers with the `forge.app_name` label are included
// — the agent doesn't manage non-Forge containers and shouldn't report
// them to the control plane.
//
// Not added to the Client interface to avoid forcing existing mocks
// (mockClient in client_test.go, and others) to implement it; consumers
// that need this method receive *dockerClient directly via Snapshotter's
// DockerLister interface (structural typing).
func (d *dockerClient) ListContainers(ctx context.Context) ([]ContainerSnapshot, error) {
	result, err := d.raw().ContainerList(ctx, dockerclient.ContainerListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("docker: list containers: %w", err)
	}

	snapshots := make([]ContainerSnapshot, 0, len(result.Items))
	for _, item := range result.Items {
		appName := item.Labels["forge.app_name"]
		if appName == "" {
			continue // skip non-Forge containers
		}

		hostPort := 0
		for _, p := range item.Ports {
			if p.PublicPort != 0 {
				hostPort = int(p.PublicPort)
				break
			}
		}

		// Phase 33-Real-9 — translate Docker primitive (`running` |
		// `paused` | `restarting` | `exited` | `dead` | `created`) into
		// the canonical SandboxState vocabulary at the boundary, so the
		// control plane never sees Docker leakage. Snapshots whose state
		// has no Forge equivalent (e.g. `paused`) are dropped — the
		// agent should never report them.
		canonical, ok := models.FromDockerState(string(item.State))
		if !ok {
			continue
		}
		snapshots = append(snapshots, ContainerSnapshot{
			AppName:     appName,
			State:       string(canonical),
			HostPort:    hostPort,
			ContainerID: item.ID,
			SandboxID:   item.Labels["forge.sandbox_id"],
		})
	}
	return snapshots, nil
}

// ── Container Port Constants ─────────────────────────────────────────────

// ContainerPort is the port exposed inside the sandbox container (Metro/Expo).
const ContainerPort = "8081/tcp"

// containerPortParsed is the pre-parsed network.Port for container port binding.
var containerPortParsed = network.MustParsePort(ContainerPort)

// CreateContainer is implemented in sandbox.go to keep this file focused
// on the interface and general Docker operations.
// See sandbox.go for the full container creation logic with security settings.
