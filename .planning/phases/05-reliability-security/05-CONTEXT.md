# Phase 5: Reliability & Security - Context

**Gathered:** 2026-04-16
**Status:** Ready for planning
**Mode:** Auto-generated (infrastructure/code phase — discuss skipped)

<domain>
## Phase Boundary

Sandboxes self-heal from crashes, idle resources are reclaimed, containers are hardened with seccomp/capabilities, and the system exposes health and metrics endpoints. Covers: auto-restart with exponential backoff (3 retries then FAILED), idle reaping (30min timeout), routing drift detector (60s Caddy vs Postgres diff), seccomp profile + capability dropping + no-new-privileges + PID limit, agent token scoping, file push signed URL security, and Prometheus /metrics endpoint.

</domain>

<decisions>
## Implementation Decisions

### Claude's Discretion
All implementation choices at Claude's discretion. Use ROADMAP success criteria as the spec.

**TDD mandate:** Write tests FIRST for all Go code.

### From Prior Phases
- Container security params already in agent/internal/docker/sandbox.go (seccomp, cap-drop, PID limit)
- HMAC signed URLs already in shared-go/auth/hmac.go
- Agent token created during node registration (Phase 3)
- Lifecycle service handles state transitions (Phase 3)
- Route batcher with 500ms debounce (Phase 4)
- Caddy client with ListRoutes for drift detection (Phase 4)

</decisions>

<code_context>
## Existing Code Insights

### Reusable Assets
- control/internal/lifecycle/ — State transitions, can add restart/idle logic
- control/internal/routing/ — CaddyClient.ListRoutes for drift detection
- agent/internal/docker/sandbox.go — Security params already defined
- shared-go/auth/ — HMAC already implemented

### Integration Points
- Auto-restart: lifecycle handles container_exited events, adds backoff logic
- Idle reaping: background goroutine checks last_active_at
- Drift detector: background goroutine diffs Caddy ListRoutes vs Postgres
- Metrics: prometheus/client_golang on /metrics endpoint

</code_context>

<specifics>
## Specific Ideas

- control/internal/lifecycle/ — Add restart backoff, idle reaping
- control/internal/routing/drift.go — Drift detector goroutine
- control/internal/api/metrics.go — Prometheus /metrics handler
- deploy/seccomp-default.json — Seccomp profile for sandbox containers

</specifics>

<deferred>
## Deferred Ideas

None — all 10 requirements in scope.

</deferred>
