# Phase 2: Agent & Container Lifecycle - Context

**Gathered:** 2026-04-16
**Status:** Ready for planning
**Mode:** Auto-generated (infrastructure phase — discuss skipped)

<domain>
## Phase Boundary

A Go agent binary runs as a systemd service on a node, creates Docker containers with proper security settings, and reliably reports container events. This covers: agent registration, heartbeat, Docker SDK container management, event stream watching with timestamp-based reconnection, file push HTTP endpoint, port allocation, image pre-pull, log retrieval, and UID/GID bind mount setup.

</domain>

<decisions>
## Implementation Decisions

### Claude's Discretion
All implementation choices are at Claude's discretion — infrastructure/code phase. Use ROADMAP phase goal, success criteria, STARTER_PLAN.md agent section, and the contracts from Phase 1 (docs/contracts/agent-protocol.md, filepush-protocol.md) as source of truth.

**TDD mandate from user:** Write concrete tests FIRST for all Go code. Tests must pass before moving to next phase.

### From Phase 1
- State machine types in shared-go/models/sandbox.go (8 states, 9 events, ValidTransitions map)
- HMAC signed URL utilities in shared-go/auth/hmac.go (SignURL/VerifyURL)
- Agent protocol defined in docs/contracts/agent-protocol.md
- File push protocol defined in docs/contracts/filepush-protocol.md
- Docker Engine 27.x required (verified by deploy/scripts/verify-docker.sh)

</decisions>

<code_context>
## Existing Code Insights

### Reusable Assets
- shared-go/models/ — SandboxState, SandboxEvent, NodeStatus, CommandType types
- shared-go/auth/hmac.go — SignURL/VerifyURL for file push endpoint auth
- docs/contracts/agent-protocol.md — Registration, heartbeat, long-poll, event reporting
- docs/contracts/filepush-protocol.md — Signed URL validation, file write to bind mount
- control/go.mod — Has Docker SDK dependency (moby/moby v27.5.1+incompatible)

### Established Patterns
- Go workspace: go.work with shared-go and control modules
- Testing: stdlib testing, testcontainers-go for integration tests
- Coding: slog for logging, stdlib net/http, envconfig for config

### Integration Points
- Agent registers with control plane (POST /v1/nodes/register)
- Agent long-polls for commands (GET /v1/agents/:id/commands)
- Agent reports events (POST /v1/agents/:id/events)
- Agent heartbeats (POST /v1/nodes/:id/heartbeat)
- File push endpoint validates HMAC signed URLs from control plane

</code_context>

<specifics>
## Specific Ideas

- Agent binary: cmd/forge-agent/main.go
- Docker SDK wrapper: agent/internal/docker/ (create, start, stop, remove, inspect)
- Events watcher: agent/internal/events/ (Docker events stream with Since reconnect)
- Control client: agent/internal/controlclient/ (register, heartbeat, poll commands, report events)
- File push: agent/internal/filepush/ (HTTP server, signed URL validation, write to bind mount)
- Port allocator: agent/internal/ports/ (allocate from 40000-50000 range)
- Health: agent/internal/health/ (heartbeat sender goroutine)
- Systemd unit file: deploy/forge-agent.service

</specifics>

<deferred>
## Deferred Ideas

None — all 12 AGNT requirements are in scope.

</deferred>
