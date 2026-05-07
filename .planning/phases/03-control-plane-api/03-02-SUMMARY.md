---
phase: 03-control-plane-api
plan: 02
subsystem: api
tags: [go, scheduler, bin-packing, pure-function, tdd]

# Dependency graph
requires:
  - phase: 01-contracts-and-shared
    provides: "shared-go/models NodeStatus constants"
provides:
  - "scheduler.Schedule() function for sandbox placement"
  - "scheduler.NodeCandidate struct for caller conversion"
  - "scheduler.ErrNoNodes and ErrNoCapacity sentinel errors"
affects: [03-control-plane-api, 04-agent]

# Tech tracking
tech-stack:
  added: []
  patterns: ["pure-function scheduler with no I/O dependencies", "single-pass O(n) best-fit selection"]

key-files:
  created:
    - control/internal/scheduler/scheduler.go
    - control/internal/scheduler/scheduler_test.go
  modified: []

key-decisions:
  - "Single-pass O(n) scan instead of sort -- avoids O(n log n) for a simple max-find"
  - "Deterministic tiebreak by input order (first wins) rather than secondary sort key"

patterns-established:
  - "Pure function scheduling: no interface, no struct receiver, just func Schedule()"
  - "NodeCandidate as plain Go struct decouples scheduler from store.Node pgtype dependencies"

requirements-completed: [CTRL-03]

# Metrics
duration: 2min
completed: 2026-04-15
---

# Phase 03 Plan 02: Bin-Packing Scheduler Summary

**Pure-function bin-packing scheduler selecting healthy node with most free RAM via single-pass O(n) scan**

## Performance

- **Duration:** 2 min
- **Started:** 2026-04-15T22:09:54Z
- **Completed:** 2026-04-15T22:12:00Z
- **Tasks:** 2 (RED + GREEN, no refactor needed)
- **Files created:** 2

## TDD Gate Compliance

- RED gate: `799c74b` (test commit) -- 9 failing tests
- GREEN gate: `b93650a` (feat commit) -- all 9 tests pass with -race
- REFACTOR gate: skipped (implementation already minimal at 30 lines of logic)

## Accomplishments

- 9 test cases covering all scheduling scenarios from spec (happy path, exclusions, errors, edge cases)
- Schedule() function: filters to healthy nodes, filters by capacity, picks most free RAM
- Sentinel errors ErrNoNodes and ErrNoCapacity for caller error handling
- NodeCandidate struct decouples scheduler from pgtype/store dependencies

## Task Commits

Each task was committed atomically:

1. **RED: Failing scheduler tests** - `799c74b` (test)
2. **GREEN: Implement Schedule()** - `b93650a` (feat)

## Files Created/Modified

- `control/internal/scheduler/scheduler.go` - Schedule() function, NodeCandidate struct, sentinel errors (67 lines)
- `control/internal/scheduler/scheduler_test.go` - 9 table-free test functions covering all spec cases (168 lines)

## Decisions Made

- Single-pass O(n) scan instead of sort+pick -- for a simple maximum-find, sorting is unnecessary overhead
- Deterministic tiebreak by input order (first candidate with highest free RAM wins) -- stable and predictable without secondary sort keys
- No refactor commit needed -- implementation is already a clean 30-line single-pass with no dead code

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered

None.

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness

- Scheduler ready for Plan 04 (sandbox lifecycle) to call when creating sandboxes
- Caller converts store.Node to scheduler.NodeCandidate before calling Schedule()
- ErrNoNodes/ErrNoCapacity can be mapped to HTTP 503/409 responses

## Self-Check: PASSED

- All files exist: scheduler.go, scheduler_test.go, SUMMARY.md
- All commits exist: 799c74b (RED), b93650a (GREEN)
- All 9 tests pass with -race

---
*Phase: 03-control-plane-api*
*Completed: 2026-04-15*
