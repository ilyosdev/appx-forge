-- +goose Up
ALTER TABLE commands
    DROP CONSTRAINT IF EXISTS commands_command_type_check;
ALTER TABLE commands
    ADD CONSTRAINT commands_command_type_check
    CHECK (command_type IN ('start_sandbox','stop_sandbox',
                            'restart_sandbox','get_logs','prune','exec'));

-- +goose Down
ALTER TABLE commands
    DROP CONSTRAINT IF EXISTS commands_command_type_check;
ALTER TABLE commands
    ADD CONSTRAINT commands_command_type_check
    CHECK (command_type IN ('start_sandbox','stop_sandbox',
                            'restart_sandbox','get_logs','prune'));
