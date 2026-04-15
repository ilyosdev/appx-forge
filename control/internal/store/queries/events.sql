-- name: RecordEvent :one
INSERT INTO events (sandbox_id, node_id, event_type, actor, prev_state, next_state, payload)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: ListEventsBySandbox :many
SELECT * FROM events WHERE sandbox_id = $1 ORDER BY created_at DESC LIMIT $2;

-- name: ListEventsByType :many
SELECT * FROM events WHERE event_type = $1 ORDER BY created_at DESC LIMIT $2;
