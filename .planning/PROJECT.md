# AppX Forge

## What This Is

A custom container orchestrator built in Go that replaces Railover/CapRover/Docker Swarm for running AppX sandboxes (React Native + Metro) across a fleet of Contabo VDS nodes. Five services — control plane, per-node agent, Caddy-based proxy, CLI, and TypeScript SDK — coordinated via Postgres as source of truth, Tailscale for cross-node mesh, and Cloudflare for edge routing. No Swarm, no docker.sock mounts, no reconciliation loops.

## Core Value

Sub-second sandbox claim and ~5s cold start with zero spurious restarts — a sandbox orchestrator simple enough to explain on one whiteboard, reliable enough to never wake you up at night.

## Requirements

### Validated

(None yet — ship to validate)

### Active

- [ ] Control plane (Go HTTP API) manages sandbox lifecycle via Postgres
- [ ] Sandbox state machine with explicit event-driven transitions (no reconciler)
- [ ] Bin-packing scheduler assigns sandboxes to least-loaded node by free RAM
- [ ] Per-node agent runs as systemd service, talks to local Docker Engine API directly
- [ ] Agent registers with control plane, sends heartbeats every 15s
- [ ] Agent watches Docker events stream for container exits/OOM — reports immediately
- [ ] Agent receives commands via long-poll from control plane
- [ ] Agent creates containers via `docker run` (no Swarm, no docker.sock mount)
- [ ] File push: control plane redirects to agent's direct HTTP endpoint with signed URL
- [ ] Caddy proxy with dynamic config via Admin API routes `*.myappx.live` to correct node
- [ ] Routing table updates pushed from control plane to Caddy on sandbox state change
- [ ] Idle reaping: sandboxes auto-stop after 30min inactivity
- [ ] Container crash detection + auto-restart with backoff
- [ ] Node failure detection + automatic reschedule within 90s
- [ ] TypeScript SDK (`forge-sdk-ts`) that appx-api imports to create/destroy/inspect sandboxes
- [ ] appx-api migrated from RailoverService to ForgeClient calls
- [ ] CLI for ops: node add/drain/remove, sandbox list/inspect/logs/restart/destroy
- [ ] Seccomp profiles + capability dropping on all sandbox containers
- [ ] Ansible playbook to bootstrap new nodes (Docker, Tailscale, agent)
- [ ] Prometheus metrics + health endpoints

### Out of Scope

- Kubernetes anything — overkill for this scale
- Docker Swarm anything — the entire reason we're rebuilding
- Raft consensus / etcd / Consul — Postgres advisory locks for v1
- Custom container runtime — Docker Engine API is fine
- Web UI for Forge — CLI for ops, appx-api dashboard for users
- Multi-region — single Cloudflare zone, single Tailscale tailnet for v1
- Firecracker VMs — stretch goal, not v1
- Sandbox live migration (CRIU) — post-v1
- Per-user resource quotas in Forge — appx-api billing handles this

## Context

- **Current pain:** Railover (CapRover fork) has a Docker Swarm 29.x bug causing 60s restart loops, docker.sock mounts are a security liability, reconciliation loops cause spurious container kills, CapRover codebase is legacy Node.js we can't reason about
- **Fleet:** Currently 1 Contabo VDS (24GB RAM, ~80 containers max). Forge is multi-node from day one — scale by adding VDS nodes
- **Integration:** appx-api (NestJS backend on Server 1) calls Forge via TypeScript SDK. Forge manages containers on Server 2+ nodes
- **Networking:** Tailscale mesh between nodes (encrypted, NAT-traversing). Cloudflare for edge DNS + CDN
- **Sandbox image:** Existing bundle-server Docker image needs trimming — Metro/Expo on port 8081, bind-mount for code
- **Database:** Postgres (Neon or Supabase managed) for Forge state. Separate from appx-api's MySQL

## Constraints

- **Stack**: Go 1.23+ for all services, TypeScript for SDK only — locked by team decision
- **No docker.sock mounts**: Agent runs on host as systemd, talks to Docker locally — non-negotiable security boundary
- **No Swarm**: Plain `docker run` via Docker SDK only
- **No reconciliation loop**: State changes only on explicit events (API call, container exit, heartbeat timeout)
- **Contracts first**: OpenAPI spec is sacred — changes require ADR
- **Postgres is truth**: No agent-local state that can't be reconstructed
- **Budget**: Contabo VDS nodes (~$15/mo each), no expensive managed services
- **Solo dev**: One developer, AI-assisted parallel execution via Claude Code + GSD

## Key Decisions

| Decision | Rationale | Outcome |
|----------|-----------|---------|
| Go over Node.js | Performance, single binary deploys, Docker SDK native | -- Pending |
| Postgres over etcd | Simpler, already know it, advisory locks for v1 HA | -- Pending |
| Caddy over Traefik | Dynamic Admin API, auto TLS, simpler config than label-based | -- Pending |
| Tailscale over WireGuard manual | NAT traversal, zero-config mesh, DERP fallback | -- Pending |
| Long-poll over WebSocket for agent commands | Simpler, identical latency in practice, easier to debug | -- Pending |
| Bind mounts over volume mounts | Direct file push to disk, inotify for HMR, simpler ops | -- Pending |
| Monorepo with Go workspaces | All services versioned together, shared types, atomic commits | -- Pending |

## Evolution

This document evolves at phase transitions and milestone boundaries.

**After each phase transition** (via `/gsd-transition`):
1. Requirements invalidated? -> Move to Out of Scope with reason
2. Requirements validated? -> Move to Validated with phase reference
3. New requirements emerged? -> Add to Active
4. Decisions to log? -> Add to Key Decisions
5. "What This Is" still accurate? -> Update if drifted

**After each milestone** (via `/gsd-complete-milestone`):
1. Full review of all sections
2. Core Value check — still the right priority?
3. Audit Out of Scope — reasons still valid?
4. Update Context with current state

---
*Last updated: 2026-04-15 after initialization*
