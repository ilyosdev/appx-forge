# Research Summary: AppX Forge

**Domain:** Custom container orchestrator for React Native/Metro sandbox management
**Researched:** 2026-04-15
**Overall confidence:** HIGH

## Executive Summary

AppX Forge is a custom container orchestrator built in Go that replaces Railover (a CapRover fork running on Docker Swarm) for managing React Native + Metro development sandboxes. The project consists of five services: a control plane (Go HTTP API backed by Postgres), per-node agents (Go binaries managing local Docker containers), a Caddy-based reverse proxy with dynamic routing, a CLI for operations, and a TypeScript SDK for integration with the existing appx-api NestJS backend.

The Go ecosystem for this type of project is mature and well-documented. The recommended stack centers on battle-tested libraries: chi for HTTP routing, pgx/v5 + sqlc for type-safe Postgres access, goose for migrations, cobra for CLI, and the Docker SDK (moby/moby/client) for container management. All choices prioritize stdlib compatibility, minimal dependencies, and debuggability by a solo developer. The total codebase should be under 8,000 lines of Go.

The most critical technical risks are: (1) Docker event stream silently disconnecting and missing container deaths, requiring reconnection with timestamp replay; (2) Caddy dropping ALL WebSocket connections on config reload, requiring aggressive batching of route updates; (3) state machine race conditions between concurrent agent reports and API calls, requiring compare-and-swap from day one; and (4) Docker SDK v28+ import path breakage, requiring pinning to v27.x.

Infrastructure validation should happen before any code is written: verify Tailscale UDP connectivity between Contabo nodes, set inotify watch limits in kernel params, and confirm Docker Engine 27.x is installed.

## Key Findings

**Stack:** Go 1.25, chi v5.2.5, pgx/v5 + sqlc v1.30.0, goose v3.27.0, cobra v1.10.2, moby/moby/client v27.x, envconfig v1.4.0, stdlib slog. All verified against current releases.

**Architecture:** Control plane (single instance, Postgres advisory locks for HA) orchestrates stateless agents via long-poll commands. Agents manage local Docker containers and report events. Caddy provides dynamic L7 routing via Admin API. Tailscale mesh encrypts all inter-node traffic.

**Critical pitfall:** Docker event stream drops are invisible -- the agent must track timestamps and replay missed events on reconnection, plus run a periodic container audit as a safety net.

## Implications for Roadmap

Based on research, suggested phase structure:

1. **Phase 0: Infrastructure Validation** - Validate assumptions before writing code
   - Addresses: Tailscale UDP connectivity, Docker Engine version, inotify limits
   - Avoids: Building on broken infrastructure (DERP relay latency pitfall)

2. **Phase 1: Contracts + Foundation** - OpenAPI spec, Postgres schema, state machine
   - Addresses: Sandbox CRUD lifecycle, event-driven state machine, migrations
   - Avoids: State machine race conditions by implementing compare-and-swap from day one
   - Note: OpenAPI spec MUST be written first -- everything derives from it

3. **Phase 2: Agent + Container Lifecycle** - Docker SDK wrapper, events, heartbeat
   - Addresses: Container create/start/stop, Docker events watcher, agent registration
   - Avoids: Event stream disconnects, port conflicts, UID mismatch

4. **Phase 3: Control Plane API + Scheduler** - HTTP handlers, bin-packing, command dispatch
   - Addresses: All REST endpoints, long-poll command dispatch, scheduler
   - Avoids: Duplicate command execution via idempotency keys

5. **Phase 4: Proxy + Routing** - Caddy integration, route management, file push
   - Addresses: Dynamic routing, Caddy Admin API, file push protocol, signed URLs
   - Avoids: WebSocket drops by batching route updates with 500ms debounce

6. **Phase 5: Reliability + Hardening** - Crash detection, auto-restart, idle reaping, seccomp
   - Addresses: Auto-restart with backoff, idle reaping, health checking, security
   - Avoids: Seccomp breaking Node.js by starting with Docker defaults first

7. **Phase 6: CLI + SDK + Integration** - Cobra CLI, TypeScript SDK, appx-api migration
   - Addresses: forge-cli commands, forge-sdk-ts, appx-api migration from Railover

8. **Phase 7: Multi-Node + Failover** - Node failure detection, reschedule, drain
   - Addresses: Multi-node scheduling, heartbeat timeout, sandbox reschedule

**Phase ordering rationale:**
- Phase 0 before code because infrastructure assumptions must be validated first
- Phase 1 before everything because OpenAPI spec gates all other tracks
- Phase 2 (agent) before Phase 3 (API) because agent has the most Docker SDK gotchas
- Phase 5 (reliability) after core works because hardening unfinished systems wastes effort
- Phase 7 (multi-node) last because single-node covers 80% of the value

**Research flags for phases:**
- Phase 0: Needs hands-on validation, cannot be researched further
- Phase 2: Docker SDK v27 vs v28 import path may need investigation
- Phase 4: Caddy WebSocket behavior needs load testing -- may need architecture change
- Phase 7: Reschedule thundering herd prevention needs deeper design

## Confidence Assessment

| Area | Confidence | Notes |
|------|------------|-------|
| Stack | HIGH | All versions verified against GitHub releases and Context7 docs |
| Features | HIGH | Well-understood from competitor analysis and existing pain points |
| Architecture | HIGH | Control plane + agent + proxy is a proven pattern |
| Pitfalls | HIGH | All documented with reproduction steps and mitigations |
| Docker SDK | MEDIUM | v28+ import issues are known; v27.x is safe but may lag |
| Tailscale | MEDIUM | Official SDK exists but limited examples; UDP unverified on Contabo |
| Caddy WebSocket | MEDIUM | No upstream fix for drop-on-reload; batching mitigates but may not eliminate |

## Gaps to Address

- Contabo UDP verification: must test Tailscale direct connectivity on actual nodes
- Docker SDK import path: pin v27.x, revisit if Docker Engine on nodes is upgraded
- Caddy WebSocket at scale: load testing needed in Phase 4
- Advisory lock failover timing: measure in Phase 1
- Metro cold start optimization: warm pool design deferred to later phases
- Sandbox image size: trimming to <500MB is Phase 2 concern
