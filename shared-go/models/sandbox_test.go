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
	// StateDestroyed is semantically terminal: no transition may leave it.
	// As of Phase 32 Wave 2 Bug 3, idempotent destroy_request self-loops
	// are permitted (StateDestroyed → StateDestroyed) so re-issued destroys
	// don't surface "invalid state transition" errors. Any non-self-loop
	// outgoing edge is still a contract violation.
	transitions := ValidTransitions[StateDestroyed]
	for event, target := range transitions {
		if target != StateDestroyed {
			t.Errorf("StateDestroyed has non-self-loop outgoing edge: event %q → %q", event, target)
		}
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
		{StateFailed, EventStarted, StateRunning}, // restart recovery ack race
		{StateRestarting, EventRestartAttempt, StateStarting},
		{StateStopped, EventScheduled, StateStarting},
		{StateDestroying, EventDestroyRequest, StateDestroying}, // Phase 32 Wave 2 Bug 3 — idempotent
		{StateDestroyed, EventDestroyRequest, StateDestroyed},   // Phase 32 Wave 2 Bug 3 — idempotent terminal
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

func TestNextState_DestroyIdempotent(t *testing.T) {
	// Phase 32 Wave 2 Bug 3 — destroy must be idempotent on already-destroyed
	// or destroying sandboxes. Mirrors the cc0c16f start_sandbox no-op pattern.
	// Production storm at 14:35:03-05 produced 24 errors/sec from the
	// reschedule-on-node-flap path re-attempting destroys on terminal rows.
	cases := []struct {
		name       string
		from       SandboxState
		event      SandboxEvent
		expectedTo SandboxState
		expectOK   bool
	}{
		{"destroyed + destroy request → no-op", StateDestroyed, EventDestroyRequest, StateDestroyed, true},
		{"destroying + destroy request → no-op", StateDestroying, EventDestroyRequest, StateDestroying, true},
		{"failed + destroy request → destroyed", StateFailed, EventDestroyRequest, StateDestroyed, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			to, ok := NextState(tc.from, tc.event)
			if ok != tc.expectOK {
				t.Fatalf("ok=%v want %v", ok, tc.expectOK)
			}
			if to != tc.expectedTo {
				t.Fatalf("to=%s want %s", to, tc.expectedTo)
			}
		})
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
