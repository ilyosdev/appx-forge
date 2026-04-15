-- +goose Up
CREATE TABLE nodes (
    id              UUID PRIMARY KEY,
    hostname        TEXT NOT NULL,
    tailscale_ip    INET NOT NULL,
    agent_listen_port INT NOT NULL DEFAULT 8090,
    capacity_mb     INT NOT NULL,
    capacity_cpu    NUMERIC(4,1) NOT NULL DEFAULT 1.0,
    used_mb         INT NOT NULL DEFAULT 0,
    running_containers INT NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'healthy'
                    CHECK (status IN ('healthy','unhealthy','draining','removed')),
    agent_version   TEXT NOT NULL DEFAULT '',
    last_seen_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    registered_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    metadata        JSONB NOT NULL DEFAULT '{}'
);

-- +goose Down
DROP TABLE IF EXISTS nodes;
