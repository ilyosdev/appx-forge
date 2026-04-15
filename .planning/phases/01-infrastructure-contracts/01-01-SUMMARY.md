---
phase: 01-infrastructure-contracts
plan: 01
subsystem: infra
tags: [go, state-machine, hmac, tdd, workspace]

# Dependency graph
requires: []
provides:
  - Go workspace with shared-go and control modules
  - Sandbox state machine (8 states, 9 events, 10 transitions) with property tests
  - HMAC-SHA256 signed URL utilities (SignURL/VerifyURL) with TDD tests
  - Shared model types (SandboxState, SandboxEvent, NodeStatus, CommandType, CommandStatus)
affects: [01-02, 01-03, 01-04, 01-05, 02-agent, 03-control-plane, 06-sdk]

# Tech tracking
tech-stack:
  added: [go-1.26.2, google/uuid-v1.6.0, pgx/v5-v5.9.1, chi/v5-v5.2.5, envconfig-v1.4.0, testcontainers-go-v0.42.0]
  patterns: [go-workspace, map-based-state-machine, hmac-signed-urls, tdd-red-green]

key-files:
  created:
    - go.work
    - shared-go/go.mod
    - shared-go/models/sandbox.go
    - shared-go/models/sandbox_test.go
    - shared-go/models/node.go
    - shared-go/models/command.go
    - shared-go/models/route.go
    - shared-go/auth/hmac.go
    - shared-go/auth/hmac_test.go
    - control/go.mod
    - control/cmd/forge-control/main.go
  modified: []

key-decisions:
  - "Go 1.26.2 installed (Homebrew latest) instead of 1.25.x -- backwards compatible, go.work uses go 1.25.0 directive"
  - "HMAC SignURL/VerifyURL uses URL-based API (sign full URL path+query) rather than sandboxID-based signing from research doc -- better for threat model T-01-02"
  - "VerifyURL strips both sig and expires params from returned URL -- prevents forwarding signed URLs (T-01-02 mitigation)"

patterns-established:
  - "State machine: map[State]map[Event]State transition table with NextState() lookup"
  - "HMAC signed URLs: SignURL appends expires+sig, VerifyURL validates with hmac.Equal constant-time comparison"
  - "TDD: test commit (RED) before feat commit (GREEN) for new implementations"

requirements-completed: [CNTR-06]

# Metrics
duration: 7min
completed: 2026-04-15
---

# Phase 01 Plan 01: Shared Go Types & State Machine Summary

**Go workspace with map-based sandbox state machine (8 states, 10 transitions) and HMAC-SHA256 signed URL utilities, fully TDD-tested**

## Performance

- **Duration:** 7 min
- **Started:** 2026-04-15T19:14:06Z
- **Completed:** 2026-04-15T19:21:27Z
- **Tasks:** 2
- **Files created:** 11

## Accomplishments
- Go workspace scaffolded with shared-go and control modules, all dependencies resolved
- Sandbox state machine with 8 states, 9 events, complete transition table verified by 9 property tests (reachability, terminality, destroy-from-any-state, invalid rejection, valid transitions)
- HMAC-SHA256 signed URL generation and verification with 6 tests covering sign/verify, tampering, expiry, wrong key, URL structure
- TDD commit pattern: test(RED) -> feat(GREEN) visible in git log for HMAC implementation

## Task Commits

Each task was committed atomically:

1. **Task 1: Scaffold Go workspace and shared-go module** - `033eec5` (chore)
2. **Task 2a: State machine property tests** - `613c237` (test)
3. **Task 2b: Failing HMAC tests** - `a6a6746` (test -- RED)
4. **Task 2c: HMAC implementation** - `8dd4539` (feat -- GREEN)

## TDD Gate Compliance

- RED gate: `a6a6746` test(01-01) commit -- HMAC tests fail (SignURL/VerifyURL undefined)
- GREEN gate: `8dd4539` feat(01-01) commit -- HMAC implementation makes all tests pass
- State machine tests pass immediately (implementation created in Task 1) -- acknowledged by plan as acceptable for foundational types

## Files Created/Modified
- `go.work` - Go workspace root with shared-go and control modules
- `shared-go/go.mod` - shared-go module with uuid dependency
- `shared-go/models/sandbox.go` - SandboxState (8), SandboxEvent (9), ValidTransitions map, NextState, AllStates, AllEvents, IsTerminal
- `shared-go/models/sandbox_test.go` - 9 property tests for state machine
- `shared-go/models/node.go` - NodeStatus type with 4 constants
- `shared-go/models/command.go` - CommandType (5) and CommandStatus (4) types
- `shared-go/models/route.go` - Package placeholder for sqlc-generated route models
- `shared-go/auth/hmac.go` - SignURL and VerifyURL with hmac.Equal constant-time comparison
- `shared-go/auth/hmac_test.go` - 6 HMAC tests covering sign/verify/tamper/expiry/wrong-key
- `control/go.mod` - control module with pgx, chi, envconfig, testcontainers-go dependencies
- `control/cmd/forge-control/main.go` - Placeholder entry point

## Decisions Made
- Go 1.26.2 installed (Homebrew latest) rather than pinning 1.25.x -- go.work directive uses 1.25.0 for compatibility; 1.26 is backwards compatible
- HMAC API uses full URL signing (path + query) rather than sandboxID-based approach from research -- provides stronger security guarantees and cleaner API for file-push protocol
- VerifyURL strips both sig and expires params from returned URL to prevent forwarding signed URLs (threat model T-01-02)
- Used `t.Fatal` instead of `t.Error` in expiry test to prevent nil pointer dereference on subsequent assertions

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Go not installed on system**
- **Found during:** Task 1 (workspace scaffolding)
- **Issue:** Go binary not found on PATH -- prerequisite for entire plan
- **Fix:** Installed Go 1.26.2 via Homebrew (latest available, compatible with go 1.25 directive)
- **Verification:** `go version` returns go1.26.2 darwin/arm64

**2. [Rule 1 - Bug] Expired URL test had nil pointer dereference**
- **Found during:** Task 2 (HMAC tests GREEN phase)
- **Issue:** Test used 0 duration expiry + 10ms sleep, but Unix timestamps have second granularity. When VerifyURL didn't reject (same second), test hit `err.Error()` on nil err causing panic
- **Fix:** Changed to -1s expiry (guaranteed expired) and used `t.Fatal` instead of `t.Error` for the nil check
- **Files modified:** shared-go/auth/hmac_test.go
- **Committed in:** 8dd4539 (part of GREEN commit)

---

**Total deviations:** 2 auto-fixed (1 blocking, 1 bug)
**Impact on plan:** Both fixes were necessary for execution. No scope creep.

## Issues Encountered
- Control module `go vet ./...` initially failed with "no packages to vet" because no Go source files existed. Added a placeholder main.go entry point.

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- shared-go module ready for import by control, agent, proxy, cli modules
- State machine types available for Postgres schema (Plan 01-02 onwards)
- HMAC utilities available for file-push protocol implementation
- Control module scaffolded with all dependencies for Postgres schema + migration work

## Self-Check: PASSED

All 11 files verified present. All 4 commits verified in git log.

---
*Phase: 01-infrastructure-contracts*
*Completed: 2026-04-15*
