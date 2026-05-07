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
// It filters to healthy nodes with sufficient free RAM, then picks the one
// with the most free RAM (best-fit descending / bin-packing heuristic).
// On ties, the candidate appearing first in the input slice wins (stable).
//
// Returns the UUID of the selected node, or:
//   - ErrNoNodes if candidates is empty
//   - ErrNoCapacity if no healthy node has enough free RAM
func Schedule(candidates []NodeCandidate, requiredMB int32) (uuid.UUID, error) {
	if len(candidates) == 0 {
		return uuid.Nil, ErrNoNodes
	}

	var bestID uuid.UUID
	var bestFree int32 = -1

	for _, c := range candidates {
		if c.Status != "healthy" {
			continue
		}

		free := c.CapacityMB - c.UsedMB
		if free < requiredMB {
			continue
		}

		if free > bestFree {
			bestFree = free
			bestID = c.ID
		}
	}

	if bestFree < 0 {
		return uuid.Nil, ErrNoCapacity
	}

	return bestID, nil
}
