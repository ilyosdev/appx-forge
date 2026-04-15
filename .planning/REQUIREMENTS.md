# Requirements: AppX Forge

**Defined:** 2026-04-15
**Core Value:** Sub-second sandbox claim, ~5s cold start, zero spurious restarts -- reliable container orchestration simple enough to explain on one whiteboard

## v1 Requirements

Requirements for production cutover (replacing Railover). Each maps to roadmap phases.

### Infrastructure Validation

- [x] **INFRA-01**: Tailscale UDP connectivity verified between Contabo VDS nodes (direct peering, not DERP relay)
- [x] **INFRA-02**: Docker Engine 27.x installed and confirmed on target nodes
- [x] **INFRA-03**: Kernel inotify watch limit set to support 80+ containers with Metro file watchers
- [x] **INFRA-04**: Cloudflare wildcard DNS configured for `*.myappx.live` pointing at proxy node(s)

### Contracts & Schema

- [x] **CNTR-01**: OpenAPI 3.1 spec defines all v1 endpoints (sandbox CRUD, node registration, agent commands, routes)
- [x] **CNTR-02**: Agent protocol documented (registration, long-poll commands, command ack, event reporting)
- [x] **CNTR-03**: File push protocol documented (control plane redirect, signed URL, agent direct endpoint)
- [x] **CNTR-04**: Proxy routing protocol documented (Caddy Admin API shape, drift detection)
- [x] **CNTR-05**: Postgres schema with migrations (nodes, sandboxes, events, commands tables)
- [x] **CNTR-06**: Sandbox state machine implemented with compare-and-swap (`UPDATE WHERE state = $expected`)

### Control Plane

- [x] **CTRL-01**: Go HTTP API serves all OpenAPI-defined endpoints via chi router
- [x] **CTRL-02**: Sandbox create endpoint accepts spec, writes PENDING row, triggers scheduling
- [x] **CTRL-03**: Bin-packing scheduler picks node with most free RAM, excludes draining/unhealthy nodes
- [x] **CTRL-04**: Command dispatch via long-poll: agent polls, control plane holds up to 30s, returns commands
- [x] **CTRL-05**: Command acknowledgment: agent reports success/failure, control plane updates sandbox state
- [x] **CTRL-06**: Node registration: agent registers on boot, receives agent_token and heartbeat interval
- [x] **CTRL-07**: Heartbeat processing: update node last_seen_at, mark unhealthy after 3 missed (45s)
- [x] **CTRL-08**: Event ingestion: agent reports container events (started, exited, OOM), control plane transitions state
- [x] **CTRL-09**: File push redirect: returns 307 to agent's direct endpoint with HMAC-signed URL (60s expiry)
- [x] **CTRL-10**: Route management: add/remove Caddy routes via Admin API on sandbox state changes
- [ ] **CTRL-11**: Routing drift detector: every 60s diff Caddy state vs Postgres, fix discrepancies (routes only, never containers)
- [ ] **CTRL-12**: Idle reaping: stop sandboxes idle > 30min (configurable), remove routes
- [ ] **CTRL-13**: Auto-restart with backoff: on container crash, restart up to 3 times with exponential backoff, then mark FAILED
- [x] **CTRL-14**: Bearer token auth on all endpoints (except /healthz and /metrics)
- [ ] **CTRL-15**: Prometheus metrics endpoint (/metrics): sandbox count by state, node utilization, request latency
- [x] **CTRL-16**: Health endpoint (/healthz): self-check + Postgres connectivity

### Agent

- [x] **AGNT-01**: Single Go binary runs as systemd service on each node
- [x] **AGNT-02**: Registers with control plane on boot, receives agent_token
- [x] **AGNT-03**: Sends heartbeat every 15s with used_mb and running container count
- [x] **AGNT-04**: Long-polls control plane for commands (start_sandbox, stop_sandbox, restart_sandbox, get_logs, prune)
- [x] **AGNT-05**: Creates Docker containers via Docker SDK: port binding, bind mount for code, resource limits, seccomp, capability dropping
- [x] **AGNT-06**: Watches Docker events stream for die/oom events, reports to control plane immediately
- [x] **AGNT-07**: Reconnects Docker event stream with `Since` timestamp on disconnect (no missed events)
- [x] **AGNT-08**: File push HTTP endpoint: validates signed URL, writes files to bind-mount directory
- [x] **AGNT-09**: Port allocator: assigns host ports from range 40000-50000, avoids conflicts
- [x] **AGNT-10**: Pre-pulls sandbox image on registration and periodically checks for new versions
- [x] **AGNT-11**: Log retrieval: wraps docker logs API, supports tail and follow modes
- [x] **AGNT-12**: Container directory setup with correct UID/GID for bind mounts

### Proxy & Routing

- [ ] **PRXY-01**: Caddy runs with base config, Cloudflare Origin CA cert for TLS termination
- [x] **PRXY-02**: Control plane adds/removes routes via Caddy Admin API (host matcher + reverse proxy upstream)
- [x] **PRXY-03**: WebSocket upgrade works through Caddy (HMR for Metro)
- [x] **PRXY-04**: Route updates batched with 500ms debounce to minimize Caddy config reloads (WebSocket drop mitigation)
- [ ] **PRXY-05**: Cloudflare DNS wildcard `*.myappx.live` points at Caddy public IP(s)

### CLI

- [ ] **CLI-01**: `forge node list` -- show all nodes with status, capacity, sandbox count
- [ ] **CLI-02**: `forge node add` -- register a new node
- [ ] **CLI-03**: `forge node drain` -- stop scheduling, let existing sandboxes idle-reap
- [ ] **CLI-04**: `forge node remove` -- remove node (only if sandbox count is 0)
- [ ] **CLI-05**: `forge sandbox list` -- filter by app, node, state
- [ ] **CLI-06**: `forge sandbox inspect` -- full sandbox details
- [ ] **CLI-07**: `forge sandbox logs` -- with --follow and --tail
- [ ] **CLI-08**: `forge sandbox restart` -- force restart
- [ ] **CLI-09**: `forge sandbox destroy` -- destroy sandbox
- [ ] **CLI-10**: `forge routes list` -- show active routes
- [ ] **CLI-11**: `forge routes verify` -- diff Caddy vs Postgres
- [ ] **CLI-12**: `forge events` -- filter by sandbox, since
- [ ] **CLI-13**: `forge healthcheck` -- control plane health

### TypeScript SDK

- [ ] **SDK-01**: ForgeClient class with baseUrl + apiKey config
- [ ] **SDK-02**: `sandboxes.create()` -- create sandbox, return Sandbox object with URL
- [ ] **SDK-03**: `sandboxes.get()` -- get sandbox by ID or app:name
- [ ] **SDK-04**: `sandboxes.list()` -- filter by user, state, app_name
- [ ] **SDK-05**: `sandboxes.destroy()` -- destroy sandbox
- [ ] **SDK-06**: `sandboxes.restart()` -- force restart
- [ ] **SDK-07**: `sandboxes.pushFiles()` -- push files (follows 307 redirect to agent)
- [ ] **SDK-08**: `sandboxes.logs()` -- get logs
- [ ] **SDK-09**: Types generated from OpenAPI spec via openapi-typescript-codegen

### Sandbox Image

- [x] **IMG-01**: Dockerfile produces <500MB image with Metro/Expo pre-installed
- [x] **IMG-02**: Runs on port 8081, accepts code via bind-mount at /app/code
- [x] **IMG-03**: Pre-installed node_modules for common Expo dependencies
- [x] **IMG-04**: Documents required env vars (APP_NAME, PORT)
- [x] **IMG-05**: Cold start to Metro responding in <10s

### Security

- [ ] **SEC-01**: Seccomp profile restricts sandbox containers to ~40 syscalls
- [ ] **SEC-02**: Capability dropping: `--cap-drop ALL` + CHOWN/SETUID/SETGID only
- [ ] **SEC-03**: `no-new-privileges:true` on all sandbox containers
- [ ] **SEC-04**: PID limit (256) on sandbox containers
- [ ] **SEC-05**: Agent token scoped per node (not global API key)
- [ ] **SEC-06**: File push signed URLs with HMAC-SHA256 + 60s expiry

### Deployment & Ops

- [ ] **OPS-01**: Ansible playbook bootstraps new node (Docker, Tailscale, agent systemd service)
- [x] **OPS-02**: docker-compose.dev.yml for local development (Postgres + control + 1 agent)
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

- **PERF-01**: Warm container pool -- pre-initialized Metro containers for sub-second claim
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
| Kubernetes / Docker Swarm | Entire reason for this project -- replacing Swarm |
| Firecracker / microVM | Threat model doesn't require VM isolation; sandboxes run trusted AI-generated code |
| Continuous reconciliation loop | Root cause of Railover 60s restart bug -- event-driven only |
| GPU support | Metro/RN bundling is CPU-bound, Contabo nodes have no GPUs |
| Custom container networking (VPC/overlay) | Sandboxes only need one HTTP port, host port mapping is sufficient |
| Web UI for Forge | CLI for ops, appx-api dashboard for users -- Forge is infrastructure |
| Per-sandbox custom DNS | Single wildcard domain is sufficient |
| Sandbox-to-sandbox networking | Sandboxes are isolated by design, no inter-sandbox communication needed |

## Traceability

Which phases cover which requirements. Updated during roadmap creation.

| Requirement | Phase | Status |
|-------------|-------|--------|
| INFRA-01 | Phase 1 | Complete |
| INFRA-02 | Phase 1 | Complete |
| INFRA-03 | Phase 1 | Complete |
| INFRA-04 | Phase 1 | Complete |
| CNTR-01 | Phase 1 | Complete |
| CNTR-02 | Phase 1 | Complete |
| CNTR-03 | Phase 1 | Complete |
| CNTR-04 | Phase 1 | Complete |
| CNTR-05 | Phase 1 | Complete |
| CNTR-06 | Phase 1 | Complete |
| IMG-01 | Phase 1 | Complete |
| IMG-02 | Phase 1 | Complete |
| IMG-03 | Phase 1 | Complete |
| IMG-04 | Phase 1 | Complete |
| IMG-05 | Phase 1 | Complete |
| AGNT-01 | Phase 2 | Complete |
| AGNT-02 | Phase 2 | Complete |
| AGNT-03 | Phase 2 | Complete |
| AGNT-04 | Phase 2 | Complete |
| AGNT-05 | Phase 2 | Complete |
| AGNT-06 | Phase 2 | Complete |
| AGNT-07 | Phase 2 | Complete |
| AGNT-08 | Phase 2 | Complete |
| AGNT-09 | Phase 2 | Complete |
| AGNT-10 | Phase 2 | Complete |
| AGNT-11 | Phase 2 | Complete |
| AGNT-12 | Phase 2 | Complete |
| CTRL-01 | Phase 3 | Complete |
| CTRL-02 | Phase 3 | Complete |
| CTRL-03 | Phase 3 | Complete |
| CTRL-04 | Phase 3 | Complete |
| CTRL-05 | Phase 3 | Complete |
| CTRL-06 | Phase 3 | Complete |
| CTRL-07 | Phase 3 | Complete |
| CTRL-08 | Phase 3 | Complete |
| CTRL-09 | Phase 3 | Complete |
| CTRL-10 | Phase 4 | Complete |
| CTRL-11 | Phase 5 | Pending |
| CTRL-12 | Phase 5 | Pending |
| CTRL-13 | Phase 5 | Pending |
| CTRL-14 | Phase 3 | Complete |
| CTRL-15 | Phase 5 | Pending |
| CTRL-16 | Phase 3 | Complete |
| PRXY-01 | Phase 4 | Pending |
| PRXY-02 | Phase 4 | Complete |
| PRXY-03 | Phase 4 | Complete |
| PRXY-04 | Phase 4 | Complete |
| PRXY-05 | Phase 4 | Pending |
| CLI-01 | Phase 6 | Pending |
| CLI-02 | Phase 6 | Pending |
| CLI-03 | Phase 6 | Pending |
| CLI-04 | Phase 6 | Pending |
| CLI-05 | Phase 6 | Pending |
| CLI-06 | Phase 6 | Pending |
| CLI-07 | Phase 6 | Pending |
| CLI-08 | Phase 6 | Pending |
| CLI-09 | Phase 6 | Pending |
| CLI-10 | Phase 6 | Pending |
| CLI-11 | Phase 6 | Pending |
| CLI-12 | Phase 6 | Pending |
| CLI-13 | Phase 6 | Pending |
| SDK-01 | Phase 6 | Pending |
| SDK-02 | Phase 6 | Pending |
| SDK-03 | Phase 6 | Pending |
| SDK-04 | Phase 6 | Pending |
| SDK-05 | Phase 6 | Pending |
| SDK-06 | Phase 6 | Pending |
| SDK-07 | Phase 6 | Pending |
| SDK-08 | Phase 6 | Pending |
| SDK-09 | Phase 6 | Pending |
| SEC-01 | Phase 5 | Pending |
| SEC-02 | Phase 5 | Pending |
| SEC-03 | Phase 5 | Pending |
| SEC-04 | Phase 5 | Pending |
| SEC-05 | Phase 5 | Pending |
| SEC-06 | Phase 5 | Pending |
| OPS-01 | Phase 6 | Pending |
| OPS-02 | Phase 3 | Complete |
| OPS-03 | Phase 6 | Pending |
| OPS-04 | Phase 6 | Pending |
| OPS-05 | Phase 6 | Pending |
| INT-01 | Phase 6 | Pending |
| INT-02 | Phase 6 | Pending |
| INT-03 | Phase 6 | Pending |
| INT-04 | Phase 6 | Pending |
| MULTI-01 | Phase 7 | Pending |
| MULTI-02 | Phase 7 | Pending |
| MULTI-03 | Phase 7 | Pending |

**Coverage:**
- v1 requirements: 88 total
- Mapped to phases: 88
- Unmapped: 0

---
*Requirements defined: 2026-04-15*
*Last updated: 2026-04-15 after roadmap creation*
