package models

// SandboxState represents the lifecycle state of a sandbox.
type SandboxState string

const (
	StatePending    SandboxState = "pending"
	StateStarting   SandboxState = "starting"
	StateRunning    SandboxState = "running"
	StateRestarting SandboxState = "restarting"
	StateStopped    SandboxState = "stopped"
	StateDestroying SandboxState = "destroying"
	StateDestroyed  SandboxState = "destroyed"
	StateFailed     SandboxState = "failed"
)

// SandboxEvent represents an event that triggers a state transition.
type SandboxEvent string

const (
	EventScheduled       SandboxEvent = "scheduled"
	EventStarted         SandboxEvent = "started"
	EventContainerExited SandboxEvent = "container_exited"
	EventIdleTimeout     SandboxEvent = "idle_timeout"
	EventDestroyRequest  SandboxEvent = "destroy_requested"
	EventDestroyed       SandboxEvent = "destroyed"
	EventRestartAttempt  SandboxEvent = "restart_attempt"
	EventNodeFailed      SandboxEvent = "node_failed"
	EventStartFailed     SandboxEvent = "start_failed"
)

// ValidTransitions defines the complete state machine.
// Key: current state. Value: map of event -> next state.
var ValidTransitions = map[SandboxState]map[SandboxEvent]SandboxState{
	StatePending: {
		EventScheduled:      StateStarting,
		EventDestroyRequest: StateDestroyed,
	},
	StateStarting: {
		EventStarted:         StateRunning,
		EventContainerExited: StateFailed,
		EventStartFailed:     StateFailed,
		EventDestroyRequest:  StateDestroying,
	},
	StateRunning: {
		EventContainerExited: StateRestarting,
		EventDestroyRequest:  StateDestroying,
		EventIdleTimeout:     StateStopped,
		EventNodeFailed:      StatePending,
	},
	StateRestarting: {
		EventRestartAttempt: StateStarting,
		EventDestroyRequest: StateDestroying,
	},
	StateStopped: {
		EventScheduled:      StateStarting,
		EventDestroyRequest: StateDestroyed,
	},
	StateDestroying: {
		EventDestroyed: StateDestroyed,
		// Phase 32 Wave 2 Bug 3 — idempotent: re-attempting destroy while
		// already destroying is a no-op self-loop (reschedule-on-node-flap
		// can re-issue destroy on rows already in terminal teardown).
		EventDestroyRequest: StateDestroying,
	},
	StateFailed: {
		EventRestartAttempt: StateStarting,
		EventDestroyRequest: StateDestroyed,
		// Restart recovery: a start_sandbox ack can arrive while a stale
		// container_exited event has already flipped us to failed. Accepting
		// this transition closes the OOM-restart race where the new
		// container is healthy but the DB says failed.
		EventStarted: StateRunning,
	},
	// Phase 32 Wave 2 Bug 3 — terminal idempotent no-op: re-attempting
	// destroy on an already-destroyed row is a self-loop, not an error.
	// IsTerminal() still reports StateDestroyed as terminal because the
	// only outgoing edge is to itself.
	StateDestroyed: {
		EventDestroyRequest: StateDestroyed,
	},
}

// NextState returns the target state for a given current state and event,
// or false if the transition is invalid.
func NextState(current SandboxState, event SandboxEvent) (SandboxState, bool) {
	transitions, ok := ValidTransitions[current]
	if !ok {
		return "", false
	}
	next, ok := transitions[event]
	return next, ok
}

// AllStates returns all defined sandbox states.
func AllStates() []SandboxState {
	return []SandboxState{
		StatePending,
		StateStarting,
		StateRunning,
		StateRestarting,
		StateStopped,
		StateDestroying,
		StateDestroyed,
		StateFailed,
	}
}

// AllEvents returns all defined sandbox events.
func AllEvents() []SandboxEvent {
	return []SandboxEvent{
		EventScheduled,
		EventStarted,
		EventContainerExited,
		EventIdleTimeout,
		EventDestroyRequest,
		EventDestroyed,
		EventRestartAttempt,
		EventNodeFailed,
		EventStartFailed,
	}
}

// IsTerminal returns true if the state has no outgoing transitions.
func IsTerminal(state SandboxState) bool {
	return state == StateDestroyed
}

// FromDockerState maps a raw Docker container State string into the
// canonical SandboxState vocabulary. The agent observes Docker primitives
// (`running | paused | restarting | exited | dead | created`) but the
// rest of the system speaks SandboxState. This translator is the single
// source of truth for the boundary; agents call it before reporting state
// up to the control plane so the control side never sees Docker leakage.
//
// Returns ("", false) when the Docker state has no canonical counterpart
// (e.g. `paused` — Forge does not pause containers, an observed paused
// container is anomalous and should not produce a state UPDATE). Callers
// should drop the snapshot rather than fabricate a state.
func FromDockerState(dockerState string) (SandboxState, bool) {
	switch dockerState {
	case "running":
		return StateRunning, true
	case "restarting":
		return StateRestarting, true
	case "exited":
		return StateStopped, true
	case "dead":
		return StateFailed, true
	case "created":
		return StateStarting, true
	case "paused":
		// No Forge equivalent; agent should not emit this.
		return "", false
	}
	return "", false
}
