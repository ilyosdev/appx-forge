package docker

import (
	"context"
	"testing"
)

// fakeContainerLister implements the DockerLister interface Snapshotter needs.
// We use a different name from mockClient (which implements the broader Client
// interface) to keep the test focused on the listing capability only.
type fakeContainerLister struct {
	containers []ContainerSnapshot
	err        error
}

func (f *fakeContainerLister) ListContainers(_ context.Context) ([]ContainerSnapshot, error) {
	return f.containers, f.err
}

func TestSnapshot_ReturnsFullContainerList(t *testing.T) {
	lister := &fakeContainerLister{
		containers: []ContainerSnapshot{
			{AppName: "pool-abc-123", State: "running", HostPort: 8081, ContainerID: "c1"},
			{AppName: "app-def-456", State: "running", HostPort: 8082, ContainerID: "c2"},
		},
	}
	snap := NewSnapshotter(lister)

	result, err := snap.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 containers, got %d", len(result))
	}
	if result[0].AppName != "pool-abc-123" {
		t.Errorf("expected pool-abc-123, got %s", result[0].AppName)
	}
	if result[1].HostPort != 8082 {
		t.Errorf("expected host_port 8082, got %d", result[1].HostPort)
	}
}

func TestSnapshot_EmptyWhenNoContainers(t *testing.T) {
	lister := &fakeContainerLister{containers: []ContainerSnapshot{}}
	snap := NewSnapshotter(lister)

	result, err := snap.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected empty slice, got nil — Snapshot must never return nil for the slice")
	}
	if len(result) != 0 {
		t.Errorf("expected empty list, got %d entries", len(result))
	}
}

func TestSnapshot_NilSliceFromListerNormalizedToEmpty(t *testing.T) {
	lister := &fakeContainerLister{containers: nil}
	snap := NewSnapshotter(lister)

	result, err := snap.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil empty slice when lister returns nil")
	}
	if len(result) != 0 {
		t.Errorf("expected 0 entries, got %d", len(result))
	}
}
