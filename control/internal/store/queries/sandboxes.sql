-- name: CreateSandbox :one
INSERT INTO sandboxes (id, app_name, user_id, image, state, resources, env, idle_timeout_seconds, metadata)
VALUES ($1, $2, $3, $4, 'pending', $5, $6, $7, $8)
RETURNING *;

-- name: GetSandbox :one
SELECT * FROM sandboxes WHERE id = $1;

-- name: GetSandboxByAppName :one
SELECT * FROM sandboxes WHERE app_name = $1;

-- name: ListSandboxes :many
SELECT * FROM sandboxes ORDER BY created_at DESC LIMIT $1;

-- name: ListSandboxesByState :many
SELECT * FROM sandboxes WHERE state = $1 ORDER BY created_at DESC;

-- name: ListSandboxesByNode :many
SELECT * FROM sandboxes WHERE node_id = $1 ORDER BY created_at DESC;

-- name: ListSandboxesByUser :many
SELECT * FROM sandboxes WHERE user_id = $1 ORDER BY created_at DESC;

-- name: TransitionSandboxState :one
UPDATE sandboxes
SET state = $1, updated_at = NOW(), state_version = state_version + 1
WHERE id = $2 AND state = $3
RETURNING *;

-- name: AssignSandboxToNode :one
UPDATE sandboxes
SET node_id = $1, host_port = $2, container_id = $3, state = 'starting', updated_at = NOW(), state_version = state_version + 1
WHERE id = $4 AND state = 'pending'
RETURNING *;

-- name: UpdateSandboxLastActive :exec
UPDATE sandboxes SET last_active_at = NOW() WHERE id = $1;

-- name: ListIdleSandboxes :many
SELECT * FROM sandboxes
WHERE state = 'running'
  AND last_active_at < NOW() - (idle_timeout_seconds || ' seconds')::INTERVAL
ORDER BY last_active_at ASC;

-- name: IncrementSandboxFailureCount :one
UPDATE sandboxes
SET failure_count = failure_count + 1, updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: ResetSandboxFailureCount :exec
UPDATE sandboxes
SET failure_count = 0, updated_at = NOW()
WHERE id = $1;

-- name: CountSandboxesByState :many
SELECT state, COUNT(*)::int AS count
FROM sandboxes
GROUP BY state;

-- name: UpdateSandboxRuntime :exec
UPDATE sandboxes
SET container_id = $1, host_port = $2, updated_at = NOW()
WHERE id = $3;

-- name: ListRunningSandboxesByNode :many
SELECT * FROM sandboxes WHERE node_id = $1 AND state = 'running' ORDER BY created_at ASC;

-- name: DeleteSandbox :exec
DELETE FROM sandboxes WHERE id = $1;

-- name: MarkSandboxVerified :exec
UPDATE sandboxes
SET verified_at = NOW(), state = $2
WHERE app_name = $1
  AND verified_at < NOW();

-- name: MarkSandboxAgentLost :exec
UPDATE sandboxes
SET state = 'destroyed',
    metadata = metadata || jsonb_build_object('reason', 'agent_lost_at_heartbeat'),
    verified_at = NOW()
WHERE app_name = $1
  AND node_id = $2
  AND state IN ('pending','starting','running','restarting')
  AND created_at < NOW() - INTERVAL '60 seconds';

-- name: ListSandboxesForNode :many
SELECT app_name, state, created_at
FROM sandboxes
WHERE node_id = $1
  AND state IN ('pending','starting','running','restarting');

-- name: MarkSandboxDestroyed :exec
UPDATE sandboxes
SET state = 'destroyed',
    metadata = metadata || jsonb_build_object('reason', $2::text),
    verified_at = NOW(),
    updated_at = NOW(),
    state_version = state_version + 1
WHERE app_name = $1
  AND state IN ('pending','starting','running','restarting');
