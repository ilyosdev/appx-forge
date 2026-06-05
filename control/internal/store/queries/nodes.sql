-- name: CreateNode :one
INSERT INTO nodes (id, hostname, tailscale_ip, agent_listen_port, capacity_mb, capacity_cpu, agent_version, metadata)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetNode :one
SELECT * FROM nodes WHERE id = $1;

-- name: ListNodes :many
SELECT * FROM nodes ORDER BY registered_at DESC;

-- name: ListHealthyNodes :many
SELECT * FROM nodes WHERE status IN ('healthy') ORDER BY (capacity_mb - used_mb) DESC;

-- name: UpdateNodeHeartbeat :exec
UPDATE nodes SET used_mb = $2, running_containers = $3, status = 'healthy', last_seen_at = NOW() WHERE id = $1;

-- name: UpdateNodeStatus :exec
UPDATE nodes SET status = $2 WHERE id = $1;

-- name: GetNodeByHostnameAndIP :one
SELECT * FROM nodes WHERE hostname = $1 AND tailscale_ip = $2;

-- name: UpdateNodeToken :exec
UPDATE nodes SET agent_token = $1, agent_version = $2, last_seen_at = NOW() WHERE id = $3;

-- name: CountActiveSandboxesByNode :one
SELECT count(*)::int FROM sandboxes WHERE node_id = $1 AND state NOT IN ('destroyed', 'stopped');

-- name: CountSchedulableSandboxesByNode :one
-- Authoritative count of sandboxes that currently hold (or are about to hold)
-- a container's RAM on a node — i.e. RAM-consuming states only. Excludes
-- terminal states (destroyed, failed) and the RAM-freed stopped/sleeping
-- state. Used by the scheduler's per-node count cap so the backstop reflects
-- a provision burst SYNCHRONOUSLY (each CreateSandbox/AssignSandboxToNode is
-- committed to this table), unlike the heartbeat-derived running_containers
-- which is only refreshed every ~15s and goes stale during a burst/failover.
SELECT count(*)::int FROM sandboxes
WHERE node_id = $1
  AND state IN ('pending', 'starting', 'running', 'restarting', 'destroying');
