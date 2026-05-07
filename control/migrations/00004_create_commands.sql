-- +goose Up
CREATE TABLE commands (
    id              UUID PRIMARY KEY,
    node_id         UUID NOT NULL REFERENCES nodes(id),
    sandbox_id      UUID REFERENCES sandboxes(id),
    command_type    TEXT NOT NULL
                    CHECK (command_type IN ('start_sandbox','stop_sandbox',
                                            'restart_sandbox','get_logs','prune')),
    payload         JSONB NOT NULL DEFAULT '{}',
    status          TEXT NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending','dispatched','completed','failed')),
    dispatched_at   TIMESTAMPTZ,
    acked_at        TIMESTAMPTZ,
    result          JSONB,
    timeout_seconds INT NOT NULL DEFAULT 60,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_commands_node_pending ON commands(node_id, status)
    WHERE status IN ('pending', 'dispatched');

-- +goose Down
DROP TABLE IF EXISTS commands;
