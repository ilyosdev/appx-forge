package scheduler

import (
	"testing"

	"github.com/google/uuid"
)

func TestSchedule_PicksMostFreeRAM(t *testing.T) {
	// 3 healthy nodes with free RAM [2000, 5000, 3000], request 512MB -> picks node with 5000 free
	node1 := uuid.New()
	node2 := uuid.New()
	node3 := uuid.New()

	candidates := []NodeCandidate{
		{ID: node1, CapacityMB: 4000, UsedMB: 2000, Status: "healthy"},
		{ID: node2, CapacityMB: 8000, UsedMB: 3000, Status: "healthy"},
		{ID: node3, CapacityMB: 6000, UsedMB: 3000, Status: "healthy"},
	}

	got, err := Schedule(candidates, 512)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != node2 {
		t.Errorf("want node2 (5000 free), got %v", got)
	}
}

func TestSchedule_ExcludesDrainingNodes(t *testing.T) {
	// 2 healthy + 1 draining, draining has most free -> picks healthy with most free
	healthy1 := uuid.New()
	healthy2 := uuid.New()
	draining := uuid.New()

	candidates := []NodeCandidate{
		{ID: healthy1, CapacityMB: 4000, UsedMB: 2000, Status: "healthy"},
		{ID: draining, CapacityMB: 16000, UsedMB: 0, Status: "draining"},
		{ID: healthy2, CapacityMB: 6000, UsedMB: 3000, Status: "healthy"},
	}

	got, err := Schedule(candidates, 512)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != healthy2 {
		t.Errorf("want healthy2 (3000 free), got %v", got)
	}
}

func TestSchedule_ExcludesUnhealthyNodes(t *testing.T) {
	// 2 healthy + 1 unhealthy, unhealthy has most free -> picks healthy with most free
	healthy1 := uuid.New()
	healthy2 := uuid.New()
	unhealthy := uuid.New()

	candidates := []NodeCandidate{
		{ID: healthy1, CapacityMB: 4000, UsedMB: 3000, Status: "healthy"},
		{ID: unhealthy, CapacityMB: 32000, UsedMB: 0, Status: "unhealthy"},
		{ID: healthy2, CapacityMB: 8000, UsedMB: 5000, Status: "healthy"},
	}

	got, err := Schedule(candidates, 256)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != healthy2 {
		t.Errorf("want healthy2 (3000 free), got %v", got)
	}
}

func TestSchedule_ExcludesRemovedNodes(t *testing.T) {
	// Node with status "removed" is excluded
	healthy := uuid.New()
	removed := uuid.New()

	candidates := []NodeCandidate{
		{ID: removed, CapacityMB: 32000, UsedMB: 0, Status: "removed"},
		{ID: healthy, CapacityMB: 4000, UsedMB: 2000, Status: "healthy"},
	}

	got, err := Schedule(candidates, 256)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != healthy {
		t.Errorf("want healthy node, got %v", got)
	}
}

func TestSchedule_ErrNoNodes(t *testing.T) {
	// No nodes at all (empty slice) -> ErrNoNodes
	_, err := Schedule(nil, 512)
	if err != ErrNoNodes {
		t.Errorf("want ErrNoNodes, got %v", err)
	}

	_, err = Schedule([]NodeCandidate{}, 512)
	if err != ErrNoNodes {
		t.Errorf("want ErrNoNodes for empty slice, got %v", err)
	}
}

func TestSchedule_ErrNoCapacity(t *testing.T) {
	// All nodes healthy but none has enough free RAM -> ErrNoCapacity
	node1 := uuid.New()
	node2 := uuid.New()

	candidates := []NodeCandidate{
		{ID: node1, CapacityMB: 2000, UsedMB: 1800, Status: "healthy"},
		{ID: node2, CapacityMB: 4000, UsedMB: 3900, Status: "healthy"},
	}

	_, err := Schedule(candidates, 512)
	if err != ErrNoCapacity {
		t.Errorf("want ErrNoCapacity, got %v", err)
	}
}

func TestSchedule_ErrNoCapacity_AllExcluded(t *testing.T) {
	// All nodes draining/unhealthy/removed -> ErrNoCapacity (no healthy nodes with capacity)
	candidates := []NodeCandidate{
		{ID: uuid.New(), CapacityMB: 8000, UsedMB: 0, Status: "draining"},
		{ID: uuid.New(), CapacityMB: 8000, UsedMB: 0, Status: "unhealthy"},
		{ID: uuid.New(), CapacityMB: 8000, UsedMB: 0, Status: "removed"},
	}

	_, err := Schedule(candidates, 256)
	if err != ErrNoCapacity {
		t.Errorf("want ErrNoCapacity when all nodes excluded, got %v", err)
	}
}

func TestSchedule_ExactFit(t *testing.T) {
	// 1 healthy node with exactly enough free RAM -> picks that node
	node := uuid.New()

	candidates := []NodeCandidate{
		{ID: node, CapacityMB: 2000, UsedMB: 1488, Status: "healthy"},
	}

	got, err := Schedule(candidates, 512)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != node {
		t.Errorf("want exact-fit node, got %v", got)
	}
}

func TestSchedule_DeterministicTiebreak(t *testing.T) {
	// 2 healthy nodes with same free RAM -> picks first (deterministic tiebreak)
	first := uuid.New()
	second := uuid.New()

	candidates := []NodeCandidate{
		{ID: first, CapacityMB: 4000, UsedMB: 2000, Status: "healthy"},
		{ID: second, CapacityMB: 4000, UsedMB: 2000, Status: "healthy"},
	}

	got, err := Schedule(candidates, 512)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != first {
		t.Errorf("want first node on tiebreak, got %v", got)
	}
}

func TestSchedule_DistributesAcrossMultipleNodes(t *testing.T) {
	// Simulate scheduling 3 sandboxes (512MB each) across 3 nodes with different initial load.
	// After each schedule, update UsedMB to reflect placement.
	// Expected: scheduler picks most-free node each time, distributing across nodes.
	node1 := uuid.New()
	node2 := uuid.New()
	node3 := uuid.New()

	candidates := []NodeCandidate{
		{ID: node1, CapacityMB: 8000, UsedMB: 2000, Status: "healthy"}, // 6000 free
		{ID: node2, CapacityMB: 8000, UsedMB: 2500, Status: "healthy"}, // 5500 free
		{ID: node3, CapacityMB: 8000, UsedMB: 3000, Status: "healthy"}, // 5000 free
	}

	// First schedule: picks node1 (most free: 6000)
	got1, err := Schedule(candidates, 512)
	if err != nil {
		t.Fatalf("schedule 1: %v", err)
	}
	if got1 != node1 {
		t.Errorf("schedule 1: want node1 (6000 free), got %v", got1)
	}

	// Update candidates to reflect placement on node1
	candidates[0].UsedMB = 2512 // 5488 free now

	// Second schedule: picks node2 (5500 free > 5488 > 5000)
	got2, err := Schedule(candidates, 512)
	if err != nil {
		t.Fatalf("schedule 2: %v", err)
	}
	if got2 != node2 {
		t.Errorf("schedule 2: want node2 (5500 free), got %v", got2)
	}

	// Update candidates to reflect placement on node2
	candidates[1].UsedMB = 3012 // 4988 free now

	// Third schedule: picks node1 (5488 free > 5000 > 4988)
	got3, err := Schedule(candidates, 512)
	if err != nil {
		t.Fatalf("schedule 3: %v", err)
	}
	if got3 != node1 {
		t.Errorf("schedule 3: want node1 (5488 free), got %v", got3)
	}

	// Verify distribution: at least 2 different nodes used
	used := make(map[uuid.UUID]int)
	used[got1]++
	used[got2]++
	used[got3]++

	if len(used) < 2 {
		t.Errorf("expected sandboxes on at least 2 different nodes, got all on same node")
	}
}

func TestSchedule_NeverOverloadsOneNode(t *testing.T) {
	// 3 nodes with equal capacity. Schedule 5 sandboxes (256MB each).
	// Most-free-RAM bin-packing naturally distributes when nodes start equal:
	// each placement shifts the ranking so the next sandbox goes elsewhere.
	node1 := uuid.New()
	node2 := uuid.New()
	node3 := uuid.New()

	candidates := []NodeCandidate{
		{ID: node1, CapacityMB: 8000, UsedMB: 2000, Status: "healthy"}, // 6000 free
		{ID: node2, CapacityMB: 8000, UsedMB: 2100, Status: "healthy"}, // 5900 free
		{ID: node3, CapacityMB: 8000, UsedMB: 2200, Status: "healthy"}, // 5800 free
	}

	placements := make(map[uuid.UUID]int)
	for i := 0; i < 5; i++ {
		got, err := Schedule(candidates, 256)
		if err != nil {
			t.Fatalf("schedule %d: %v", i, err)
		}
		placements[got]++

		// Update used MB on the selected node
		for j := range candidates {
			if candidates[j].ID == got {
				candidates[j].UsedMB += 256
			}
		}
	}

	// At least 2 different nodes must be used
	if len(placements) < 2 {
		t.Errorf("expected load spread across nodes, got all on one: %v", placements)
	}

	// No single node should get all 5 sandboxes
	for id, count := range placements {
		if count == 5 {
			t.Errorf("node %v received all 5 sandboxes -- scheduler should distribute", id)
		}
	}
}
