# Phase 4: Proxy, Routing & File Push - Context

**Gathered:** 2026-04-16
**Status:** Ready for planning
**Mode:** Auto-generated (infrastructure/code phase — discuss skipped)

<domain>
## Phase Boundary

Sandboxes are accessible via `{id}.myappx.live` URLs with WebSocket support, and route updates happen without dropping existing connections. This covers: Caddy base config with Cloudflare Origin CA, Go client for Caddy Admin API, route add/remove on sandbox state changes, route update batching with 500ms debounce, Cloudflare wildcard DNS verification, and full E2E flow testing.

</domain>

<decisions>
## Implementation Decisions

### Claude's Discretion
All implementation choices at Claude's discretion. Use docs/contracts/proxy-routing.md as source of truth.

**TDD mandate:** Write tests FIRST for all Go code.

### From Prior Phases
- Control plane already has route management placeholder (CTRL-10 in requirements)
- Caddy Admin API shapes documented in docs/contracts/proxy-routing.md
- Sandbox lifecycle events trigger route add/remove
- Cloudflare Origin CA cert for TLS termination
- 500ms debounce for route updates to prevent WebSocket drops (from PITFALLS research)

</decisions>

<code_context>
## Existing Code Insights

### Reusable Assets
- control/internal/api/ — Existing HTTP handlers, chi router
- control/internal/lifecycle/ — Sandbox lifecycle triggers route events
- docs/contracts/proxy-routing.md — Caddy Admin API shapes, @id routes, drift detection
- shared-go/models/sandbox.go — SandboxState for route triggers

### Integration Points
- control/internal/routing/ — New package for Caddy Admin API client
- Lifecycle service calls routing on state transitions (RUNNING → add route, STOPPED/DESTROYED → remove)
- Caddy runs alongside on proxy node(s), Admin API on localhost:2019

</code_context>

<specifics>
## Specific Ideas

- control/internal/routing/caddy.go — Caddy Admin API client (add/remove/list routes)
- control/internal/routing/batcher.go — 500ms debounce for route update batching
- proxy/Caddyfile — Base config with Cloudflare Origin CA, admin API enabled
- Lifecycle hooks: on sandbox RUNNING → AddRoute, on STOPPED/DESTROYED → RemoveRoute

</specifics>

<deferred>
## Deferred Ideas

None — all 6 requirements in scope (PRXY-01..05, CTRL-10).

</deferred>
