# Agent Protocol

Version: 0.1.0
Last updated: 2026-04-15

## Overview

Forge agents are stateless Go binaries running as systemd services on each node. They communicate with the control plane exclusively via HTTP. An agent never talks to another agent -- all coordination flows through the control plane.

The agent binary is self-contained (~20MB). It has no local database and holds no persistent state beyond what Docker and the filesystem provide. If an agent crashes and restarts, it re-registers and resumes normal operation.

## Registration

On startup, the agent sends `POST /v1/nodes/register` with its node metadata:

```json
{
  "hostname": "node-1",
  "tailscale_ip": "100.64.1.5",
  "agent_listen_port": 8090,
  "capacity_mb": 24000,
  "capacity_cpu": 8.0,
  "agent_version": "0.1.0"
}
```

The control plane returns:

```json
{
  "node_id": "550e8400-e29b-41d4-a716-446655440000",
  "agent_token": "forge_agt_a1b2c3d4e5f6...",
  "heartbeat_interval_seconds": 15
}
```

### Behavior

- The agent stores `node_id` and `agent_token` in memory (not on disk).
- `agent_token` is included as `Authorization: Bearer <agent_token>` on all subsequent requests to the control plane.
- Re-registration with the same `hostname` + `tailscale_ip` is **idempotent** -- it returns the existing `node_id` with a fresh `agent_token`.
- The `/v1/nodes/register` endpoint does **not** require authentication (it is how the agent obtains its token).

### Startup Sequence

1. Read configuration from environment variables (`FORGE_CONTROL_URL`, `FORGE_HOSTNAME`, etc.)
2. Detect Tailscale IP from local interface
3. Send registration request
4. On success: start heartbeat loop, command poll loop, Docker events watcher, and HTTP server
5. On failure: retry with exponential backoff (see [Reconnection](#reconnection))

## Heartbeat

Every `heartbeat_interval_seconds` (default 15s), the agent sends `POST /v1/nodes/{id}/heartbeat` with current resource usage:

```json
{
  "used_mb": 8500,
  "running_containers": 12
}
```

### Control Plane Processing

- Updates `last_seen_at`, `used_mb`, and `running_containers` for the node.
- 3 consecutive missed heartbeats (45s with default interval) marks the node as `unhealthy`.
- An `unhealthy` node is excluded from the scheduler -- no new sandboxes are placed on it.
- When heartbeats resume, the node transitions back to `healthy`.

### Agent Behavior

- Heartbeat runs in a dedicated goroutine with a `time.Ticker`.
- On HTTP error (5xx, timeout), the agent logs a warning but does **not** stop. It retries on the next tick.
- On 404 (node not found), the agent re-registers (it was likely evicted).

## Command Polling (Long-Poll)

The agent continuously polls for commands using `GET /v1/agents/{id}/commands?wait=30`:

```
GET /v1/agents/{node_id}/commands?wait=30
Authorization: Bearer <agent_token>
```

### Control Plane Behavior

- Holds the HTTP connection open for up to `wait` seconds (default 30, maximum 60).
- If one or more commands are pending, returns them immediately without waiting.
- If no commands are pending and the timeout expires, returns an empty array.
- Response:

```json
{
  "commands": [
    {
      "id": "cmd-550e8400-e29b-41d4-a716-446655440001",
      "type": "start_sandbox",
      "sandbox_id": "550e8400-e29b-41d4-a716-446655440002",
      "payload": { ... },
      "issued_at": "2026-04-15T12:00:00Z",
      "timeout_seconds": 60
    }
  ]
}
```

### Agent Behavior

- Immediately starts a new long-poll request after processing the previous response.
- Processes each command sequentially (one at a time) to avoid resource contention.
- If the command `timeout_seconds` has already elapsed when the command is received, the agent skips it and acks with `failure` + `"error": "command timed out before execution"`.

## Command Types

### `start_sandbox`

Creates and starts a Docker container for a sandbox.

```json
{
  "id": "cmd-uuid",
  "type": "start_sandbox",
  "sandbox_id": "sandbox-uuid",
  "payload": {
    "app_name": "my-cool-app",
    "image": "appx/sandbox:v1",
    "resources": {
      "cpu_cores": 0.5,
      "memory_mb": 512
    },
    "env": {
      "APP_NAME": "my-cool-app",
      "PORT": "8081"
    }
  }
}
```

Agent steps:
1. Allocate a host port from the local range (40000-50000).
2. Create bind-mount directory: `/var/lib/forge/sandboxes/{app_name}/code/`.
3. `docker create` with image, port binding, bind mount, resource limits, seccomp profile, and dropped capabilities.
4. `docker start` the container.
5. Ack with `{ "status": "success", "result": { "container_id": "abc123", "host_port": 43210 } }`.

### `stop_sandbox`

Stops and removes a sandbox container.

```json
{
  "id": "cmd-uuid",
  "type": "stop_sandbox",
  "sandbox_id": "sandbox-uuid",
  "payload": {
    "container_id": "abc123"
  }
}
```

Agent steps:
1. `docker stop` with 10s timeout.
2. `docker rm` the container.
3. Optionally clean up bind-mount directory (configurable).
4. Release the host port.
5. Ack with `{ "status": "success", "result": {} }`.

### `restart_sandbox`

Restarts a running sandbox container.

```json
{
  "id": "cmd-uuid",
  "type": "restart_sandbox",
  "sandbox_id": "sandbox-uuid",
  "payload": {
    "container_id": "abc123"
  }
}
```

Agent steps:
1. `docker restart` with 10s timeout.
2. Ack with `{ "status": "success", "result": {} }`.

### `get_logs`

Retrieves container logs.

```json
{
  "id": "cmd-uuid",
  "type": "get_logs",
  "sandbox_id": "sandbox-uuid",
  "payload": {
    "container_id": "abc123",
    "tail": 100,
    "follow": false
  }
}
```

Agent steps:
1. `docker logs` with `--tail` and optional `--follow`.
2. Ack with `{ "status": "success", "result": { "logs": "..." } }`.

### `prune`

Removes stopped containers and unused images to reclaim disk space.

```json
{
  "id": "cmd-uuid",
  "type": "prune",
  "sandbox_id": null,
  "payload": {}
}
```

Agent steps:
1. `docker container prune -f` (only stopped containers).
2. `docker image prune -f` (only dangling images).
3. Ack with `{ "status": "success", "result": { "containers_removed": 3, "images_removed": 1, "space_reclaimed_mb": 512 } }`.

### `exec`

Executes an arbitrary command inside a running sandbox container and returns its output. Dispatched when a caller hits `POST /v1/sandboxes/{id}/exec` on the control plane.

```json
{
  "id": "cmd-uuid",
  "type": "exec",
  "sandbox_id": "sandbox-uuid",
  "payload": {
    "command": "npm test",
    "cwd": "/app",
    "env": {
      "CI": "true"
    },
    "timeout_seconds": 120
  }
}
```

Payload fields:
- `command` (string, required): the command to run inside the container.
- `cwd` (string, optional): working directory inside the container. Default `/app`.
- `env` (map of string→string, optional): additional environment variables merged on top of the container's environment.
- `timeout_seconds` (int, optional): inner execution timeout. Default `120`, capped at `300`.

Agent steps:
1. Resolve the sandbox's `container_id` from the in-memory map populated at startup and on `start_sandbox`.
2. Build a `docker exec` invocation with `cwd`, merged `env`, and the provided `command`.
3. Wrap execution in `context.WithTimeout(payload.timeout_seconds)` (default 120s, capped at 300s).
4. Capture stdout and stderr as separate streams via `stdcopy.StdCopy`.
5. Apply per-stream truncation: keep the first 30KB (head) and the last 10KB (tail) of each stream. If truncation occurred, set the corresponding `*_truncated` flag in the result.
6. Ack with `{ "status": "success", "result": { "exit_code": 0, "stdout": "...", "stderr": "...", "stdout_truncated": false, "stderr_truncated": false, "duration_ms": 842 } }`.

Failure semantics:
- If the inner timeout fires, the agent still acks `"status": "success"` with `exit_code: -1`, partial stdout/stderr captured up to the timeout, and `stderr` appended with `[execution timed out after N seconds]` (where `N` is the effective timeout).
- If the container is not running or `docker exec` itself fails before the command starts, the agent acks `"status": "failure"` with an `error` describing the cause (e.g. `"container not running"`).

## Command Acknowledgment

After executing a command, the agent sends `POST /v1/agents/{id}/commands/{cmd_id}/ack`:

### Success

```json
{
  "status": "success",
  "result": {
    "container_id": "abc123",
    "host_port": 43210
  }
}
```

### Failure

```json
{
  "status": "failure",
  "error": "image not found: appx/sandbox:v99"
}
```

### Behavior

- Ack is **idempotent** -- sending it twice for the same `cmd_id` is safe. The control plane records the first ack and ignores duplicates.
- If the ack request fails (network error), the agent retries once after 1 second. If the retry also fails, the agent logs the error and moves on. The control plane has a timeout-based fallback for un-acked commands.

## Event Reporting

The agent watches the Docker events stream for container lifecycle events:

```go
// Filter: type=container, event in [die, oom, start, health_status]
```

When a relevant event occurs, the agent sends `POST /v1/agents/{id}/events`:

```json
{
  "sandbox_id": "550e8400-e29b-41d4-a716-446655440002",
  "event_type": "container_exited",
  "container_id": "abc123",
  "exit_code": 137,
  "payload": {
    "oom_killed": true
  }
}
```

### Event Types

| Event Type | Trigger | Typical Payload |
|------------|---------|-----------------|
| `container_started` | Container transitions to `running` | `{}` |
| `container_exited` | Container exits (die event) | `{ "exit_code": 137 }` |
| `container_oom` | Container killed by OOM | `{ "oom_killed": true, "exit_code": 137 }` |
| `container_unhealthy` | Health check fails | `{ "failing_streak": 3 }` |

### Behavior

- Events are **fire-and-forget**: the agent retries once on network error, then drops the event.
- The control plane uses events to drive state machine transitions (e.g., `container_exited` triggers restart logic or transition to `failed`).
- The agent extracts the `sandbox_id` from the container name (convention: `forge-{app_name}`), then looks up the sandbox ID via a local in-memory map populated at startup and on `start_sandbox` commands.
- Events for containers not managed by Forge (no `forge-` prefix) are ignored.

## Reconnection

### Docker Events Stream

- On disconnect, the agent reconnects using the `Since` timestamp of the last received event.
- This ensures no events are missed during the reconnection window.
- Reconnection uses exponential backoff: 1s, 2s, 4s, 8s, max 30s.

### Control Plane Connection

- On HTTP error for any request (registration, heartbeat, command poll, ack, event report):
  - Exponential backoff: 1s, 2s, 4s, 8s, max 30s.
  - Reset backoff to 1s on any successful request.
- On 401 Unauthorized (token expired/revoked):
  - Re-register to obtain a new `agent_token`.
  - If re-registration also fails, continue with exponential backoff.

### Long-Poll Specifics

- HTTP timeout for long-poll is set to `wait + 5` seconds (e.g., 35s for a 30s wait) to account for network latency.
- If the long-poll times out on the client side (no response within `wait + 5`), the agent immediately starts a new poll. This is expected behavior, not an error.

## Agent HTTP Server

The agent exposes its own HTTP server on the node's Tailscale IP for direct communication:

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/v1/sandboxes/{id}/files` | POST | File push (signed URL, see [File Push Protocol](filepush-protocol.md)) |
| `/healthz` | GET | Agent health check |

- Listens on `{tailscale_ip}:{agent_listen_port}` (default port 8090).
- Only accessible within the Tailscale mesh -- not exposed to the public internet.
- File push endpoint validates HMAC signatures (see filepush-protocol.md).

## Security

- All agent-to-control-plane requests include `Authorization: Bearer <agent_token>`.
- The agent token is scoped to the specific node -- it cannot be used to impersonate other nodes.
- Agent HTTP server is accessible only via Tailscale (private mesh network).
- File push endpoint requires HMAC-SHA256 signed URLs (see filepush-protocol.md).
