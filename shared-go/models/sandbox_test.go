package models

import "testing"

func TestAllStatesHaveTransitions(t *testing.T) {
	for _, state := range AllStates() {
		if state == StateDestroyed {
			continue // terminal state, no outgoing transitions expected
		}
		transitions, ok := ValidTransitions[state]
		if !ok || len(transitions) == 0 {
			t.Errorf("state %q has no outgoing transitions", state)
		}
	}
}

func TestDestroyedIsTerminal(t *testing.T) {
	transitions, hasTransitions := ValidTransitions[StateDestroyed]
	if hasTransitions && len(transitions) > 0 {
		t.Error("StateDestroyed should be terminal (no outgoing transitions)")
	}
}

func TestEveryStateCanReachDestroyed(t *testing.T) {
	// BFS backward from StateDestroyed: mark states that can reach it
	reachable := map[SandboxState]bool{StateDestroyed: true}
	changed := true
	for changed {
		changed = false
		for state, transitions := range ValidTransitions {
			if reachable[state] {
				continue
			}
			for _, next := range transitions {
				if reachable[next] {
					reachable[state] = true
					changed = true
					break
				}
			}
		}
	}

	for _, state := range AllStates() {
		if state == StateDestroyed {
			continue
		}
		if !reachable[state] {
			t.Errorf("state %q cannot reach StateDestroyed", state)
		}
	}
}

func TestDestroyRequestAlwaysAccepted(t *testing.T) {
	// Destroy should work from any active (non-terminal, non-destroying) state
	activeStates := []SandboxState{
		StatePending, StateStarting, StateRunning, StateRestarting,
		StateStopped, StateFailed,
	}
	for _, state := range activeStates {
		next, ok := NextState(state, EventDestroyRequest)
		if !ok {
			t.Errorf("EventDestroyRequest not accepted in state %q", state)
		}
		if next != StateDestroying && next != StateDestroyed {
			t.Errorf("EventDestroyRequest in %q should lead to destroying/destroyed, got %q", state, next)
		}
	}
}

func TestInvalidTransitionsRejected(t *testing.T) {
	tests := []struct {
		state SandboxState
		event SandboxEvent
	}{
		{StateDestroyed, EventStarted},
		{StateDestroyed, EventScheduled},
		{StatePending, EventStarted},
		{StateRunning, EventScheduled},
		{StateStarting, EventIdleTimeout},
	}
	for _, tt := range tests {
		_, ok := NextState(tt.state, tt.event)
		if ok {
			t.Errorf("transition (%q, %q) should be invalid", tt.state, tt.event)
		}
	}
}

func TestValidTransitionsAccepted(t *testing.T) {
	tests := []struct {
		state    SandboxState
		event    SandboxEvent
		expected SandboxState
	}{
		{StatePending, EventScheduled, StateStarting},
		{StateStarting, EventStarted, StateRunning},
		{StateRunning, EventContainerExited, StateRestarting},
		{StateRunning, EventDestroyRequest, StateDestroying},
		{StateDestroying, EventDestroyed, StateDestroyed},
		{StateRunning, EventIdleTimeout, StateStopped},
		{StateRunning, EventNodeFailed, StatePending},
		{StateFailed, EventRestartAttempt, StateStarting},
		{StateRestarting, EventRestartAttempt, StateStarting},
		{StateStopped, EventScheduled, StateStarting},
	}
	for _, tt := range tests {
		next, ok := NextState(tt.state, tt.event)
		if !ok {
			t.Errorf("transition (%q, %q) should be valid", tt.state, tt.event)
			continue
		}
		if next != tt.expected {
			t.Errorf("transition (%q, %q): got %q, want %q", tt.state, tt.event, next, tt.expected)
		}
	}
}

func TestIsTerminal(t *testing.T) {
	if !IsTerminal(StateDestroyed) {
		t.Error("StateDestroyed should be terminal")
	}
	nonTerminal := []SandboxState{
		StatePending, StateStarting, StateRunning, StateRestarting,
		StateStopped, StateDestroying, StateFailed,
	}
	for _, state := range nonTerminal {
		if IsTerminal(state) {
			t.Errorf("state %q should not be terminal", state)
		}
	}
}

func TestAllStatesCount(t *testing.T) {
	states := AllStates()
	if len(states) != 8 {
		t.Errorf("expected 8 states, got %d", len(states))
	}
}

func TestAllEventsCount(t *testing.T) {
	events := AllEvents()
	if len(events) != 9 {
		t.Errorf("expected 9 events, got %d", len(events))
	}
}
