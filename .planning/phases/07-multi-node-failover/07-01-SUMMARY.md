---
phase: 07-multi-node-failover
plan: 01
subsystem: control-plane
tags: [failover, rescheduler, lifecycle, tdd]
dependency_graph:
  requires: [scheduler, lifecycle, state-machine]
  provides: [rescheduler, ListRunningSandboxesByNode-query]
  affects: [lifecycle-package, sandboxes-sql]
tech_stack:
  added: []
  patterns: [narrow-interface-per-service, sequential-batch-processing, partial-failure-tolerance]
key_files:
  created:
    - control/internal/lifecycle/reschedule.go
    - control/internal/lifecycle/reschedule_test.go
  modified:
    - control/internal/store/queries/sandboxes.sql
    - control/internal/store/sandboxes.sql.go
decisions:
  - Sequential per-sandbox processing prevents thundering herd on remaining nodes
  - CAS (UPDATE WHERE state=running) prevents double-reschedule of same sandbox
  - No-capacity fallback transitions sandbox to FAILED rather than looping indefinitely
  - Best-effort route removal -- errors logged, never block reschedule flow
  - Failure count reset to 0 on reschedule -- sandbox gets fresh start on new node
metrics:
  duration: 312s
  completed: "2026-04-16T01:48:21Z"
  tasks: 2
  files: 4
---

# Phase 07 Plan 01: Sandbox Rescheduler Summary

TDD-driven Rescheduler with RescheduleNode method, 7 comprehensive tests, and ListRunningSandboxesByNode sqlc query.

## What Was Done

### Task 1: RED -- Add sqlc query + write failing reschedule tests (d50787f)

- Added `ListRunningSandboxesByNode` query to `sandboxes.sql` filtering by node_id AND state='running', ordered by created_at ASC
- Ran `sqlc generate` to produce typed Go code in `sandboxes.sql.go`
- Created `reschedule_test.go` with 7 test functions covering:
  1. Two RUNNING sandboxes on failed node -- both rescheduled to healthy nodes
  2. No RUNNING sandboxes -- returns RescheduleResult with Count=0
  3. No healthy nodes have capacity -- sandbox transitions to FAILED
  4. Caddy routes removed for each rescheduled sandbox via RouteNotifier
  5. node_failed and scheduled events recorded for audit trail
  6. Scheduler picks node with most free RAM (bin-packing)
  7. Partial failure -- one sandbox fails, others still proceed, errors collected
- Verified RED: `go test` fails with "undefined: NewRescheduler" (7 references)

### Task 2: GREEN -- Implement Rescheduler (25bd6ec)

- Created `reschedule.go` with:
  - `RescheduleStore` interface (7 narrow methods)
  - `RescheduleResult` struct with Count, Failed, Errors fields
  - `Rescheduler` struct with store, notifier, logger
  - `NewRescheduler` constructor (nil logger defaults to slog.Default)
  - `RescheduleNode` public method: lists RUNNING sandboxes, sequential processing, aggregate result
  - `rescheduleSandbox` private method implementing 9-step flow:
    1. CAS transition running -> pending
    2. Record node_failed event (actor: "rescheduler")
    3. Best-effort Caddy route removal
    4. Reset failure count
    5. List healthy nodes + convert to scheduler candidates
    6. Parse memory_mb from sandbox resources (default 512)
    7. Schedule via bin-packing
    8. No-capacity: transition to FAILED + record reschedule_failed event
    9. Assign to node, dispatch start_sandbox command, record scheduled event
- All 7 tests pass with -race flag, zero regressions in lifecycle package, go vet clean

## Deviations from Plan

None -- plan executed exactly as written.

## Decisions Made

1. **Sequential processing** (T-07-01 mitigation): Each sandbox processed one at a time, no goroutines, preventing thundering herd when multiple sandboxes need rescheduling simultaneously
2. **CAS state transition** (T-07-02 mitigation): `UPDATE WHERE state='running'` prevents double-reschedule if two monitors detect the same node failure
3. **Sandbox IDs and node IDs only in logs** (T-07-03 mitigation): No env vars or secrets appear in reschedule log messages
4. **No retry in rescheduler** (T-07-04 mitigation): If rescheduled sandbox fails immediately on new node, the existing RestartManager handles it (max 3 retries), not the Rescheduler

## TDD Gate Compliance

1. RED gate: `test(07-01)` commit d50787f -- 7 tests referencing undefined NewRescheduler
2. GREEN gate: `feat(07-01)` commit 25bd6ec -- all 7 tests pass with -race
3. REFACTOR gate: not needed -- implementation is clean

## Self-Check: PASSED

- reschedule.go: FOUND
- reschedule_test.go: FOUND (455 lines, exceeds 150 min)
- sandboxes.sql: FOUND with ListRunningSandboxesByNode
- sandboxes.sql.go: FOUND with ListRunningSandboxesByNode
- Commit d50787f (RED): FOUND
- Commit 25bd6ec (GREEN): FOUND
- NewRescheduler export: FOUND
- RescheduleResult export: FOUND
