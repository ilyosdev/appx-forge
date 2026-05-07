---
phase: 05-reliability-security
plan: 01
subsystem: control/lifecycle
tags: [restart, backoff, resilience, tdd]
dependency_graph:
  requires: []
  provides: [RestartManager, RestartStore, CrashResult, HandleCrash, HandleRestarted]
  affects: [control/internal/lifecycle, control/internal/store]
tech_stack:
  added: []
  patterns: [exponential-backoff, crash-recovery, failure-count-tracking]
key_files:
  created:
    - control/internal/lifecycle/restart.go
    - control/internal/lifecycle/restart_test.go
  modified:
    - control/internal/store/queries/sandboxes.sql
    - control/internal/store/sandboxes.sql.go
decisions:
  - "Backoff uses baseDelay * 2^(count-1) after increment: 5s, 10s, 20s for attempts 1-3"
  - "Failure count incremented atomically via SQL before decision -- prevents race conditions"
  - "Restart dispatches start_sandbox command (not restart_sandbox) to re-create container"
  - "Delay encoded in command payload for agent-side enforcement, not control-plane sleep"
metrics:
  duration: 239s
  completed: "2026-04-15T23:58:53Z"
  tasks_completed: 1
  tasks_total: 1
  files_changed: 4
---

# Phase 05 Plan 01: Auto-Restart with Exponential Backoff Summary

RestartManager with TDD-driven exponential backoff (5s/10s/20s) capping at 3 retries before FAILED state, with sqlc-managed failure_count tracking.

## Changes Made

### Task 1: Add sqlc queries + TDD restart backoff logic

**TDD RED phase** (commit `95cde03`):
- Added 3 new sqlc queries to `sandboxes.sql`: `IncrementSandboxFailureCount` (:one), `ResetSandboxFailureCount` (:exec), `CountSandboxesByState` (:many)
- Regenerated `sandboxes.sql.go` via `sqlc generate` -- new Go functions available
- Created `restart_test.go` with 8 failing tests covering all restart scenarios

**TDD GREEN phase** (commit `31a4028`):
- Created `restart.go` (183 lines) with `RestartManager` struct
- `HandleCrash(ctx, sandbox)`: atomically increments failure_count, computes exponential backoff delay, transitions restarting->starting via state machine, dispatches start_sandbox command with delay metadata
- `HandleRestarted(ctx, sandboxID)`: resets failure_count to zero on successful restart
- `CrashResult` struct returns `ShouldRestart` bool and `Delay` duration
- After 3 failed attempts (failure_count > maxRestartAttempts), transitions to FAILED
- All 8 tests pass with `-race`, zero regressions across entire `control/...` test suite

### Key Implementation Details

- **Backoff formula**: `baseDelay * 2^(newCount-1)` where baseDelay=5s, newCount is post-increment
- **Atomic increment**: failure_count incremented in DB before decision logic -- no TOCTOU race
- **Command dispatch**: Uses `start_sandbox` command type (not `restart_sandbox`) since the container needs re-creation
- **Delay enforcement**: Delay is in command payload `restart_delay` field -- agent waits before executing
- **Timeout adjustment**: Command timeout is `60 + delay` seconds to account for backoff wait

## Deviations from Plan

None -- plan executed exactly as written.

## TDD Gate Compliance

- RED gate: `test(05-01)` commit `95cde03` -- 8 tests, all failing (undefined NewRestartManager)
- GREEN gate: `feat(05-01)` commit `31a4028` -- all 8 tests passing with -race
- REFACTOR gate: not needed -- implementation is clean at 183 lines

## Threat Mitigations Applied

- **T-05-01 (DoS)**: `maxRestartAttempts=3` cap prevents infinite restart loops; exponential backoff (5s/10s/20s) prevents rapid cycling
- **T-05-02 (Tampering)**: failure_count modified only via atomic SQL `SET failure_count = failure_count + 1` -- agent cannot directly set the value

## Commits

| # | Hash | Message |
|---|------|---------|
| 1 | `95cde03` | test(05-01): add failing tests for restart backoff logic |
| 2 | `31a4028` | feat(05-01): implement RestartManager with exponential backoff |

## Self-Check: PASSED

All 5 created/modified files verified on disk. Both commit hashes (95cde03, 31a4028) found in git log.
