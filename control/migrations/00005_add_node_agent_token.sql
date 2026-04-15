-- +goose Up
ALTER TABLE nodes ADD COLUMN agent_token TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE nodes DROP COLUMN agent_token;
