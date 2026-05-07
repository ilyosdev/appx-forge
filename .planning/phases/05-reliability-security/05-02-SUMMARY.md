---
phase: 05-reliability-security
plan: 02
subsystem: infra
tags: [goroutine, ticker, caddy, drift-detection, idle-reaper, background-workers]

requires:
  - phase: 04-routing-proxy
    provides: CaddyClient (ListRoutes, AddRoute, RemoveRoute), RouteManager, Batcher
  - phase: 03-control-plane
    provides: LifecycleService, Store interfaces, state machine
provides:
  - IdleReaper background goroutine with per-sandbox idle_timeout_seconds
  - DriftDetector background goroutine for Caddy-Postgres reconciliation
affects: [05-03-wiring, control-plane, main-goroutine-startup]

tech-stack:
  added: []
  patterns: [ticker-loop-with-context-cancellation, interface-based-dependency-injection-for-background-workers]

key-files:
  created:
    - control/internal/lifecycle/idle_reaper.go
    - control/internal/lifecycle/idle_reaper_test.go
    - control/internal/routing/drift.go
    - control/internal/routing/drift_test.go
  modified: []

key-decisions:
  - "IdleReaper reuses existing RouteNotifier interface from lifecycle package for route cleanup notifications"
  - "DriftDetector returns early (no partial fixes) when either Caddy or Postgres data source fails"
  - "Both goroutines use NewX(store, ..., interval) constructors with 0 defaulting to 60s for testability"
  - "IdleReaper records actor as 'idle-reaper' to distinguish from control-plane and agent events"

patterns-established:
  - "Background worker pattern: struct with Run(ctx) method, ticker loop, exported detect/reap for direct testing"
  - "Narrow interface pattern: IdleReaperStore and DriftStore define only methods each worker needs"

requirements-completed: [CTRL-11, CTRL-12]

duration: 8min
completed: 2026-04-16
---

# Phase 05 Plan 02: Idle Reaper & Drift Detector Summary

**TDD background workers: IdleReaper stops sandboxes idle beyond per-sandbox timeout, DriftDetector reconciles Caddy routes with Postgres every 60s**

## Performance

- **Duration:** 8 min (497s)
- **Started:** 2026-04-16T00:01:31Z
- **Completed:** 2026-04-16T00:09:48Z
- **Tasks:** 2
- **Files modified:** 4

## Accomplishments
- IdleReaper queries ListIdleSandboxes (per-sandbox idle_timeout_seconds, not hardcoded), transitions each to stopped, dispatches stop_sandbox command, records idle_timeout event, and notifies route manager
- DriftDetector diffs Caddy routes vs Postgres running sandboxes: removes stale routes (in Caddy but not running in Postgres), adds missing routes (running in Postgres but not in Caddy) with node lookup for upstream address
- Both workers continue processing remaining items on individual failures (no batch abort)
- 12 total tests (5 idle reaper + 7 drift detector) all pass with -race

## Task Commits

Each task was committed atomically:

1. **Task 1: TDD idle reaper** - `b64386c` (test) + `aadbe0c` (feat)
2. **Task 2: TDD drift detector** - `aaec447` (test) + `bc09c8a` (feat)

_TDD tasks have separate RED (test) and GREEN (feat) commits_

## Files Created/Modified
- `control/internal/lifecycle/idle_reaper.go` - IdleReaper struct with Run(ctx), reap(), and reapOne() methods
- `control/internal/lifecycle/idle_reaper_test.go` - 5 tests: no-op, two sandboxes, continue-on-failure, route notifier, event recording
- `control/internal/routing/drift.go` - DriftDetector struct with Run(ctx) and detect() methods
- `control/internal/routing/drift_test.go` - 7 tests: in-sync, stale removal, missing addition, both, ListRoutes failure, ListSandboxes failure, node lookup failure

## Decisions Made
- IdleReaper reuses existing RouteNotifier interface -- no new interface needed for route cleanup
- DriftDetector defines RouteListFetcher (ListRoutes + AddRoute + RemoveRoute) as separate interface from Flusher since it does individual operations, not batched
- DriftDetector defines DriftStore with only ListSandboxesByState + GetNode -- minimal interface per interface segregation principle
- Both workers return early on data source failures to prevent partial/incorrect fixes
- IdleReaper uses "idle-reaper" as actor in events to distinguish from "control-plane" and "agent"

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered
None

## User Setup Required
None - no external service configuration required.

## TDD Gate Compliance

- RED gate: `b64386c` (test) and `aaec447` (test) commits exist
- GREEN gate: `aadbe0c` (feat) and `bc09c8a` (feat) commits exist after respective RED gates
- REFACTOR gate: Not needed -- implementation clean on first pass

## Next Phase Readiness
- IdleReaper and DriftDetector are ready to be started as goroutines in main.go (Plan 03 wiring)
- Both have Run(ctx context.Context) methods compatible with goroutine startup pattern
- No blockers

## Self-Check: PASSED

- All 4 created files exist on disk
- All 4 commit hashes (b64386c, aadbe0c, aaec447, bc09c8a) verified in git log
- All 13 acceptance criteria grep checks pass

---
*Phase: 05-reliability-security*
*Completed: 2026-04-16*
