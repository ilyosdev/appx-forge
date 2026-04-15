package docker

import (
	"context"
	"fmt"
)

// CreateContainer creates and starts a sandbox container with the given spec.
// It prepares bind mounts, sets security options, and returns the container ID.
//
// Full implementation with security hardening will be added in Plan 02-02 Task 2.
func (d *dockerClient) CreateContainer(ctx context.Context, spec *SandboxSpec) (string, error) {
	if spec == nil {
		return "", fmt.Errorf("docker: sandbox spec is nil")
	}
	// Stub: will be fully implemented in Task 2 with security settings,
	// port binding, resource limits, bind mounts, and capability dropping.
	return "", fmt.Errorf("docker: CreateContainer not yet implemented")
}
