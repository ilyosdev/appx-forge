---
phase: 06-cli-sdk-integration
plan: 03
subsystem: cli
tags: [go, cobra, cli, http-client, tabwriter]

requires:
  - phase: 06-01
    provides: Control plane API endpoints (nodes, sandboxes, routes, events, healthz)
provides:
  - forge CLI binary with 13 commands for fleet management
  - HTTP client wrapper for control plane API with RFC 7807 error handling
  - Table and JSON output formatting
affects: [ops-runbooks, deployment, monitoring]

tech-stack:
  added: [spf13/cobra v1.10.0]
  patterns: [constructor-based Cobra commands for test isolation, httptest mock servers]

key-files:
  created:
    - cli/cmd/forge/main.go
    - cli/cmd/forge/root.go
    - cli/cmd/forge/client.go
    - cli/cmd/forge/output.go
    - cli/cmd/forge/node.go
    - cli/cmd/forge/node_test.go
    - cli/cmd/forge/sandbox.go
    - cli/cmd/forge/sandbox_test.go
    - cli/cmd/forge/routes.go
    - cli/cmd/forge/routes_test.go
    - cli/cmd/forge/events.go
    - cli/cmd/forge/events_test.go
    - cli/cmd/forge/health.go
    - cli/cmd/forge/health_test.go
    - cli/go.mod
    - cli/go.sum
  modified:
    - go.work

key-decisions:
  - "Constructor-based newRootCmd() instead of global var -- enables test isolation with fresh command trees"
  - "envOrDefault() reads FORGE_API_URL and FORGE_API_TOKEN from env with flag override -- 12-factor compatible"
  - "getNoAuth() method for /v1/healthz endpoint -- per OpenAPI spec security:[] designation"
  - "Client-side --since filter using UTC timestamps -- consistent with server's ISO 8601 Z-suffix format"
  - "routes verify compares routes vs running sandboxes and exits 1 on drift -- enables monitoring integration"

patterns-established:
  - "Cobra command constructor pattern: newXxxCmd() returns *cobra.Command for test isolation"
  - "httptest.NewServer mock pattern for CLI testing without real API"
  - "resolveClient(cmd) reads flags and returns apiClient -- consistent flag resolution"

requirements-completed: [CLI-01, CLI-02, CLI-03, CLI-04, CLI-05, CLI-06, CLI-07, CLI-08, CLI-09, CLI-10, CLI-11, CLI-12, CLI-13]

duration: 13min
completed: 2026-04-16
---

# Phase 06 Plan 03: Forge CLI Summary

**Go CLI binary with 13 commands (node/sandbox/routes/events/health) using Cobra, backed by HTTP client talking to control plane API**

## Performance

- **Duration:** 13 min
- **Started:** 2026-04-16T01:17:26Z
- **Completed:** 2026-04-16T01:30:46Z
- **Tasks:** 2
- **Files modified:** 17

## Accomplishments
- All 13 CLI commands implemented: 4 node (list/add/drain/remove), 5 sandbox (list/inspect/logs/restart/destroy), 2 routes (list/verify), 1 events, 1 healthcheck
- HTTP client with Bearer auth, RFC 7807 error parsing, and unauthenticated endpoint support
- 23 tests all passing with httptest mock servers -- zero real API dependency
- Routes verify detects orphan routes and missing routes with exit code 1 on drift
- Token never logged or printed in help output (T-06-08 mitigation)

## TDD Gate Compliance

TDD gates followed for both tasks:
- Task 1: RED (7208714) -> GREEN (9b44e70)
- Task 2: RED (83a0471) -> GREEN (7e02d37)

## Task Commits

Each task was committed atomically:

1. **Task 1: CLI scaffold + HTTP client + node + sandbox commands**
   - `7208714` (test) - Failing tests for node and sandbox commands
   - `9b44e70` (feat) - Implementation passing all 12 tests

2. **Task 2: Routes, events, healthcheck commands**
   - `83a0471` (test) - Failing tests for routes, events, healthcheck
   - `7e02d37` (feat) - Implementation passing all 11 tests

## Files Created/Modified
- `cli/go.mod` - CLI Go module with cobra dependency
- `cli/cmd/forge/main.go` - CLI entry point
- `cli/cmd/forge/root.go` - Cobra root command with --api-url, --api-token, -o flags
- `cli/cmd/forge/client.go` - HTTP client: get/post/del with Bearer auth, RFC 7807 parsing
- `cli/cmd/forge/output.go` - tabwriter-based table printer, truncateID helper
- `cli/cmd/forge/node.go` - Node subcommands: list, add, drain, remove
- `cli/cmd/forge/node_test.go` - 5 tests for node commands
- `cli/cmd/forge/sandbox.go` - Sandbox subcommands: list, inspect, logs, restart, destroy
- `cli/cmd/forge/sandbox_test.go` - 7 tests for sandbox commands
- `cli/cmd/forge/routes.go` - Routes list and verify (drift detection)
- `cli/cmd/forge/routes_test.go` - 3 tests for routes commands
- `cli/cmd/forge/events.go` - Events with --sandbox, --type, --since, --limit filters
- `cli/cmd/forge/events_test.go` - 4 tests for events command
- `cli/cmd/forge/health.go` - Healthcheck without auth, exit 1 on unhealthy
- `cli/cmd/forge/health_test.go` - 4 tests for healthcheck
- `go.work` - Added ./cli to workspace

## Decisions Made
- Constructor-based newRootCmd() instead of global var -- enables test isolation with fresh command trees per test
- envOrDefault() reads FORGE_API_URL and FORGE_API_TOKEN from env with flag override -- 12-factor compatible
- getNoAuth() method for /v1/healthz endpoint -- per OpenAPI spec security:[] designation
- Client-side --since filter using UTC timestamps -- consistent with server's ISO 8601 Z-suffix format
- Routes verify compares routes vs running sandboxes and exits 1 on drift -- enables monitoring integration via exit code

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] UTC timestamp mismatch in events --since filter**
- **Found during:** Task 2 (Events command)
- **Issue:** Test used time.Now() (local tz) with literal "Z" suffix, but time.Parse treated "Z" as literal producing UTC times. Comparison between local sinceTime and UTC eventTime caused incorrect filtering.
- **Fix:** Used time.Now().UTC() in both test data generation and sinceTime calculation for consistent UTC comparison.
- **Files modified:** cli/cmd/forge/events.go, cli/cmd/forge/events_test.go
- **Verification:** TestEventsWithSinceFilter passes -- old events filtered, recent events kept
- **Committed in:** 7e02d37 (Task 2 GREEN commit)

---

**Total deviations:** 1 auto-fixed (1 bug fix)
**Impact on plan:** Essential correctness fix for timezone handling. No scope creep.

## Issues Encountered
None beyond the UTC timestamp fix documented above.

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- CLI binary builds and all 23 tests pass
- Ready for ops use once control plane is deployed
- Future enhancement: shell completions, --output=json mode for all commands

## Self-Check: PASSED

- All 16 files verified present
- All 4 commits verified in git log

---
*Phase: 06-cli-sdk-integration*
*Completed: 2026-04-16*
