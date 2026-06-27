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
	// CmdStartHmr starts a PER-TURN EPHEMERAL HMR container — a dev Metro
	// (`expo start`) that lives only while an edit/gen turn is active, in its
	// own box, bound to the project's LIVE code dir so uncommitted edits HMR
	// during the turn. It is "build_export inverted": keeps the image's dev
	// Metro CMD, exposes a host port, binds live code (not a snapshot), and is
	// labeled forge.hmr_id (NOT forge.app_name) so the heartbeat reconciler /
	// OrphanHunter never see it. Like build_export it does NOT transition
	// sandbox state. See the agent's executeStartHmr and control DispatchStartHmr.
	// Gated entirely behind the backend ISOLATED_HMR flag.
	CmdStartHmr CommandType = "start_hmr"
	// CmdStopHmr force-removes the per-turn ephemeral HMR container created by
	// CmdStartHmr and releases its host port, after the turn settles + a grace
	// window. See the agent's executeStopHmr and control DispatchStopHmr.
	CmdStopHmr CommandType = "stop_hmr"
)

// CommandStatus represents the lifecycle status of a dispatched command.
type CommandStatus string

const (
	CmdPending    CommandStatus = "pending"
	CmdDispatched CommandStatus = "dispatched"
	CmdCompleted  CommandStatus = "completed"
	CmdFailed     CommandStatus = "failed"
)
