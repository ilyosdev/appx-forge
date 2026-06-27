package docker

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	dockerclient "github.com/moby/moby/client"
)

// buildIDLabel is the Docker label stamped on ephemeral build-worker
// containers (instead of forge.app_name). Keeping build workers out of the
// forge.app_name namespace is load-bearing: ListContainers (and therefore the
// heartbeat snapshot + orphan reconciler) keys exclusively on forge.app_name,
// so a forge.build_id-labeled container is invisible to drift detection and is
// never reaped mid-export.
const buildIDLabel = "forge.build_id"

// hmrIDLabel is the Docker label stamped on per-turn ephemeral HMR containers
// (instead of forge.app_name). Same isolation rationale as buildIDLabel:
// ListContainers (and therefore the heartbeat snapshot + orphan reconciler)
// keys exclusively on forge.app_name, so a forge.hmr_id-labeled container is
// invisible to drift detection and is never reaped mid-turn. The HMR reaper +
// stop_hmr handler reach it via ListHmrWorkers instead.
const hmrIDLabel = "forge.hmr_id"

// ── CPU Burst Cap Tracking ───────────────────────────────────────────────

// containerCPUCaps records the original NanoCPUs cap each sandbox was created
// with, keyed by Docker container ID. The per-build CPU burst (ExecSpec.CPUBurst)
// temporarily raises a container's cap and must restore the exact original value
// afterwards — this map is that source of truth. Protected by cpuCapsMu.
var (
	containerCPUCaps = make(map[string]int64)
	cpuCapsMu        sync.RWMutex
)

// storeOriginalCPUCap saves a container's original NanoCPUs cap. Called once
// from CreateContainer, before any exec can run.
func storeOriginalCPUCap(containerID string, nanos int64) {
	cpuCapsMu.Lock()
	defer cpuCapsMu.Unlock()
	containerCPUCaps[containerID] = nanos
}

// getOriginalCPUCap returns the stored original NanoCPUs cap for a container,
// and whether it was tracked. An untracked container (ok == false) means the
// caller should skip the burst rather than guess a cap.
func getOriginalCPUCap(containerID string) (int64, bool) {
	cpuCapsMu.RLock()
	defer cpuCapsMu.RUnlock()
	nanos, ok := containerCPUCaps[containerID]
	return nanos, ok
}

// clearCPUCap removes a container from the cap tracker. Called on container
// removal so the map can't grow unbounded across sandbox churn.
func clearCPUCap(containerID string) {
	cpuCapsMu.Lock()
	defer cpuCapsMu.Unlock()
	delete(containerCPUCaps, containerID)
}

// ── Sandbox Container Lifecycle ──────────────────────────────────────────

// CreateContainer creates and starts a sandbox container with the given spec.
// It prepares bind mounts, sets security options (seccomp, capability dropping,
// no-new-privileges, PID limit), resource limits, and port bindings.
//
// Security hardening applied:
//   - CapDrop ALL + CapAdd only CHOWN, SETUID, SETGID (minimum for Node.js)
//   - Seccomp profile loaded from spec.SeccompPath
//   - no-new-privileges:true prevents privilege escalation
//   - PidsLimit 256 prevents fork bombs
//   - Memory and CPU limits via cgroups
//   - ReadonlyRootfs false (Metro writes cache files)
func (d *dockerClient) CreateContainer(ctx context.Context, spec *SandboxSpec) (string, error) {
	if spec == nil {
		return "", fmt.Errorf("docker: sandbox spec is nil")
	}

	// 1. Prepare bind mount directory with correct UID/GID.
	codePath, err := PrepareBindMount(spec.SandboxDir, spec.AppName)
	if err != nil {
		return "", fmt.Errorf("docker: prepare bind mount: %w", err)
	}

	// 2. Build environment variables from map to []string.
	env := make([]string, 0, len(spec.Env))
	for k, v := range spec.Env {
		env = append(env, k+"="+v)
	}

	// 3. Build container config.
	//
	// Phase 32 Wave 2 Bug 7 — set the forge.app_name label so the agent's
	// heartbeat snapshot (ListContainers in client.go) can find this
	// container. Without the label, the snapshot's filter excludes
	// every Forge-created container; control's reconciler then sees
	// running sandboxes as "missing on agent" and destroys them within
	// seconds of reaching running state.
	cfg := &container.Config{
		Image: spec.Image,
		// Set /etc/hostname to `forge-{appName}` so the sandbox entrypoint can
		// recover APP_NAME from it. Docker defaults /etc/hostname to the random
		// container ID (not the container name), which silently defeated the
		// entrypoint's hostname fallback — so EXPO_PACKAGER_PROXY_URL was never
		// set and Expo Go got `{host}:8081` manifest URLs ("Could not connect to
		// development server", since only :443 is exposed via Caddy). The
		// backend also sends APP_NAME via createApp env, but it is dropped in the
		// control→agent hop; deriving it from the locally-known spec.AppName here
		// is the reliable fix.
		Hostname:     "forge-" + spec.AppName,
		Env:          env,
		ExposedPorts: network.PortSet{containerPortParsed: {}},
		Labels: map[string]string{
			"forge.app_name":   spec.AppName,
			"forge.sandbox_id": spec.SandboxID,
		},
	}

	// 4. Build host config with security hardening and resource limits.
	hostCfg := &container.HostConfig{
		PortBindings: network.PortMap{
			containerPortParsed: []network.PortBinding{{
				HostIP:   netip.MustParseAddr("0.0.0.0"),
				HostPort: strconv.Itoa(spec.HostPort),
			}},
		},
		Binds: []string{
			codePath + ":/app/code",
			// Shared Metro transform cache — dedups framework-file transforms
			// across every sandbox on the host. Created by Server 2 host setup
			// (see phase 19-B3-SPEC.md, section B3-4).
			"/mnt/metro-cache:/mnt/metro-cache",
		},
		Resources:      hardenedResources(spec.MemoryMB, spec.CPUCores),
		SecurityOpt:    hardenedSecurityOpt(spec.SeccompPath),
		CapDrop:        hardenedCapDrop(),
		CapAdd:         hardenedCapAdd(),
		ReadonlyRootfs: false, // Metro writes cache; use tmpfs for /tmp instead
	}

	// 5. Create container with forge-{appName} naming convention.
	containerName := "forge-" + spec.AppName

	// Remove any pre-existing container with the same name (idempotent restart).
	// Docker container names are unique per host; a previous crashed container
	// would block re-creation. Best-effort: ignore "not found" errors.
	_, _ = d.raw().ContainerRemove(ctx, containerName, dockerclient.ContainerRemoveOptions{Force: true})

	result, err := d.raw().ContainerCreate(ctx, dockerclient.ContainerCreateOptions{
		Config:     cfg,
		HostConfig: hostCfg,
		Name:       containerName,
	})
	if err != nil {
		return "", fmt.Errorf("docker: create container %s: %w", containerName, err)
	}

	// 6. Start the container.
	_, err = d.raw().ContainerStart(ctx, result.ID, dockerclient.ContainerStartOptions{})
	if err != nil {
		return "", fmt.Errorf("docker: start container %s: %w", containerName, err)
	}

	// Record the original CPU cap so a per-build CPU burst (ExecSpec.CPUBurst)
	// can restore the exact value after the exec completes.
	storeOriginalCPUCap(result.ID, int64(spec.CPUCores*1e9))

	return result.ID, nil
}

// ── Shared Security Hardening ────────────────────────────────────────────
//
// Extracted so CreateContainer (live dev sandboxes) and CreateBuildWorker
// (ephemeral export workers) apply BYTE-IDENTICAL hardening from one source.
// Each returns a fresh value so callers never alias a shared mutable slice.
//
// NOTE on seccomp: the profile is whatever spec.SeccompPath carries today
// (historically empty). This hardening is reused verbatim for build workers —
// it deliberately does NOT change seccomp behavior. Any seccomp profile change
// is a separate, independently-verified change so deploying the build-worker
// feature can never alter live sandbox creation.

func hardenedResources(memoryMB int64, cpuCores float64) container.Resources {
	return container.Resources{
		Memory:    memoryMB * 1024 * 1024,
		NanoCPUs:  int64(cpuCores * 1e9),
		PidsLimit: int64Ptr(256),
	}
}

func hardenedSecurityOpt(seccompPath string) []string {
	return []string{
		"seccomp=" + seccompPath,
		"no-new-privileges:true",
	}
}

func hardenedCapDrop() []string { return []string{"ALL"} }

func hardenedCapAdd() []string { return []string{"CHOWN", "SETUID", "SETGID"} }

// ── Build Worker Lifecycle (isolated cold export) ────────────────────────

// CreateBuildWorker creates and starts an ephemeral build-worker container for
// an isolated cold web export. It is the structural fix for the dual-Metro OOM:
// the worker has NO dev Metro (PID1 is `sleep infinity`), its own 2GiB/2cpu
// cgroup, and mounts only a SNAPSHOT of the project code — so the exporter
// never shares a cgroup with the running dev sandbox and never touches the live
// /app/code.
//
// Divergences from CreateContainer (all load-bearing):
//   - ENTRYPOINT is overridden to `tini --` (no entrypoint.sh): the image's
//     seed logic re-clobbers app.json/package.json/etc over the bind mount on
//     boot, which would replace the snapshot's PROJECT app.json with the bare
//     template before the export's baseUrl patch runs. Bypassing it keeps the
//     worker export byte-equivalent to the in-container export.
//   - CMD is `sleep infinity`: the agent execs the export afterwards.
//   - Labeled forge.build_id (NOT forge.app_name / forge.sandbox_id), so the
//     container is invisible to ListContainers / the heartbeat reconciler and
//     is never reaped mid-export.
//   - No ExposedPorts / PortBindings: zero inbound surface.
//   - Container name forge-build-<BuildID>: a namespace distinct from
//     forge-<app>, so the idempotent pre-clean below can never remove a live
//     dev container.
//
// Same hardening as CreateContainer (CapDrop/CapAdd/seccomp/no-new-privileges/
// PidsLimit) via the shared helpers. storeOriginalCPUCap is recorded so an
// ExecRun CPUBurst can raise+restore the cap exactly like the dev path.
func (d *dockerClient) CreateBuildWorker(ctx context.Context, spec *BuildWorkerSpec) (string, error) {
	if spec == nil {
		return "", fmt.Errorf("docker: build worker spec is nil")
	}
	if spec.BuildID == "" {
		return "", fmt.Errorf("docker: build worker spec missing build ID")
	}
	if spec.Image == "" {
		return "", fmt.Errorf("docker: build worker spec missing image")
	}
	if spec.CodePath == "" {
		return "", fmt.Errorf("docker: build worker spec missing code path")
	}

	cfg := &container.Config{
		Image:      spec.Image,
		Entrypoint: []string{"/usr/bin/tini", "--"},
		Cmd:        []string{"sleep", "infinity"},
		Labels: map[string]string{
			buildIDLabel: spec.BuildID,
		},
		// No ExposedPorts — the worker serves nothing.
	}

	hostCfg := &container.HostConfig{
		// No PortBindings — zero inbound surface.
		Binds: []string{
			spec.CodePath + ":/app/code",
			// Shared Metro transform cache (cross-tenant, same trust as dev
			// sandboxes) — keeps the cold export fast.
			"/mnt/metro-cache:/mnt/metro-cache",
		},
		Resources:      hardenedResources(spec.MemoryMB, spec.CPUCores),
		SecurityOpt:    hardenedSecurityOpt(spec.SeccompPath),
		CapDrop:        hardenedCapDrop(),
		CapAdd:         hardenedCapAdd(),
		ReadonlyRootfs: false, // export writes to the RW snapshot bind
	}

	containerName := "forge-build-" + spec.BuildID

	// Idempotent: remove any leftover worker of the same name. Safe — the
	// forge-build- prefix can never collide with a live forge-<app> sandbox.
	_, _ = d.raw().ContainerRemove(ctx, containerName, dockerclient.ContainerRemoveOptions{Force: true})

	result, err := d.raw().ContainerCreate(ctx, dockerclient.ContainerCreateOptions{
		Config:     cfg,
		HostConfig: hostCfg,
		Name:       containerName,
	})
	if err != nil {
		return "", fmt.Errorf("docker: create build worker %s: %w", containerName, err)
	}

	if _, err := d.raw().ContainerStart(ctx, result.ID, dockerclient.ContainerStartOptions{}); err != nil {
		return "", fmt.Errorf("docker: start build worker %s: %w", containerName, err)
	}

	// Track the original CPU cap so an ExecRun CPUBurst can restore it.
	storeOriginalCPUCap(result.ID, int64(spec.CPUCores*1e9))

	return result.ID, nil
}

// SnapshotCodeDir copies a project's live code directory to an isolated build
// snapshot under buildsDir, returning the snapshot's code path (.../code).
//
// Snapshot rules (each one a correctness landmine):
//   - Plain `cp -a` (archive mode): preserves the node_modules SYMLINK as a
//     symlink (the v19 worker resolves /opt/expo-shared-deps internally; a
//     dangling host symlink is fine). MUST NOT use `cp -aL`/`-L` (would
//     dereference node_modules and copy GBs) or `cp -al` (hardlinks — a
//     hardlinked app.json would mutate the LIVE dir when the export patches it,
//     violating the snapshot invariant). app.json must be a real copy.
//   - Destination lives OUTSIDE the per-app sandbox tree (buildsDir, e.g.
//     /var/lib/forge/builds/<buildID>/code), so a dev-sandbox destroy /
//     idle-reap / workdir GC can never wipe an in-flight build.
//   - `cp -a SRC DEST` where DEST does not yet exist creates DEST as a copy of
//     SRC. The parent (buildsDir/<buildID>) is created+chowned first; DEST
//     itself must NOT pre-exist or cp would nest the copy inside it.
func SnapshotCodeDir(ctx context.Context, buildsDir, buildID, srcCodeDir string) (string, error) {
	if buildID == "" {
		return "", fmt.Errorf("docker: snapshot missing build ID")
	}
	if strings.Contains(buildID, "..") || strings.ContainsAny(buildID, "/\\") {
		return "", fmt.Errorf("docker: invalid build ID %q", buildID)
	}

	parent := filepath.Join(buildsDir, buildID)
	destCode := filepath.Join(parent, "code")

	// Fresh start: remove any stale snapshot for this build ID.
	if err := os.RemoveAll(parent); err != nil {
		return "", fmt.Errorf("docker: clear stale snapshot %s: %w", parent, err)
	}
	if err := os.MkdirAll(parent, 0755); err != nil {
		return "", fmt.Errorf("docker: create snapshot parent %s: %w", parent, err)
	}
	// Parent owned by the sandbox user so cp -a (run as root) writing a copy
	// that preserves source ownership (already appuser) sits under an
	// appuser-owned tree, matching the dev bind-mount layout.
	if err := chownFunc(parent, sandboxUserUID, sandboxUserGID); err != nil {
		return "", fmt.Errorf("docker: chown snapshot parent %s: %w", parent, err)
	}

	// Plain archive copy. cp -a == -dR --preserve=all: recursive, preserves
	// symlinks (node_modules) WITHOUT dereferencing, preserves ownership/mode.
	cmd := exec.CommandContext(ctx, "cp", "-a", srcCodeDir, destCode)
	if out, err := cmd.CombinedOutput(); err != nil {
		// Best-effort cleanup of a partial copy on failure.
		_ = os.RemoveAll(parent)
		return "", fmt.Errorf("docker: snapshot cp -a %s -> %s: %w (%s)",
			srcCodeDir, destCode, err, strings.TrimSpace(string(out)))
	}

	return destCode, nil
}

// ── HMR Worker Lifecycle (per-turn ephemeral dev Metro) ──────────────────

// CreateHmrWorker creates and starts a per-turn ephemeral HMR container — a dev
// Metro (`expo start`) that exists only while a turn is active and shows
// uncommitted edits via HMR. It is CreateBuildWorker INVERTED on four axes:
//
//	build worker                  hmr worker
//	────────────────────────────  ────────────────────────────────────────
//	entrypoint/CMD → sleep        KEEP image default (tini → entrypoint.sh,
//	                              CMD `npx expo start --port 8081`)
//	no ports                      expose + bind 8081 → 0.0.0.0:HostPort
//	bind a SNAPSHOT of code       bind the LIVE code dir (uncommitted edits)
//	label forge.build_id          label forge.hmr_id
//
// Like the build worker it is labeled OUT of the forge.app_name namespace, so
// it is invisible to ListContainers / the heartbeat reconciler / OrphanHunter
// (the box outlives its start command; only stop_hmr or the HMR reaper removes
// it). It is named forge-hmr-<TurnID>, a namespace distinct from forge-<app>,
// so the idempotent pre-clean below can never remove a live dev container.
//
// Same hardening as CreateContainer (CapDrop/CapAdd/seccomp/no-new-privileges/
// PidsLimit) via the shared helpers. storeOriginalCPUCap is recorded so the
// StartContainer cap re-assert path stays consistent (HMR boxes don't burst,
// but tracking is harmless and keeps the cap map coherent on removal).
func (d *dockerClient) CreateHmrWorker(ctx context.Context, spec *HmrWorkerSpec) (string, error) {
	if spec == nil {
		return "", fmt.Errorf("docker: hmr worker spec is nil")
	}
	if spec.TurnID == "" {
		return "", fmt.Errorf("docker: hmr worker spec missing turn ID")
	}
	if strings.Contains(spec.TurnID, "..") || strings.ContainsAny(spec.TurnID, "/\\") {
		return "", fmt.Errorf("docker: invalid hmr turn ID %q", spec.TurnID)
	}
	if spec.Image == "" {
		return "", fmt.Errorf("docker: hmr worker spec missing image")
	}
	if spec.CodePath == "" {
		return "", fmt.Errorf("docker: hmr worker spec missing code path")
	}
	if spec.HostPort <= 0 {
		return "", fmt.Errorf("docker: hmr worker spec missing host port")
	}

	// Env map → []string (the box runs the image CMD directly; env must be set
	// at create time since there is no later exec to carry it).
	env := make([]string, 0, len(spec.Env))
	for k, v := range spec.Env {
		env = append(env, k+"="+v)
	}

	cfg := &container.Config{
		Image: spec.Image,
		// KEEP the image default entrypoint + CMD (no override) so
		// `npx expo start --port 8081` runs and entrypoint.sh seeds normally.
		Env:          env,
		ExposedPorts: network.PortSet{containerPortParsed: {}},
		Labels: map[string]string{
			hmrIDLabel: spec.TurnID,
		},
	}

	hostCfg := &container.HostConfig{
		PortBindings: network.PortMap{
			containerPortParsed: []network.PortBinding{{
				HostIP:   netip.MustParseAddr("0.0.0.0"),
				HostPort: strconv.Itoa(spec.HostPort),
			}},
		},
		Binds: []string{
			// LIVE code dir (NOT a snapshot) — this is what makes in-turn
			// uncommitted edits reach Metro HMR.
			spec.CodePath + ":/app/code",
			// Shared Metro transform cache — same cross-tenant trust as dev
			// sandboxes; keeps the cold `expo start` boot fast.
			"/mnt/metro-cache:/mnt/metro-cache",
		},
		Resources:      hardenedResources(spec.MemoryMB, spec.CPUCores),
		SecurityOpt:    hardenedSecurityOpt(spec.SeccompPath),
		CapDrop:        hardenedCapDrop(),
		CapAdd:         hardenedCapAdd(),
		ReadonlyRootfs: false, // Metro writes cache to the RW bind
	}

	containerName := "forge-hmr-" + spec.TurnID

	// Idempotent: remove any leftover HMR box of the same name. Safe — the
	// forge-hmr- prefix can never collide with a live forge-<app> sandbox.
	_, _ = d.raw().ContainerRemove(ctx, containerName, dockerclient.ContainerRemoveOptions{Force: true})

	result, err := d.raw().ContainerCreate(ctx, dockerclient.ContainerCreateOptions{
		Config:     cfg,
		HostConfig: hostCfg,
		Name:       containerName,
	})
	if err != nil {
		return "", fmt.Errorf("docker: create hmr worker %s: %w", containerName, err)
	}

	if _, err := d.raw().ContainerStart(ctx, result.ID, dockerclient.ContainerStartOptions{}); err != nil {
		return "", fmt.Errorf("docker: start hmr worker %s: %w", containerName, err)
	}

	// Track the original CPU cap so StartContainer's cap re-assert stays
	// coherent and RemoveContainer can clear it.
	storeOriginalCPUCap(result.ID, int64(spec.CPUCores*1e9))

	return result.ID, nil
}

// ── Bind Mount Setup ─────────────────────────────────────────────────────

// sandboxUserUID is the UID of the node user inside sandbox containers.
const sandboxUserUID = 1000

// sandboxUserGID is the GID of the node user inside sandbox containers.
const sandboxUserGID = 1000

// chownFunc is the function used to change file ownership.
// Defaults to os.Chown. Overridden in tests where chown requires root.
var chownFunc = os.Chown

// PrepareBindMount creates the bind mount directory for a sandbox's code.
// It validates the appName to prevent path traversal attacks, creates the
// directory with 0755 permissions, and sets ownership to UID/GID 1000
// (the node user inside the container).
//
// Returns the full path to the code directory.
func PrepareBindMount(sandboxDir, appName string) (string, error) {
	// Validate appName: reject path traversal and directory separators.
	if strings.Contains(appName, "..") {
		return "", fmt.Errorf("docker: invalid app name %q: contains path traversal", appName)
	}
	if strings.Contains(appName, "/") || strings.Contains(appName, string(os.PathSeparator)) {
		return "", fmt.Errorf("docker: invalid app name %q: contains path separator", appName)
	}
	if filepath.IsAbs(appName) {
		return "", fmt.Errorf("docker: invalid app name %q: absolute path not allowed", appName)
	}

	codePath := filepath.Join(sandboxDir, appName, "code")

	if err := os.MkdirAll(codePath, 0755); err != nil {
		return "", fmt.Errorf("docker: create bind mount dir %s: %w", codePath, err)
	}

	// Set ownership to sandbox user (UID/GID 1000) for Node.js inside container.
	if err := chownFunc(codePath, sandboxUserUID, sandboxUserGID); err != nil {
		return "", fmt.Errorf("docker: chown bind mount dir %s: %w", codePath, err)
	}

	return codePath, nil
}

// ── Helpers ──────────────────────────────────────────────────────────────

// int64Ptr returns a pointer to the given int64 value.
func int64Ptr(v int64) *int64 { return &v }
