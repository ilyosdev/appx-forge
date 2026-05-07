---
phase: 06-cli-sdk-integration
plan: 01
subsystem: control-plane-api
tags: [api, handlers, tdd, nodes, routes, events, logs]
dependency_graph:
  requires: []
  provides: [node-list-api, node-drain-api, node-remove-api, route-list-api, event-list-api, log-proxy-api]
  affects: [cli, sdk, appx-api]
tech_stack:
  added: []
  patterns: [interface-based-dependency-injection, store-adapter-pattern, proxy-handler]
key_files:
  created:
    - control/internal/api/events.go
    - control/internal/api/events_test.go
    - control/internal/api/routes_handlers.go
    - control/internal/api/routes_handlers_test.go
    - control/internal/api/logs.go
    - control/internal/api/logs_test.go
  modified:
    - control/internal/api/nodes.go
    - control/internal/api/nodes_test.go
    - control/internal/api/server.go
    - control/internal/api/routes.go
    - control/internal/store/queries/nodes.sql
    - control/internal/store/queries/events.sql
    - control/internal/store/nodes.sql.go
    - control/internal/store/events.sql.go
    - control/cmd/forge-control/main.go
    - control/tests/integration_test.go
decisions:
  - "nodeResponse struct omits agent_token field rather than filtering at serialization time (T-06-01)"
  - "LogProxyStore reuses existing filePushAdapter since it already implements GetSandbox and GetNode with correct return types"
  - "60-second HTTP client timeout on log proxy prevents indefinite connection holds (T-06-02)"
  - "EventStore as separate interface from AgentStore for event reading vs event writing separation"
metrics:
  duration: 843s
  completed: "2026-04-16T00:55:00Z"
  tasks: 2
  files: 16
---

# Phase 06 Plan 01: Control Plane API Endpoints Summary

Six new API handlers for CLI/SDK ops endpoints with complete TDD coverage and threat mitigations.

## What Was Done

### Task 1: Node List/Drain/Remove + Events + Routes Handlers

**RED:** Wrote 20 failing tests across 3 test files covering all handler behaviors.

**GREEN:** Implemented 5 handlers and 3 new interfaces:
- `handleListNodes` (GET /v1/nodes): Lists all registered nodes with status, capacity, sandbox count. Maps store.Node to nodeResponse struct that intentionally omits agent_token (T-06-01 mitigation).
- `handleDrainNode` (POST /v1/nodes/{id}/drain): Verifies node exists, sets status to "draining".
- `handleRemoveNode` (DELETE /v1/nodes/{id}): Verifies node exists, checks CountActiveSandboxesByNode > 0 returns 409 Conflict, otherwise sets status "removed" (T-06-03 mitigation).
- `handleListRoutes` (GET /v1/routes): Fetches routes from Caddy via RouteListFetcher interface.
- `handleListEvents` (GET /v1/events): Dispatches to ListEventsBySandbox, ListEventsByType, or ListRecentEvents based on query params. Default limit 100, max 1000.

Added sqlc queries: `CountActiveSandboxesByNode`, `ListRecentEvents`.

Extended NodeStore interface with `ListNodes`, `UpdateNodeStatus`, `CountActiveSandboxesByNode`.

### Task 2: Sandbox Log Proxy Handler

**RED:** Wrote 5 failing tests using httptest.Server as mock agent.

**GREEN:** Implemented `handleGetLogs` (GET /v1/sandboxes/{id}/logs):
- Resolves sandbox -> node -> constructs agent URL: `http://{tailscale_ip}:{agent_listen_port}/sandboxes/{id}/logs`
- Forwards tail and follow query parameters
- Returns 404 for missing sandbox, 503 for unassigned node, 502 for agent unreachable
- 60-second timeout on proxy HTTP client (T-06-02 mitigation)
- Streams response body with text/plain content type

## Deviations from Plan

None -- plan executed exactly as written.

## Decisions Made

1. **nodeResponse struct for token exclusion**: Rather than filtering agent_token at JSON serialization time (which risks accidental exposure if someone adds `json:"-"` and later removes it), the nodeResponse struct simply does not have an AgentToken field. The mapping function storeNodeToResponse copies only safe fields.

2. **filePushAdapter reuse for LogProxyStore**: The existing filePushAdapter already satisfies LogProxyStore (GetSandbox + GetNode returning store.Node), so no new adapter was needed.

3. **60s HTTP client timeout**: The log proxy creates an HTTP request to the agent with a 60-second timeout to prevent indefinite connection holds when follow=true (T-06-02).

4. **EventStore separate from AgentStore**: Event reading (ListEventsBySandbox, ListEventsByType, ListRecentEvents) is a separate interface from AgentStore (which handles event writing via HandleEvent). This follows the read/write separation pattern already established in the codebase.

## TDD Gate Compliance

- RED gate: `370c04a` test(06-01) -- Task 1 failing tests
- GREEN gate: `acd4a40` feat(06-01) -- Task 1 implementation
- RED gate: `8f1281c` test(06-01) -- Task 2 failing tests
- GREEN gate: `dcc1079` feat(06-01) -- Task 2 implementation

All gates present and in correct order.

## Test Results

72 tests pass in `./internal/api/...` (20 new + 52 existing):

New tests:
- TestListNodes_ReturnsNodeList, TestListNodes_EmptyList
- TestDrainNode_ValidRequest, TestDrainNode_UnknownNode
- TestRemoveNode_ZeroSandboxes, TestRemoveNode_HasActiveSandboxes, TestRemoveNode_UnknownNode
- TestListRoutes_ReturnsRoutes, TestListRoutes_EmptyList, TestListRoutes_FetcherError
- TestListEvents_BySandboxID, TestListEvents_ByEventType, TestListEvents_NoFilters_DefaultLimit, TestListEvents_CustomLimit, TestListEvents_StoreError
- TestGetLogs_ValidSandbox, TestGetLogs_TailParam, TestGetLogs_UnknownSandbox, TestGetLogs_SandboxNotAssigned, TestGetLogs_ConstructsCorrectAgentURL

`go vet ./...` clean.

## Self-Check: PASSED

All created files exist. All commit hashes verified in git log.
