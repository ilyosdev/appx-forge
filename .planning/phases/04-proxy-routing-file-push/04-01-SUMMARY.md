---
phase: 04-proxy-routing-file-push
plan: 01
subsystem: infra
tags: [caddy, reverse-proxy, routing, debounce, batching]

# Dependency graph
requires:
  - phase: 03-control-plane-core
    provides: sandbox lifecycle service and state machine
provides:
  - CaddyClient with AddRoute, RemoveRoute, ListRoutes for Caddy Admin API
  - Route update batcher with 500ms debounce and maxBuf=50 overflow flush
  - Flusher interface for batch apply abstraction
  - Route struct (AppName, SandboxID, Upstream)
affects: [04-02-lifecycle-routing-integration, 04-03-drift-detection]

# Tech tracking
tech-stack:
  added: []
  patterns: [httptest mock server for HTTP client testing, time.AfterFunc debounce pattern, mutex-protected flush with unlock during I/O]

key-files:
  created:
    - control/internal/routing/caddy.go
    - control/internal/routing/caddy_test.go
    - control/internal/routing/batcher.go
    - control/internal/routing/batcher_test.go
  modified: []

key-decisions:
  - "Route JSON constructed from validated Route struct fields -- no raw user JSON (T-04-02 mitigation)"
  - "RemoveRoute treats 404 as success for idempotent removal"
  - "Batcher dedup uses last-write-wins per app_name in pending map"
  - "flushLocked releases mutex during Flusher.Apply to avoid holding lock during I/O"

patterns-established:
  - "httptest.NewServer for mocking external HTTP APIs in Go unit tests"
  - "Flusher interface decouples batcher from Caddy client for testability"
  - "NewBatcherWithDebounce constructor for fast test timing without waiting 500ms"

requirements-completed: [PRXY-02, PRXY-04]

# Metrics
duration: 4min
completed: 2026-04-15
---

# Phase 04 Plan 01: Caddy Client & Route Batcher Summary

**Caddy Admin API client with AddRoute/RemoveRoute/ListRoutes and 500ms debounce batcher with maxBuf=50 overflow flush**

## Performance

- **Duration:** 4 min (256s)
- **Started:** 2026-04-15T23:16:54Z
- **Completed:** 2026-04-15T23:21:10Z
- **Tasks:** 2
- **Files modified:** 4

## Accomplishments
- CaddyClient sends correct JSON body to Caddy Admin API per proxy-routing.md contract
- RemoveRoute is idempotent (404 treated as success)
- ListRoutes parses Caddy JSON response into typed Route structs
- Batcher debounces at 500ms, flushes immediately at buffer size 50
- Batcher deduplicates: add+remove same app within window = net remove (last write wins)
- 16 tests pass with -race flag

## Task Commits

Each task was committed atomically:

1. **Task 1: Caddy Admin API client with TDD** - `cc4aebd` (feat)
2. **Task 2: Route update batcher with 500ms debounce TDD** - `6bb1121` (feat)

## Files Created/Modified
- `control/internal/routing/caddy.go` - CaddyClient with AddRoute, RemoveRoute, ListRoutes
- `control/internal/routing/caddy_test.go` - 10 unit tests with httptest mock server
- `control/internal/routing/batcher.go` - Batcher with Flusher interface, debounce, maxBuf flush
- `control/internal/routing/batcher_test.go` - 6 unit tests with mock flusher

## Decisions Made
- Route JSON constructed from validated Route struct fields, not raw user JSON (T-04-02 threat mitigation)
- RemoveRoute treats 404 as success for idempotent removal per contract
- Batcher uses last-write-wins dedup per app_name in pending map
- flushLocked releases mutex during Flusher.Apply call to avoid holding lock during network I/O
- mockFlusher.setErr method used in tests to avoid data race on error field

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Fixed data race in mockFlusher.err field access**
- **Found during:** Task 2 (batcher test GREEN phase)
- **Issue:** Test set `f.err` directly from test goroutine while `Apply` read it from timer goroutine, causing -race detector failure
- **Fix:** Added mutex-protected `setErr()` method to mockFlusher, replaced direct field assignment
- **Files modified:** control/internal/routing/batcher_test.go
- **Verification:** All 6 batcher tests pass with -race
- **Committed in:** 6bb1121 (Task 2 commit)

---

**Total deviations:** 1 auto-fixed (1 bug fix)
**Impact on plan:** Test-only fix for race safety. No scope creep.

## TDD Gate Compliance

Both tasks followed RED/GREEN/REFACTOR within their commits:
- Task 1: RED (compilation failure confirmed) -> GREEN (10 tests pass) -> committed as `feat(04-01)`
- Task 2: RED (compilation failure confirmed) -> GREEN (6 tests pass, race fix applied) -> committed as `feat(04-01)`

Note: Since RED phase produces non-compilable code (types not yet defined), test and implementation are committed together in the GREEN commit. TDD flow was verified by running tests at each phase boundary.

## Issues Encountered
None

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- CaddyClient and Batcher ready for lifecycle integration (Plan 02)
- Flusher interface enables lifecycle service to wire CaddyClient as the concrete flusher
- Route struct provides the data type for routing state

## Self-Check: PASSED

- All 4 files exist on disk
- Both commits (cc4aebd, 6bb1121) found in git log
- caddy_test.go: 319 lines (min: 80)
- batcher_test.go: 249 lines (min: 60)
- 16 tests pass with -race

---
*Phase: 04-proxy-routing-file-push*
*Completed: 2026-04-15*
