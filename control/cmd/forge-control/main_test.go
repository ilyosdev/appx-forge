// Phase 32 Wave 2 Bug 4 — heartbeat flap debounce.
//
// monitorHeartbeats previously marked a node unhealthy and triggered
// RescheduleNode on the FIRST tick where last_seen exceeded the threshold.
// A brief flap (e.g. agent under load skipping one heartbeat tick) cascaded
// into a reschedule storm. These tests pin the new behaviour: a node must
// stay over-threshold for UnhealthyConfirmTicks consecutive ticks before
// reschedule is triggered, and any healthy heartbeat resets the streak.
package main

import (
	"net/netip"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/appx/forge/control/internal/store"
)

const (
	testIntervalSec = 15
	// threshold = interval * miss = 15 * 3 = 45s, matches the production
	// HeartbeatThreshold derivation in monitorHeartbeats.
	testThreshold = 45 * time.Second
)

// makeNode builds a healthy store.Node with the given last-seen time.
func makeNode(t *testing.T, hostname string, lastSeen time.Time) store.Node {
	t.Helper()
	return store.Node{
		ID:              pgtype.UUID{Bytes: uuid.New(), Valid: true},
		Hostname:        hostname,
		TailscaleIp:     netip.MustParseAddr("100.64.0.1"),
		AgentListenPort: 8090,
		Status:          "healthy",
		LastSeenAt:      pgtype.Timestamptz{Time: lastSeen, Valid: true},
	}
}

// TestEvaluateHeartbeats_BriefFlapNoReschedule asserts that a node which
// misses 1-2 ticks then recovers does NOT trigger a reschedule.
func TestEvaluateHeartbeats_BriefFlapNoReschedule(t *testing.T) {
	streaks := make(map[[16]byte]int)
	node := makeNode(t, "node-flap", time.Time{}) // last_seen rewritten per tick
	now := time.Now()

	// Tick 1: missed (elapsed = 50s > 45s threshold). streak -> 1.
	node.LastSeenAt = pgtype.Timestamptz{Time: now.Add(-50 * time.Second), Valid: true}
	decisions := evaluateHeartbeats([]store.Node{node}, streaks, now, testThreshold, testIntervalSec, defaultUnhealthyConfirmTicks)
	if len(decisions) != 1 {
		t.Fatalf("tick 1: want 1 decision, got %d", len(decisions))
	}
	if decisions[0].shouldMarkUnhealthy {
		t.Fatalf("tick 1: streak=1 should NOT trigger reschedule (need >=%d)", defaultUnhealthyConfirmTicks)
	}
	if got := streaks[node.ID.Bytes]; got != 1 {
		t.Fatalf("tick 1: want streak=1, got %d", got)
	}

	// Tick 2: still missed. streak -> 2.
	node.LastSeenAt = pgtype.Timestamptz{Time: now.Add(-65 * time.Second), Valid: true}
	now = now.Add(testIntervalSec * time.Second)
	decisions = evaluateHeartbeats([]store.Node{node}, streaks, now, testThreshold, testIntervalSec, defaultUnhealthyConfirmTicks)
	if decisions[0].shouldMarkUnhealthy {
		t.Fatalf("tick 2: streak=2 should NOT trigger reschedule")
	}
	if got := streaks[node.ID.Bytes]; got != 2 {
		t.Fatalf("tick 2: want streak=2, got %d", got)
	}

	// Tick 3: agent recovered — fresh heartbeat (elapsed < threshold).
	now = now.Add(testIntervalSec * time.Second)
	node.LastSeenAt = pgtype.Timestamptz{Time: now.Add(-5 * time.Second), Valid: true}
	decisions = evaluateHeartbeats([]store.Node{node}, streaks, now, testThreshold, testIntervalSec, defaultUnhealthyConfirmTicks)
	if len(decisions) != 0 {
		t.Fatalf("tick 3: recovered node should produce no decisions, got %d", len(decisions))
	}
	if got, ok := streaks[node.ID.Bytes]; ok && got != 0 {
		t.Fatalf("tick 3: streak should be reset to 0 (or absent), got %d", got)
	}
}

// TestEvaluateHeartbeats_SustainedFailureTriggersReschedule asserts that
// after UnhealthyConfirmTicks consecutive missed heartbeats the node is
// flagged for reschedule.
func TestEvaluateHeartbeats_SustainedFailureTriggersReschedule(t *testing.T) {
	streaks := make(map[[16]byte]int)
	node := makeNode(t, "node-down", time.Time{})
	base := time.Now()

	for i := 1; i <= defaultUnhealthyConfirmTicks; i++ {
		now := base.Add(time.Duration(i) * testIntervalSec * time.Second)
		// Always over threshold.
		node.LastSeenAt = pgtype.Timestamptz{Time: now.Add(-100 * time.Second), Valid: true}
		decisions := evaluateHeartbeats([]store.Node{node}, streaks, now, testThreshold, testIntervalSec, defaultUnhealthyConfirmTicks)
		if len(decisions) != 1 {
			t.Fatalf("tick %d: want 1 decision, got %d", i, len(decisions))
		}
		want := i >= defaultUnhealthyConfirmTicks
		if decisions[0].shouldMarkUnhealthy != want {
			t.Fatalf("tick %d: shouldMarkUnhealthy=%v, want %v (streak=%d)",
				i, decisions[0].shouldMarkUnhealthy, want, decisions[0].streak)
		}
	}
}

// TestEvaluateHeartbeats_StreakResetsAfterReschedule asserts that once a
// reschedule has been triggered the streak is wiped, so a subsequent flap
// has to clear the threshold again from scratch.
func TestEvaluateHeartbeats_StreakResetsAfterReschedule(t *testing.T) {
	streaks := make(map[[16]byte]int)
	node := makeNode(t, "node-down", time.Time{})
	base := time.Now()

	// Drive past the confirm threshold.
	for i := 1; i <= defaultUnhealthyConfirmTicks; i++ {
		now := base.Add(time.Duration(i) * testIntervalSec * time.Second)
		node.LastSeenAt = pgtype.Timestamptz{Time: now.Add(-100 * time.Second), Valid: true}
		_ = evaluateHeartbeats([]store.Node{node}, streaks, now, testThreshold, testIntervalSec, defaultUnhealthyConfirmTicks)
	}
	// On the tick the reschedule fires, the streak should be cleared so the
	// monitor doesn't keep re-firing every subsequent tick while the node
	// row is in the process of being flipped to 'unhealthy'.
	if got, ok := streaks[node.ID.Bytes]; ok && got != 0 {
		t.Fatalf("after reschedule: streak should be cleared, got %d", got)
	}
}

// TestEvaluateHeartbeats_OnlyHealthyNodesEvaluated asserts that nodes
// already in unhealthy / draining / removed states are skipped (the
// monitor only acts on healthy nodes).
func TestEvaluateHeartbeats_OnlyHealthyNodesEvaluated(t *testing.T) {
	streaks := make(map[[16]byte]int)
	now := time.Now()

	healthy := makeNode(t, "node-h", now.Add(-100*time.Second))
	unhealthy := makeNode(t, "node-u", now.Add(-100*time.Second))
	unhealthy.Status = "unhealthy"
	noLastSeen := makeNode(t, "node-n", time.Time{})
	noLastSeen.LastSeenAt = pgtype.Timestamptz{Valid: false}

	decisions := evaluateHeartbeats(
		[]store.Node{healthy, unhealthy, noLastSeen},
		streaks, now, testThreshold, testIntervalSec, defaultUnhealthyConfirmTicks,
	)
	if len(decisions) != 1 {
		t.Fatalf("want 1 decision (only healthy node over threshold), got %d", len(decisions))
	}
	if decisions[0].nodeID != healthy.ID {
		t.Fatalf("want decision for %v, got %v", healthy.ID, decisions[0].nodeID)
	}
}
