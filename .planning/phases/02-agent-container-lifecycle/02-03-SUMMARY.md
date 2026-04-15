---
phase: 02-agent-container-lifecycle
plan: 03
subsystem: agent
tags: [go, docker-events, reconnection, image-pull, container-logs, tdd]

# Dependency graph
requires:
  - phase: 02-agent-container-lifecycle
    plan: 02
    provides: "Docker Client interface with EventsStream, PullImage, GetLogs methods"
provides:
  - "Events Watcher with timestamp-based reconnection and exponential backoff"
  - "ImagePuller with retry and periodic re-pull support"
  - "LogReader with tail and follow mode support"
affects: [02-04, 02-06]

# Tech tracking
tech-stack:
  added: []
  patterns: [EventSource interface for mock injection, exponential backoff with capped max, interface-per-concern for testability]

key-files:
  created:
    - agent/internal/events/watcher.go
    - agent/internal/events/watcher_test.go
    - agent/internal/docker/images.go
    - agent/internal/docker/images_test.go
    - agent/internal/docker/logs.go
    - agent/internal/docker/logs_test.go
  modified: []

key-decisions:
  - "EventSource interface decouples Watcher from Docker client for testability"
  - "ImagePullClient and LogClient are narrow interfaces (one method each) for focused mocking"
  - "Watcher tracks lastEventTime and passes as Since on reconnect to avoid missing events"

patterns-established:
  - "EventSource mock: scenario-based mock returns pre-configured event/error channels per call"
  - "Narrow client interfaces: ImagePullClient, LogClient wrap single Docker Client methods"
  - "Backoff pattern: base * 2^attempt with configurable max, exposed fields for test override"

requirements-completed: [AGNT-06, AGNT-07, AGNT-10, AGNT-11]

# Metrics
duration: 2min
completed: 2026-04-15
---

# Phase 02 Plan 03: Events Watcher, Image Pull, Log Retrieval Summary

**Docker events watcher with Since-timestamp reconnection and exponential backoff, image pre-puller with retry, and log reader with tail/follow -- all TDD with 14 tests passing**

## Performance

- **Duration:** 2 min
- **Started:** 2026-04-15T21:33:09Z
- **Completed:** 2026-04-15T21:35:15Z
- **Tasks:** 2
- **Files modified:** 6

## Accomplishments
- Events Watcher filters forge- prefix containers, maps Docker actions to SandboxEvent types, reconnects with Since=lastEventTime on stream error, and applies exponential backoff (1s base, 2x multiplier, 30s max)
- ImagePuller with PullOnce (retry with exponential backoff) and StartPeriodicPull (goroutine with ticker)
- LogReader wraps Docker GetLogs with tail count and follow mode passthrough

## Task Commits

Each task was committed atomically:

1. **Task 1a: Events watcher tests (RED)** - `70f25de` (test) - 8 failing test functions
2. **Task 1b: Events watcher implementation (GREEN)** - `9429adb` (feat) - All 8 tests pass
3. **Task 2a: Image pull and log tests (RED)** - `5bd4a20` (test) - 6 failing test functions
4. **Task 2b: Image pull and log implementation (GREEN)** - `80877fe` (feat) - All 6 tests pass

## Files Created/Modified
- `agent/internal/events/watcher.go` - Watcher struct with Watch() loop, processStream, mapEvent, exponential backoff reconnection
- `agent/internal/events/watcher_test.go` - 8 tests: die, oom, start events, forge- prefix filter, app name extraction, Since reconnect, exponential backoff, context cancellation
- `agent/internal/docker/images.go` - ImagePuller with PullOnce (retry) and StartPeriodicPull (ticker goroutine)
- `agent/internal/docker/images_test.go` - 3 tests: successful pull, retry on failure, periodic pull
- `agent/internal/docker/logs.go` - LogReader with ReadLogs delegating to Docker client
- `agent/internal/docker/logs_test.go` - 3 tests: tail mode, follow mode, ReadCloser consumption

## Decisions Made
- EventSource interface (single method: EventsStream) decouples Watcher from full Docker Client for focused testing
- ImagePullClient and LogClient are narrow single-method interfaces rather than requiring the full Client -- follows interface segregation principle
- Watcher tracks lastEventTime in the loop and passes it as Since on reconnect -- the #1 pitfall from research (ensures no missed events)

## Deviations from Plan

None - plan executed exactly as written. Tasks 1 and 2 RED phases were committed by a prior execution; this session completed the Task 2 GREEN phase.

## Issues Encountered
None.

## User Setup Required
None - no external service configuration required.

## TDD Gate Compliance

- Task 1 RED gate: `70f25de` (test commit with 8 watcher tests)
- Task 1 GREEN gate: `9429adb` (feat commit with all 8 tests passing)
- Task 2 RED gate: `5bd4a20` (test commit with 6 image/log tests)
- Task 2 GREEN gate: `80877fe` (feat commit with all 6 tests passing)

## Next Phase Readiness
- Events Watcher ready for integration into agent startup sequence
- ImagePuller ready for agent startup (pre-pull sandbox image on registration)
- LogReader ready for use by command executor (get_logs command type)
- All 14 tests pass with -race flag and go vet reports no issues

## Self-Check: PASSED

- All 6 created files verified present on disk
- All 4 commits verified in git log (70f25de, 9429adb, 5bd4a20, 80877fe)

---
*Phase: 02-agent-container-lifecycle*
*Completed: 2026-04-15*
