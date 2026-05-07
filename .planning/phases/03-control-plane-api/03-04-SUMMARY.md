---
phase: 03-control-plane-api
plan: 04
subsystem: api
tags: [go, chi, lifecycle, state-machine, sandbox, crud, scheduler]

requires:
  - phase: 03-01
    provides: "HTTP server scaffold, chi router, RFC 7807 errors, bearer auth middleware"
  - phase: 03-02
    provides: "Bin-packing scheduler for node selection"
provides:
  - "Sandbox lifecycle service: create, destroy, restart, handle ack, handle event"
  - "Sandbox CRUD HTTP handlers matching OpenAPI spec"
  - "SandboxLifecycle and SandboxReader interfaces for handler decoupling"
  - "State machine enforcement via models.NextState for all transitions"
affects: [03-05, 03-06, agent-protocol, sdk]

tech-stack:
  added: []
  patterns: [lifecycle-service-pattern, interface-based-mock-testing, nil-guard-handlers]

key-files:
  created:
    - control/internal/lifecycle/lifecycle.go
    - control/internal/lifecycle/lifecycle_test.go
    - control/internal/api/sandboxes.go
    - control/internal/api/sandboxes_test.go
  modified:
    - control/internal/api/server.go
    - control/internal/api/server_test.go
    - control/internal/api/health_test.go

key-decisions:
  - "Store interface in lifecycle package decouples from sqlc Queries for mock-based unit testing"
  - "SandboxLifecycle and SandboxReader interfaces decouple HTTP handlers from lifecycle service"
  - "Nil guards on all sandbox handlers prevent panic when services not configured during tests"
  - "container_oom maps to EventContainerExited (same state machine transition as exit)"
  - "RestartSandbox has no state transition -- restart is at Docker level, sandbox stays running"
  - "app_name validated with regex ^[a-z0-9][a-z0-9-]{1,62}$ per OpenAPI spec"

patterns-established:
  - "Lifecycle service pattern: business logic in lifecycle package, HTTP handlers delegate to it"
  - "Interface segregation: SandboxLifecycle for mutations, SandboxReader for queries"
  - "Nil guard pattern: all handlers check for nil service before proceeding"

requirements-completed: [CTRL-02, CTRL-05, CTRL-08]

duration: 8min
completed: 2026-04-15
---

# Phase 03 Plan 04: Sandbox Lifecycle & CRUD Summary

**Sandbox lifecycle service with state machine enforcement and 5 CRUD HTTP endpoints matching OpenAPI spec**

## Performance

- **Duration:** 8 min
- **Started:** 2026-04-15T22:23:58Z
- **Completed:** 2026-04-15T22:32:35Z
- **Tasks:** 2
- **Files modified:** 7

## Accomplishments

- Lifecycle service orchestrating full sandbox lifecycle: create (schedule + dispatch), destroy, restart, handle ack, handle event
- All state transitions enforced via models.NextState state machine with CAS in DB
- 5 sandbox HTTP endpoints (POST create, GET by id/app:name, GET list with filters, DELETE destroy, POST restart) matching OpenAPI spec
- Every state change recorded as an event with actor, prev_state, next_state (T-03-11 audit trail)
- 43 tests pass with -race (11 lifecycle + 32 API)

## Task Commits

Each task was committed atomically:

1. **Task 1: Lifecycle service with TDD**
   - `95c26fe` (test): add failing tests for sandbox lifecycle service
   - `79fbcbc` (feat): implement sandbox lifecycle service
2. **Task 2: Sandbox HTTP handlers with TDD**
   - `38c1ea1` (test): add sandbox HTTP handler tests
   - `5cd5bc0` (feat): implement sandbox CRUD HTTP handlers

## Files Created/Modified

- `control/internal/lifecycle/lifecycle.go` - Sandbox lifecycle service: create, destroy, restart, HandleAck, HandleEvent
- `control/internal/lifecycle/lifecycle_test.go` - 11 lifecycle unit tests with mock store
- `control/internal/api/sandboxes.go` - 5 sandbox HTTP handlers, request/response types, app_name validation
- `control/internal/api/sandboxes_test.go` - 11 sandbox handler tests with mock lifecycle and reader
- `control/internal/api/server.go` - Added lifecycle and sandboxReader fields, real sandbox routes
- `control/internal/api/server_test.go` - Updated NewServer calls for new signature
- `control/internal/api/health_test.go` - Updated NewServer calls for new signature

## Decisions Made

- Store interface in lifecycle package decouples from sqlc Queries for mock-based unit testing
- SandboxLifecycle and SandboxReader interfaces decouple HTTP handlers from lifecycle service
- Nil guards on all sandbox handlers prevent panic when services not configured during tests
- container_oom maps to EventContainerExited (same state machine transition as container exit)
- RestartSandbox has no state transition -- restart is at Docker level, sandbox stays running
- app_name validated with regex per OpenAPI spec

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Added nil guards to sandbox handlers**
- **Found during:** Task 2 (sandbox HTTP handlers)
- **Issue:** NewServer called with nil lifecycle/sandboxReader in existing tests caused panic in handleListSandboxes when GET /v1/sandboxes was hit
- **Fix:** Added nil checks at the start of all 5 sandbox handlers returning 503 if services not configured
- **Files modified:** control/internal/api/sandboxes.go
- **Verification:** TestServer_AuthenticatedRouteAcceptsValidToken passes without panic
- **Committed in:** 5cd5bc0 (Task 2 GREEN commit)

---

**Total deviations:** 1 auto-fixed (1 bug)
**Impact on plan:** Essential for correctness when handlers called with nil services. No scope creep.

## Issues Encountered

None.

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness

- Sandbox CRUD endpoints ready for agent command polling (Plan 05) and event reporting (Plan 06)
- Lifecycle service HandleAck and HandleEvent ready to be called from agent endpoints
- SandboxLifecycle interface established for future handler extensions

---
*Phase: 03-control-plane-api*
*Completed: 2026-04-15*
