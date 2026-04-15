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
	},
	StateFailed: {
		EventRestartAttempt: StateStarting,
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
