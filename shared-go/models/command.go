package models

// CommandType represents the type of command dispatched to an agent.
type CommandType string

const (
	CmdStartSandbox   CommandType = "start_sandbox"
	CmdStopSandbox    CommandType = "stop_sandbox"
	CmdRestartSandbox CommandType = "restart_sandbox"
	CmdGetLogs        CommandType = "get_logs"
	CmdPrune          CommandType = "prune"
)

// CommandStatus represents the lifecycle status of a dispatched command.
type CommandStatus string

const (
	CmdPending    CommandStatus = "pending"
	CmdDispatched CommandStatus = "dispatched"
	CmdCompleted  CommandStatus = "completed"
	CmdFailed     CommandStatus = "failed"
)
