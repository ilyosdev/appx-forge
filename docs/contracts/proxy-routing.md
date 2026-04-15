# Proxy Routing Protocol

Version: 0.1.0
Last updated: 2026-04-15

## Overview

The control plane manages Caddy reverse proxy routes via the Caddy Admin API. Each running sandbox gets a route mapping `{app_name}.myappx.live` to the node's Tailscale IP and host port. The control plane is the sole owner of routing state -- it adds routes when sandboxes start and removes them when sandboxes stop or are destroyed.

Caddy is the only L7 proxy in the architecture. There is no Nginx, no Traefik, no custom Go reverse proxy. Caddy was chosen for its dynamic Admin API (no config file reloads), automatic WebSocket proxying, and battle-tested HTTP/2 support.

## Caddy Base Config

Caddy runs on the proxy node (or co-located with a lightweight agent) with this base configuration:

- **Port 443** (TLS): Serves all sandbox traffic via HTTPS.
- **Port 80**: Redirects all HTTP requests to HTTPS.
- **TLS**: Cloudflare Origin CA certificate (15-year validity). No ACME/Let's Encrypt -- Cloudflare handles client-facing TLS, and Origin CA handles Cloudflare-to-origin encryption.
- **Admin API**: Listens on `localhost:2019` (Caddy default). **Not exposed publicly.** Only the control plane (running on the same node or accessible via Tailscale) communicates with it.
- **WebSocket upgrade**: Automatic. No extra configuration needed for Metro HMR WebSocket connections.

### Minimal Caddyfile (initial bootstrap)

```json
{
  "admin": {
    "listen": "localhost:2019"
  },
  "apps": {
    "http": {
      "servers": {
        "srv0": {
          "listen": [":443"],
          "tls_connection_policies": [{
            "certificate_selection": {
              "any_tag": ["origin"]
            }
          }],
          "routes": []
        }
      }
    },
    "tls": {
      "certificates": {
        "load_files": [{
          "certificate": "/etc/caddy/certs/origin.pem",
          "key": "/etc/caddy/certs/origin-key.pem",
          "tags": ["origin"]
        }]
      }
    }
  }
}
```

The `routes` array starts empty. The control plane populates it via the Admin API as sandboxes come online.

## Route Addition

When a sandbox reaches the `running` state (confirmed by agent command ack), the control plane adds a route to Caddy:

```http
POST http://localhost:2019/config/apps/http/servers/srv0/routes
Content-Type: application/json

{
  "@id": "route-{app_name}",
  "match": [{ "host": ["{app_name}.myappx.live"] }],
  "handle": [{
    "handler": "reverse_proxy",
    "upstreams": [{ "dial": "{tailscale_ip}:{host_port}" }],
    "transport": { "protocol": "http" },
    "headers": {
      "request": {
        "set": {
          "X-Forwarded-Host": ["{http.request.host}"],
          "X-Sandbox-ID": ["{sandbox_id}"]
        }
      }
    }
  }]
}
```

### Key Design Points

- **`@id` field**: Assigns a stable identifier (`route-{app_name}`) to the route. This allows direct path-based access for updates and deletes without scanning the routes array by index.
- **`transport.protocol: http`**: Traffic between Caddy and the agent uses plain HTTP over Tailscale. TLS is handled by Cloudflare on the client side and by Caddy's Origin CA cert on the origin side. Agent-to-Caddy traffic is already encrypted by Tailscale.
- **`X-Forwarded-Host`**: Passes the original `Host` header to the sandbox container. Metro may use this for HMR WebSocket URL generation.
- **`X-Sandbox-ID`**: Passes the sandbox UUID for logging and debugging within the container.

### Success Response

Caddy returns `200 OK` when the route is added successfully. The route takes effect immediately -- no restart or reload needed.

## Route Removal

When a sandbox is destroyed, stopped, or fails permanently, the control plane removes its route:

```http
DELETE http://localhost:2019/id/route-{app_name}
```

### Behavior

- Returns `200 OK` on success.
- Returns `404 Not Found` if the route does not exist (already removed). This is expected and safe -- removal is **idempotent**.
- The control plane does not need to check if the route exists before deleting. It always attempts the delete and ignores 404.

## Route Update

If a sandbox moves to a different node (e.g., after reschedule following node failure), the control plane updates the route's upstream:

```http
PATCH http://localhost:2019/id/route-{app_name}/handle/0/upstreams
Content-Type: application/json

[{ "dial": "{new_tailscale_ip}:{new_host_port}" }]
```

This atomically updates the upstream without removing and re-adding the route, avoiding a brief window where the route is missing.

## Batch Updates (WebSocket Drop Mitigation)

### Problem

Caddy reloads its internal routing table on every route change via the Admin API. While individual reloads are fast (~50ms), rapid changes (10+ sandboxes starting simultaneously) cause multiple reloads in quick succession. Each reload **may** drop in-flight WebSocket connections (Metro HMR), causing brief reconnection delays for users of unrelated sandboxes.

### Solution: 500ms Debounce Batching

The control plane batches route changes with a **500ms debounce**:

1. Route change events are collected in a buffer.
2. When the first event arrives, a 500ms timer starts.
3. Subsequent events arriving within the 500ms window are added to the buffer.
4. When the timer fires (or the buffer reaches 50 changes), the control plane flushes all buffered changes in a single Caddy API call.

### Batch Flush

For batch updates, the control plane uses `POST /load` with the complete route configuration:

```http
POST http://localhost:2019/load
Content-Type: application/json

{
  "admin": { ... },
  "apps": {
    "http": {
      "servers": {
        "srv0": {
          "routes": [ ... all routes ... ]
        }
      }
    }
  }
}
```

This replaces the entire route table in a single reload, regardless of how many individual routes changed.

### When Batching Applies

- Sandbox creation bursts (e.g., 10 users creating apps simultaneously)
- Node failure recovery (all sandboxes on the failed node are rescheduled, triggering route updates)
- System startup (pre-warmed sandboxes all becoming `running` at once)

### When Batching Does NOT Apply

- Single sandbox start/stop: direct `POST`/`DELETE` to the Admin API is fine (one reload for one change).
- Drift correction: handled by the drift detector, which already batches its own fixes.

## Drift Detection

Every 60 seconds, the control plane runs a drift detection check to ensure Caddy's route table matches the expected state in Postgres.

### Process

1. **Query Caddy** for active routes:
   ```http
   GET http://localhost:2019/config/apps/http/servers/srv0/routes
   ```

2. **Query Postgres** for expected routes:
   ```sql
   SELECT s.app_name, n.tailscale_ip, s.host_port
   FROM sandboxes s
   JOIN nodes n ON s.node_id = n.id
   WHERE s.state = 'running'
   ```

3. **Diff** the two sets:

| Condition | Action | Log Level |
|-----------|--------|-----------|
| Route in Caddy but not in Postgres (orphan) | `DELETE` from Caddy | `slog.Warn("routing drift corrected", "app_name", appName, "action", "removed")` |
| Route in Postgres but not in Caddy (missing) | `POST` to Caddy | `slog.Warn("routing drift corrected", "app_name", appName, "action", "added")` |
| Route in both but different upstream | `PATCH` update in Caddy | `slog.Warn("routing drift corrected", "app_name", appName, "action", "updated")` |
| Route matches | No action | (not logged) |

### Important Constraints

- **The drift detector only touches routes, NEVER containers.** Routing drift and container state are completely separate concerns. If a container is missing but the route exists, the drift detector removes the route -- it does not try to restart the container.
- **Drift corrections are batched.** If the drift detector finds multiple discrepancies, it applies all corrections in a single `POST /load` call to minimize Caddy reloads.
- **Drift detection is informational.** Every correction is logged as a warning because drift should not occur during normal operation. Persistent drift (same correction repeated across multiple cycles) indicates a bug in the route management logic and should trigger an alert.

### Drift Detection Frequency

- **Default interval**: 60 seconds.
- **Configurable** via `FORGE_DRIFT_CHECK_INTERVAL` environment variable.
- **Disabled** if set to `0` (useful during development or testing).

## Cloudflare DNS

The Cloudflare configuration complements the Caddy proxy:

### DNS Record

- **Wildcard record**: `*.myappx.live` configured as a CNAME or A record pointing to the Caddy proxy node's public IP address.
- **Cloudflare proxy mode**: **ON** (orange cloud) for DDoS protection, CDN caching, and WAF.

### SSL/TLS Settings

- **SSL mode**: **Full (Strict)** -- Cloudflare validates the Origin CA certificate on the Caddy server.
- **Origin certificate**: Cloudflare Origin CA cert installed on Caddy (15-year validity, no renewal needed).
- **Minimum TLS version**: 1.2.

### Important Settings

- **HTTP/3 (QUIC)**: **DISABLED** in Cloudflare to avoid `ERR_QUIC_PROTOCOL_ERROR` browser errors. This is a known issue with Cloudflare proxied WebSocket connections over QUIC.
- **WebSockets**: **Enabled** in Cloudflare dashboard (required for Metro HMR).
- **Always Use HTTPS**: **Enabled** (Cloudflare redirects HTTP to HTTPS before traffic reaches Caddy).

### DNS Propagation

When the wildcard DNS record is created, all `{app_name}.myappx.live` subdomains automatically resolve to the Caddy proxy node. No per-sandbox DNS changes are needed -- the wildcard handles all subdomains.

## Failure Modes

| Scenario | Impact | Recovery |
|----------|--------|----------|
| Caddy process crashes | All sandbox traffic drops | systemd auto-restarts Caddy; routes are in-memory and lost -- drift detector repopulates from Postgres on next cycle |
| Caddy Admin API unreachable | New routes cannot be added/removed | Control plane retries with exponential backoff (1s, 2s, 4s, max 30s); existing routes continue serving |
| Control plane crashes | No new routes added, no drift detection | Existing routes continue serving; drift accumulates until control plane restarts |
| Node goes offline | Caddy proxies to unreachable upstream | Caddy returns 502 to client; drift detector removes orphan route after sandbox is rescheduled |
| Cloudflare outage | All traffic drops (edge CDN down) | No mitigation -- Cloudflare is the edge; monitor via status page |

## Monitoring

The control plane exposes these metrics related to routing:

| Metric | Type | Description |
|--------|------|-------------|
| `forge_routes_total` | Gauge | Current number of active routes in Caddy |
| `forge_route_updates_total` | Counter | Total route add/remove/update operations |
| `forge_drift_corrections_total` | Counter | Total drift corrections applied |
| `forge_caddy_api_latency_seconds` | Histogram | Caddy Admin API request latency |
| `forge_caddy_api_errors_total` | Counter | Caddy Admin API errors by status code |
