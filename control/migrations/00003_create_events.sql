-- +goose Up
CREATE TABLE events (
    id          BIGSERIAL PRIMARY KEY,
    sandbox_id  UUID,
    node_id     UUID,
    event_type  TEXT NOT NULL,
    actor       TEXT NOT NULL DEFAULT 'system',
    prev_state  TEXT,
    next_state  TEXT,
    payload     JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_events_sandbox ON events(sandbox_id, created_at DESC);
CREATE INDEX idx_events_type ON events(event_type, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS events;
