# Feature Research

**Domain:** Container orchestrator / sandbox management for React Native + Metro sandboxes
**Researched:** 2026-04-15
**Confidence:** HIGH

## Context

Forge is a purpose-built sandbox orchestrator replacing Railover/CapRover/Docker Swarm. It manages React Native + Metro dev sandboxes across a fleet of Contabo VDS nodes. The "users" of Forge are twofold: (1) the appx-api backend calling Forge via SDK to provision sandboxes, and (2) ops humans using the CLI. End-users never interact with Forge directly.

Competitive reference points: Fly.io Machines, E2B sandboxes, Daytona, Railway, Render, CodeSandbox/Together infra, Modal. These platforms range from general-purpose PaaS (Fly, Railway) to AI-agent-specific sandbox runtimes (E2B, Daytona, Modal). Forge sits in a narrow niche: purpose-built for a single workload type (Metro bundler sandboxes) with a known resource profile.

---

## Feature Landscape

### Table Stakes (Users Expect These)

Features that must work correctly or the system is not viable. "Users" here means the appx-api integration and ops team.

| Feature | Why Expected | Complexity | Notes |
|---------|--------------|------------|-------|
| **Sandbox CRUD lifecycle** | Core purpose. Create, inspect, destroy sandboxes via API. Every orchestrator has this. Fly.io Machines API, E2B SDK, Daytona SDK all expose it. | MEDIUM | State machine with ~8 states. Existing STARTER_PLAN design is solid. PENDING -> STARTING -> RUNNING -> STOPPED -> DESTROYED. |
| **Event-driven state machine** | Fly.io has 12+ states with explicit transitions. E2B tracks sandbox lifecycle events. Without a well-defined state machine, you get Railover's ghost container problem. | MEDIUM | Log every transition to events table. No silent state changes. Already designed in STARTER_PLAN. |
| **Container crash detection + auto-restart** | K8s does exponential backoff (10s, 20s, 40s... capped at 5m). Docker does 100ms doubling to 1m. Fly.io auto-restarts on exit. Without this, every Metro OOM kills a sandbox permanently. | MEDIUM | Watch Docker events stream for `die`/`oom`. Report to control plane. Control plane decides restart vs. fail based on failure_count and backoff. Cap at 3 retries, then mark FAILED. |
| **Health checking** | Every orchestrator has this. K8s has liveness/readiness/startup probes. Fly.io has TCP/HTTP checks. Current Railover has gRPC health checks. Without health checks, containers silently die. | MEDIUM | Agent probes container HTTP on port 8081 (Metro status). Report to control plane. Separate startup probe (Metro takes 5-10s) from liveness (is it still responding). |
| **Node registration + heartbeat** | Fly.io agents heartbeat to control plane. E2B tracks compute node availability. Without heartbeats, control plane cannot detect node failures. | LOW | Agent sends heartbeat every 15s. Control plane marks node unhealthy after 3 missed heartbeats (45s). Already in STARTER_PLAN. |
| **Node failure detection + sandbox reschedule** | Fly.io migrates machines on host failure. K8s reschedules pods when nodes go NotReady. Without this, a dead node means all its sandboxes are gone forever. | HIGH | Detect via missed heartbeats. Reschedule RUNNING sandboxes to other nodes within 90s. File state is lost (bind mounts are node-local) -- appx-api must re-push code. |
| **Bin-packing scheduler** | K8s default scheduler scores nodes by resource fit. Fly.io places machines by region and capacity. Even a simple "pick node with most free RAM" beats random. | MEDIUM | Score = free_ram / total_ram. Pick highest score. Exclude draining/unhealthy nodes. Add CPU scoring later if needed. |
| **File push to sandbox** | E2B has filesystem write API. CodeSandbox/Together push code to VMs. This is the primary data path -- AI generates code, pushes to sandbox, Metro rebuilds. | MEDIUM | Control plane redirects to agent's HTTP endpoint with signed URL. Agent writes to bind-mount directory. Metro detects changes via inotify. Already designed in STARTER_PLAN. |
| **Dynamic routing (wildcard DNS)** | Fly.io has Fly Proxy with custom domains and wildcards. Railway routes via private network DNS. Every sandbox needs a URL like `{appName}.myappx.live`. | MEDIUM | Caddy Admin API for dynamic route config. Cloudflare wildcard DNS pointing at Caddy. Control plane adds/removes routes on sandbox state changes. |
| **TLS termination** | Fly.io terminates TLS at proxy. Railway handles TLS automatically. Users expect HTTPS URLs. Cloudflare Origin Certs simplify this -- no Let's Encrypt renewal. | LOW | Cloudflare Origin CA cert on Caddy. 15-year validity. No renewal logic needed. Already planned. |
| **Resource limits (memory, CPU)** | Every orchestrator enforces limits. K8s uses cgroups v2 requests/limits. Docker has --memory and --cpus flags. Without limits, one Metro OOM can kill the node. | LOW | Set via Docker container creation: Memory (512MB default), CPU (0.5 cores), PID limit (256). Enforced by cgroups v2. Already in agent StartSandbox code. |
| **Container logs** | Fly.io has `fly logs`. K8s has `kubectl logs`. Railway streams logs. Ops needs to debug why a sandbox failed. | LOW | Agent wraps `docker logs` API. Control plane proxies or redirects. CLI: `forge sandbox logs <id> --follow`. |
| **Idle reaping / auto-stop** | Fly.io has auto_stop_machines (stop after no traffic). Railway sleeps after 10min inactivity. Render sleeps after 15min. Sandboxes cost RAM -- must reclaim idle ones. | MEDIUM | Track last_active_at on sandbox. Cron or event-driven check: if idle > 30min, stop container. Proxy can wake on next request (stretch goal). |
| **Prometheus metrics + health endpoints** | Every production service exposes /metrics and /healthz. Without observability, you are flying blind. | LOW | Expose sandbox count by state, node utilization, request latency, error rates. Use Go prometheus/client. |
| **CLI for operations** | Fly.io has flyctl. Railway has railway CLI. K8s has kubectl. Ops needs to inspect, debug, drain nodes without writing API calls. | MEDIUM | `forge node list/add/drain/remove`, `forge sandbox list/inspect/logs/restart/destroy`, `forge routes list/verify`. ~1500 LOC of Go with cobra. |
| **TypeScript SDK** | E2B has Python + JS SDKs. Fly.io has flyctl + API. The appx-api (NestJS) needs a typed client. Raw HTTP calls are error-prone. | LOW | Generate from OpenAPI spec. Thin wrapper: create, get, destroy, pushFiles, restart. ~500 LOC + generated types. |
| **Seccomp + capability dropping** | K8s PodSecurityStandards require restricted profiles. Docker best practice is `--cap-drop ALL` + explicit adds. Without this, a compromised sandbox can escalate to root on the node. | LOW | Seccomp JSON profile (restrict to ~40 syscalls). Drop all capabilities, add only CHOWN/SETUID/SETGID (minimum for Node.js). no-new-privileges:true. Already in STARTER_PLAN agent code. |

### Differentiators (Competitive Advantage)

Features that go beyond table stakes and give Forge an edge for this specific use case.

| Feature | Value Proposition | Complexity | Notes |
|---------|-------------------|------------|-------|
| **Warm container pool** | Sub-second sandbox claim. Pre-warm containers with Metro already running and placeholder code. On deploy: claim warm container, push real code, Metro rebuilds in ~200ms. E2B achieves <200ms with Firecracker snapshots. Forge achieves similar with pre-warmed Docker containers. | MEDIUM | Pool maintains N warm containers (configurable per plan tier). Replenishment runs on claim events. Current Railover pool logic works -- port to Forge control plane. Critical for the "instant preview" UX that differentiates AppX. |
| **No reconciliation loop** | Railover/CapRover's biggest pain: a reconciler that kills containers unexpectedly every 60s. Forge uses event-driven state transitions only. State changes happen on explicit events (API call, container exit, heartbeat timeout), never on a timer. Zero spurious restarts. | LOW | This is an architectural decision, not a feature to build. Enforce it by never implementing a periodic state-correction loop. Routing drift checker (Caddy vs Postgres) is the one exception -- runs every 60s but only fixes routes, never kills containers. |
| **Direct agent file push (bypass control plane)** | E2B routes all operations through their API. Fly.io proxies through Fly Proxy. Forge redirects file pushes directly to the agent on the target node via Tailscale. Zero bandwidth through control plane for the hot path (code push during AI generation). Reduces latency by ~50ms per push. | LOW | Control plane returns 307 redirect with signed URL. Agent validates signature, writes files to bind mount. Already designed. The key insight: during AI generation, 10+ files are pushed rapidly -- this path must be fast. |
| **Metro-aware health checking** | Generic orchestrators check HTTP 200 or TCP connect. Forge's agent can probe Metro-specific status: is the bundler running? Is it currently bundling? What's the last bundle timestamp? This prevents false-positive "healthy" when Metro is crashed but the HTTP server still responds 200 on a stale page. | MEDIUM | Agent sends structured health payload: `{metro_running: bool, last_bundle_ms: int, memory_mb: int}`. Control plane uses this for smarter decisions: don't restart during active bundling, alert if memory > 80% of limit. |
| **Tailscale mesh networking** | Private encrypted network between all nodes without manual WireGuard configuration. NAT-traversing (Contabo sometimes blocks UDP). DERP relay fallback adds ~30-80ms but always works. No overlay network complexity like Docker Swarm VXLAN. | LOW | Already planned. Install Tailscale on each node via Ansible. Agent uses Tailscale IP for all inter-node communication. Caddy routes to Tailscale IPs. |
| **Node drain with graceful sandbox migration** | K8s has `kubectl drain` with PodDisruptionBudgets. Fly.io migrates machines on host maintenance. Forge needs this for node OS updates: stop scheduling new sandboxes, wait for existing ones to finish/idle-reap, then safely remove node. | MEDIUM | `forge node drain <id>`: set node status to "draining", scheduler excludes it, existing sandboxes continue until idle timeout or manual destroy. `forge node remove <id>`: only succeeds if sandbox count is 0. |
| **Event audit log** | Every state transition logged to events table with structured payload. Fly.io has a similar events API. This gives ops full visibility: "why did sandbox X restart?" -- look at events: `container_exit(exit_code=137, reason=OOM)` -> `restart_scheduled` -> `started`. | LOW | Already designed in schema. Append-only events table. CLI: `forge events --sandbox=X --since=1h`. Invaluable for debugging without SSH-ing into nodes. |
| **Image pre-pull on node registration** | Docker image pull is 30+ seconds for the sandbox image (~500MB). Pre-pulling on agent startup eliminates this from the critical path. | LOW | Agent pulls sandbox image immediately after registration. Background job checks for new image versions periodically. New sandbox creation never waits for pull. |
| **Signed URL authentication for file push** | Instead of long-lived API keys on agents, control plane issues short-lived signed URLs for each file push operation. Limits blast radius of a compromised URL. | LOW | HMAC-SHA256 with expiry timestamp (60s). Agent validates signature before accepting any file write. Standard pattern. |
| **Routing drift detection** | Caddy's routing state can drift from Postgres truth (crashed config update, partial apply). A periodic reconciler (every 60s) diffs Caddy state vs Postgres and fixes discrepancies. This is NOT a container reconciler -- it only touches routes. | LOW | `forge routes verify` also exposes this. Diff Caddy's current config against expected routes from Postgres. Fix any drift. Log it as an event. |

### Anti-Features (Commonly Requested, Often Problematic)

| Feature | Why Requested | Why Problematic | Alternative |
|---------|---------------|-----------------|-------------|
| **Kubernetes / Swarm integration** | "Use existing orchestrator instead of building your own." | K8s is massive overkill for <100 containers on <5 nodes. Swarm has the exact 60s restart bug we're escaping. Both add operational complexity that exceeds the problem's complexity. | Plain `docker run` via Docker Engine API. Forge IS the orchestrator -- simple enough to fit in <5K LOC. |
| **Firecracker/microVM isolation** | E2B and Daytona use Firecracker for VM-level isolation. Seems like the "right" security choice. | Massive implementation complexity. Requires custom kernel images, rootfs management, snapshot infrastructure. Our sandboxes run trusted code (AI-generated RN) for known users, not arbitrary untrusted code from the internet. Container isolation with seccomp is sufficient for this threat model. | Docker containers with seccomp profiles + capability dropping + no-new-privileges. Upgrade path to Firecracker is documented as stretch goal if threat model changes. |
| **Sandbox suspend/resume (CRIU)** | Fly.io has suspend/resume with ~200ms resume time. Would save RAM by suspending idle sandboxes. | CRIU is fragile with Node.js/Metro processes. Complex setup (requires specific kernel patches, PID namespace handling). Metro has internal state (file watchers, module graph) that may not survive checkpoint. | Stop + restart (cold start ~5-10s for Metro). Warm pool covers the "instant" use case. Idle sandboxes get stopped, not suspended. |
| **Live sandbox migration** | Move a running sandbox from one node to another without downtime. Useful for node maintenance. | Requires CRIU + filesystem sync + network switchover. Extremely complex. For dev sandboxes (not production servers), a 5-10s interruption during node drain is acceptable. | Drain node: let sandboxes idle-timeout. If urgent: destroy on old node, re-create on new node, re-push code from appx-api. ~15s total. |
| **Web UI / dashboard for Forge** | "Visualize sandbox states, node health, resource usage in a browser." | Solo dev building another web app on top of the system being built. The appx-api admin dashboard already shows container state. Forge's job is to be infrastructure, not a product. | CLI for ops. appx-api admin dashboard for user-facing status. Prometheus + Grafana if visualization is truly needed. |
| **Per-sandbox custom networking (VPC/overlay)** | Each sandbox gets its own network namespace with custom DNS, firewall rules, outbound filtering. | Overkill for dev preview sandboxes that only need to serve HTTP on one port. Adds complexity to proxy routing and debugging. | All sandboxes share host network namespace with port mapping. Each sandbox binds to a unique host port (40000-50000 range). Caddy routes by hostname to the correct port. |
| **Multi-region deployment** | Run sandboxes in multiple geographic regions for lower latency. | Single region (Germany/Contabo) is fine for v1. Multi-region needs per-region control planes, global routing, data sync. Latency is dominated by Metro cold start (5-10s), not network RTT (~100ms). | Single Cloudflare zone, single Tailscale tailnet. Document multi-region as post-v1 stretch goal. |
| **GPU support** | AI workloads sometimes need GPUs (Modal, E2B offer this). | Metro/React Native bundling is CPU-bound, not GPU-bound. Contabo VDS nodes don't have GPUs. Adding GPU support is a different product entirely. | Not applicable to this workload. |
| **Automatic horizontal scaling** | Scale sandbox count based on demand. Spin up new VDS nodes automatically. | Contabo provisioning is manual (no API for instant node creation). Node bootstrapping takes 5-10 minutes (Docker + Tailscale + agent install). Demand is predictable enough for manual scaling. | `forge node add` via CLI + Ansible playbook. Monitor pool utilization, add nodes manually when utilization exceeds 70%. |
| **Continuous reconciliation loop** | "Periodically check all containers match desired state and fix drift." | This is EXACTLY what caused Railover's 60s restart bug. The reconciler would see temporary state (container restarting) and "fix" it by killing the container. Event-driven is strictly better: state only changes in response to explicit events. | Event-driven: Docker events stream (container exit/OOM), heartbeat timeout (node failure), API calls (user actions). The only periodic check is routing drift (Caddy vs Postgres, routes only, never containers). |

---

## Feature Dependencies

```
[Sandbox CRUD Lifecycle]
    |
    +--requires--> [Event-Driven State Machine]
    |                  |
    |                  +--requires--> [Events Audit Log]
    |
    +--requires--> [Node Registration + Heartbeat]
    |                  |
    |                  +--enables--> [Node Failure Detection + Reschedule]
    |                  |
    |                  +--enables--> [Bin-Packing Scheduler]
    |
    +--requires--> [Resource Limits (Memory, CPU)]
    |
    +--enables--> [Container Crash Detection + Auto-Restart]
    |
    +--enables--> [Health Checking]
    |
    +--enables--> [Container Logs]

[File Push to Sandbox]
    |
    +--requires--> [Sandbox CRUD Lifecycle]
    +--enhanced-by--> [Direct Agent File Push (signed URL)]
    +--enhanced-by--> [Image Pre-Pull]

[Dynamic Routing (Wildcard DNS)]
    |
    +--requires--> [TLS Termination]
    +--requires--> [Sandbox CRUD Lifecycle]
    +--enhanced-by--> [Routing Drift Detection]

[Idle Reaping / Auto-Stop]
    |
    +--requires--> [Health Checking]
    +--requires--> [Dynamic Routing] (to remove routes on stop)

[Warm Container Pool]
    |
    +--requires--> [Sandbox CRUD Lifecycle]
    +--requires--> [File Push to Sandbox]
    +--requires--> [Bin-Packing Scheduler]

[Node Drain]
    |
    +--requires--> [Idle Reaping]
    +--requires--> [Bin-Packing Scheduler] (to exclude draining nodes)

[CLI]
    |
    +--requires--> [All Control Plane API endpoints]

[TypeScript SDK]
    |
    +--requires--> [All Control Plane API endpoints]
    +--requires--> [OpenAPI Spec]
```

### Dependency Notes

- **Warm Pool requires File Push:** a warm container needs placeholder code pushed on creation, then real code pushed on claim.
- **Node Drain requires Idle Reaping:** draining a node depends on sandboxes eventually stopping via idle timeout.
- **Routing Drift Detection enhances Dynamic Routing:** it is not required for routing to work, but prevents silent routing failures that are hard to debug.
- **Health Checking requires Sandbox Lifecycle:** you can only check health of sandboxes that exist in a known state.
- **CLI and SDK require all API endpoints:** they are consumers, not providers. Build API first, then clients.

---

## MVP Definition

### Launch With (v1)

Minimum viable to replace Railover and serve production traffic.

- [x] Sandbox CRUD lifecycle with event-driven state machine -- core purpose
- [x] Node registration + heartbeat -- foundational for multi-node
- [x] Bin-packing scheduler -- assign sandboxes to nodes
- [x] Container crash detection + auto-restart with backoff -- reliability baseline
- [x] File push (direct to agent) -- the primary data path for AI generation
- [x] Dynamic routing via Caddy + Cloudflare -- sandboxes need URLs
- [x] TLS termination via Cloudflare Origin Cert -- HTTPS is non-negotiable
- [x] Resource limits (memory, CPU, PIDs) -- prevent noisy neighbors
- [x] Container logs -- basic debugging capability
- [x] Idle reaping (30min timeout) -- cost control
- [x] Seccomp profiles + capability dropping -- security baseline
- [x] Health checking (HTTP probe on Metro port) -- detect dead containers
- [x] Prometheus metrics + /healthz -- basic observability
- [x] TypeScript SDK -- appx-api integration point
- [x] CLI (core commands: node list/add, sandbox list/inspect/logs/destroy) -- ops capability

### Add After Validation (v1.1)

Features to add once v1 is stable and serving production traffic for 1+ weeks.

- [ ] Warm container pool -- add when cold start latency is validated as a problem
- [ ] Metro-aware health checking -- add when basic HTTP probe proves insufficient
- [ ] Node failure detection + automatic reschedule -- add when running on 2+ nodes
- [ ] Node drain -- add before the first planned node maintenance
- [ ] Event audit log query API -- add when debugging via raw SQL becomes painful
- [ ] Routing drift detection -- add when first routing inconsistency is observed
- [ ] Image pre-pull on registration -- add when image pull delays are measured
- [ ] CLI: routes verify, events query -- expand CLI as operational needs emerge

### Future Consideration (v2+)

Features to defer until product-market fit is validated and scale demands them.

- [ ] Firecracker/microVM upgrade -- only if security threat model changes (untrusted users)
- [ ] Sandbox suspend/resume via CRIU -- only if Metro proves checkpoint-safe
- [ ] Multi-region deployment -- only if user base is geographically distributed
- [ ] Automatic horizontal node scaling -- only if node count exceeds 5-10
- [ ] Web UI / Forge dashboard -- only if CLI proves genuinely insufficient for ops

---

## Feature Prioritization Matrix

| Feature | User Value | Implementation Cost | Priority |
|---------|------------|---------------------|----------|
| Sandbox CRUD lifecycle | HIGH | MEDIUM | P1 |
| Event-driven state machine | HIGH | MEDIUM | P1 |
| Node registration + heartbeat | HIGH | LOW | P1 |
| Bin-packing scheduler | HIGH | LOW | P1 |
| Container crash detection | HIGH | MEDIUM | P1 |
| File push to sandbox | HIGH | MEDIUM | P1 |
| Dynamic routing | HIGH | MEDIUM | P1 |
| TLS termination | HIGH | LOW | P1 |
| Resource limits | HIGH | LOW | P1 |
| Container logs | MEDIUM | LOW | P1 |
| Idle reaping | HIGH | MEDIUM | P1 |
| Seccomp + capability drop | HIGH | LOW | P1 |
| Health checking | HIGH | MEDIUM | P1 |
| Prometheus metrics | MEDIUM | LOW | P1 |
| TypeScript SDK | HIGH | LOW | P1 |
| CLI (core) | MEDIUM | MEDIUM | P1 |
| Warm container pool | HIGH | MEDIUM | P2 |
| Metro-aware health checks | MEDIUM | MEDIUM | P2 |
| Node failure + reschedule | HIGH | HIGH | P2 |
| Node drain | MEDIUM | MEDIUM | P2 |
| Event audit API | LOW | LOW | P2 |
| Routing drift detection | MEDIUM | LOW | P2 |
| Image pre-pull | MEDIUM | LOW | P2 |
| Firecracker upgrade | LOW | HIGH | P3 |
| Suspend/resume (CRIU) | LOW | HIGH | P3 |
| Multi-region | LOW | HIGH | P3 |
| Auto horizontal scaling | LOW | HIGH | P3 |

---

## Competitor Feature Analysis

| Feature | Fly.io Machines | E2B Sandboxes | Daytona | Railway | Forge (Our Plan) |
|---------|-----------------|---------------|---------|---------|-----------------|
| **Isolation** | Firecracker microVMs | Firecracker microVMs | Docker (Kata optional) | Containers | Docker + seccomp + cap-drop |
| **Cold start** | Sub-second (VM) | <200ms (snapshot) | ~90ms (snapshot) | Seconds | ~5s (Metro), <1s with warm pool |
| **State machine** | 12+ states (created, started, stopped, suspended, failed, destroyed, replaced, migrated...) | Simple (running, stopped) | Simple (running, archived) | Simple (running, sleeping) | 8 states (pending, starting, running, restarting, stopped, failed, destroying, destroyed) |
| **Auto-stop idle** | Yes (stop or suspend) | 24h max session | Yes (archiving) | 10min inactivity | 30min idle timeout |
| **Suspend/resume** | Yes (~200ms resume) | No | No | No | No (v1), possible v2 |
| **File push** | Via volumes or API | SDK filesystem API | SDK filesystem API | Git push | Direct HTTP to agent, signed URLs |
| **Private networking** | 6PN (WireGuard mesh) | N/A | N/A | WireGuard mesh | Tailscale mesh |
| **Custom domains** | Yes (wildcard + LE) | No (sandbox URLs) | No | Yes | Yes (wildcard via Cloudflare) |
| **Health checks** | TCP/HTTP probes | Sandbox timeout | Heartbeat | Platform-managed | HTTP probe + Metro-specific |
| **Warm pool** | No (fast VMs instead) | Snapshots serve this role | Snapshots serve this role | No | Yes, pre-warmed Metro containers |
| **Node management** | Managed by Fly | Managed by E2B | Managed by Daytona | Managed by Railway | Self-managed via CLI + Ansible |
| **Observability** | Logs, metrics, Sentry | SDK events | Logs | Logs, metrics | Prometheus + structured events |
| **Security model** | VM boundary (hardware) | VM boundary (hardware) | Container (shared kernel) | Container | Container + seccomp (sufficient for trusted code) |
| **Pricing model** | Per-second VM usage | Per-second vCPU + RAM | Per-second | Per-second | Self-hosted (Contabo VDS ~$15/mo/node) |

### Key Insights from Competitor Analysis

1. **E2B and Daytona use Firecracker snapshots for sub-200ms starts.** Forge achieves comparable UX via warm container pool -- different mechanism, similar result for the user. The tradeoff is RAM (warm pool consumes memory) vs. complexity (snapshot infrastructure).

2. **Fly.io's suspend/resume is uniquely powerful** -- preserves full memory state. This is NOT worth pursuing for v1 because Metro's internal state (module graph, file watchers) is unlikely to survive checkpoint cleanly.

3. **Railway and Render have the simplest idle management** -- basic inactivity timers. Forge's 30min idle timeout is in the same ballpark and sufficient.

4. **No competitor has Metro-specific health checking.** This is a genuine differentiator -- knowing if Metro is bundling vs. just having an HTTP server alive prevents false-positive health checks.

5. **Self-hosted cost advantage is massive.** E2B charges ~$0.05/vCPU-hour. A Contabo VDS with 6 vCPUs costs ~$15/month total, or ~$0.002/vCPU-hour -- 25x cheaper. The tradeoff is operational burden, which Forge's simplicity keeps manageable.

---

## Sources

- [Fly.io Machine States and Lifecycle](https://fly.io/docs/machines/machine-states/)
- [Fly.io Autostop/Autostart](https://fly.io/docs/launch/autostop-autostart/)
- [Fly.io Machine Suspend/Resume](https://fly.io/docs/reference/suspend-resume/)
- [Fly.io Machines API](https://fly.io/docs/machines/api/)
- [Fly.io TLS Termination](https://fly.io/docs/security/tls-termination/)
- [Fly.io Private Networking](https://fly.io/docs/networking/private-networking/)
- [E2B Documentation](https://e2b.dev/docs)
- [E2B GitHub](https://github.com/e2b-dev/E2B)
- [E2B vs Modal Comparison](https://northflank.com/blog/e2b-vs-modal)
- [Daytona GitHub](https://github.com/daytonaio/daytona)
- [Daytona vs E2B Comparison](https://northflank.com/blog/daytona-vs-e2b-ai-code-execution-sandboxes)
- [Railway Serverless/App Sleeping](https://docs.railway.com/reference/app-sleeping)
- [Sandbox Providers Overview](https://northflank.com/blog/sandbox-providers)
- [Best Code Execution Sandbox for AI Agents](https://northflank.com/blog/best-code-execution-sandbox-for-ai-agents)
- [Kubernetes Health Probes Guide](https://kubernetes.io/docs/concepts/configuration/liveness-readiness-startup-probes/)
- [Container Isolation: gVisor vs Firecracker](https://northflank.com/blog/kata-containers-vs-firecracker-vs-gvisor)
- [MicroVM Isolation in 2026](https://emirb.github.io/blog/microvm-2026/)
- [Docker Container Restart Policies](https://oneuptime.com/blog/post/2026-01-25-docker-container-restart-policies/view)
- [Kubernetes Bin Packing](https://kubernetes.io/docs/concepts/scheduling-eviction/resource-bin-packing/)
- [Pre-Warming Pool for Sandbox Cold Start](https://pacoxu.wordpress.com/2025/12/02/agent-sandbox-pre-warming-pool-makes-secure-containers-cold-start-lightning-fast/)
- [Kubernetes Multi-tenancy](https://kubernetes.io/docs/concepts/security/multi-tenancy/)
- [Cloudflare Sandbox SDK](https://developers.cloudflare.com/sandbox/)

---
*Feature research for: Container orchestrator / sandbox management (AppX Forge)*
*Researched: 2026-04-15*
