---
phase: 03-control-plane-api
plan: 05
subsystem: control-plane
tags: [api, agent, long-poll, hmac, file-push, tdd]
dependency_graph:
  requires: [03-03, 03-04]
  provides: [agent-poll, agent-ack, agent-events, file-push-redirect]
  affects: [control/internal/api]
tech_stack:
  added: [shared-go/auth]
  patterns: [long-poll, hmac-signed-redirect, interface-per-handler]
key_files:
  created:
    - control/internal/api/agents.go
  modified:
    - control/internal/api/agents_test.go
    - control/internal/api/server.go
    - control/go.mod
decisions:
  - "Poll loop uses 1s ticker with immediate first check for responsiveness"
  - "FilePushStore as separate interface from SandboxReader to keep handler dependencies minimal"
  - "UpdateSandboxLastActive on file push is non-fatal (warn + continue)"
metrics:
  duration: 463s
  completed: "2026-04-15T22:43:44Z"
  tasks: 2
  tests: 18
  files_created: 1
  files_modified: 3
---

# Phase 03 Plan 05: Agent Endpoints Summary

Long-poll command dispatch, command ack, container event reporting, and HMAC-signed file push redirect -- completing the agent-control plane communication loop with 18 httptest-based tests.

## What Was Built

### Task 1: Long-poll, Ack, and Event Handlers

**Long-poll (GET /v1/agents/{id}/commands):**
- Holds connection for up to `wait` seconds (default 30, clamped 1-60)
- Immediate return when pending commands exist (no unnecessary waiting)
- Returns `{"commands": []}` on timeout
- Poll loop: immediate first check, then 1s ticker intervals
- Maps store.Command to API response format (id, type, sandbox_id, payload, issued_at, timeout_seconds)

**Ack (POST /v1/agents/{id}/commands/{cmd_id}/ack):**
- Accepts `{status, error, result}` body
- Looks up command to get sandbox_id and command_type
- Delegates to lifecycle.HandleAck for state machine transitions
- Returns 200 on success, 404 for unknown command

**Event (POST /v1/agents/{id}/events):**
- Accepts `{sandbox_id, event_type, container_id, exit_code, payload}`
- Validates required fields (sandbox_id, event_type)
- Delegates to lifecycle.HandleEvent for state machine transitions
- Returns 200 on success, 400 for missing required fields

### Task 2: File Push Redirect

**File Push (POST /v1/sandboxes/{id}/files):**
- Looks up sandbox -> node (tailscale_ip, agent_listen_port)
- Generates HMAC-signed URL via `auth.SignURL` with 60s expiry
- Returns 307 Temporary Redirect with Location header
- Updates `last_active_at` to prevent idle reaping
- Returns 404 (unknown sandbox), 503 (not yet scheduled), 400 (invalid ID)

## Interfaces Added

```go
// AgentStore: PollPendingCommands, GetCommand
// AgentLifecycle: HandleAck, HandleEvent
// FilePushStore: GetSandbox, GetNode, UpdateSandboxLastActive
```

Server struct extended with `agentStore`, `agentLifecycle`, `filePushStore` fields and `hmacSecret` in `serverConfig`.

## Test Coverage

| Test | Behavior |
|------|----------|
| TestPollCommands_WithPendingCommands | Returns immediately with commands |
| TestPollCommands_Timeout_ReturnsEmpty | Waits ~1s, returns empty array |
| TestPollCommands_WaitClamped | wait=120 clamped to 60 |
| TestPollCommands_DefaultWait | No wait param defaults to 30s |
| TestPollCommands_InvalidNodeID | Returns 400 |
| TestAckCommand_Success | Calls HandleAck with correct params |
| TestAckCommand_Failure | Passes failure status through |
| TestAckCommand_InvalidCmdID | Returns 400 |
| TestReportEvent_Success | Calls HandleEvent with correct params |
| TestReportEvent_MissingSandboxID | Returns 400 |
| TestReportEvent_MissingEventType | Returns 400 |
| TestReportEvent_InvalidNodeID | Returns 400 |
| TestFilePush_Success_307Redirect | 307 + valid signed URL |
| TestFilePush_SandboxNotFound | Returns 404 |
| TestFilePush_SandboxNotScheduled | Returns 503 |
| TestFilePush_InvalidSandboxID | Returns 400 |
| TestFilePush_SignedURL_Has60sExpiry | Expires within 55-65s window |
| TestFilePush_UpdatesLastActive | Calls UpdateSandboxLastActive |

All 18 new tests + 34 existing tests = 52 total, all passing with `-race`.

## Commits

| Hash | Type | Description |
|------|------|-------------|
| d082b34 | test | Failing tests for long-poll, ack, event (RED) |
| 3cd34a4 | feat | Implement long-poll, ack, event handlers (GREEN) |
| d11536a | test | Failing tests for file push redirect (RED) |
| 39d10bb | feat | Implement file push 307 redirect with HMAC (GREEN) |

## Deviations from Plan

None -- plan executed exactly as written.

## Threat Mitigations Applied

| Threat ID | Mitigation |
|-----------|------------|
| T-03-14 | HMAC-signed URL with 60s expiry; only control plane signs |
| T-03-15 | HMAC covers full URL path+query via auth.SignURL |
| T-03-16 | Wait param capped at 60s; context timeout prevents goroutine leak |

## TDD Gate Compliance

- RED gate: `test(03-05)` commits d082b34 and d11536a (failing tests written first)
- GREEN gate: `feat(03-05)` commits 3cd34a4 and 39d10bb (implementation passes tests)
- REFACTOR gate: not needed (code is clean)

## Self-Check: PASSED

- All 3 key files exist on disk
- All 4 commit hashes verified in git log
