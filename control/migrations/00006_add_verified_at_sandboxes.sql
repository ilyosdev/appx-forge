-- +goose Up
-- Phase 30 — add verified_at for continuous reconciliation against agent reality.
-- Default NOW() so existing rows are deemed fresh on deploy (no one-shot mass-stale).
ALTER TABLE sandboxes ADD COLUMN verified_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

CREATE INDEX idx_sandboxes_verified_at ON sandboxes(verified_at);

-- +goose Down
DROP INDEX IF EXISTS idx_sandboxes_verified_at;
ALTER TABLE sandboxes DROP COLUMN IF EXISTS verified_at;
