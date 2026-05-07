-- +goose Up
CREATE TABLE sandboxes (
    id              UUID PRIMARY KEY,
    app_name        TEXT NOT NULL UNIQUE,
    user_id         TEXT NOT NULL,
    node_id         UUID REFERENCES nodes(id),
    container_id    TEXT,
    host_port       INT,
    image           TEXT NOT NULL,
    state           TEXT NOT NULL DEFAULT 'pending'
                    CHECK (state IN ('pending','starting','running','restarting',
                                     'stopped','destroying','destroyed','failed')),
    state_version   INT NOT NULL DEFAULT 0,
    resources       JSONB NOT NULL DEFAULT '{"cpu_cores": 0.5, "memory_mb": 512}',
    env             JSONB NOT NULL DEFAULT '{}',
    idle_timeout_seconds INT NOT NULL DEFAULT 1800,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_active_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    failure_count   INT NOT NULL DEFAULT 0,
    metadata        JSONB NOT NULL DEFAULT '{}'
);

CREATE INDEX idx_sandboxes_app_name ON sandboxes(app_name);
CREATE INDEX idx_sandboxes_state ON sandboxes(state);
CREATE INDEX idx_sandboxes_node ON sandboxes(node_id);
CREATE INDEX idx_sandboxes_user ON sandboxes(user_id);

-- +goose Down
DROP TABLE IF EXISTS sandboxes;
