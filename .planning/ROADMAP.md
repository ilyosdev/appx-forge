# Roadmap: AppX Forge

## Overview

AppX Forge replaces Railover/CapRover/Docker Swarm with a purpose-built Go container orchestrator for React Native sandboxes. The roadmap moves from infrastructure validation through contracts, agent, control plane, proxy, reliability hardening, SDK/CLI/integration, and finally multi-node failover -- each phase delivering a verifiable capability that builds on the last. Single-node correctness is proven before multi-node is attempted. The appx-api cutover is the final gate.

## Phases

**Phase Numbering:**
- Integer phases (1, 2, 3): Planned milestone work
- Decimal phases (2.1, 2.2): Urgent insertions (marked with INSERTED)

Decimal phases appear between their surrounding integers in numeric order.

- [x] **Phase 1: Infrastructure & Contracts** - Validate infra assumptions and define all API contracts, schemas, and state machine
- [ ] **Phase 2: Agent & Container Lifecycle** - Go agent binary that creates, monitors, and reports on Docker containers
- [ ] **Phase 3: Control Plane API** - HTTP API that orchestrates sandbox lifecycle via scheduler and command dispatch
- [ ] **Phase 4: Proxy, Routing & File Push** - Caddy dynamic routing and file push protocol for live sandbox access
- [ ] **Phase 5: Reliability & Security** - Crash recovery, idle reaping, seccomp hardening, and observability
- [ ] **Phase 6: CLI, SDK & appx-api Integration** - Ops tooling, TypeScript SDK, and full Railover replacement in appx-api
- [ ] **Phase 7: Multi-Node & Failover** - Distribute sandboxes across nodes with automatic failure recovery

## Phase Details

### Phase 1: Infrastructure & Contracts
**Goal**: All infrastructure assumptions validated and every protocol/schema documented so downstream phases can build against stable contracts
**Depends on**: Nothing (first phase)
**Requirements**: INFRA-01, INFRA-02, INFRA-03, INFRA-04, CNTR-01, CNTR-02, CNTR-03, CNTR-04, CNTR-05, CNTR-06, IMG-01, IMG-02, IMG-03, IMG-04, IMG-05
**Success Criteria** (what must be TRUE):
  1. Tailscale direct peering confirmed between two Contabo VDS nodes (not DERP relay)
  2. Docker Engine 27.x runs on target nodes with inotify watch limit supporting 80+ containers
  3. Cloudflare wildcard DNS resolves `*.myappx.live` to the proxy node IP
  4. OpenAPI 3.1 spec exists defining all v1 endpoints, and agent/file-push/proxy protocols are documented
  5. Postgres schema with migrations runs cleanly, and sandbox state machine transitions pass compare-and-swap tests
**Plans:** 5 plans

Plans:
- [x] 01-01-PLAN.md -- Go workspace + shared models + state machine TDD + HMAC utilities
- [x] 01-02-PLAN.md -- Postgres schema + migrations + sqlc + CAS integration tests TDD
- [x] 01-03-PLAN.md -- Contract documents (OpenAPI, agent protocol, file push, proxy routing)
- [x] 01-04-PLAN.md -- Infrastructure verification scripts (Tailscale, Docker, inotify, DNS)
- [x] 01-05-PLAN.md -- Sandbox Docker image + smoke test

### Phase 2: Agent & Container Lifecycle
**Goal**: A Go agent binary runs as a systemd service on a node, creates Docker containers with proper security settings, and reliably reports container events
**Depends on**: Phase 1
**Requirements**: AGNT-01, AGNT-02, AGNT-03, AGNT-04, AGNT-05, AGNT-06, AGNT-07, AGNT-08, AGNT-09, AGNT-10, AGNT-11, AGNT-12
**Success Criteria** (what must be TRUE):
  1. Agent binary starts as a systemd service, registers with a control plane endpoint, and begins sending heartbeats every 15s
  2. Agent creates a Docker container with bind mount, port binding, resource limits, and seccomp profile when it receives a start_sandbox command
  3. Agent detects a container crash (die/OOM event) within 2s and reports it to the control plane
  4. Agent reconnects Docker event stream after disconnect without missing any events (timestamp-based replay)
  5. File push endpoint accepts a signed URL request and writes files to the correct bind-mount directory
**Plans:** 6 plans

Plans:
- [x] 02-01-PLAN.md -- Agent module scaffold + config + port allocator TDD
- [ ] 02-02-PLAN.md -- Docker SDK wrapper + container security settings TDD
- [ ] 02-03-PLAN.md -- Docker events watcher + image pre-pull + log retrieval TDD
- [ ] 02-04-PLAN.md -- Control plane HTTP client + heartbeat sender TDD
- [ ] 02-05-PLAN.md -- File push HTTP handler with HMAC validation TDD
- [ ] 02-06-PLAN.md -- Agent main binary + command executor + systemd service

### Phase 3: Control Plane API
**Goal**: A running Go HTTP API that accepts sandbox requests, schedules them to nodes, dispatches commands to agents via long-poll, and transitions sandbox state based on agent reports
**Depends on**: Phase 2
**Requirements**: CTRL-01, CTRL-02, CTRL-03, CTRL-04, CTRL-05, CTRL-06, CTRL-07, CTRL-08, CTRL-09, CTRL-14, CTRL-16, OPS-02
**Success Criteria** (what must be TRUE):
  1. `POST /sandboxes` creates a PENDING sandbox row, scheduler assigns it to a node, and the agent receives a start_sandbox command via long-poll
  2. Agent command acknowledgment (success/failure) transitions sandbox state correctly in the database
  3. Node registration and heartbeat processing work: new agent registers, heartbeats update last_seen_at, 3 missed heartbeats mark node unhealthy
  4. File push request returns 307 redirect to the agent with a valid HMAC-signed URL
  5. docker-compose.dev.yml starts Postgres + control plane + 1 agent for local development
**Plans**: TBD

### Phase 4: Proxy, Routing & File Push
**Goal**: Sandboxes are accessible via `{id}.myappx.live` URLs with WebSocket support, and route updates happen without dropping existing connections
**Depends on**: Phase 3
**Requirements**: PRXY-01, PRXY-02, PRXY-03, PRXY-04, PRXY-05, CTRL-10
**Success Criteria** (what must be TRUE):
  1. Caddy serves sandbox traffic on `{id}.myappx.live` with TLS termination via Cloudflare Origin CA cert
  2. Control plane adds/removes Caddy routes via Admin API when sandbox state changes
  3. WebSocket connections (Metro HMR) survive route updates for other sandboxes (500ms debounce batching verified)
  4. A full end-to-end flow works: create sandbox, push files, access via browser, see Metro output
**Plans**: TBD

### Phase 5: Reliability & Security
**Goal**: Sandboxes self-heal from crashes, idle resources are reclaimed, containers are hardened with seccomp/capabilities, and the system exposes health and metrics endpoints
**Depends on**: Phase 4
**Requirements**: CTRL-11, CTRL-12, CTRL-13, CTRL-15, SEC-01, SEC-02, SEC-03, SEC-04, SEC-05, SEC-06
**Success Criteria** (what must be TRUE):
  1. A crashed container auto-restarts up to 3 times with exponential backoff, then transitions to FAILED state
  2. A sandbox idle for 30 minutes is automatically stopped and its route removed
  3. Routing drift detector identifies and fixes discrepancies between Caddy state and Postgres within 60s
  4. All sandbox containers run with seccomp profile, dropped capabilities, no-new-privileges, and PID limit
  5. `/metrics` returns Prometheus-format sandbox counts by state, node utilization, and request latency
**Plans**: TBD

### Phase 6: CLI, SDK & appx-api Integration
**Goal**: Ops can manage the fleet via CLI, appx-api creates sandboxes via the TypeScript SDK, and Railover is fully replaced
**Depends on**: Phase 5
**Requirements**: CLI-01, CLI-02, CLI-03, CLI-04, CLI-05, CLI-06, CLI-07, CLI-08, CLI-09, CLI-10, CLI-11, CLI-12, CLI-13, SDK-01, SDK-02, SDK-03, SDK-04, SDK-05, SDK-06, SDK-07, SDK-08, SDK-09, INT-01, INT-02, INT-03, INT-04, OPS-01, OPS-03, OPS-04, OPS-05
**Success Criteria** (what must be TRUE):
  1. `forge node list` and `forge sandbox list` show accurate fleet state; `forge sandbox logs --follow` streams logs in real time
  2. ForgeClient TypeScript SDK can create a sandbox, push files, get logs, and destroy it -- all with typed responses generated from the OpenAPI spec
  3. appx-api uses ForgeClient instead of RailoverService for all container operations, and RailoverService/ContainerReconciler/CircuitBreaker are deleted
  4. Integration test: appx-api creates sandbox via SDK, pushes files, verifies the preview URL returns content
  5. Ansible playbook bootstraps a fresh node with Docker, Tailscale, and agent systemd service
**Plans**: TBD

### Phase 7: Multi-Node & Failover
**Goal**: Sandboxes are distributed across 3+ nodes, and a node failure triggers automatic reschedule of its running sandboxes within 90s
**Depends on**: Phase 6
**Requirements**: MULTI-01, MULTI-02, MULTI-03
**Success Criteria** (what must be TRUE):
  1. Scheduler distributes sandboxes across 3+ nodes based on available RAM (no single node gets all sandboxes)
  2. When a node stops sending heartbeats, it is marked unhealthy within 45s and its RUNNING sandboxes begin rescheduling
  3. Rescheduled sandboxes are running on healthy nodes within 90s of the original node failure

## Progress

**Execution Order:**
Phases execute in numeric order: 1 -> 2 -> 3 -> 4 -> 5 -> 6 -> 7

| Phase | Plans Complete | Status | Completed |
|-------|----------------|--------|-----------|
| 1. Infrastructure & Contracts | 5/5 | Complete | 2026-04-16 |
| 2. Agent & Container Lifecycle | 0/6 | Planning complete | - |
| 3. Control Plane API | 0/0 | Not started | - |
| 4. Proxy, Routing & File Push | 0/0 | Not started | - |
| 5. Reliability & Security | 0/0 | Not started | - |
| 6. CLI, SDK & appx-api Integration | 0/0 | Not started | - |
| 7. Multi-Node & Failover | 0/0 | Not started | - |
