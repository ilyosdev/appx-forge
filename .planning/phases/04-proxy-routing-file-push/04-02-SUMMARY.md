---
phase: 04-proxy-routing-file-push
plan: 02
subsystem: routing
tags: [caddy, proxy, lifecycle, state-machine, reverse-proxy]

# Dependency graph
requires:
  - phase: 04-01
    provides: CaddyClient, Batcher, Route, RouteChange, Flusher interface
provides:
  - RouteManager service translating lifecycle events to batched route changes
  - RouteNotifier interface for lifecycle -> routing decoupling
  - CaddyClient.Apply implementing Flusher for batched route operations
  - CaddyAdminURL configuration field
affects: [04-03, phase-05-drift-detector]

# Tech tracking
tech-stack:
  added: []
  patterns: [RouteNotifier callback interface, SetRouteNotifier setter injection, best-effort notification with warning logs]

key-files:
  created:
    - control/internal/routing/manager.go
    - control/internal/routing/manager_test.go
  modified:
    - control/internal/lifecycle/lifecycle.go
    - control/internal/lifecycle/lifecycle_test.go
    - control/internal/routing/caddy.go
    - control/internal/config/config.go
    - control/cmd/forge-control/main.go
    - control/tests/integration_test.go
    - docker-compose.dev.yml

key-decisions:
  - "GetNodeByID method name avoids clash with api.NodeStore.GetNode (returns store.Node vs NodeRecord)"
  - "CaddyClient.Apply implements Flusher with partial-failure tolerance using errors.Join"
  - "Route notifications are best-effort: errors logged as warnings, never propagated to callers"
  - "Only HandleAck triggers route adds; HandleEvent container_started does NOT add routes"
  - "Enqueuer interface on RouteManager enables spy-based testing without real Batcher"

patterns-established:
  - "RouteNotifier: lifecycle calls routing via interface, injected via SetRouteNotifier setter"
  - "Best-effort notification: route errors logged, never block state transitions"
  - "GetNodeByID naming convention resolves return-type conflicts across interface boundaries"

requirements-completed: [CTRL-10, PRXY-03]

# Metrics
duration: 10min
completed: 2026-04-15
---

# Phase 04 Plan 02: Route Manager Lifecycle Integration Summary

**RouteManager wired into sandbox lifecycle: auto-adds Caddy routes on RUNNING, auto-removes on exit/stop/destroy via batched Flusher**

## Performance

- **Duration:** 10 min (627s)
- **Started:** 2026-04-15T23:23:45Z
- **Completed:** 2026-04-15T23:34:12Z
- **Tasks:** 2
- **Files modified:** 9

## Accomplishments
- RouteManager translates lifecycle state changes into batched Caddy route operations via Enqueuer interface
- RouteNotifier interface decouples lifecycle from routing package -- nil-safe for backward compatibility
- Full wiring in main.go: CaddyClient -> Batcher -> RouteManager -> lifecycle.SetRouteNotifier
- 8 new tests (3 RouteManager + 5 lifecycle route notification) all passing with -race

## Task Commits

Each task was committed atomically:

1. **Task 1 RED: Failing tests** - `76a7bf9` (test)
2. **Task 1 GREEN: RouteManager + lifecycle RouteNotifier** - `5286fd1` (feat)
3. **Task 2: Wire into main.go + config** - `25bd281` (feat)

## Files Created/Modified
- `control/internal/routing/manager.go` - RouteManager: OnSandboxRunning enqueues add, OnSandboxStopped enqueues remove
- `control/internal/routing/manager_test.go` - 3 tests: add enqueue, remove enqueue, empty upstream guard
- `control/internal/lifecycle/lifecycle.go` - RouteNotifier interface, SetRouteNotifier, notifyRouteAdd/Remove helpers
- `control/internal/lifecycle/lifecycle_test.go` - 5 route tests: ack start, ack stop, event container_exited, container_started no-op, nil safety
- `control/internal/routing/caddy.go` - CaddyClient.Apply implementing Flusher (batched add/remove with partial failure tolerance)
- `control/internal/config/config.go` - CaddyAdminURL field with default http://localhost:2019
- `control/cmd/forge-control/main.go` - Full routing wiring + storeAdapter.GetNodeByID
- `control/tests/integration_test.go` - integrationAdapter.GetNodeByID for expanded lifecycle.Store
- `docker-compose.dev.yml` - FORGE_CADDY_ADMIN_URL environment variable

## Decisions Made
- **GetNodeByID naming**: lifecycle.Store uses `GetNodeByID` (returns store.Node) to avoid clash with api.NodeStore.GetNode (returns NodeRecord). Same Go pattern as filePushAdapter but solved via method rename instead of separate adapter type.
- **CaddyClient.Apply**: Added directly to CaddyClient rather than a separate adapter, since CaddyClient is the natural Flusher implementation. Uses errors.Join for partial failure tolerance.
- **Route add only via HandleAck**: container_started events via HandleEvent do NOT trigger route adds. Only the authoritative start_sandbox ack with host_port data triggers OnSandboxRunning.
- **Enqueuer interface**: RouteManager accepts Enqueuer (single-method interface) rather than *Batcher, enabling spy-based unit testing without the real timer/goroutine machinery.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] CaddyClient missing Flusher.Apply method**
- **Found during:** Task 2 (main.go wiring)
- **Issue:** Plan called `routing.NewBatcher(caddyClient, logger)` but CaddyClient did not implement Flusher (missing Apply method). Build failed.
- **Fix:** Added `CaddyClient.Apply(ctx, adds, removes)` that iterates over adds/removes calling AddRoute/RemoveRoute with partial-failure tolerance via errors.Join.
- **Files modified:** control/internal/routing/caddy.go
- **Verification:** `go build ./control/cmd/forge-control/` succeeds
- **Committed in:** 25bd281 (Task 2 commit)

**2. [Rule 3 - Blocking] integrationAdapter missing GetNodeByID**
- **Found during:** Task 2 (full test suite run)
- **Issue:** Integration test's integrationAdapter did not implement the expanded lifecycle.Store interface (missing GetNodeByID).
- **Fix:** Added `GetNodeByID` method delegating to q.GetNode.
- **Files modified:** control/tests/integration_test.go
- **Verification:** `go test ./control/tests/ -count=1 -race` passes
- **Committed in:** 25bd281 (Task 2 commit)

---

**Total deviations:** 2 auto-fixed (2 blocking)
**Impact on plan:** Both blocking fixes were necessary for compilation. No scope creep.

## TDD Gate Compliance

- RED gate: `76a7bf9` (test commit with all 8 failing tests)
- GREEN gate: `5286fd1` (feat commit making all 8 tests pass)
- REFACTOR gate: skipped (code was clean, no refactoring needed)

## Issues Encountered
None beyond the auto-fixed deviations above.

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- Route manager fully wired: Caddy routes auto-managed on sandbox state changes
- Ready for Plan 04-03 (file push proxy routing)
- Phase 5 drift detector can query expected routes via lifecycle state vs Caddy ListRoutes

---
*Phase: 04-proxy-routing-file-push*
*Completed: 2026-04-15*
