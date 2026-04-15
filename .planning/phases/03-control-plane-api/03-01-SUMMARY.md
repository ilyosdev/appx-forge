---
phase: 03-control-plane-api
plan: 01
subsystem: api
tags: [chi, envconfig, rfc7807, bearer-auth, go, sqlc, http-server]

requires:
  - phase: 01-contracts-schemas
    provides: OpenAPI spec, sqlc schema, goose migrations, HMAC auth utils
provides:
  - Chi router factory with public and authenticated route groups
  - Bearer token auth middleware with constant-time comparison
  - RFC 7807 ProblemDetail error response helpers
  - /v1/healthz health check endpoint with Postgres connectivity
  - envconfig-based Config struct for control plane
  - GetNodeByHostnameAndIP and UpdateNodeToken sqlc queries
  - Migration 00005 adding agent_token column to nodes table
affects: [03-02, 03-03, 03-04, 03-05, 03-06]

tech-stack:
  added: [go-chi/chi/v5 v5.2.5, kelseyhightower/envconfig v1.4.0]
  patterns: [chi middleware composition, PoolPinger interface for testable health checks, RFC 7807 error responses, serverConfig for test-friendly DI]

key-files:
  created:
    - control/internal/config/config.go
    - control/internal/config/config_test.go
    - control/internal/api/errors.go
    - control/internal/api/middleware.go
    - control/internal/api/middleware_test.go
    - control/internal/api/server.go
    - control/internal/api/server_test.go
    - control/internal/api/health.go
    - control/internal/api/health_test.go
    - control/migrations/00005_add_node_agent_token.sql
  modified:
    - control/internal/store/queries/nodes.sql
    - control/internal/store/nodes.sql.go
    - control/internal/store/models.go
    - control/go.mod
    - control/go.sum

key-decisions:
  - "PoolPinger interface decouples health handler from pgxpool.Pool for mock-based testing"
  - "serverConfig struct allows nil config in tests that only need public routes"
  - "Placeholder /v1/sandboxes route exists only to verify auth middleware works -- replaced by real handlers in Plan 03-02+"
  - "Empty envconfig prefix with full FORGE_ names in struct tags matches Phase 02 convention"

patterns-established:
  - "PoolPinger interface: test health checks without real Postgres"
  - "WriteProblem + helpers: consistent RFC 7807 error responses across all handlers"
  - "BearerAuth middleware: crypto/subtle constant-time comparison for timing attack prevention"
  - "Chi route groups: public routes (healthz, metrics) vs authenticated routes (everything else)"

requirements-completed: [CTRL-01, CTRL-14, CTRL-16]

duration: 6min
completed: 2026-04-15
---

# Phase 03 Plan 01: HTTP Server Scaffold Summary

**Chi router with bearer auth middleware, RFC 7807 errors, /healthz endpoint, and envconfig-based configuration**

## Performance

- **Duration:** 6 min
- **Started:** 2026-04-15T22:00:41Z
- **Completed:** 2026-04-15T22:07:04Z
- **Tasks:** 2
- **Files modified:** 15

## Accomplishments
- Config struct loads from FORGE_* env vars with sensible defaults (listen addr, heartbeat settings)
- BearerAuth middleware with constant-time token comparison rejects invalid/missing tokens with 401 ProblemJSON
- RFC 7807 ProblemDetail type with helpers for all standard error codes (400, 401, 404, 409, 503)
- /v1/healthz returns 200 with {status, postgres, uptime_seconds} when DB reachable, 503 when not
- GetNodeByHostnameAndIP and UpdateNodeToken sqlc queries for idempotent node re-registration
- Migration 00005 adds agent_token column to nodes table
- 17 tests total, all passing with -race flag

## Task Commits

Each task was committed atomically:

1. **Task 1: Config + RFC 7807 errors + auth middleware** - `126c89a` (feat)
2. **Task 2: Chi router server + /healthz handler** - `4fc3851` (feat)

## Files Created/Modified
- `control/internal/config/config.go` - Envconfig-based Config struct with FORGE_* prefix
- `control/internal/config/config_test.go` - Config parsing tests (defaults, required fields)
- `control/internal/api/errors.go` - RFC 7807 ProblemDetail type and error helpers
- `control/internal/api/middleware.go` - BearerAuth middleware with constant-time compare
- `control/internal/api/middleware_test.go` - Auth middleware tests (valid/invalid/missing/wrong scheme)
- `control/internal/api/server.go` - Chi router factory with public and authenticated route groups
- `control/internal/api/server_test.go` - Server integration tests (public healthz, auth rejection, 404)
- `control/internal/api/health.go` - /v1/healthz handler with Postgres ping
- `control/internal/api/health_test.go` - Health check tests (healthy/unhealthy via mock PoolPinger)
- `control/migrations/00005_add_node_agent_token.sql` - Adds agent_token column to nodes
- `control/internal/store/queries/nodes.sql` - Added GetNodeByHostnameAndIP and UpdateNodeToken
- `control/internal/store/nodes.sql.go` - Regenerated sqlc with new queries
- `control/internal/store/models.go` - Regenerated sqlc with AgentToken field on Node
- `control/go.mod` - Added chi v5.2.5 and envconfig v1.4.0

## Decisions Made
- PoolPinger interface decouples health handler from pgxpool.Pool for testability without real database
- serverConfig struct allows nil config in tests that only need public routes (no auth setup needed)
- Empty envconfig prefix with full FORGE_ names in struct tags -- consistent with Phase 02 agent convention
- Placeholder /v1/sandboxes route only for verifying auth middleware works -- replaced by real handlers in Plan 03-02+

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered
None

## User Setup Required
None - no external service configuration required.

## Known Stubs
- `control/internal/api/server.go:68` - Placeholder `/v1/sandboxes` handler returns NotFound. Intentional: will be replaced by real sandbox handlers in Plan 03-02. Does not block this plan's objective (HTTP scaffold).

## Next Phase Readiness
- Server package is ready for handler plans (03-02 through 03-06) to import and mount routes
- NewServer, BearerAuth, WriteProblem, PoolPinger all exported and tested
- Config struct ready for main.go to parse and pass to NewServer

---
*Phase: 03-control-plane-api*
*Completed: 2026-04-15*
