# Requirements: AppX Forge

**Defined:** 2026-04-15
**Core Value:** Sub-second sandbox claim, ~5s cold start, zero spurious restarts — reliable container orchestration simple enough to explain on one whiteboard

## v1 Requirements

Requirements for production cutover (replacing Railover). Each maps to roadmap phases.

### Infrastructure Validation

- [ ] **INFRA-01**: Tailscale UDP connectivity verified between Contabo VDS nodes (direct peering, not DERP relay)
- [ ] **INFRA-02**: Docker Engine 27.x installed and confirmed on target nodes
- [ ] **INFRA-03**: Kernel inotify watch limit set to support 80+ containers with Metro file watchers
- [ ] **INFRA-04**: Cloudflare wildcard DNS configured for `*.myappx.live` pointing at proxy node(s)

### Contracts & Schema

- [ ] **CNTR-01**: OpenAPI 3.1 spec defines all v1 endpoints (sandbox CRUD, node registration, agent commands, routes)
- [ ] **CNTR-02**: Agent protocol documented (registration, long-poll commands, command ack, event reporting)
- [ ] **CNTR-03**: File push protocol documented (control plane redirect, signed URL, agent direct endpoint)
- [ ] **CNTR-04**: Proxy routing protocol documented (Caddy Admin API shape, drift detection)
- [ ] **CNTR-05**: Postgres schema with migrations (nodes, sandboxes, events, commands tables)
- [ ] **CNTR-06**: Sandbox state machine implemented with compare-and-swap (`UPDATE WHERE state = $expected`)

### Control Plane

- [ ] **CTRL-01**: Go HTTP API serves all OpenAPI-defined endpoints via chi router
- [ ] **CTRL-02**: Sandbox create endpoint accepts spec, writes PENDING row, triggers scheduling
- [ ] **CTRL-03**: Bin-packing scheduler picks node with most free RAM, excludes draining/unhealthy nodes
- [ ] **CTRL-04**: Command dispatch via long-poll: agent polls, control plane holds up to 30s, returns commands
- [ ] **CTRL-05**: Command acknowledgment: agent reports success/failure, control plane updates sandbox state
- [ ] **CTRL-06**: Node registration: agent registers on boot, receives agent_token and heartbeat interval
- [ ] **CTRL-07**: Heartbeat processing: update node last_seen_at, mark unhealthy after 3 missed (45s)
- [ ] **CTRL-08**: Event ingestion: agent reports container events (started, exited, OOM), control plane transitions state
- [ ] **CTRL-09**: File push redirect: returns 307 to agent's direct endpoint with HMAC-signed URL (60s expiry)
- [ ] **CTRL-10**: Route management: add/remove Caddy routes via Admin API on sandbox state changes
- [ ] **CTRL-11**: Routing drift detector: every 60s diff Caddy state vs Postgres, fix discrepancies (routes only, never containers)
- [ ] **CTRL-12**: Idle reaping: stop sandboxes idle > 30min (configurable), remove routes
- [ ] **CTRL-13**: Auto-restart with backoff: on container crash, restart up to 3 times with exponential backoff, then mark FAILED
- [ ] **CTRL-14**: Bearer token auth on all endpoints (except /healthz and /metrics)
- [ ] **CTRL-15**: Prometheus metrics endpoint (/metrics): sandbox count by state, node utilization, request latency
- [ ] **CTRL-16**: Health endpoint (/healthz): self-check + Postgres connectivity

### Agent

- [ ] **AGNT-01**: Single Go binary runs as systemd service on each node
- [ ] **AGNT-02**: Registers with control plane on boot, receives agent_token
- [ ] **AGNT-03**: Sends heartbeat every 15s with used_mb and running container count
- [ ] **AGNT-04**: Long-polls control plane for commands (start_sandbox, stop_sandbox, restart_sandbox, get_logs, prune)
- [ ] **AGNT-05**: Creates Docker containers via Docker SDK: port binding, bind mount for code, resource limits, seccomp, capability dropping
- [ ] **AGNT-06**: Watches Docker events stream for die/oom events, reports to control plane immediately
- [ ] **AGNT-07**: Reconnects Docker event stream with `Since` timestamp on disconnect (no missed events)
- [ ] **AGNT-08**: File push HTTP endpoint: validates signed URL, writes files to bind-mount directory
- [ ] **AGNT-09**: Port allocator: assigns host ports from range 40000-50000, avoids conflicts
- [ ] **AGNT-10**: Pre-pulls sandbox image on registration and periodically checks for new versions
- [ ] **AGNT-11**: Log retrieval: wraps docker logs API, supports tail and follow modes
- [ ] **AGNT-12**: Container directory setup with correct UID/GID for bind mounts

### Proxy & Routing

- [ ] **PRXY-01**: Caddy runs with base config, Cloudflare Origin CA cert for TLS termination
- [ ] **PRXY-02**: Control plane adds/removes routes via Caddy Admin API (host matcher + reverse proxy upstream)
- [ ] **PRXY-03**: WebSocket upgrade works through Caddy (HMR for Metro)
- [ ] **PRXY-04**: Route updates batched with 500ms debounce to minimize Caddy config reloads (WebSocket drop mitigation)
- [ ] **PRXY-05**: Cloudflare DNS wildcard `*.myappx.live` points at Caddy public IP(s)

### CLI

- [ ] **CLI-01**: `forge node list` — show all nodes with status, capacity, sandbox count
- [ ] **CLI-02**: `forge node add` — register a new node
- [ ] **CLI-03**: `forge node drain` — stop scheduling, let existing sandboxes idle-reap
- [ ] **CLI-04**: `forge node remove` — remove node (only if sandbox count is 0)
- [ ] **CLI-05**: `forge sandbox list` — filter by app, node, state
- [ ] **CLI-06**: `forge sandbox inspect` — full sandbox details
- [ ] **CLI-07**: `forge sandbox logs` — with --follow and --tail
- [ ] **CLI-08**: `forge sandbox restart` — force restart
- [ ] **CLI-09**: `forge sandbox destroy` — destroy sandbox
- [ ] **CLI-10**: `forge routes list` — show active routes
- [ ] **CLI-11**: `forge routes verify` — diff Caddy vs Postgres
- [ ] **CLI-12**: `forge events` — filter by sandbox, since
- [ ] **CLI-13**: `forge healthcheck` — control plane health

### TypeScript SDK

- [ ] **SDK-01**: ForgeClient class with baseUrl + apiKey config
- [ ] **SDK-02**: `sandboxes.create()` — create sandbox, return Sandbox object with URL
- [ ] **SDK-03**: `sandboxes.get()` — get sandbox by ID or app:name
- [ ] **SDK-04**: `sandboxes.list()` — filter by user, state, app_name
- [ ] **SDK-05**: `sandboxes.destroy()` — destroy sandbox
- [ ] **SDK-06**: `sandboxes.restart()` — force restart
- [ ] **SDK-07**: `sandboxes.pushFiles()` — push files (follows 307 redirect to agent)
- [ ] **SDK-08**: `sandboxes.logs()` — get logs
- [ ] **SDK-09**: Types generated from OpenAPI spec via openapi-typescript-codegen

### Sandbox Image

- [ ] **IMG-01**: Dockerfile produces <500MB image with Metro/Expo pre-installed
- [ ] **IMG-02**: Runs on port 8081, accepts code via bind-mount at /app/code
- [ ] **IMG-03**: Pre-installed node_modules for common Expo dependencies
- [ ] **IMG-04**: Documents required env vars (APP_NAME, PORT)
- [ ] **IMG-05**: Cold start to Metro responding in <10s

### Security

- [ ] **SEC-01**: Seccomp profile restricts sandbox containers to ~40 syscalls
- [ ] **SEC-02**: Capability dropping: `--cap-drop ALL` + CHOWN/SETUID/SETGID only
- [ ] **SEC-03**: `no-new-privileges:true` on all sandbox containers
- [ ] **SEC-04**: PID limit (256) on sandbox containers
- [ ] **SEC-05**: Agent token scoped per node (not global API key)
- [ ] **SEC-06**: File push signed URLs with HMAC-SHA256 + 60s expiry

### Deployment & Ops

- [ ] **OPS-01**: Ansible playbook bootstraps new node (Docker, Tailscale, agent systemd service)
- [ ] **OPS-02**: docker-compose.dev.yml for local development (Postgres + control + 1 agent)
- [ ] **OPS-03**: Runbook: add new node to fleet
- [ ] **OPS-04**: Runbook: recover failed sandbox
- [ ] **OPS-05**: Runbook: debug stuck container

### appx-api Integration

- [ ] **INT-01**: appx-api imports forge-sdk-ts, replaces RailoverService calls with ForgeClient
- [ ] **INT-02**: Delete RailoverService, ContainerReconcilerService, ContainerCircuitBreakerService from backend
- [ ] **INT-03**: Update container-state.types.ts to align with Forge sandbox states
- [ ] **INT-04**: Integration tests: appx-api creates sandbox via SDK, pushes files, verifies URL works

### Multi-Node

- [ ] **MULTI-01**: Scheduler distributes sandboxes across 3+ nodes
- [ ] **MULTI-02**: Node failure detection: missed heartbeats -> mark unhealthy -> reschedule RUNNING sandboxes
- [ ] **MULTI-03**: Reschedule completes within 90s of node failure detection

## v2 Requirements

Deferred to future release. Tracked but not in current roadmap.

### Performance

- **PERF-01**: Warm container pool — pre-initialized Metro containers for sub-second claim
- **PERF-02**: Metro cold start optimization via snapshot pool
- **PERF-03**: Docker checkpoint/restore (CRIU) for sandbox suspend/resume

### Scale

- **SCALE-01**: Multi-region deployment with per-region control planes
- **SCALE-02**: Automatic node provisioning via Contabo API (if available)
- **SCALE-03**: Per-user resource quotas enforced in scheduler

### Advanced Ops

- **ADV-01**: Forge web dashboard for visualization
- **ADV-02**: Live sandbox migration between nodes (CRIU-based)
- **ADV-03**: Terraform modules for Contabo node provisioning

## Out of Scope

| Feature | Reason |
|---------|--------|
| Kubernetes / Docker Swarm | Entire reason for this project — replacing Swarm |
| Firecracker / microVM | Threat model doesn't require VM isolation; sandboxes run trusted AI-generated code |
| Continuous reconciliation loop | Root cause of Railover 60s restart bug — event-driven only |
| GPU support | Metro/RN bundling is CPU-bound, Contabo nodes have no GPUs |
| Custom container networking (VPC/overlay) | Sandboxes only need one HTTP port, host port mapping is sufficient |
| Web UI for Forge | CLI for ops, appx-api dashboard for users — Forge is infrastructure |
| Per-sandbox custom DNS | Single wildcard domain is sufficient |
| Sandbox-to-sandbox networking | Sandboxes are isolated by design, no inter-sandbox communication needed |

## Traceability

Which phases cover which requirements. Updated during roadmap creation.

| Requirement | Phase | Status |
|-------------|-------|--------|
| INFRA-01 | Phase 1 | Pending |
| INFRA-02 | Phase 1 | Pending |
| INFRA-03 | Phase 1 | Pending |
| INFRA-04 | Phase 1 | Pending |
| CNTR-01 | Phase 1 | Pending |
| CNTR-02 | Phase 1 | Pending |
| CNTR-03 | Phase 1 | Pending |
| CNTR-04 | Phase 1 | Pending |
| CNTR-05 | Phase 1 | Pending |
| CNTR-06 | Phase 1 | Pending |
| (remaining mapped during roadmap creation) | | |

**Coverage:**
- v1 requirements: 67 total
- Mapped to phases: (pending roadmap)
- Unmapped: (pending roadmap)

---
*Requirements defined: 2026-04-15*
*Last updated: 2026-04-15 after initial definition*
