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

-- name: AssignSandboxToNodeUnderCap :one
-- Atomic conditional assign for the per-node count cap. Assigns the pending
-- sandbox to the node ONLY IF the node's live schedulable count is strictly
-- below @cap (an INTEGER, the per-node ceiling), measured in the SAME
-- statement's snapshot. The caller MUST hold a per-node advisory lock
-- (pg_advisory_xact_lock) in the enclosing transaction BEFORE running this so
-- concurrent assigns to the same node serialize — without that, two
-- simultaneous creates would each read the same pre-burst count and both pass
-- (the TOCTOU window this guards against). When the node is at/over cap the
-- UPDATE matches zero rows and returns no row (pgx.ErrNoRows), so the caller
-- picks another node or returns ErrNoCapacity.
--
-- NOTE: @cap is a named parameter (sqlc.arg) so the generated binding is a
-- typed INT field (Cap int32), NOT a UUID. A previous attempt left this as a
-- bare $3 positional which sqlc mis-bound to the node UUID, producing
-- "operator does not exist: bigint < uuid" at runtime on every capped assign.
UPDATE sandboxes AS s
SET node_id = @node_id, state = 'starting', updated_at = NOW(), state_version = state_version + 1
WHERE s.id = @id AND s.state = 'pending'
  AND (
    SELECT count(*) FROM sandboxes s2
    WHERE s2.node_id = @node_id
      AND s2.state IN ('pending', 'starting', 'running', 'restarting', 'destroying')
  ) < @cap::int
RETURNING s.*;

-- name: UpdateSandboxLastActive :exec
UPDATE sandboxes SET last_active_at = NOW() WHERE id = $1;

-- name: ListIdleSandboxes :many
SELECT * FROM sandboxes
WHERE state = 'running'
  AND last_active_at < NOW() - (idle_timeout_seconds || ' seconds')::INTERVAL
ORDER BY last_active_at ASC;

-- name: ListStoppedExpired :many
-- Sleep-not-destroy (2026-06-11): slept (docker-stopped, kept-on-disk)
-- sandboxes older than the retention window — the second-tier reaper
-- destroys these for real. updated_at is bumped by the running->stopped
-- transition, so this measures time-since-sleep.
SELECT * FROM sandboxes
WHERE state = 'stopped'
  AND updated_at < NOW() - (sqlc.arg(retention_seconds)::int || ' seconds')::INTERVAL
ORDER BY updated_at ASC;

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
-- Phase 33-Real-7 — guard against silent terminal-state flip. Without
-- the state-IN clause, a heartbeat reporting state='running' for an
-- app_name whose row is failed/destroying/destroyed would silently
-- resurrect it. Terminal rows must only leave terminal state via the
-- lifecycle layer's explicit transitions.
UPDATE sandboxes
SET verified_at = NOW(), state = $2
WHERE app_name = $1
  AND verified_at < NOW()
  AND state IN ('pending','starting','running','restarting','stopped');

-- name: ListTerminalSandboxesForNode :many
-- Phase 33-Real-7 — returns rows the reconciler must issue stop_sandbox
-- for: state is terminal but the agent still observes the container,
-- so the orphan needs explicit destroy dispatch.
SELECT id, app_name, container_id
FROM sandboxes
WHERE node_id = $1
  AND state IN ('failed', 'destroying', 'destroyed')
  AND container_id IS NOT NULL
  AND container_id <> '';

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
    metadata = metadata || jsonb_build_object('reason', sqlc.arg(reason)::text),
    verified_at = NOW(),
    updated_at = NOW(),
    state_version = state_version + 1
WHERE app_name = $1
  AND state IN ('pending','starting','running','restarting');
