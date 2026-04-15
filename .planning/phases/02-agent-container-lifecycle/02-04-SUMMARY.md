---
phase: 02-agent-container-lifecycle
plan: 04
subsystem: agent
tags: [go, http-client, heartbeat, control-plane, tdd]

requires:
  - phase: 02-01
    provides: agent module scaffold with config and port allocator

provides:
  - Control plane HTTP client with 5 operations (Register, Heartbeat, PollCommands, AckCommand, ReportEvent)
  - Heartbeat sender goroutine with configurable interval
  - HeartbeatClient and ResourceCollector interfaces for decoupled dependency injection

affects: [02-05-command-executor, 02-06-agent-main, 03-control-plane]

tech-stack:
  added: []
  patterns:
    - "httptest.NewServer for mock control plane in tests"
    - "sync.RWMutex protecting shared nodeID/token state"
    - "Interface-based dependency injection (HeartbeatClient, ResourceCollector)"
    - "time.Ticker for consistent heartbeat intervals"
    - "Exponential backoff on 5xx registration failures"

key-files:
  created:
    - agent/internal/controlclient/types.go
    - agent/internal/controlclient/client.go
    - agent/internal/controlclient/client_test.go
    - agent/internal/health/heartbeat.go
    - agent/internal/health/heartbeat_test.go

key-decisions:
  - "PollCommands creates dedicated http.Client per call with wait+5s timeout rather than modifying shared client"
  - "Heartbeat sender uses interface-based HeartbeatClient and ResourceCollector for testability"
  - "doAuthRequest helper centralizes bearer token injection for all authenticated endpoints"
  - "Re-registration on 401 retries the original request once with new credentials"
  - "Agent token stored in memory only, protected by RWMutex, never logged"

patterns-established:
  - "Mock HTTP server pattern: httptest.NewServer with handler routing by URL path"
  - "Heartbeat interface pattern: HeartbeatClient + ResourceCollector decouples health from transport"
  - "Auth retry pattern: on 401, re-register then retry original request once"

requirements-completed: [AGNT-02, AGNT-03, AGNT-04]

duration: 5min
completed: 2026-04-15
---

# Phase 02 Plan 04: Control Client & Heartbeat Summary

**HTTP client for all 5 control plane operations with automatic re-registration on auth failures, plus heartbeat sender goroutine with configurable interval and error resilience**

## Performance

- **Duration:** 5 min
- **Started:** 2026-04-15T20:52:21Z
- **Completed:** 2026-04-15T20:58:10Z
- **Tasks:** 2/2
- **Files created:** 5

## Accomplishments

### Task 1: Control plane HTTP client with TDD
- Created request/response types matching agent-protocol.md and OpenAPI spec
- Implemented Client with Register, Heartbeat, PollCommands, AckCommand, ReportEvent
- Register retries on 5xx with exponential backoff (1s, 2s, 4s, 8s, max 5 attempts)
- Heartbeat re-registers on 404 (node evicted from control plane)
- PollCommands uses wait+5s HTTP timeout per spec
- All authenticated requests include Authorization: Bearer header
- Automatic re-registration on 401 Unauthorized from any endpoint
- ReportEvent is fire-and-forget with single retry on error
- 12 tests passing with -race flag

### Task 2: Heartbeat sender goroutine with TDD
- HeartbeatSender.Start() blocks on time.Ticker loop, sends heartbeat each tick
- Calls ResourceCollector.Collect() for current used_mb and running_containers
- Errors logged as warnings but do not stop the sender (per protocol spec)
- Stops cleanly on context cancellation
- Interface-based design: HeartbeatClient + ResourceCollector for testability
- 5 tests passing with -race flag

## Deviations from Plan

None - plan executed exactly as written.

## TDD Gate Compliance

- RED: `test(02-04): add failing control client tests` (49c60d6) - compilation failure, no NewClient
- GREEN: `feat(02-04): implement control client for all 5 control plane operations` (2030f4f) - 12 tests pass
- RED: `test(02-04): add failing heartbeat sender tests` (565820e) - compilation failure, no NewHeartbeatSender
- GREEN: `feat(02-04): implement heartbeat sender goroutine` (cab87d9) - 5 tests pass

## Verification

```
17 tests passing with -race:
  controlclient: 12 tests (15.1s total, 10s from timeout test)
  health: 5 tests (1.9s total)
  go vet: clean
```

## Self-Check: PASSED

All 5 created files verified on disk. All 4 commit hashes verified in git log.
