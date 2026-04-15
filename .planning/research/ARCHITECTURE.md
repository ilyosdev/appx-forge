# Architecture Research: Custom Container Orchestrator

**Domain:** Container orchestration (custom, non-Kubernetes)
**Researched:** 2026-04-15
**Confidence:** HIGH

## Standard Architecture

### System Overview

```
                          Cloudflare (DNS + CDN)
                                  |
                          *.myappx.live
                                  |
                    +-------------+-------------+
                    |                           |
              +-----------+               +-----------+
              |   Caddy   |               |   Caddy   |  (optional 2nd)
              |  (Proxy)  |               |  (Proxy)  |
              +-----+-----+               +-----+-----+
                    |                           |
          Tailscale Mesh (WireGuard encrypted)
                    |
    +---------------+---------------+
    |               |               |
+---+---+     +----+----+    +-----+-----+
| Agent |     |  Agent  |   |   Agent   |
| Node1 |     |  Node2  |   |   Node3   |
+---+---+     +----+----+   +-----+-----+
    |               |              |
 [Sandboxes]    [Sandboxes]   [Sandboxes]
 (Docker)       (Docker)      (Docker)

                    |
                    | Long-Poll Commands
                    | Heartbeats
                    | Status Reports
                    |
            +-------+--------+
            | forge-control  |
            | (Control Plane)|
            +-------+--------+
                    |
              +-----+------+
              |  Postgres  |
              | (Neon/     |
              |  Supabase) |
              +------------+

        +-------+     +----------+
        |  CLI  |     | SDK (TS) |
        +-------+     +----------+
            |               |
            +----> forge-control API <----+
                                          |
                                     appx-api
                                    (NestJS)
```

### Component Responsibilities

| Component | Responsibility | Communication |
|-----------|----------------|---------------|
| **forge-control** | Source of truth. Owns sandbox state machine, scheduler, routing table, API surface. Single Go binary, ~5K LOC. | Exposes REST API (chi). Pushes routes to Caddy Admin API. Serves long-poll endpoint for agents. Reads/writes Postgres. |
| **forge-agent** | Executes Docker operations on local host. Watches container events. Reports status. Receives files. Single Go binary, ~3K LOC. | Long-polls control plane for commands. POSTs heartbeats + status. Talks to local Docker Engine API. Exposes HTTP for file push. |
| **Caddy (proxy)** | Routes wildcard traffic to correct node:port. Terminates Cloudflare Origin TLS. Handles WebSocket upgrade. | Receives route config via Admin API from control plane. Proxies HTTP/WS to agents over Tailscale. |
| **Postgres** | Persistent state: nodes, sandboxes, events, routes. Advisory locks for leader election. | pgx/v5 from control plane only. No other service talks to DB. |
| **forge-cli** | Operator tool for node/sandbox/route management. | Calls control plane REST API. |
| **forge-sdk-ts** | TypeScript client for appx-api integration. | Calls control plane REST API. Generated from OpenAPI spec. |
| **Tailscale mesh** | Encrypted cross-node networking. NAT-traversing with DERP fallback. | All agent-to-control and proxy-to-agent traffic flows over Tailscale IPs. |

## Recommended Project Structure

```
appx-forge/
├── go.work                          # Go workspace root
├── docs/
│   ├── contracts/
│   │   ├── control-api.openapi.yaml # Sacred contract -- write FIRST
│   │   ├── agent-protocol.md        # Command/ack protocol
│   │   ├── filepush-protocol.md     # Signed URL redirect pattern
│   │   └── proxy-routing.md         # Caddy Admin API usage
│   ├── adr/                         # Architecture Decision Records
│   └── runbooks/                    # Ops procedures
├── control/                         # forge-control service
│   ├── go.mod
│   ├── cmd/forge-control/main.go    # Entry point
│   ├── internal/
│   │   ├── api/                     # HTTP handlers (chi)
│   │   │   ├── router.go            # Route registration
│   │   │   ├── sandbox_handler.go   # Sandbox CRUD
│   │   │   ├── node_handler.go      # Node registration/heartbeat
│   │   │   └── middleware.go        # Auth, logging, request ID
│   │   ├── scheduler/              # Bin-packing placement
│   │   │   ├── scheduler.go        # Interface + default impl
│   │   │   └── binpack.go          # Best-fit decreasing scorer
│   │   ├── lifecycle/              # Sandbox state machine
│   │   │   ├── state.go            # State type + valid transitions
│   │   │   └── machine.go          # Transition logic + event emit
│   │   ├── commands/               # Command dispatch to agents
│   │   │   ├── queue.go            # Per-agent command queue
│   │   │   └── longpoll.go         # Long-poll handler
│   │   ├── routing/                # Proxy route management
│   │   │   ├── caddy_client.go     # Caddy Admin API client
│   │   │   └── reconciler.go       # Periodic drift check
│   │   └── store/                  # Postgres queries (sqlc)
│   │       ├── queries/            # .sql files
│   │       ├── models.go           # Generated
│   │       └── db.go               # Generated
│   ├── migrations/                 # SQL (goose)
│   └── tests/
├── agent/                          # forge-agent
│   ├── go.mod
│   ├── cmd/forge-agent/main.go
│   ├── internal/
│   │   ├── docker/                 # Docker SDK wrappers
│   │   │   ├── containers.go       # Create/start/stop/remove
│   │   │   ├── events.go           # Events stream watcher
│   │   │   └── images.go           # Pre-pull management
│   │   ├── controlclient/          # HTTP client to control plane
│   │   │   ├── client.go           # Registration + heartbeat
│   │   │   └── longpoll.go         # Command polling loop
│   │   ├── executor/               # Command execution
│   │   │   └── executor.go         # Dispatch commands to handlers
│   │   ├── filepush/               # File upload HTTP server
│   │   │   └── handler.go          # Signed URL verification + write
│   │   ├── ports/                  # Host port allocation
│   │   │   └── allocator.go        # Range 40000-50000
│   │   └── health/                 # Self-health + heartbeat
│   └── tests/
├── shared-go/                      # Shared types
│   ├── go.mod
│   ├── models/                     # Sandbox, Node, Command structs
│   └── auth/                       # Signed URL generation/verification
├── cli/                            # forge-cli (cobra)
│   ├── go.mod
│   └── cmd/forge/
├── sdk-ts/                         # TypeScript SDK
│   ├── package.json
│   └── src/
├── sandbox-image/                  # Base Docker image
│   └── Dockerfile
└── deploy/
    ├── docker-compose.dev.yml      # Local dev
    ├── ansible/                    # Node bootstrap
    └── terraform/                  # Cloudflare DNS
```

### Structure Rationale

- **Go workspaces (go.work):** All Go services share a workspace for atomic commits and shared types, but each has its own go.mod for independent builds. This is how Fly.io, Nomad, and most Go monorepos organize multiple services.
- **internal/ everywhere:** Go convention -- prevents external imports of private packages. Forces clean API boundaries.
- **shared-go/ minimal:** Only truly shared types (models, auth). Resist the urge to put business logic here. If control and agent need different behavior, duplicate rather than abstract.
- **contracts/ written first:** The OpenAPI spec gates all parallel work. Tracks B-F generate code from it. This pattern comes directly from the Nomad, Fly.io, and Railway architectures where the API contract is the integration boundary.
- **store/ with sqlc:** Generated code lives alongside queries. No ORM abstraction layer -- raw SQL with type safety. This is the dominant Go+Postgres pattern in 2025-2026.

## Architectural Patterns

### Pattern 1: Event-Driven State Machine (No Reconciler)

**What:** Sandbox state changes happen ONLY in response to explicit events (API call, container exit report, heartbeat timeout). No background loop polls and "fixes" state. Every transition is logged to an append-only events table.

**When to use:** Always for this system. This is the primary architectural distinction from Railover/CapRover/Docker Swarm, which all use reconciliation loops that cause spurious restarts.

**Trade-offs:**
- Pro: Deterministic behavior, no spurious state changes, full audit trail
- Pro: Much simpler to debug -- every state change has a recorded cause
- Con: Requires disciplined event reporting from agents
- Con: Orphaned containers possible if agent misses a "die" event (mitigated by periodic agent-side container list sync)

**Evidence:** Fly.io moved away from Nomad partly because of reconciler issues. Their flyd keeps state local to workers with explicit event-driven updates. E2B uses event-driven sandbox lifecycle with explicit timeout-based termination. The "Build an Orchestrator in Go" book (Manning) structures its Cube orchestrator around explicit task state transitions.

**State machine (from STARTER_PLAN, validated against Docker container lifecycle best practices):**

```
     create()
        |
     PENDING ---scheduler picks node---> STARTING
        |                                    |
   (no nodes)                     (agent reports running)
        |                                    |
     FAILED                              RUNNING
                                            |
                          +-----------------+-----------------+
                          |                 |                 |
                   container exits    destroy() called   idle timeout
                          |                 |                 |
                     RESTARTING         DESTROYING         STOPPED
                          |                 |
                (restart attempt)           |
                          |              DESTROYED
                  RUNNING / FAILED
```

Key design rules:
- Transitions are a function: `(currentState, event) -> (newState, sideEffects)`
- Invalid transitions return an error, never silently succeed
- Every transition writes to `events` table before updating `sandboxes` table (crash-safe ordering)
- The `failure_count` field on sandbox enables exponential backoff for restart attempts

**Example:**
```go
type State string

const (
    StatePending    State = "pending"
    StateStarting   State = "starting"
    StateRunning    State = "running"
    StateRestarting State = "restarting"
    StateStopped    State = "stopped"
    StateDestroying State = "destroying"
    StateDestroyed  State = "destroyed"
    StateFailed     State = "failed"
)

type Event string

const (
    EventScheduled       Event = "scheduled"
    EventStarted         Event = "started"
    EventContainerExited Event = "container_exited"
    EventIdleTimeout     Event = "idle_timeout"
    EventDestroyRequest  Event = "destroy_requested"
    EventDestroyed       Event = "destroyed"
    EventRestartAttempt  Event = "restart_attempt"
    EventNodeFailed      Event = "node_failed"
)

var validTransitions = map[State]map[Event]State{
    StatePending:    {EventScheduled: StateStarting, EventDestroyRequest: StateDestroyed},
    StateStarting:   {EventStarted: StateRunning, EventContainerExited: StateFailed},
    StateRunning:    {EventContainerExited: StateRestarting, EventDestroyRequest: StateDestroying, EventIdleTimeout: StateStopped, EventNodeFailed: StatePending},
    StateRestarting: {EventRestartAttempt: StateStarting, EventDestroyRequest: StateDestroying},
    StateStopped:    {EventScheduled: StateStarting, EventDestroyRequest: StateDestroyed},
    StateDestroying: {EventDestroyed: StateDestroyed},
    StateFailed:     {EventRestartAttempt: StateStarting, EventDestroyRequest: StateDestroyed},
}

func (sm *Machine) Transition(sandboxID string, event Event) (State, error) {
    current := sm.store.GetState(sandboxID)
    transitions, ok := validTransitions[current]
    if !ok {
        return current, fmt.Errorf("no transitions from state %s", current)
    }
    next, ok := transitions[event]
    if !ok {
        return current, fmt.Errorf("invalid event %s in state %s", event, current)
    }
    // Write event FIRST (crash safety), then update state
    sm.store.RecordEvent(sandboxID, event, current, next)
    sm.store.UpdateState(sandboxID, next)
    return next, nil
}
```

### Pattern 2: Long-Poll Command Dispatch with Idempotent Acknowledgment

**What:** Agents poll `GET /v1/agents/:id/commands?wait=30s`. Control plane holds the connection open until a command is queued or timeout expires. Agent executes command, then POSTs ack with result. Commands have unique IDs for deduplication.

**When to use:** For all control-plane-to-agent communication in v1.

**Trade-offs:**
- Pro: Simpler than WebSocket/gRPC (no connection state, no proto files, standard HTTP)
- Pro: Identical latency to WebSocket in practice (command available = immediate response)
- Pro: Easy to debug with curl
- Pro: Firewall-friendly (outbound HTTP from agent)
- Con: Slightly more overhead than persistent connections at very high command rates (not a concern at <100 nodes)
- Con: 30s max wait means worst-case 30s latency for commands queued just after a timeout

**Evidence:** Consul uses long-polling for service discovery watches. Fly.io's attache component tracks Consul via long-poll HTTP endpoints. HashiCorp Vault uses long-poll for secret rotation notifications. The pattern is battle-tested for agent communication at scale.

**Why not gRPC:** gRPC adds proto file maintenance, code generation step, and bidirectional streaming complexity that isn't needed for a command/ack pattern. At <100 nodes with <10 commands/second, HTTP long-poll is simpler and equally performant. Can always upgrade to gRPC later without changing the protocol semantics.

**Why not SSE:** SSE is unidirectional (server-to-client only). We need the agent to ack commands. With SSE, we'd need a separate POST endpoint for acks anyway, effectively building long-poll with extra steps.

**Implementation detail -- idempotency:**
```go
// Agent side: command processing loop
func (a *Agent) CommandLoop(ctx context.Context) {
    for {
        cmd, err := a.controlClient.PollCommand(ctx, 30*time.Second)
        if err != nil {
            slog.Error("poll failed", "err", err)
            time.Sleep(2 * time.Second) // backoff on error
            continue
        }
        if cmd == nil {
            continue // timeout, poll again
        }

        // Idempotency: check if we already processed this command
        if a.processedCommands.Has(cmd.ID) {
            a.controlClient.AckCommand(cmd.ID, AckResult{Status: "already_processed"})
            continue
        }

        result := a.executeCommand(cmd)
        a.controlClient.AckCommand(cmd.ID, result)
        a.processedCommands.Add(cmd.ID) // LRU cache, keep last 1000
    }
}

// Control plane side: command queue per agent
type CommandQueue struct {
    mu       sync.Mutex
    commands map[string][]Command  // agentID -> pending commands
    waiters  map[string]chan struct{} // agentID -> notify channel
}

func (q *CommandQueue) Enqueue(agentID string, cmd Command) {
    q.mu.Lock()
    q.commands[agentID] = append(q.commands[agentID], cmd)
    if waiter, ok := q.waiters[agentID]; ok {
        close(waiter) // wake up long-poll
    }
    q.mu.Unlock()
}

func (q *CommandQueue) Poll(agentID string, timeout time.Duration) *Command {
    q.mu.Lock()
    if cmds := q.commands[agentID]; len(cmds) > 0 {
        cmd := cmds[0]
        q.commands[agentID] = cmds[1:]
        q.mu.Unlock()
        return &cmd
    }
    // No commands: create waiter and block
    waiter := make(chan struct{})
    q.waiters[agentID] = waiter
    q.mu.Unlock()

    select {
    case <-waiter:
        return q.dequeue(agentID)
    case <-time.After(timeout):
        return nil
    }
}
```

### Pattern 3: Best-Fit Decreasing Bin-Packing Scheduler

**What:** When placing a sandbox, score all healthy nodes by how well the sandbox fits (tightest fit = highest score). Pick the node where the sandbox leaves the least free resources. This maximizes density -- packing nodes full before using new ones.

**When to use:** Default scheduling strategy. Keeps fewer nodes active (cost savings) and makes it easier to drain nodes for maintenance.

**Trade-offs:**
- Pro: Maximizes node utilization, minimizes cost
- Pro: Simple to implement (< 100 LOC)
- Con: Can create hotspots if not combined with anti-affinity (same user's sandboxes on same node)
- Con: No spread = correlated failure risk (mitigated: this isn't a mission-critical HA system)

**Evidence:** Nomad's `BinPackIterator` in `scheduler/rank.go` uses this exact pattern with a `binPackingMaxFitScore` of 18.0, scoring based on free CPU and memory percentages. Kubernetes' `MostRequestedPriority` scheduler does the same. The "Build an Orchestrator in Go" book implements this in its scheduler chapter.

**Implementation:**
```go
type Scorer func(node Node, request ResourceRequest) float64

func BinPackScore(node Node, req ResourceRequest) float64 {
    freeMemAfter := float64(node.CapacityMB-node.UsedMB-req.MemoryMB) / float64(node.CapacityMB)
    freeCPUAfter := float64(node.CapacityCPU-node.UsedCPU-req.CPU) / float64(node.CapacityCPU)

    if freeMemAfter < 0 || freeCPUAfter < 0 {
        return -1 // doesn't fit
    }

    // Higher score = tighter fit = less wasted resources
    // Normalize to 0-1 range where 1 = perfect fit
    memScore := 1.0 - freeMemAfter
    cpuScore := 1.0 - freeCPUAfter
    return (memScore + cpuScore) / 2.0
}

func (s *Scheduler) PickNode(req ResourceRequest) (*Node, error) {
    nodes := s.store.ListHealthyNodes()

    var best *Node
    bestScore := -1.0

    for _, node := range nodes {
        score := BinPackScore(node, req)
        if score > bestScore {
            bestScore = score
            best = &node
        }
    }

    if best == nil {
        return nil, ErrNoFeasibleNode
    }
    return best, nil
}
```

### Pattern 4: Signed-URL File Push (Direct-to-Agent)

**What:** When SDK calls `POST /v1/sandboxes/:id/files`, control plane does NOT receive the file body. Instead, it generates a short-lived signed URL pointing at the agent's direct HTTP endpoint and returns a 307 redirect. The client (appx-api) follows the redirect and uploads directly to the agent. File goes client -> agent, never through control plane.

**When to use:** All file push operations. Critical for AI generation flow where multiple files are pushed rapidly during code generation.

**Trade-offs:**
- Pro: Control plane never handles large payloads (stays lightweight)
- Pro: Lower latency (one fewer hop)
- Pro: Agent writes directly to bind mount (inotify triggers Metro HMR)
- Con: Client must follow redirects (trivial in any HTTP client)
- Con: Agent must verify signed URLs (HMAC, ~20 LOC)
- Con: Agent must be network-reachable from client (Tailscale handles this)

**Evidence:** This is the standard pattern for S3 presigned uploads, Google Cloud signed URLs, and MinIO presigned PUTs. E2B uses signature-based access control with time-limited tokens for file operations. Railway's stacker model similarly has compute nodes handle data directly.

**Flow:**
```
appx-api (SDK)
    |
    | POST /v1/sandboxes/:id/files
    |   (small JSON body: just file metadata)
    v
forge-control
    |
    | 1. Look up sandbox -> find node_id, agent IP
    | 2. Generate signed URL: HMAC(agent_secret, sandbox_id, expiry)
    | 3. Return 307 Redirect to agent endpoint
    v
forge-agent (on target node)
    |
    | POST /sandboxes/:id/files?token=<signed>&expires=<ts>
    |   (body: tar.gz of files)
    | 1. Verify HMAC signature
    | 2. Extract tar to /var/lib/forge/sandboxes/<app>/code/
    | 3. inotify triggers Metro HMR
    | 4. Return 200 with written file list
    v
Metro detects changes -> HMR update in <500ms
```

### Pattern 5: Docker Events Stream with Reconnect

**What:** Agent watches Docker daemon's event stream filtered for container `die` and `oom` events. On event, immediately reports to control plane. On stream error, reconnects with backoff.

**When to use:** Always running while agent is alive. This is the primary mechanism for crash detection -- faster than any polling-based health check.

**Trade-offs:**
- Pro: Near-instant crash detection (sub-second)
- Pro: Zero overhead when nothing is happening (stream is idle)
- Pro: Docker SDK handles the HTTP/chunked-encoding complexity
- Con: OOM events can repeat rapidly (need deduplication)
- Con: Stream can break on Docker daemon restart (need reconnect logic)

**Evidence:** Docker Go SDK's `client.Events()` returns channels for messages and errors, standard reconnect pattern. The `docker/go-events` library provides `ExponentialBackoff` and `Breaker` patterns for resilience. The "Build an Orchestrator in Go" book covers this pattern in its worker chapters.

**Implementation:**
```go
func (a *Agent) WatchEvents(ctx context.Context) {
    filters := filters.NewArgs(
        filters.Arg("type", "container"),
        filters.Arg("event", "die"),
        filters.Arg("event", "oom"),
        filters.Arg("label", "managed-by=forge"),
    )

    backoff := time.Second
    for {
        msgCh, errCh := a.docker.Events(ctx, events.ListOptions{Filters: filters})
        backoff = time.Second // reset on successful connect

        for {
            select {
            case msg := <-msgCh:
                sandboxID := a.extractSandboxID(msg.Actor.Attributes["name"])
                if sandboxID == "" {
                    continue // not our container
                }
                exitCode := msg.Actor.Attributes["exitCode"]
                a.controlClient.ReportContainerExit(ctx, sandboxID, exitCode, msg.Action)

            case err := <-errCh:
                if err != nil {
                    slog.Error("docker events stream error", "err", err)
                }
                goto reconnect

            case <-ctx.Done():
                return
            }
        }

    reconnect:
        time.Sleep(backoff)
        backoff = min(backoff*2, 30*time.Second)
    }
}
```

### Pattern 6: Caddy Admin API Route Management

**What:** Control plane manages Caddy's routing table via its REST Admin API on `localhost:2019`. Routes are added/removed as sandboxes start/stop. A lightweight reconciler runs every 60s to fix drift.

**When to use:** All routing configuration. No Caddyfile editing, no file-based config reloads.

**Trade-offs:**
- Pro: ~50ms config reload, no connection drops
- Pro: ACID guarantees per request (with Etag-based optimistic concurrency)
- Pro: Battle-tested proxy (HTTP/2, WebSocket, auto-TLS)
- Pro: JSON API is easy to test and debug
- Con: Another process to manage (Caddy binary)
- Con: Route state is ephemeral in Caddy memory (must be reconstructable from Postgres)

**Evidence:** Caddy Admin API documentation confirms: changes occur without downtime, failed reloads automatically rollback, and the `@id` feature allows direct access to individual routes without knowing their array position.

**Key implementation detail -- use `@id` for O(1) route access:**
```go
func (c *CaddyClient) AddRoute(appName string, nodeIP string, port int) error {
    route := map[string]interface{}{
        "@id": fmt.Sprintf("forge-%s", appName),
        "match": []map[string]interface{}{
            {"host": []string{fmt.Sprintf("%s.myappx.live", appName)}},
        },
        "handle": []map[string]interface{}{
            {
                "handler":   "reverse_proxy",
                "upstreams": []map[string]string{{"dial": fmt.Sprintf("%s:%d", nodeIP, port)}},
            },
        },
    }
    return c.post("/config/apps/http/servers/forge/routes", route)
}

func (c *CaddyClient) RemoveRoute(appName string) error {
    return c.delete(fmt.Sprintf("/id/forge-%s", appName))
}
```

## Data Flow

### Sandbox Creation Flow (Critical Path)

```
SDK: POST /v1/sandboxes {appName, image, resources}
    |
    v
Control Plane:
    1. Validate request
    2. Insert sandbox row (state=PENDING)
    3. Run scheduler: score all healthy nodes, pick best-fit
    4. Update sandbox row (state=STARTING, node_id=X)
    5. Enqueue command to agent X: {type: "start_sandbox", payload: spec}
    6. Return sandbox object (state=STARTING, url=appName.myappx.live)
    |
    v
Agent X (picks up command via long-poll within <1s):
    1. Allocate host port (e.g., 43210)
    2. Create bind-mount directory /var/lib/forge/sandboxes/<appName>/code/
    3. docker create + docker start (with seccomp, cap-drop, bind mount)
    4. Ack command: {status: "ok", container_id: "abc123", port: 43210}
    |
    v
Control Plane (on ack):
    1. Update sandbox: state=RUNNING, container_id, host_port
    2. Push route to Caddy: appName.myappx.live -> nodeIP:43210
    3. Record event: {type: "started", sandbox_id, node_id}
    |
    v
Caddy: appName.myappx.live now resolves to the running container

Total time: <3s orchestration + ~5s Metro cold start = ~8s end-to-end
```

### File Push Flow (Hot Path)

```
appx-api (during AI generation):
    |
    | forge.sandboxes.pushFiles(id, files)
    |   SDK sends POST /v1/sandboxes/:id/files
    v
Control Plane:
    1. Look up sandbox -> node_id, agent Tailscale IP
    2. Generate signed URL (HMAC, 60s expiry)
    3. Return 307 -> https://<tailscale-ip>:<agent-port>/sandboxes/:id/files?token=...
    |
    v
Agent (direct upload, no control plane in data path):
    1. Verify HMAC signature + expiry
    2. Extract files to bind mount path
    3. Return 200 with file list
    |
    v
Metro (in container): inotify detects changes -> HMR update

Total time: <500ms for the push + HMR cycle
```

### Crash Recovery Flow

```
Docker (in container): process exits (OOM, crash, etc.)
    |
    | Docker event: {type: "container", action: "die", exitCode: "137"}
    v
Agent (events watcher):
    1. Extract sandbox ID from container name ("forge-<appName>")
    2. POST /v1/nodes/:id/status {sandbox_id, event: "container_exited", exit_code: 137}
    |
    v
Control Plane:
    1. State machine transition: RUNNING -> RESTARTING
    2. Check failure_count:
       - If < 3: enqueue "start_sandbox" command, increment failure_count
       - If >= 3: transition to FAILED, no restart
    3. Backoff: 2^failure_count seconds before restart command
    4. Record event
    |
    v
Agent (if restarting):
    1. Docker create + start (same spec, same bind mount)
    2. Ack with new container_id
    3. Control plane: RESTARTING -> STARTING -> RUNNING
```

### Node Failure Detection

```
Agent heartbeat (every 15s): POST /v1/nodes/:id/heartbeat
    |
    v
Control Plane (on each heartbeat):
    1. Update nodes.last_seen_at = NOW()
    |
    v
Control Plane (background ticker, every 30s):
    1. SELECT * FROM nodes WHERE last_seen_at < NOW() - INTERVAL '45s' AND status = 'healthy'
    2. For each stale node:
       a. Mark node status = 'unhealthy'
       b. For each sandbox on node:
          - State machine: RUNNING -> PENDING (node_id = NULL)
          - Scheduler re-places onto a healthy node
       c. Record events
    3. NOTE: This is the ONE background process. It's not a reconciler --
       it only reacts to missed heartbeats, a specific detectable failure.
```

### Idle Reaping Flow

```
Control Plane (background ticker, every 5min):
    1. SELECT * FROM sandboxes
       WHERE state = 'running'
       AND last_active_at < NOW() - INTERVAL '30min'
    2. For each idle sandbox:
       - State machine: RUNNING -> STOPPED
       - Enqueue "stop_sandbox" command to agent
       - Remove route from Caddy
    3. Stopped sandboxes retain their bind-mount data
       - Can be restarted without re-pushing files
```

## Scaling Considerations

| Scale | Architecture Adjustments |
|-------|--------------------------|
| 1-3 nodes, <100 sandboxes | Single control plane instance. Single Caddy. Postgres on managed service (Neon free tier). No HA needed. This is v1. |
| 3-10 nodes, 100-500 sandboxes | Add hot-standby control plane via Postgres advisory lock leader election. Second Caddy for redundancy. Monitor Postgres connection count. |
| 10-50 nodes, 500-2000 sandboxes | Caddy route table at ~2000 entries is still fine. Long-poll starts to have many concurrent connections -- consider SSE or gRPC streaming for agent commands. Shard Postgres reads if needed. |
| 50+ nodes | Evaluate market-based scheduling (Fly.io pattern) where agents bid for work instead of central assignment. Consider regional control planes. This is unlikely to be needed for AppX. |

### Scaling Priorities

1. **First bottleneck: Postgres connections.** Each agent long-poll holds a goroutine on the control plane, but not a DB connection. Scheduler and state transitions are the DB-heavy paths. At ~100 nodes with pgx pool size 20, this is fine. Monitor `pg_stat_activity`.

2. **Second bottleneck: Command dispatch latency.** With 50+ agents all long-polling, the in-memory command queue and goroutine count become relevant. At this point, switch to SSE (server push, no polling overhead) or a message queue (Redis pub/sub per agent). This is a v2 concern.

3. **Third bottleneck: Caddy route table.** Caddy handles thousands of routes easily. At 10,000+ routes, the JSON config size and reload time grow. At that point, consider custom Go reverse proxy with in-memory route map (the STARTER_PLAN already identifies this as a ~300 LOC fallback).

## Anti-Patterns

### Anti-Pattern 1: Reconciliation Loops

**What people do:** Run a background goroutine that periodically lists all Docker containers, compares against desired state in DB, and kills/creates containers to match.

**Why it's wrong:** This is exactly what causes Railover's 60s restart loop. Reconcilers make it impossible to distinguish intentional operations from loop-driven corrections. They race with explicit commands, causing spurious restarts and cascading failures.

**Do this instead:** Event-driven state machine. State only changes on: (a) explicit API call, (b) agent reporting a Docker event, (c) heartbeat timeout detection. The ONE background process (node health checker) reacts to a specific failure signal, not a generic "desired vs actual" diff.

### Anti-Pattern 2: Control Plane Talks to Docker

**What people do:** Control plane SSH's into nodes or mounts docker.sock to manage containers directly.

**Why it's wrong:** Massive security risk (docker.sock = root access). Network latency issues. Control plane must know Docker API details. Single point of failure for all container operations.

**Do this instead:** Agent on each node talks to local Docker Engine API. Control plane sends high-level commands ("start sandbox X with spec Y"), agent translates to Docker API calls. This is the universal pattern: Nomad servers -> Nomad clients, K8s API server -> kubelet, Fly.io control -> flyd workers.

### Anti-Pattern 3: Distributed State Without Consensus

**What people do:** Give agents their own databases or state files, then try to merge state across nodes without a consensus protocol.

**Why it's wrong:** Split-brain scenarios. Conflicting decisions about sandbox placement. Stale state leading to double-scheduling. Fly.io can do local-state-per-worker because they built custom gossip replication (Corrosion) -- that's multi-year engineering.

**Do this instead:** Single Postgres as source of truth. Agents are stateless -- they can reconstruct everything from Docker inspect + next long-poll from control plane. Control plane is the only writer to Postgres. At v1 scale (1-10 nodes), this is correct and simple.

### Anti-Pattern 4: File Push Through Control Plane

**What people do:** Client uploads files to control plane, control plane forwards to agent. Double the bandwidth, double the latency.

**Why it's wrong:** Control plane becomes a bottleneck for data transfer. During AI generation, multiple files are pushed per second. Control plane should handle control flow, not data flow.

**Do this instead:** Signed-URL redirect pattern. Control plane returns a redirect to the agent's direct endpoint. Files flow client -> agent with zero control-plane involvement in the data path.

### Anti-Pattern 5: Over-Engineering the Scheduler

**What people do:** Implement multi-dimensional bin packing, affinity rules, topology-aware scheduling, preemption, and priority queues for v1.

**Why it's wrong:** With 1-3 nodes, all healthy nodes are probably equally good. Complex scheduling logic adds bugs and debugging complexity for near-zero benefit at this scale.

**Do this instead:** Start with simple best-fit by free RAM. The scheduler is behind an interface -- swap the implementation later. Fly.io explicitly rejected complex bin packing, calling it "Katamari Damacy scheduling" that created hotspots.

## Integration Points

### External Services

| Service | Integration Pattern | Notes |
|---------|---------------------|-------|
| **Postgres (Neon/Supabase)** | pgx/v5 connection pool from control plane only. sqlc for type-safe queries. goose for migrations. | Avoid PgBouncer in transaction mode if using advisory locks for leader election -- locks are session-scoped. Use direct connections for leader election, pooled for normal queries. |
| **Cloudflare** | DNS: `*.myappx.live` CNAME to Caddy public IPs (proxied). SSL: Full mode with Origin Certificate on Caddy. | No API integration needed for v1 -- DNS is static wildcard. Disable HTTP/3 (QUIC) in Cloudflare to avoid ERR_QUIC_PROTOCOL_ERROR (known issue from Railover). |
| **Tailscale** | Agent + control plane + Caddy all join same tailnet. Communication uses Tailscale IPs (100.x.y.z). | Install via Ansible on node bootstrap. Auth key per node. DERP relay adds 30-80ms if UDP blocked -- test with Contabo first. |
| **Docker Engine** | Agent uses `github.com/docker/docker/client` (Go SDK). Never `docker.sock` mount. Agent runs as systemd service on host. | Pre-pull sandbox image on agent registration. Label all managed containers `managed-by=forge` for filtering. |
| **Caddy** | Control plane calls Admin API on `localhost:2019` (or Tailscale IP if Caddy is on separate node). JSON config for route management. | Use `@id` for O(1) route lookup/removal. Base config loaded from file on Caddy startup; dynamic routes added via API. |

### Internal Boundaries

| Boundary | Communication | Build Order Dependency |
|----------|---------------|----------------------|
| **SDK -> Control Plane** | REST HTTP (generated from OpenAPI). JSON request/response. Bearer token auth. | SDK depends on OpenAPI spec (Track A output). Can mock control plane for parallel dev. |
| **Control Plane -> Agent** | Long-poll HTTP for commands. Agent POST for heartbeat/status. Both over Tailscale. | Agent depends on agent-protocol.md contract (Track A output). Can mock control plane. |
| **Control Plane -> Caddy** | Caddy Admin API (localhost:2019). JSON route config. | Control plane routing module depends on Caddy being available. Can mock Caddy API for tests. |
| **Agent -> Docker** | Docker Engine Go SDK. Local Unix socket. | Agent depends on Docker being installed. Testcontainers-go for integration tests. |
| **CLI -> Control Plane** | Same REST API as SDK. | CLI depends on OpenAPI spec. Shares HTTP client code with SDK tests. |
| **appx-api -> SDK** | npm package import. | appx-api migration depends on SDK + running control plane. Last integration to build. |

## Suggested Build Order

Based on dependency analysis, the optimal build sequence:

```
Week 1: Foundation (gates everything)
├── 1a. OpenAPI spec (control-api.openapi.yaml) -- FIRST, gates all tracks
├── 1b. Postgres schema + migrations
├── 1c. Sandbox state machine (pure Go, unit tested)
├── 1d. Control plane HTTP handlers (chi, from OpenAPI)
└── 1e. Agent Docker wrapper + events watcher (parallel with 1a-1d)

Week 2: Integration (builds on foundation)
├── 2a. Agent command loop (long-poll + ack)
├── 2b. Scheduler (bin-pack by RAM)
├── 2c. Caddy route management client
├── 2d. File push (signed URL + agent handler)
└── 2e. Sandbox image trimming (parallel, independent)

Week 3: Multi-Node + Ops
├── 3a. Heartbeat + node failure detection
├── 3b. Crash detection + auto-restart with backoff
├── 3c. Idle reaping
├── 3d. CLI (cobra, hitting control plane API)
├── 3e. Ansible playbook for node bootstrap
└── 3f. TypeScript SDK (generated from OpenAPI)

Week 4: Migration + Hardening
├── 4a. appx-api migration (ForgeClient replaces RailoverService)
├── 4b. Seccomp profiles + security hardening
├── 4c. Prometheus metrics + health endpoints
├── 4d. Integration tests (end-to-end)
└── 4e. Runbooks + documentation
```

**Critical path:** OpenAPI spec -> state machine + handlers -> agent command loop -> scheduler + routing -> end-to-end demo.

**Parallel tracks after OpenAPI spec exists:** Agent Docker work (Track B), Caddy proxy (Track C), sandbox image (Track D), and CLI/SDK (Track F) can all proceed simultaneously using mocks.

## How Industry Leaders Structure Their Orchestration

### Fly.io (flyd + flaps)

- **Architecture:** Decentralized. Each worker runs flyd with local state in BoltDB. Flaps is a stateful proxy/scheduler that queries workers via direct RPC.
- **State:** Local to workers, replicated via Corrosion (custom gossip layer over SQLite). No central DB for placement decisions.
- **Scheduling:** Market-based. Scheduler "bids" on worker capacity. Synchronous -- if placement fails, return error immediately (no pending queue).
- **Lesson for Forge:** Their market-based model is elegant but overkill at <50 nodes. Take the "no pending queue" philosophy -- if no node can fit the sandbox, fail fast.

### Railway

- **Architecture:** Temporal workflows for deployment orchestration. Custom Go control plane for networking.
- **Networking:** Event-based. When orchestrator provisions a container, it assigns an IPv6 and pushes to a network control plane. Updates propagate to all proxies and DNS servers within milliseconds over gRPC.
- **Compute:** GCP VMs with custom non-K8s orchestrator. "Stackers" are worker nodes.
- **Lesson for Forge:** Event-based routing updates (push to proxy on state change) is the right pattern. Don't poll Caddy to check route state.

### E2B

- **Architecture:** API layer + control plane + compute clusters. Node-based orchestration with health tracking.
- **Virtualization:** Firecracker microVMs (not Docker containers). <200ms cold start via VM snapshots.
- **Templates:** Pre-built environments cached locally on nodes. Snapshot-and-restore for near-instant starts.
- **Security:** Four-layer isolation (KVM, Firecracker VMM, guest OS, application daemon).
- **API:** Dual-protocol: REST for lifecycle, gRPC for real-time ops.
- **Lesson for Forge:** Template pre-caching on nodes is critical for cold start times. Pre-pull sandbox images during agent registration, not on first sandbox create. Their snapshot model is a future upgrade path (Docker checkpoint/CRIU or Firecracker migration).

### The "Cube" Orchestrator (Manning book)

- **Architecture:** Manager + Workers + Scheduler. Manager assigns tasks to workers based on scheduler decisions. Workers execute via Docker SDK.
- **State machine:** Task lifecycle: Pending -> Scheduled -> Running -> Completed/Failed.
- **Scheduler:** Pluggable interface. Starts with round-robin, upgrades to bin-packing.
- **Lesson for Forge:** The book validates the control-plane/agent split as the standard architecture. The pluggable scheduler interface is the right design.

## Sources

- [Fly.io Architecture](https://fly.io/docs/reference/architecture/) - HIGH confidence
- [Carving The Scheduler Out of Our Orchestrator (Fly.io)](https://fly.io/blog/carving-the-scheduler-out-of-our-orchestrator/) - HIGH confidence
- [A Foolish Consistency: Consul at Fly.io](https://fly.io/blog/a-foolish-consistency/) - HIGH confidence
- [Railway Horizontal Scaling Architecture](https://blog.railway.com/p/launch-week-01-horizontal-scaling) - MEDIUM confidence
- [E2B Architecture Breakdown (Dwarves Memo)](https://memo.d.foundation/breakdown/e2b) - MEDIUM confidence
- [Nomad Scheduling Architecture](https://developer.hashicorp.com/nomad/docs/concepts/scheduling/how-scheduling-works) - HIGH confidence
- [Nomad BinPackIterator (rank.go)](https://github.com/hashicorp/nomad/blob/main/scheduler/rank.go) - HIGH confidence
- [Caddy Admin API](https://caddyserver.com/docs/api) - HIGH confidence (Context7 + official docs)
- [Docker Go SDK](https://pkg.go.dev/github.com/docker/docker/client) - HIGH confidence (Context7)
- [Chi Router](https://github.com/go-chi/chi) - HIGH confidence (Context7)
- [PostgreSQL Advisory Locks for Leader Election](https://ramitmittal.com/blog/general/leader-election-advisory-locks) - HIGH confidence
- [go-pglock (Postgres Advisory Lock Library)](https://github.com/allisson/go-pglock) - MEDIUM confidence
- [Build an Orchestrator in Go (From Scratch)](https://www.manning.com/books/build-an-orchestrator-in-go-from-scratch) - HIGH confidence
- [Tailscale Mesh Networking](https://tailscale.com/blog/how-tailscale-works) - HIGH confidence
- [Docker Events Monitoring](https://docs.docker.com/reference/cli/docker/system/events/) - HIGH confidence
- [Goose Migrations](https://github.com/pressly/goose) - HIGH confidence
- [sqlc Type-Safe Queries](https://docs.sqlc.dev/) - HIGH confidence
- [Idempotent Receiver Pattern (Martin Fowler)](https://martinfowler.com/articles/patterns-of-distributed-systems/idempotent-receiver.html) - HIGH confidence
- [Fly.io replaces Nomad analysis](https://curiouslynerdy.com/fly-io-replaces-nomad-with-homegrown/) - MEDIUM confidence

---
*Architecture research for: Custom Go container orchestrator (AppX Forge)*
*Researched: 2026-04-15*
