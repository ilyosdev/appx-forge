---
phase: 05-reliability-security
plan: 03
subsystem: api, infra, security
tags: [prometheus, metrics, seccomp, goroutine, restart-manager, idle-reaper, drift-detector]

requires:
  - phase: 05-01
    provides: RestartManager with exponential backoff, failure count queries
  - phase: 05-02
    provides: IdleReaper, DriftDetector background workers

provides:
  - Prometheus /metrics endpoint with sandbox counts and node utilization
  - Seccomp profile for sandbox containers (deploy/seccomp-default.json)
  - Fully wired binary with all reliability goroutines and restart manager
  - MetricsStore interface and Server.SetMetricsStore setter

affects: [deployment, monitoring, agent]

tech-stack:
  added: [prometheus/client_golang]
  patterns: [Prometheus text format metrics, seccomp syscall whitelist, background goroutine wiring with ctx cancellation]

key-files:
  created:
    - control/internal/api/metrics.go
    - control/internal/api/metrics_test.go
    - deploy/seccomp-default.json
  modified:
    - control/internal/api/server.go
    - control/internal/api/routes.go
    - control/internal/config/config.go
    - control/internal/lifecycle/lifecycle.go
    - control/cmd/forge-control/main.go
    - control/go.mod
    - control/go.sum

key-decisions:
  - "Hand-crafted Prometheus text format instead of full registry -- simpler for 2 gauges, no need for histograms"
  - "MetricsStore as setter pattern (SetMetricsStore) consistent with SetAgentDeps/SetFilePushStore"
  - "driftStoreAdapter separate type to resolve GetNode return type conflict (store.Node vs api.NodeRecord)"
  - "RestartManager integrated via LifecycleService.SetRestartManager -- HandleEvent delegates on restarting, HandleAck resets on recovery"

patterns-established:
  - "Background goroutine wiring: create with New*, launch with go X.Run(ctx), log start with interval"
  - "Seccomp default-deny with explicit syscall whitelist for Node.js/Metro sandbox containers"

requirements-completed: [CTRL-15, SEC-01, SEC-02, SEC-03, SEC-04, SEC-05, SEC-06]

duration: 7min
completed: 2026-04-16
---

# Phase 05 Plan 03: Metrics, Wiring & Seccomp Summary

**Prometheus /metrics endpoint with sandbox/node gauges, seccomp syscall whitelist, and full reliability goroutine wiring into forge-control binary**

## Performance

- **Duration:** 7 min (426s)
- **Started:** 2026-04-16T00:13:29Z
- **Completed:** 2026-04-16T00:20:35Z
- **Tasks:** 2
- **Files modified:** 10

## Accomplishments
- Prometheus-compatible /metrics endpoint serving forge_sandbox_count{state=...} and forge_node_utilization_ratio{node=...} gauges, unauthenticated
- Restrictive seccomp profile (164 allowed syscalls, default SCMP_ACT_ERRNO) for sandbox containers
- RestartManager wired into LifecycleService for crash recovery with exponential backoff
- IdleReaper and DriftDetector background goroutines started in main.go with configurable intervals
- MetricsStore wired into API server for real-time operational metrics

## Task Commits

Each task was committed atomically:

1. **Task 1: Prometheus /metrics handler + seccomp profile** - `d142c9c` (feat)
2. **Task 2: Wire reliability goroutines + restart manager into main.go** - `93b42ce` (feat)

## Files Created/Modified
- `control/internal/api/metrics.go` - MetricsStore interface and handleMetrics handler
- `control/internal/api/metrics_test.go` - 5 tests covering happy path, auth bypass, store errors
- `deploy/seccomp-default.json` - Restrictive seccomp profile (164 syscalls, default deny)
- `control/internal/api/server.go` - Added metricsStore field and SetMetricsStore setter
- `control/internal/api/routes.go` - Added /v1/metrics as public unauthenticated route
- `control/internal/config/config.go` - Added IdleReaperIntervalSeconds and DriftDetectorIntervalSeconds
- `control/internal/lifecycle/lifecycle.go` - Added restartMgr field, SetRestartManager, HandleEvent/HandleAck integration
- `control/cmd/forge-control/main.go` - Wired RestartManager, IdleReaper, DriftDetector, MetricsStore; added store adapter methods and driftStoreAdapter
- `control/go.mod` - Added prometheus/client_golang dependency
- `control/go.sum` - Updated dependency checksums

## Decisions Made
- Hand-crafted Prometheus text format instead of full registry -- simpler for 2 gauges, avoids process/runtime collector overhead
- MetricsStore as setter pattern (SetMetricsStore) consistent with existing SetAgentDeps/SetFilePushStore pattern
- driftStoreAdapter as separate type to resolve GetNode return type conflict (store.Node vs api.NodeRecord)
- RestartManager integrated via LifecycleService.SetRestartManager rather than constructor injection -- keeps New() signature stable

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 2 - Missing Critical] Added RestartManager integration into LifecycleService**
- **Found during:** Task 2
- **Issue:** Plan Step 4 noted that LifecycleService might not have SetRestartManager integration. It did not exist.
- **Fix:** Added restartMgr field, SetRestartManager setter, HandleEvent delegation to HandleCrash on restarting transition, HandleAck delegation to HandleRestarted on successful recovery
- **Files modified:** control/internal/lifecycle/lifecycle.go
- **Verification:** go test ./control/... passes
- **Committed in:** 93b42ce (Task 2 commit)

---

**Total deviations:** 1 auto-fixed (1 missing critical)
**Impact on plan:** Auto-fix was anticipated by plan Step 4. No scope creep.

## Issues Encountered
None

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- Phase 05 (Reliability & Security) is now complete -- all 3 plans executed
- Binary compiles with all background workers: heartbeat monitor, idle reaper, drift detector
- RestartManager handles crash recovery with exponential backoff (5s/10s/20s, max 3 attempts)
- Seccomp profile ready for agent to reference via SandboxSpec.SeccompPath
- /metrics endpoint ready for Prometheus scraping
- All SEC-01 through SEC-06 requirements verified passing

## Self-Check: PASSED

- All created files exist on disk
- All task commits (d142c9c, 93b42ce) found in git log
- Build compiles: `go build ./control/cmd/forge-control/` succeeds
- All tests pass: `go test ./control/... -count=1 -race` zero failures
- Agent security tests pass: TestCreateContainerSecurityOpt, TestCreateContainerCapabilities, TestCreateContainerPidsLimit

---
*Phase: 05-reliability-security*
*Completed: 2026-04-16*
