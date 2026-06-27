-- +goose Up
-- Per-turn ephemeral HMR tier (Step 3): widen the command_type CHECK constraint
-- to admit start_hmr / stop_hmr. MUST deploy BEFORE any agent/control that emits
-- them — otherwise the CreateCommand INSERT is rejected by the constraint.
ALTER TABLE commands
    DROP CONSTRAINT IF EXISTS commands_command_type_check;
ALTER TABLE commands
    ADD CONSTRAINT commands_command_type_check
    CHECK (command_type IN ('start_sandbox','stop_sandbox',
                            'restart_sandbox','get_logs','prune','exec',
                            'build_export','start_hmr','stop_hmr'));

-- +goose Down
ALTER TABLE commands
    DROP CONSTRAINT IF EXISTS commands_command_type_check;
ALTER TABLE commands
    ADD CONSTRAINT commands_command_type_check
    CHECK (command_type IN ('start_sandbox','stop_sandbox',
                            'restart_sandbox','get_logs','prune','exec','build_export'));
