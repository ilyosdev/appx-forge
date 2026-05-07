---
phase: 03-control-plane-api
plan: 03
subsystem: api
tags: [chi, http-handlers, node-registration, heartbeat, crypto-rand, agent-token, go]

requires:
  - phase: 03-control-plane-api
    provides: Chi router, serverConfig, BearerAuth middleware, RFC 7807 errors, sqlc queries (CreateNode, GetNodeByHostnameAndIP, UpdateNodeToken, UpdateNodeHeartbeat, GetNode)
provides:
  - Node registration handler (POST /v1/nodes/register) with idempotent re-registration
  - Heartbeat handler (POST /v1/nodes/{id}/heartbeat) with auth and 404 handling
  - NodeStore interface for testable handler dependency injection
  - Agent token generation (crypto/rand 32 bytes -> 64-char hex)
  - writeJSON and parseUUID/formatUUID helper functions
affects: [03-04, 03-05, 03-06]

tech-stack:
  added: []
  patterns: [NodeStore interface for handler-level mocking, nodeRecord/createNodeArgs decoupled from sqlc types, writeJSON response helper]

key-files:
  created:
    - control/internal/api/nodes.go
  modified:
    - control/internal/api/nodes_test.go
    - control/internal/api/server.go
    - control/internal/api/server_test.go
    - control/internal/api/health_test.go

key-decisions:
  - "NodeStore interface decouples handlers from sqlc-generated store.Queries for mock-based unit testing"
  - "nodeRecord and createNodeArgs types decouple handler layer from sqlc-generated types -- handlers never import store package"
  - "Heartbeat checks node existence via GetNode before UpdateNodeHeartbeat since sqlc :exec discards affected row count"
  - "NewServer signature extended with NodeStore and heartbeatIntervalSec params (nil/0 defaults safe for existing callers)"

patterns-established:
  - "NodeStore interface: narrow interface per handler domain for focused mocking"
  - "nodeRecord/createNodeArgs: handler-layer types decoupled from store-layer types"
  - "writeJSON helper: consistent JSON response encoding across all handlers"
  - "parseUUID/formatUUID: pgtype.UUID <-> string conversion helpers"

requirements-completed: [CTRL-06, CTRL-07]

duration: 6min
completed: 2026-04-15
---

# Phase 03 Plan 03: Node Registration and Heartbeat Summary

**Node registration and heartbeat HTTP handlers with NodeStore interface, crypto/rand agent tokens, and 10 httptest-based tests**

## Performance

- **Duration:** 6 min
- **Started:** 2026-04-15T22:14:29Z
- **Completed:** 2026-04-15T22:21:25Z
- **Tasks:** 2
- **Files modified:** 5

## Accomplishments
- POST /v1/nodes/register creates new node (201) or re-registers existing node with fresh agent_token
- POST /v1/nodes/{id}/heartbeat updates used_mb, running_containers, last_seen_at with auth guard
- NodeStore interface enables mock-based testing without database -- all 10 handler tests use mocks
- Agent token generated with crypto/rand (32 bytes hex-encoded, 64 chars), never logged per T-03-09
- Registration is unauthenticated (public route group), heartbeat requires Bearer auth

## Task Commits

Each task was committed atomically:

1. **Task 1: Node registration handler with TDD** - `c7f04cf` (feat)
2. **Task 2: Heartbeat handler with TDD** - `114e420` (test)

## Files Created/Modified
- `control/internal/api/nodes.go` - Registration + heartbeat handlers, NodeStore interface, nodeRecord/createNodeArgs types, writeJSON/parseUUID/formatUUID helpers
- `control/internal/api/nodes_test.go` - 10 tests: 6 registration (valid, re-reg, missing hostname, missing IP, invalid JSON, no auth) + 4 heartbeat (valid, unknown node, no auth, invalid UUID)
- `control/internal/api/server.go` - Server struct extended with nodeStore + heartbeatIntervalSeconds; NewServer accepts NodeStore + interval; registration route in public group, heartbeat in authenticated group
- `control/internal/api/server_test.go` - Updated NewServer calls with new params (nil, 0)
- `control/internal/api/health_test.go` - Updated NewServer calls with new params (nil, 0)

## Decisions Made
- NodeStore interface decouples handlers from sqlc-generated store.Queries -- enables focused mock-based unit testing without database
- nodeRecord and createNodeArgs are handler-layer types separate from sqlc types -- handlers never import the store package directly
- Heartbeat verifies node existence via GetNode before UpdateNodeHeartbeat because sqlc `:exec` discards the CommandTag (affected rows)
- NewServer extended with two additional params rather than functional options -- simpler, existing callers updated with nil/0 defaults

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered
None

## User Setup Required
None - no external service configuration required.

## TDD Gate Compliance
- Task 1: `test(...)` and `feat(...)` combined in single commit `c7f04cf` (tests written first but shipped together since handler types needed for compilation)
- Task 2: `test(03-03)` commit `114e420` adds heartbeat tests against handler implemented in Task 1

## Next Phase Readiness
- Node handlers complete -- registration and heartbeat endpoints mounted and tested
- NodeStore interface pattern established for future handler plans to follow
- writeJSON, parseUUID, formatUUID helpers available for all subsequent handlers
- Plan 03-04 (sandbox CRUD) can import and extend the Server with additional handler domains

---
*Phase: 03-control-plane-api*
*Completed: 2026-04-15*

## Self-Check: PASSED
- All key files exist on disk
- All commit hashes found in git log
