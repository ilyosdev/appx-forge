# Pitfalls Research

**Domain:** Custom container orchestrator (Go control plane + per-node agent + Caddy proxy + Tailscale mesh)
**Researched:** 2026-04-15
**Confidence:** HIGH (all pitfalls verified against official docs, SDK source, or multiple community reports)

## Critical Pitfalls

### Pitfall 1: Docker Event Stream Silently Disconnects, Missing Container Deaths

**What goes wrong:**
The Docker Go SDK `client.Events()` returns two channels (`<-chan events.Message` and `<-chan error`). The error channel emits an `io.EOF` or context cancellation when the Docker daemon restarts, the connection drops, or the daemon is temporarily unreachable. During the reconnection gap, container `die` and `oom` events are lost. The agent thinks a crashed container is still running. The control plane never triggers a restart.

**Why it happens:**
The event stream is a long-lived HTTP connection to `/events` on the Docker socket. Any interruption (daemon restart, OOM kill of dockerd, socket timeout) silently closes it. The Go SDK does not auto-reconnect. The starter plan's `WatchEvents()` code has a `time.Sleep(time.Second)` then "reconnect" comment but does not pass a `Since` timestamp, meaning all events during the gap are lost forever.

**How to avoid:**
1. Track the `timeNano` field of every processed event as `lastSeenTimestamp`.
2. On reconnect, pass `types.EventsOptions{Since: lastSeenTimestamp}` to replay missed events.
3. Add a reconciliation fallback: every 60s, `docker inspect` all known containers and compare state with control plane. This catches any events missed even with `Since` (e.g., if the daemon was fully down during the gap).
4. Set a maximum gap threshold (e.g., 5 minutes). If the agent was disconnected longer than this, do a full container audit instead of relying on `Since` replay.

**Warning signs:**
- Agent logs show `docker events error: EOF` but no subsequent "container died" events.
- Containers in `exited` state per `docker ps -a` but control plane shows them as `running`.
- `failure_count` stays at 0 for containers that have actually crashed.

**Phase to address:**
Phase 1 (Agent foundation) -- the event watcher is the agent's core reliability mechanism. Get it wrong here and everything downstream (crash detection, auto-restart, node failure detection) is unreliable.

---

### Pitfall 2: Postgres Advisory Lock Released on Wrong Connection (Connection Pool Mismatch)

**What goes wrong:**
Session-level advisory locks (`pg_advisory_lock()`) are bound to the specific TCP connection (session) that acquired them. With Go's `pgxpool`, the connection that acquires the lock may be returned to the pool. A subsequent `pg_advisory_unlock()` call may execute on a *different* connection, silently failing to release the lock. The lock is held until the original connection is closed or the process dies. Meanwhile, a hot-standby control plane instance blocks forever trying to acquire the same lock.

**Why it happens:**
`pgxpool.Pool.Exec()` acquires a connection from the pool, executes the query, and releases the connection back. Two separate `Exec()` calls (lock + unlock) may use different connections. This is well-documented in the pgx ecosystem and has caused production outages in multiple projects (including a documented case in Qube Cinema's engineering blog).

**How to avoid:**
1. Use `pgxpool.Pool.Acquire()` to get a dedicated `*pgxpool.Conn`, acquire the advisory lock on that connection, and hold it for the entire duration. Never release it back to the pool.
2. Alternatively, use transaction-level advisory locks (`pg_advisory_xact_lock()`) which are automatically released when the transaction commits/rolls back. But this means the lock is held only for the transaction duration, not for leader election.
3. For leader election specifically: acquire a session-level advisory lock on a dedicated connection that the leader holds for its entire lifetime. Wrap it in a goroutine with a context that cancels on shutdown.
4. Never use PgBouncer in transaction-pooling mode with session-level advisory locks -- they are fundamentally incompatible.

**Warning signs:**
- `pg_locks` shows advisory locks held by connections that are idle (returned to pool).
- Hot-standby instance logs show "waiting for advisory lock" indefinitely.
- Leader election works in dev (single connection) but fails in production (pool).

**Phase to address:**
Phase 1 (Control plane foundation) -- advisory locks are the HA mechanism for v1. If this is broken, two control plane instances can run simultaneously, issuing conflicting commands to agents.

---

### Pitfall 3: Caddy Config Reload Drops All Active WebSocket Connections

**What goes wrong:**
Every time the Caddy config is reloaded via the Admin API (e.g., adding a route for a new sandbox), ALL active WebSocket connections through Caddy are forcibly closed -- including connections to completely unrelated routes. Users connected to Expo Go via WebSocket for HMR see their connection drop every time any sandbox is created or destroyed anywhere in the system. With 80+ sandboxes churning, this means constant disconnects.

**Why it happens:**
Caddy's architecture ties open connections to the config that created them. When a new config is loaded, the old config is unloaded and all connections referencing it are closed. The `stream_close_delay` option only delays the close (does not prevent it). This is a known, unresolved issue (GitHub issues #5471, #6420, #7222, all open as of 2025).

**How to avoid:**
1. Batch route updates aggressively. Do not call Caddy's Admin API per-sandbox. Collect all route changes over a 500ms-1s window and apply them in a single API call.
2. Set `stream_close_delay` to at least 5s to give clients time to reconnect gracefully instead of all reconnecting simultaneously (thundering herd).
3. Design Expo Go / HMR clients to handle WebSocket disconnects gracefully with automatic reconnect + exponential backoff. This is non-optional.
4. Consider the fallback architecture: if WebSocket stability through Caddy proves untenable, route HMR WebSocket connections directly to agents over Tailscale IPs, bypassing Caddy entirely. Caddy only proxies HTTP requests for the bundle. This is the nuclear option but may be necessary.
5. Long-term: watch Caddy issue #3143 (partial config reloads) which would solve this properly.

**Warning signs:**
- Expo Go clients show frequent "WebSocket connection closed" errors correlated with sandbox creation/destruction.
- Metric spikes in WebSocket reconnections coincide with Caddy config reload events.
- HMR feels "flaky" even though Metro is stable.

**Phase to address:**
Phase 2 (Proxy integration + file push + HMR) -- this will be discovered during HMR testing. Must be addressed before the system handles real traffic.

---

### Pitfall 4: State Machine Race Between Agent Reports and API Calls

**What goes wrong:**
The sandbox state machine has concurrent writers: (a) the API handler processing a `DELETE /v1/sandboxes/:id` call, and (b) the agent reporting a container crash at the same moment. Both attempt a state transition simultaneously. Without proper serialization, you get:
- API sets state to `destroying`, agent sets state to `restarting` -- sandbox is now in an impossible state.
- Agent reports container started (state -> `running`), but API already moved it to `destroyed` -- container runs forever with no routing, no cleanup.
- Two agents (after reschedule) both report success -- duplicate running containers for the same sandbox.

**Why it happens:**
Postgres `UPDATE ... SET state = 'X'` does not check the current state. Without a WHERE clause that includes the expected current state (compare-and-swap), any concurrent writer wins. The starter plan's state machine diagram shows valid transitions but the schema has no mechanism to enforce them.

**How to avoid:**
1. Every state transition must be a compare-and-swap: `UPDATE sandboxes SET state = $new, updated_at = NOW() WHERE id = $id AND state = $current RETURNING *`. If zero rows affected, the transition was rejected -- retry or abort.
2. Log every transition attempt (successful or rejected) to the `events` table with the requesting actor (api, agent, scheduler, reaper).
3. Add a `state_version` integer column. Increment on every transition. Agents include the last known `state_version` in their reports. Control plane rejects stale reports.
4. Define clear priority rules: API-initiated destroys always win over agent-initiated restarts. Agent reports are informational -- control plane decides the action.

**Warning signs:**
- `events` table shows impossible state sequences (e.g., `running -> running`, `destroyed -> restarting`).
- Orphaned containers visible in `docker ps` on agents but not in control plane's sandbox list.
- Sandbox stuck in `starting` state forever because the agent's "started" report was processed after a concurrent destroy.

**Phase to address:**
Phase 1 (Control plane state machine) -- this must be baked into the state machine from day one. Retrofitting compare-and-swap onto a state machine that assumed single-writer semantics is a rewrite.

---

### Pitfall 5: Port Allocation Conflicts on Agent Under Concurrent Sandbox Creation

**What goes wrong:**
Two concurrent `start_sandbox` commands arrive at the same agent. Both call `portAllocator.Allocate()`, both get port 40000 (or whatever is "next"). The first `ContainerCreate` succeeds. The second fails with "port already allocated" from Docker. The agent reports failure, the control plane marks the sandbox as failed, and it gets rescheduled to the same node (still no free port tracking), creating a failure loop.

**Why it happens:**
The starter plan's port allocator is sketched as `a.portAllocator.Allocate()` but the implementation details are unspecified. A naive "increment counter" approach races under concurrency. Docker itself checks for port conflicts at bind time, but by then you've already created the container (which now needs cleanup).

**How to avoid:**
1. Use a mutex-protected port set on the agent. `Allocate()` acquires the lock, finds the next free port, marks it as in-use, releases the lock. `Release(port)` returns it to the pool.
2. Alternatively: let Docker allocate the port. Pass `HostPort: "0"` in `PortBindings` and read back the assigned port from `ContainerInspect()`. This eliminates the allocator entirely. Report the assigned port to the control plane.
3. If using a local allocator, persist the allocated set to disk. On agent restart, scan `docker ps` to rebuild the in-use set rather than starting from scratch (which would collide with still-running containers).
4. Validate port availability with a quick TCP dial before binding. Not perfect (TOCTOU) but catches 99% of conflicts.

**Warning signs:**
- Sandbox creation failures with "port already allocated" or "bind: address already in use" in agent logs.
- The same port appears in multiple sandbox records in the database.
- Sandbox creation works fine at low concurrency but fails under burst creation (e.g., pool pre-warming).

**Phase to address:**
Phase 1 (Agent container creation) -- the Docker-assigned port approach (HostPort: "0") should be the default. It eliminates an entire class of bugs.

---

### Pitfall 6: Bind Mount UID/GID Mismatch Causes Silent Permission Failures

**What goes wrong:**
The agent creates `/var/lib/forge/sandboxes/{app}/code/` as root (UID 0). The sandbox container runs Node.js as a non-root user (e.g., UID 1000, per security best practices). Node.js/Metro cannot read or write files in the bind-mounted directory. Metro silently fails to detect file changes (inotify watches fail on directories you can't read). File pushes appear to succeed (the agent writes as root) but the container cannot read the pushed files.

**Why it happens:**
Linux bind mounts preserve the host filesystem's ownership. The container's user namespace sees raw UIDs. If the host directory is owned by root and the container process runs as UID 1000, permission denied. The starter plan's Dockerfile snippet shows `CapDrop: ["ALL"]` and `CapAdd: ["CHOWN", "SETUID", "SETGID"]` but does not address the directory ownership.

**How to avoid:**
1. In the sandbox Dockerfile, define a fixed user (e.g., `RUN useradd -u 1000 -m appuser`). Document this UID.
2. In the agent, create the bind-mount directory with `os.Chown(codePath, 1000, 1000)` after `os.MkdirAll()`.
3. Add an integration test that pushes a file as the agent, then verifies the container process can read it.
4. Never run the container as root. The CHOWN/SETUID/SETGID capabilities are for the entrypoint init, not for Metro.

**Warning signs:**
- Metro logs show "EACCES: permission denied" when reading files.
- File push endpoint returns 200 but container logs show "no such file" or "permission denied".
- HMR doesn't trigger after file push (inotify can't watch directories it can't read).

**Phase to address:**
Phase 1 (Agent file push + sandbox image) -- test this end-to-end before declaring file push "working". It will silently appear to work if you only test from the agent side.

---

### Pitfall 7: inotify Watch Limit Exhaustion Across Containers

**What goes wrong:**
`fs.inotify.max_user_watches` is a kernel-level limit shared by ALL containers on the host (because containers share the host kernel). Default is 8,192. Metro watches the entire `node_modules` tree plus the app directory. A single Metro instance can easily consume 5,000+ watches. With 80 containers on one node, you blow past the limit. New containers fail to start Metro (or Metro silently stops watching for changes), and HMR breaks.

**Why it happens:**
Docker containers cannot modify `fs.inotify.max_user_watches` -- it is a host kernel parameter. Each inotify watch consumes ~1KB of non-swappable kernel memory. Most Contabo VDS images ship with the default limit.

**How to avoid:**
1. In the Ansible node bootstrap playbook, set `fs.inotify.max_user_watches=524288` and `fs.inotify.max_user_instances=8192` in `/etc/sysctl.conf`. Apply with `sysctl -p`.
2. In Metro's configuration, use `watchFolders` to limit watched paths to only the code directory (not all of `node_modules`). Pre-installed `node_modules` should not be watched.
3. Add a Prometheus metric for current inotify usage on each node. Alert at 80% capacity.
4. Budget: 524,288 watches / 80 containers = ~6,500 watches per container. Verify Metro stays within this budget.

**Warning signs:**
- Metro logs: "ENOSPC: System limit for number of file watchers reached".
- HMR works for the first N containers but stops working for later ones.
- `cat /proc/sys/fs/inotify/max_user_watches` returns 8192 (the too-low default).

**Phase to address:**
Phase 1 (Node bootstrap / Ansible playbook) -- set this in the Ansible playbook before any containers are created. Debugging inotify exhaustion after the fact is extremely painful because the symptom (HMR stops) is far from the cause (kernel limit).

---

### Pitfall 8: Tailscale DERP Relay Fallback Adds Unacceptable Latency for File Push

**What goes wrong:**
Contabo restricts outbound UDP on some VDS configurations. Tailscale's WireGuard tunnel requires UDP port 41641 for direct peer-to-peer connections. When UDP is blocked, Tailscale falls back to DERP (Designated Encrypted Relay for Packets) -- a TCP-based relay through Tailscale's servers. This adds 30-80ms per round-trip. For file push (which may involve 10-20 files per AI generation), this turns a <100ms operation into 1-2 seconds. For HMR WebSocket traffic, every keystroke has noticeable lag.

**Why it happens:**
Some Contabo VDS plans use shared networking infrastructure that blocks or rate-limits UDP. Tailscale's DERP fallback is designed for this case but trades latency for connectivity. The starter plan mentions this risk but the mitigation ("allow inbound UDP 41641 in Contabo firewall") requires verifying it actually works on your specific Contabo plan.

**How to avoid:**
1. Test UDP connectivity FIRST, before committing to multi-node. Run `tailscale netcheck` on each node and verify direct connections (not relayed).
2. Open UDP port 41641 in the Contabo firewall panel and verify with `tailscale status` that connections show "direct" not "relay".
3. If UDP is truly blocked on Contabo's network level (not just firewall), consider deploying a self-hosted DERP relay on one of the Contabo nodes (co-located, <1ms added latency).
4. As absolute fallback: file push could go over plain HTTPS between nodes instead of Tailscale, using the public IPs with mutual TLS. This loses the NAT-traversal benefit but eliminates the relay hop.

**Warning signs:**
- `tailscale status` shows "relay" or DERP server name instead of "direct" for peer connections.
- `tailscale ping <peer>` shows latency >20ms between nodes in the same datacenter.
- File push takes >500ms consistently.

**Phase to address:**
Phase 0 (Infrastructure setup) -- validate UDP connectivity on Contabo before writing any agent code. If UDP is blocked, the entire networking architecture needs adjustment.

---

### Pitfall 9: Seccomp/Capability Dropping Breaks Node.js/Metro in Subtle Ways

**What goes wrong:**
The starter plan drops all capabilities (`CapDrop: ["ALL"]`) and adds back only `CHOWN`, `SETUID`, `SETGID`. It also applies a custom seccomp profile. Node.js and Metro rely on several syscalls that may be blocked:
- `clone3`: Required by Node.js 18+ for thread creation. Blocked by older Docker seccomp defaults. Container fails to start with cryptic error.
- `perf_event_open`: Used by Node.js for profiling/diagnostics. Not critical but generates noisy warning logs.
- `inotify_init1`: Required by Metro for file watching. Must be in the seccomp allow-list.
- `epoll_*`: Required by libuv (Node.js event loop). Usually allowed but custom profiles may block them.

**Why it happens:**
Docker's default seccomp profile is permissive enough for most Node.js workloads. But the starter plan uses a custom profile (`/etc/forge/seccomp-default.json`) that may be more restrictive. Additionally, dropping ALL capabilities may prevent Node.js from binding to privileged operations needed during startup.

**How to avoid:**
1. Start with Docker's default seccomp profile (not a custom one) and verify Metro works.
2. Use `strace -f -c node server.js` inside a container to inventory all syscalls Metro actually makes during startup, file watching, and bundling.
3. Build the custom seccomp profile by starting from Docker's default and removing only the syscalls you explicitly want to block (deny-list approach), not by starting from empty and adding (allow-list approach).
4. Test the seccomp profile with a real Metro cold start + file push + HMR cycle, not just "container starts".
5. Required capabilities beyond the minimum: verify whether `NET_BIND_SERVICE` is needed (Metro on port 8081 is >1024, so probably not).

**Warning signs:**
- Container exits immediately with exit code 1 and no useful logs.
- Node.js warning: "libuv: unable to create worker thread" (clone3 blocked).
- Metro starts but file watching doesn't work (inotify syscalls blocked).
- `dmesg` on the host shows seccomp audit messages for the container.

**Phase to address:**
Phase 1 (Sandbox image) -- test with Docker's default seccomp first. Only introduce a custom profile after Metro is confirmed working. Apply the custom profile in a later hardening phase.

---

### Pitfall 10: Long-Poll Command Dispatch Delivers Duplicate Commands

**What goes wrong:**
Agent polls `GET /v1/agents/:id/commands?wait=30s`. Network hiccup causes the agent's HTTP client to time out. Agent retries immediately. Meanwhile, the control plane already dispatched the command on the first request and is waiting for the ack. The second poll gets the same command again (because it hasn't been acked). Agent executes `start_sandbox` twice, creating two containers for the same sandbox.

**Why it happens:**
Long-poll is inherently at-least-once delivery. The control plane cannot distinguish between "agent received command but hasn't acked yet" and "agent never received command." Without idempotency keys, re-delivery causes duplicate execution.

**How to avoid:**
1. Every command has a unique `command_id`. The agent tracks the last N processed command IDs in memory. If a command ID was already processed, skip it and just send the ack.
2. `start_sandbox` must be idempotent: before creating a container, check if a container named `forge-{appName}` already exists on this node. If yes, inspect it and report its state instead of creating a new one.
3. `stop_sandbox` is naturally idempotent (stopping a stopped container is a no-op in Docker).
4. Add a `dispatched_at` timestamp and `acked_at` timestamp to the commands table. Commands that have been dispatched but not acked for >60s get re-dispatched (with the same command_id).
5. The control plane should mark commands as `dispatched` (not `completed`) when sent via long-poll. Only mark `completed` when the ack arrives.

**Warning signs:**
- Two containers running for the same sandbox on the same node.
- Agent logs show the same command_id being processed twice.
- `events` table shows duplicate `started` events for the same sandbox within seconds.

**Phase to address:**
Phase 1 (Agent command dispatch) -- idempotency must be designed in from the start. Adding it later requires auditing every command handler.

---

## Technical Debt Patterns

| Shortcut | Immediate Benefit | Long-term Cost | When Acceptable |
|----------|-------------------|----------------|-----------------|
| Skip event-driven reconciliation, rely solely on Docker events | Simpler agent, no polling loop | Missed events = phantom containers, leaked resources, state drift | Never -- always have a periodic reconciliation fallback |
| Use Caddy Admin API for per-sandbox route changes | Simple 1:1 mapping | WebSocket drops on every config reload, O(N) config size growth | MVP only -- batch updates and consider direct WebSocket routing by Phase 2 |
| In-memory port allocator without persistence | Fast, no disk I/O | Agent restart causes port conflicts with running containers | Never -- always rebuild from `docker ps` on startup |
| Single Postgres connection for advisory lock leader election | Simple, works in dev | If connection drops, leader election is lost but no one notices until failover | MVP -- add a health check that verifies the lock is still held every 30s |
| Trust agent's reported `used_mb` for scheduling | No need to query Docker per-sandbox | Agent can report stale memory data, scheduler over-packs nodes | Phase 1 -- replace with actual `docker stats` data by Phase 3 |
| Inline state machine transitions (no compare-and-swap) | Fewer SQL round-trips | Race conditions under concurrent state changes | Never -- compare-and-swap from day one |

## Integration Gotchas

| Integration | Common Mistake | Correct Approach |
|-------------|----------------|------------------|
| Docker SDK `ContainerCreate` | Not checking for name conflicts before creating | Always `ContainerInspect` first, or use unique names with random suffix. Handle `409 Conflict` gracefully |
| Docker SDK `ContainerRemove` | Calling remove on a running container without force flag | Always `ContainerStop` first (with timeout), then `ContainerRemove(force: true)`. Handle "no such container" (already removed) as success |
| Caddy Admin API `POST /config/` | Replacing entire config on every route change | Use `PATCH /config/apps/http/servers/srv0/routes/...` to modify individual routes. Full config POST is atomic but triggers full reload |
| Caddy Admin API concurrent writes | Two route updates at the same time overwrite each other | Use `ETag` / `If-Match` headers for optimistic concurrency control. Or serialize all Caddy writes through a single goroutine |
| pgx `Pool.Exec()` for advisory locks | Assuming lock and unlock happen on the same connection | Use `Pool.Acquire()` to get a dedicated connection, hold it for the lock's lifetime |
| Tailscale in Docker containers | Assuming MagicDNS works inside containers automatically | Containers need `100.100.100.100` in `/etc/resolv.conf`. Use `--dns 100.100.100.100` in `docker run` or configure via Tailscale's `--accept-dns` on the host |
| Cloudflare proxy + WebSocket | Assuming Cloudflare passes WebSocket upgrades with no timeout | Cloudflare has a 100-second idle timeout for WebSocket connections. Metro HMR may idle longer. Send periodic ping frames |

## Performance Traps

| Trap | Symptoms | Prevention | When It Breaks |
|------|----------|------------|----------------|
| Per-sandbox Caddy API call | Config reload latency grows linearly with sandbox count; WebSocket drops increase | Batch route changes with 500ms debounce; single Caddy API call per batch | >20 sandbox creates/destroys per minute |
| Unbounded events table growth | Query performance on `events` table degrades; disk fills up | Partition by `created_at` month. Retain 30 days. Add `created_at` index | >100k events/day (~50 sandboxes with moderate churn) |
| Full `docker inspect` reconciliation on every heartbeat | Agent CPU spikes; heartbeat latency exceeds 15s interval | Reconcile on a separate 60s timer, not on heartbeat. Heartbeat should be lightweight (just timestamp + sandbox count) | >50 containers per node |
| Blocking file push on large codebases | File push takes >5s, Metro starts bundling before all files are written | Stream files as tar.gz, extract atomically to a temp directory, then `rename()` to the bind mount path. Single atomic swap | Projects with >100 files or >10MB total |
| Control plane queries all sandboxes for scheduler | Scheduler latency grows with total sandbox count | Cache node capacity in memory, update on events. Scheduler queries only nodes, not all sandboxes | >500 total sandboxes across fleet |

## Security Mistakes

| Mistake | Risk | Prevention |
|---------|------|------------|
| Agent exposes file push HTTP endpoint on public IP | Anyone can push arbitrary code into sandbox containers | Bind agent HTTP server to Tailscale interface only (`100.x.x.x`). Require signed URL tokens. Never bind to `0.0.0.0` |
| Signed URL tokens with no expiry or long expiry | Leaked token allows file push indefinitely | 60-second TTL on signed URLs. Include sandbox ID in the token payload. Verify on the agent side |
| Sandbox container can access Docker socket | Container escape to host root | The plan already avoids docker.sock mounts -- never regress on this. Agent talks to Docker locally as a systemd service, containers never touch Docker |
| No network isolation between sandbox containers | One container can attack/probe others on the same node | Use `--network=none` + only expose the mapped port. Or create a per-sandbox Docker network with no inter-container routing |
| Seccomp profile allows `ptrace` | Container can trace other processes, potential escape | Ensure `ptrace` is blocked in custom seccomp profile. Docker default blocks it, but verify in custom profile |
| Agent API key stored in environment variable, visible in `docker inspect` | Other containers or host processes can read the key | Use Docker secrets or file-based config. Never pass API keys as container env vars visible in inspect output |

## "Looks Done But Isn't" Checklist

- [ ] **Docker event watcher:** Often missing `Since` timestamp on reconnect -- verify by killing dockerd, starting a container externally, restarting dockerd, and checking the agent detects the new container
- [ ] **State machine:** Often missing compare-and-swap enforcement -- verify by sending a destroy API call and an agent crash report simultaneously for the same sandbox
- [ ] **File push:** Often missing atomic write -- verify by pushing 50 files simultaneously and checking Metro doesn't bundle a half-written state
- [ ] **Port allocator:** Often missing persistence across restarts -- verify by restarting the agent while containers are running, then creating a new sandbox (should not conflict)
- [ ] **Caddy routing:** Often missing cleanup on sandbox destroy -- verify by creating and destroying 100 sandboxes, then checking `GET /config/` shows zero routes
- [ ] **Idle reaper:** Often missing `last_active_at` update on file push -- verify that pushing files resets the 30-minute idle timer
- [ ] **Health check:** Often reports "healthy" during Metro cold start (port bound but not ready) -- verify by checking that the health probe waits for Metro's `ready` event, not just TCP port open
- [ ] **Node drain:** Often creates new sandboxes on draining node during the drain process -- verify by starting a drain and simultaneously creating 10 sandboxes
- [ ] **Advisory lock HA:** Often works in dev (single instance) but deadlocks with pool in production -- verify by running two control plane instances against the same Postgres and confirming only one acquires the lock

## Recovery Strategies

| Pitfall | Recovery Cost | Recovery Steps |
|---------|---------------|----------------|
| Missed Docker events (phantom running containers) | LOW | Run `forge-cli sandbox reconcile` that compares Docker state with control plane state on every node. Automated in 60s reconciler |
| Advisory lock stuck (no leader) | MEDIUM | Connect to Postgres, run `SELECT pg_advisory_unlock_all()` for the stuck session. Restart both control plane instances. Fix the connection pooling code |
| Caddy config drift (routes for destroyed sandboxes) | LOW | `forge-cli routes verify` diffs Caddy config against Postgres. `forge-cli routes sync --force` rebuilds Caddy config from scratch |
| Port allocation conflict (two containers same port) | MEDIUM | Stop the conflicting container. Rebuild port allocator state from `docker ps`. Restart affected sandbox |
| State machine corruption (impossible state) | HIGH | Manual investigation required. Check `events` table for the sequence. Force-set state via `forge-cli sandbox set-state`. Root-cause the missing compare-and-swap |
| inotify exhaustion (HMR broken) | LOW | Increase `fs.inotify.max_user_watches` on host. `sysctl -p`. No container restart needed -- Metro retries watch creation |
| UID mismatch (container can't read files) | MEDIUM | `chown -R 1000:1000 /var/lib/forge/sandboxes/{app}/code/` on host. Restart container. Fix agent's `MkdirAll` to set correct ownership |

## Pitfall-to-Phase Mapping

| Pitfall | Prevention Phase | Verification |
|---------|------------------|--------------|
| Docker event stream disconnect | Phase 1: Agent foundation | Integration test: kill dockerd, verify event replay via `Since` |
| Advisory lock connection pool | Phase 1: Control plane HA | Run two instances, verify single leader, kill leader, verify failover |
| Caddy WebSocket drops on reload | Phase 2: Proxy + HMR | Load test: create sandboxes while HMR clients are connected, measure disconnect rate |
| State machine races | Phase 1: Control plane state machine | Concurrent integration test: 10 goroutines issuing transitions for same sandbox |
| Port allocation conflicts | Phase 1: Agent container creation | Concurrent test: 10 simultaneous `start_sandbox` commands to same agent |
| Bind mount UID/GID | Phase 1: Sandbox image + agent | End-to-end test: push file, verify container can read it |
| inotify watch exhaustion | Phase 0: Node bootstrap (Ansible) | Verify `max_user_watches` is 524288 on provisioned nodes |
| Tailscale DERP fallback | Phase 0: Infrastructure validation | `tailscale netcheck` shows direct connectivity between all nodes |
| Seccomp breaking Node.js | Phase 1: Sandbox image (hardening sub-phase) | Full Metro lifecycle test under the custom seccomp profile |
| Long-poll duplicate commands | Phase 1: Agent command dispatch | Test: disconnect agent mid-command, reconnect, verify no duplicate execution |

## Sources

- [Docker Events API documentation](https://docs.docker.com/reference/cli/docker/system/events/)
- [Docker Go SDK client package](https://pkg.go.dev/github.com/docker/docker/client)
- [PostgreSQL Advisory Locks official docs](https://www.postgresql.org/docs/current/explicit-locking.html)
- [Qube Cinema: Pitfall of PostgreSQL Advisory Locks with Go's DB Connection Pool](https://engineering.qubecinema.com/2019/08/26/unlocking-advisory-locks.html)
- [PostgreSQL Advisory Locks explained (Flavio Del Grosso)](https://flaviodelgrosso.com/blog/postgresql-advisory-locks)
- [pgx issue #1859: pgxpool opens more connections than configured](https://github.com/jackc/pgx/issues/1859)
- [pgx issue #1190: Successful connections continuously increase](https://github.com/jackc/pgx/issues/1190)
- [Caddy Admin API documentation](https://caddyserver.com/docs/api)
- [Caddy issue #6420: Active WebSocket connections closed on config reload](https://github.com/caddyserver/caddy/issues/6420)
- [Caddy issue #7222: Preserve WebSocket connections when unrelated routes updated](https://github.com/caddyserver/caddy/issues/7222)
- [Caddy issue #5471: Don't close active WebSocket connections on config reload](https://github.com/caddyserver/caddy/issues/5471)
- [Caddy reverse_proxy documentation (stream_close_delay)](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy)
- [Caddy JSON route ordering discussion](https://caddy.community/t/caddy-json-config-subroutes-ids-and-directive-order/28730)
- [Tailscale DERP servers documentation](https://tailscale.com/kb/1232/derp-servers)
- [Contabo: WireGuard vs Tailscale (UDP discussion)](https://contabo.com/blog/wireguard-vs-tailscale/)
- [Tailscale issue #14467: Docker containers cannot communicate over MagicDNS](https://github.com/tailscale/tailscale/issues/14467)
- [Tailscale issue #15471: MagicDNS queries failing](https://github.com/tailscale/tailscale/issues/15471)
- [Docker seccomp security profiles](https://docs.docker.com/engine/security/seccomp/)
- [containerd issue #6203: seccomp failure with clone3](https://github.com/containerd/containerd/issues/6203)
- [Docker bind mount permissions guide (2025)](https://eastondev.com/blog/en/posts/dev/20251217-docker-mount-permissions-guide/)
- [inotify watch limits (Baeldung)](https://www.baeldung.com/linux/inotify-upper-limit-reached)
- [code-server issue #628: inotify limits in Docker](https://github.com/coder/code-server/issues/628)
- [Docker zombie process handling (Stormkit)](https://www.stormkit.io/blog/hunting-zombie-processes-in-go-and-docker)
- [Deduplication in Distributed Systems (Architecture Weekly)](https://www.architecture-weekly.com/p/deduplication-in-distributed-systems)
- [Health Checks vs Heartbeats (Level Up Coding)](https://blog.levelupcoding.com/p/health-checks-vs-heartbeats)
- [Go os.WriteFile is not atomic (Go issue #56173)](https://github.com/golang/go/issues/56173)

---
*Pitfalls research for: Custom container orchestrator (AppX Forge)*
*Researched: 2026-04-15*
