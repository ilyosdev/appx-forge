-- name: CreateCommand :one
INSERT INTO commands (id, node_id, sandbox_id, command_type, payload, timeout_seconds)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetCommand :one
SELECT * FROM commands WHERE id = $1;

-- name: PollPendingCommands :many
UPDATE commands
SET status = 'dispatched', dispatched_at = NOW()
WHERE id IN (
    SELECT c.id FROM commands c
    WHERE c.node_id = $1 AND c.status = 'pending'
    ORDER BY c.created_at ASC
    LIMIT 10
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: AckCommand :exec
UPDATE commands
SET status = $2, acked_at = NOW(), result = $3
WHERE id = $1;
