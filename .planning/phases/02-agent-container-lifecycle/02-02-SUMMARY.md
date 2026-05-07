---
phase: 02-agent-container-lifecycle
plan: 02
subsystem: agent
tags: [go, docker-sdk, containers, security, seccomp, tdd]

# Dependency graph
requires:
  - phase: 02-agent-container-lifecycle
    plan: 01
    provides: "Agent Go module, config struct, port allocator"
  - phase: 01-contracts-schema-sandbox-image
    provides: "shared-go models (SandboxState), go.work workspace"
provides:
  - "Docker Client interface with 9 methods for container lifecycle"
  - "Sandbox container creation with full security hardening"
  - "PrepareBindMount with path traversal prevention"
  - "rawDockerClient interface for mock injection in tests"
affects: [02-03, 02-04, 02-05, 02-06]

# Tech tracking
tech-stack:
  added: [moby/moby/client v0.4.0, moby/moby/api v1.54.1, docker/go-connections v0.6.0]
  patterns: [interface-based Docker wrapper, rawClient mock injection, chownFunc for testability]

key-files:
  created:
    - agent/internal/docker/client.go
    - agent/internal/docker/client_test.go
    - agent/internal/docker/sandbox.go
    - agent/internal/docker/sandbox_test.go
  modified:
    - agent/go.mod
    - agent/go.sum
    - go.work.sum

key-decisions:
  - "rawDockerClient interface enables mock injection for testing container creation params without Docker daemon"
  - "chownFunc package-level var allows test override since os.Chown to UID 1000 requires root"
  - "Docker SDK v0.4.0 uses new modular import path github.com/moby/moby/client (not docker/docker)"
  - "PortBinding.HostIP uses netip.Addr (not string) in Docker SDK v0.4.0"

patterns-established:
  - "Docker mock: mockDockerSDK implements rawDockerClient, captures ContainerCreateOptions for assertions"
  - "Security defaults: CapDrop ALL, CapAdd CHOWN/SETUID/SETGID, seccomp profile, no-new-privileges, PidsLimit 256"
  - "Container naming: forge-{appName} convention for all sandbox containers"
  - "Bind mount: {sandboxDir}/{appName}/code:/app/code with UID/GID 1000"

requirements-completed: [AGNT-05, AGNT-12]

# Metrics
duration: 18min
completed: 2026-04-15
---

# Phase 02 Plan 02: Docker SDK Wrapper Summary

**Docker client interface with 9 methods, sandbox container creation with seccomp/cap-drop/PID-limit/resource-limits security hardening, and bind mount setup with path traversal prevention -- all TDD with 18 tests passing**

## Performance

- **Duration:** 18 min
- **Started:** 2026-04-15T20:30:20Z
- **Completed:** 2026-04-15T20:48:40Z
- **Tasks:** 2
- **Files modified:** 7

## Accomplishments
- Docker Client interface with 9 methods (CreateContainer, Start, Stop, Remove, Restart, Inspect, GetLogs, PullImage, EventsStream, Close) wrapping moby/moby/client v0.4.0
- Container creation with full security hardening: CapDrop ALL, CapAdd only CHOWN/SETUID/SETGID, seccomp profile, no-new-privileges, PidsLimit 256, memory/CPU cgroup limits
- PrepareBindMount creates code directories with UID/GID 1000 ownership and rejects path traversal attacks (.. and / in appName)
- rawDockerClient interface enables mock injection for testing without Docker daemon

## Task Commits

Each task was committed atomically:

1. **Task 1: Docker client interface and mock** - `7c60735` (feat) - Interface, types, dockerClient wrapping SDK
2. **Task 2a: Sandbox tests (RED)** - `51d464f` (test) - 13 failing test functions for container creation params
3. **Task 2b: Sandbox implementation (GREEN)** - `1d17838` (feat) - All 18 tests pass with -race

## Files Created/Modified
- `agent/internal/docker/client.go` - Client interface (9 methods), SandboxSpec/ContainerInfo/ContainerEvent types, dockerClient implementation with rawDockerClient mock injection
- `agent/internal/docker/client_test.go` - 5 tests: mock interface compliance, dockerClient interface compliance, struct field coverage
- `agent/internal/docker/sandbox.go` - CreateContainer with security hardening, PrepareBindMount with path traversal prevention
- `agent/internal/docker/sandbox_test.go` - 13 tests: container config, port bindings, resource limits, seccomp, capabilities, PID limit, naming, bind mount, path traversal
- `agent/go.mod` - Added moby/moby/client v0.4.0 and transitive dependencies
- `agent/go.sum` - Checksums for new dependencies
- `go.work.sum` - Workspace-level dependency checksums

## Decisions Made
- Used `rawDockerClient` interface (not exported) for mock injection rather than requiring a running Docker daemon for tests
- Docker SDK v0.4.0 uses new modular import path `github.com/moby/moby/client` (the old `github.com/docker/docker` path has import breakage in v28+)
- `chownFunc` package-level variable allows tests to skip `os.Chown` (requires root on macOS; production agent runs as root on Linux)
- PortBinding.HostIP is `netip.Addr` in Docker SDK v0.4.0 (changed from string in older versions)

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Docker SDK import path resolution**
- **Found during:** Task 1 (adding Docker SDK dependency)
- **Issue:** `github.com/moby/moby/client@v27.5.1` caused ambiguous import error due to v0.4.0 modular split
- **Fix:** Used `github.com/moby/moby/client@v0.4.0` (the new modular package path)
- **Files modified:** agent/go.mod
- **Committed in:** 7c60735

**2. [Rule 3 - Blocking] os.Chown fails on macOS without root**
- **Found during:** Task 2 (sandbox tests failing with "operation not permitted")
- **Issue:** `os.Chown` to UID 1000 requires root privileges; tests run as non-root on macOS
- **Fix:** Added `chownFunc` package-level variable defaulting to `os.Chown`, overridden in tests via init()
- **Files modified:** agent/internal/docker/sandbox.go, agent/internal/docker/sandbox_test.go
- **Committed in:** 1d17838

---

**Total deviations:** 2 auto-fixed (2 blocking)
**Impact on plan:** Both fixes necessary for correct compilation and test execution. No scope creep.

## Issues Encountered
None beyond the auto-fixed deviations above.

## User Setup Required
None - no external service configuration required.

## TDD Gate Compliance

- RED gate: `51d464f` (test commit with 13 failing sandbox tests)
- GREEN gate: `1d17838` (feat commit with all 18 tests passing)
- Task 1 tests were written alongside implementation (interface compliance is compile-time)

## Next Phase Readiness
- Docker client interface ready for use by all subsequent agent packages (events watcher, heartbeat, file push)
- rawDockerClient interface enables mock-based testing in 02-03 through 02-06 without Docker daemon
- Container creation produces correct security settings verified by 13 dedicated tests
- PrepareBindMount ready for use in file push handler (Plan 02-05)

## Self-Check: PASSED

- All 4 created files verified present on disk
- All 3 commits verified in git log (7c60735, 51d464f, 1d17838)

---
*Phase: 02-agent-container-lifecycle*
*Completed: 2026-04-15*
