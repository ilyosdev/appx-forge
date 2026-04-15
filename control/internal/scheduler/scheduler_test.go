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
