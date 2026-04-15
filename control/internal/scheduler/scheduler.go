// Package scheduler implements bin-packing node selection for sandbox placement.
// It selects the healthy node with the most free RAM that can fit the requested
// sandbox resources, excluding draining, unhealthy, and removed nodes.
package scheduler

import (
	"errors"

	"github.com/google/uuid"
)

// Sentinel errors for scheduling failures.
var (
	// ErrNoNodes is returned when the candidate list is empty.
	ErrNoNodes = errors.New("scheduler: no nodes available")

	// ErrNoCapacity is returned when no healthy node has enough free RAM.
	ErrNoCapacity = errors.New("scheduler: no node has sufficient capacity")
)

// NodeCandidate holds the fields needed for scheduling decisions.
// Callers convert store.Node to NodeCandidate before calling Schedule.
type NodeCandidate struct {
	ID         uuid.UUID
	CapacityMB int32
	UsedMB     int32
	Status     string
}

// Schedule selects the best node for a sandbox requiring requiredMB of RAM.
// It returns the UUID of the selected node, or an error.
func Schedule(candidates []NodeCandidate, requiredMB int32) (uuid.UUID, error) {
	// TODO: implement
	return uuid.Nil, nil
}
