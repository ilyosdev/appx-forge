# Phase 7: Multi-Node & Failover - Context

**Gathered:** 2026-04-16
**Status:** Ready for planning
**Mode:** Auto-generated (code phase — discuss skipped)

<domain>
## Phase Boundary

Sandboxes are distributed across 3+ nodes, and a node failure triggers automatic reschedule of its running sandboxes within 90s. Covers: scheduler distributing across multiple nodes by available RAM, node failure detection via missed heartbeats (3 missed = unhealthy at 45s), automatic reschedule of RUNNING sandboxes from failed nodes to healthy ones, and the full reschedule completing within 90s.

</domain>

<decisions>
## Implementation Decisions

### Claude's Discretion
All choices at Claude's discretion. Only 3 requirements — focused phase.

**TDD mandate:** Write tests FIRST.

### From Prior Phases
- Scheduler already does bin-packing by free RAM (Phase 3, control/internal/scheduler/)
- Heartbeat monitor already detects unhealthy nodes (Phase 3, forge-control main.go)
- Lifecycle service handles state transitions (Phase 3)
- Route management updates Caddy on state changes (Phase 4)
- All infrastructure for multi-node exists — this phase wires the reschedule logic

</decisions>

<code_context>
## Existing Code Insights

### Reusable Assets
- control/internal/scheduler/scheduler.go — Schedule() picks node with most free RAM
- control/internal/lifecycle/lifecycle.go — CreateSandbox, DestroySandbox, HandleEvent
- control/internal/lifecycle/restart.go — RestartManager handles crash recovery
- control/cmd/forge-control/main.go — Heartbeat monitor goroutine already marks nodes unhealthy

### What Needs Adding
- Reschedule logic: when node goes unhealthy, find its RUNNING sandboxes, reschedule to other nodes
- Reschedule = destroy on failed node + create on healthy node (file state lost, appx-api re-pushes)
- Time budget: 45s detection + 45s reschedule = 90s total

</code_context>

<specifics>
## Specific Ideas

- control/internal/lifecycle/reschedule.go — Reschedule logic
- Extend heartbeat monitor: on unhealthy → trigger reschedule for that node's sandboxes
- sqlc query: ListRunningSandboxesByNode

</specifics>

<deferred>
## Deferred Ideas

None — only 3 requirements (MULTI-01, MULTI-02, MULTI-03).

</deferred>
