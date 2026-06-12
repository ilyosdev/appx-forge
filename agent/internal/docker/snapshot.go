// Phase 30 — Snapshot of all Forge-managed containers from Docker truth.
//
// The agent's heartbeat to the control plane carries the FULL container
// list (not just a count) so the control plane can reconcile its DB
// against agent reality on every receive. This file defines the snapshot
// shape + the wrapper that produces it. The concrete docker.dockerClient
// satisfies DockerLister via its ListContainers method (defined in
// client.go).
//
// Naming: ContainerSnapshot (not ContainerInfo) avoids the collision with
// the existing ContainerInfo struct in client.go, which represents a
// single inspected container's state and serves a different purpose.
package docker

import (
	"context"
)

// ContainerSnapshot is the agent-side representation of a single
// Forge-managed container, matching the heartbeat protocol shape that the
// control plane consumes.
//
// Phase 33-Real-9 — State is the canonical SandboxState vocabulary
// (`running | starting | restarting | stopped | failed`), translated from
// Docker primitives at the boundary in client.go via models.FromDockerState.
// Control plane trusts the field directly; no further translation needed.
type ContainerSnapshot struct {
	AppName     string `json:"app_name"`
	State       string `json:"state"`
	HostPort    int    `json:"host_port"`
	ContainerID string `json:"container_id"`
	// SandboxID is recovered from the forge.sandbox_id container label —
	// empty for containers created before the label existed (2026-06-12).
	// Lets a restarted agent rebuild its in-memory sandbox map from Docker
	// truth instead of 404ing every push/exec until the container cycles.
	SandboxID string `json:"sandbox_id,omitempty"`
}

// DockerLister is the interface Snapshotter needs from the Docker client.
// Defined here (consumer side) so tests can fake it without depending on
// the concrete dockerClient.
type DockerLister interface {
	ListContainers(ctx context.Context) ([]ContainerSnapshot, error)
}

// Snapshotter wraps a DockerLister and produces full container snapshots.
// Used by the heartbeat sender (per-tick) and on agent startup (rebuild
// in-memory cache from Docker truth).
type Snapshotter struct {
	client DockerLister
}

// NewSnapshotter constructs a Snapshotter. Callers pass either the
// concrete *dockerClient (production) or a fake (tests).
func NewSnapshotter(client DockerLister) *Snapshotter {
	return &Snapshotter{client: client}
}

// Snapshot returns the full list of Forge-managed containers visible to
// the Docker daemon. Returns ([], nil) — never (nil, nil) — when there
// are no containers, so callers can iterate without nil-checking the
// slice.
func (s *Snapshotter) Snapshot(ctx context.Context) ([]ContainerSnapshot, error) {
	containers, err := s.client.ListContainers(ctx)
	if err != nil {
		return nil, err
	}
	if containers == nil {
		return []ContainerSnapshot{}, nil
	}
	return containers, nil
}
