---
phase: 03-control-plane-api
plan: 06
subsystem: api
tags: [go, chi, pgx, goose, docker-compose, testcontainers, integration-test, heartbeat]

# Dependency graph
requires:
  - phase: 03-control-plane-api/03-01
    provides: "Database schema, migrations, store queries"
  - phase: 03-control-plane-api/03-03
    provides: "HTTP server, handlers, middleware, health endpoint"
  - phase: 03-control-plane-api/03-04
    provides: "Sandbox lifecycle service and CRUD handlers"
  - phase: 03-control-plane-api/03-05
    provides: "Agent endpoints (long-poll, ack, event, file push)"
provides:
  - "Runnable forge-control binary with full dependency wiring"
  - "Background heartbeat monitor for unhealthy node detection"
  - "docker-compose.dev.yml for local Postgres + control plane"
  - "Integration tests proving full sandbox lifecycle against real Postgres"
  - "Store adapter bridging sqlc Queries to handler/lifecycle interfaces"
affects: [agent-integration, deployment, multi-node]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "storeAdapter pattern for bridging sqlc Queries to interface types"
    - "filePushAdapter for method-name conflicts (GetNode returns different types)"
    - "RegisterRoutes method extracts route setup from constructor"
    - "SetAgentDeps/SetFilePushStore setter injection for optional dependencies"
    - "float64ToNumeric helper for pgtype.Numeric construction"

key-files:
  created:
    - control/cmd/forge-control/main.go
    - control/internal/api/routes.go
    - control/tests/integration_test.go
    - docker-compose.dev.yml
    - control/Dockerfile
    - control/.gitignore
  modified:
    - control/internal/api/server.go
    - control/internal/api/nodes.go
    - control/internal/api/nodes_test.go
    - control/internal/lifecycle/lifecycle.go

key-decisions:
  - "Separate filePushAdapter type to resolve GetNode return type conflict (NodeRecord vs store.Node)"
  - "Export NodeRecord/CreateNodeArgs types and add NewServerConfig constructor for cross-package use"
  - "float64ToNumeric scales by 1000 with Exp=-3 since pgtype.Numeric.Scan rejects float64"
  - "Migrations dir fallback: tries ./migrations first (Docker), then control/migrations (go run from root)"

patterns-established:
  - "storeAdapter: single struct implements NodeStore, SandboxReader, AgentStore, lifecycle.Store"
  - "filePushAdapter: separate struct when interface methods have same name but different return types"
  - "RegisterRoutes: route registration extracted from constructor into dedicated method"

requirements-completed: [OPS-02]

# Metrics
duration: 16min
completed: 2026-04-15
---

# Phase 03 Plan 06: Binary Wiring, Docker Compose, and Integration Tests Summary

**Runnable forge-control binary with config/Postgres/migrations/heartbeat wiring, docker-compose dev stack, and 3 integration tests proving full sandbox lifecycle against real Postgres**

## Performance

- **Duration:** 16 min
- **Started:** 2026-04-15T22:46:27Z
- **Completed:** 2026-04-15T23:02:35Z
- **Tasks:** 2
- **Files modified:** 11

## Accomplishments
- forge-control binary compiles and wires all dependencies: config, Postgres pool, goose migrations, store, lifecycle, HTTP server, heartbeat monitor
- Background heartbeat monitor marks nodes unhealthy after missing HeartbeatMissThreshold consecutive heartbeats
- docker-compose.dev.yml starts Postgres 16-alpine with healthcheck + forge-control service
- 3 integration tests against real Postgres via testcontainers: healthz 200, sandbox create-to-command dispatch, node register + heartbeat

## Task Commits

Each task was committed atomically:

1. **Task 1: forge-control binary + routes file + heartbeat monitor** - `ccba74e` (feat)
2. **Task 2: docker-compose.dev.yml + integration test** - `c8188fc` (feat)

## Files Created/Modified
- `control/cmd/forge-control/main.go` - Binary entrypoint with config, pool, migrations, lifecycle, server, heartbeat monitor, graceful shutdown
- `control/internal/api/routes.go` - RegisterRoutes method consolidating all route registration
- `control/internal/api/server.go` - NewServerConfig constructor, SetAgentDeps/SetFilePushStore setters, uses RegisterRoutes
- `control/internal/api/nodes.go` - Exported NodeRecord/CreateNodeArgs, fixed null metadata default
- `control/internal/api/nodes_test.go` - Updated for exported type names
- `control/internal/lifecycle/lifecycle.go` - Fixed null metadata default to `{}`
- `control/tests/integration_test.go` - 3 integration tests with integrationAdapter
- `docker-compose.dev.yml` - Postgres + forge-control dev stack
- `control/Dockerfile` - Multi-stage Go build for forge-control
- `control/.gitignore` - Ignore compiled binary

## Decisions Made
- Separate `filePushAdapter` type needed because `FilePushStore.GetNode` returns `store.Node` while `NodeStore.GetNode` returns `NodeRecord` -- Go forbids same method name with different return types on one struct
- Exported `NodeRecord`/`CreateNodeArgs`/`NewServerConfig` to enable cross-package adapter construction in main.go
- `float64ToNumeric` scales float64 by 1000 then stores with `Exp=-3` because `pgtype.Numeric.Scan` rejects `float64` input
- Migrations directory has two-path fallback: `./migrations` for Docker context, `control/migrations` for `go run` from repo root

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Fixed null metadata in lifecycle.CreateSandbox**
- **Found during:** Task 2 (integration test)
- **Issue:** When `CreateRequest.Metadata` is nil, `metadataJSON` stayed nil causing NOT NULL constraint violation on sandboxes.metadata
- **Fix:** Changed default from `var metadataJSON []byte` to `metadataJSON := []byte("{}")`
- **Files modified:** control/internal/lifecycle/lifecycle.go
- **Verification:** TestIntegration_CreateSandbox_DispatchesCommand passes
- **Committed in:** c8188fc (Task 2 commit)

**2. [Rule 1 - Bug] Fixed null metadata in node registration handler**
- **Found during:** Task 2 (integration test)
- **Issue:** When registration request omits `metadata` field, handler passed nil to CreateNode causing NOT NULL constraint violation
- **Fix:** Changed default from `var metadata []byte` to `metadata := []byte("{}")`
- **Files modified:** control/internal/api/nodes.go
- **Verification:** TestIntegration_RegisterAndHeartbeat passes
- **Committed in:** c8188fc (Task 2 commit)

**3. [Rule 1 - Bug] Fixed pgtype.Numeric construction for capacity_cpu**
- **Found during:** Task 2 (integration test)
- **Issue:** `pgtype.Numeric.Scan(float64)` returns error "cannot scan float64" -- silently ignored with `_ =`, leaving Numeric invalid (NULL)
- **Fix:** Created `float64ToNumeric` helper using `big.NewInt(scaled)` with `Exp=-3`
- **Files modified:** control/cmd/forge-control/main.go, control/tests/integration_test.go
- **Verification:** TestIntegration_RegisterAndHeartbeat passes
- **Committed in:** c8188fc (Task 2 commit)

---

**Total deviations:** 3 auto-fixed (3 bugs via Rule 1)
**Impact on plan:** All fixes necessary for correctness -- null defaults and numeric conversion were latent bugs exposed by integration tests. No scope creep.

## Issues Encountered
None beyond the auto-fixed bugs above.

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- Phase 03 (Control Plane API) is complete: all 6 plans executed
- forge-control binary runnable locally via `go run ./control/cmd/forge-control/` or `docker-compose -f docker-compose.dev.yml up`
- Ready for agent integration testing (Phase 04) and multi-node deployment
- All store queries, lifecycle service, HTTP handlers, and integration tests proven against real Postgres

---
*Phase: 03-control-plane-api*
*Completed: 2026-04-15*
