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
UPDATE nodes SET used_mb = $2, running_containers = $3, last_seen_at = NOW() WHERE id = $1;

-- name: UpdateNodeStatus :exec
UPDATE nodes SET status = $2 WHERE id = $1;
