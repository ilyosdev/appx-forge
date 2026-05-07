# Phase 3: Control Plane API - Context

**Gathered:** 2026-04-16
**Status:** Ready for planning
**Mode:** Auto-generated (infrastructure/code phase — discuss skipped)

<domain>
## Phase Boundary

A running Go HTTP API that accepts sandbox requests, schedules them to nodes, dispatches commands to agents via long-poll, and transitions sandbox state based on agent reports. Includes: chi router, all REST endpoints from OpenAPI spec, bin-packing scheduler, long-poll command dispatch, node registration/heartbeat/unhealthy detection, file push redirect with HMAC, bearer token auth, health endpoint, and docker-compose.dev.yml for local development.

</domain>

<decisions>
## Implementation Decisions

### Claude's Discretion
All implementation choices are at Claude's discretion. Use ROADMAP phase goal, success criteria, docs/contracts/control-api.openapi.yaml as source of truth.

**TDD mandate from user:** Write concrete tests FIRST for all Go code.

### From Phase 1
- Postgres schema + sqlc-generated store code in control/internal/store/
- State machine CAS transitions: `UPDATE WHERE state = $expected`
- OpenAPI spec at docs/contracts/control-api.openapi.yaml (16 operationIds)
- HMAC signed URL utils in shared-go/auth/hmac.go

### From Phase 2
- Agent control client expects: POST /v1/nodes/register, POST /v1/nodes/:id/heartbeat, GET /v1/agents/:id/commands, POST /v1/agents/:id/commands/:cmd_id/ack, POST /v1/agents/:id/events
- Agent file push endpoint validates HMAC signed URLs
- Command types: start_sandbox, stop_sandbox, restart_sandbox, get_logs, prune

</decisions>

<code_context>
## Existing Code Insights

### Reusable Assets
- control/internal/store/ — sqlc-generated queries (17 queries): CreateNode, CreateSandbox, TransitionSandboxState, PollPendingCommands, etc.
- control/migrations/ — 4 goose migrations (nodes, sandboxes, events, commands)
- control/tests/ — testcontainers-go test helpers with snapshot/restore
- shared-go/models/ — SandboxState, NodeStatus, CommandType types
- shared-go/auth/ — SignURL/VerifyURL for file push redirect
- docs/contracts/control-api.openapi.yaml — Full API spec

### Established Patterns
- Go workspace with go.work
- chi for HTTP routing (per CLAUDE_CODE_KICKOFF.md)
- pgx/v5 for Postgres
- slog for structured logging
- envconfig for config
- testcontainers-go for integration tests

### Integration Points
- Control plane serves HTTP API that agent polls
- Agent registers, heartbeats, polls commands, acks, reports events
- File push: control plane returns 307 redirect to agent with signed URL
- Caddy proxy will consume routing table (Phase 4)

</code_context>

<specifics>
## Specific Ideas

- cmd/forge-control/main.go — binary entrypoint
- control/internal/api/ — HTTP handlers (chi)
- control/internal/scheduler/ — bin-packing (pick node with most free RAM)
- control/internal/lifecycle/ — sandbox lifecycle (state transitions + command dispatch)
- control/internal/routing/ — routing table management (prepare for Phase 4 Caddy)
- docker-compose.dev.yml at repo root — Postgres + control + 1 agent

</specifics>

<deferred>
## Deferred Ideas

None — all 12 CTRL requirements in scope.

</deferred>
