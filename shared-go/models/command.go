package models

// CommandType represents the type of command dispatched to an agent.
type CommandType string

const (
	CmdStartSandbox   CommandType = "start_sandbox"
	CmdStopSandbox    CommandType = "stop_sandbox"
	CmdRestartSandbox CommandType = "restart_sandbox"
	CmdGetLogs        CommandType = "get_logs"
	CmdExec           CommandType = "exec"
	CmdPrune          CommandType = "prune"
	// CmdBuildExport runs a cold `expo export` in a SEPARATE ephemeral
	// build-worker container (no dev Metro) against a SNAPSHOT of the
	// project code, so the exporter never shares a cgroup with the running
	// dev sandbox. The output is an artifact (dist/), fetched back via the
	// build-scoped dist endpoint — not stdout. See the agent's
	// executeBuildExport and the control DispatchBuildExport.
	CmdBuildExport CommandType = "build_export"
)

// CommandStatus represents the lifecycle status of a dispatched command.
type CommandStatus string

const (
	CmdPending    CommandStatus = "pending"
	CmdDispatched CommandStatus = "dispatched"
	CmdCompleted  CommandStatus = "completed"
	CmdFailed     CommandStatus = "failed"
)
