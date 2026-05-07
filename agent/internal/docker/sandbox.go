package docker

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	dockerclient "github.com/moby/moby/client"
)

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
		Image:        spec.Image,
		Env:          env,
		ExposedPorts: network.PortSet{containerPortParsed: {}},
		Labels: map[string]string{
			"forge.app_name": spec.AppName,
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
		Resources: container.Resources{
			Memory:   spec.MemoryMB * 1024 * 1024,
			NanoCPUs: int64(spec.CPUCores * 1e9),
			PidsLimit: int64Ptr(256),
		},
		SecurityOpt: []string{
			"seccomp=" + spec.SeccompPath,
			"no-new-privileges:true",
		},
		CapDrop:        []string{"ALL"},
		CapAdd:         []string{"CHOWN", "SETUID", "SETGID"},
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
