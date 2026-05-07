---
phase: 07-multi-node-failover
plan: 02
subsystem: infra
tags: [go, scheduler, heartbeat, failover, rescheduler, bin-packing]

# Dependency graph
requires:
  - phase: 07-multi-node-failover/01
    provides: Rescheduler unit with RescheduleNode, RescheduleStore interface
provides:
  - Rescheduler wired into heartbeat monitor for automatic node failover
  - Multi-node scheduler distribution tests proving MULTI-01
affects: [deployment, operations, monitoring]

# Tech tracking
tech-stack:
  added: []
  patterns: [heartbeat-triggered reschedule, sequential per-sandbox failover]

key-files:
  created: []
  modified:
    - control/cmd/forge-control/main.go
    - control/internal/scheduler/scheduler_test.go

key-decisions:
  - "Reschedule errors logged as warnings, not fatal -- heartbeat loop stays alive"
  - "NeverOverloadsOneNode test uses close-capacity nodes (100MB gaps) so 256MB placement shifts ranking"

patterns-established:
  - "Heartbeat -> reschedule wiring: monitorHeartbeats receives *lifecycle.Rescheduler as parameter"
  - "storeAdapter satisfies RescheduleStore via delegation to sqlc Queries"

requirements-completed: [MULTI-01, MULTI-02, MULTI-03]

# Metrics
duration: 968s
completed: 2026-04-16
---

# Phase 07 Plan 02: Rescheduler Wiring & Multi-Node Distribution Tests Summary

**Heartbeat monitor triggers automatic sandbox reschedule on node failure; 11 scheduler tests prove 3+ node distribution**

## Performance

- **Duration:** 968s (~16 min)
- **Started:** 2026-04-16T01:51:25Z
- **Completed:** 2026-04-16T02:07:33Z
- **Tasks:** 2
- **Files modified:** 2

## Accomplishments
- Rescheduler wired into heartbeat monitor -- when a node misses heartbeats, its running sandboxes are automatically rescheduled to healthy nodes
- storeAdapter extended with ListRunningSandboxesByNode to satisfy RescheduleStore interface
- Two new scheduler tests prove multi-node distribution: sequential scheduling with updated UsedMB shifts node selection (MULTI-01)
- Full control module builds, vets, and passes all tests with -race (11 scheduler tests, lifecycle tests, integration tests)

## Task Commits

Each task was committed atomically:

1. **Task 1: Wire rescheduler into heartbeat monitor** - `ef181e2` (feat)
2. **Task 2: Add multi-node scheduler distribution tests** - `5069140` (test)

## Files Created/Modified
- `control/cmd/forge-control/main.go` - Added ListRunningSandboxesByNode on storeAdapter, NewRescheduler in main(), RescheduleNode call in monitorHeartbeats
- `control/internal/scheduler/scheduler_test.go` - Added TestSchedule_DistributesAcrossMultipleNodes and TestSchedule_NeverOverloadsOneNode

## Decisions Made
- Reschedule errors logged as warnings (not fatal) so heartbeat monitor loop survives partial failures
- NeverOverloadsOneNode test uses nodes with close initial capacity (100MB gaps between 6000/5900/5800 free) so that each 256MB placement shifts which node has most free RAM -- proves distribution rather than always hitting the biggest node

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Fixed NeverOverloadsOneNode test node capacities**
- **Found during:** Task 2
- **Issue:** Plan's original test used nodes with 4000/8000/6000 capacity starting at 0 used. Node2 (8000 free) dominates by 2000MB margin, so 5x256MB placements all go to node2 (6720 free still > 6000). Test fails because scheduler correctly picks most-free node.
- **Fix:** Changed to equal-capacity nodes (8000MB each) with close initial UsedMB (2000/2100/2200) creating 100MB gaps. Now each 256MB placement shifts the ranking, proving distribution.
- **Files modified:** control/internal/scheduler/scheduler_test.go
- **Verification:** All 11 tests pass with -race
- **Committed in:** 5069140 (Task 2 commit)

---

**Total deviations:** 1 auto-fixed (1 bug in test setup)
**Impact on plan:** Test logic corrected to match actual scheduler behavior. No scope creep.

## Issues Encountered
None

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- Phase 07 complete: rescheduler unit tested (Plan 01) and wired into production heartbeat monitor (Plan 02)
- Full failover pipeline: heartbeat miss -> mark unhealthy -> reschedule running sandboxes -> dispatch start commands to new nodes
- 11 scheduler tests cover: bin-packing, exclusion of draining/unhealthy/removed nodes, capacity limits, tiebreaking, multi-node distribution

---
*Phase: 07-multi-node-failover*
*Completed: 2026-04-16*
