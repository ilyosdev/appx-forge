package ports

import (
	"fmt"
	"sync"
)

// Allocator manages a pool of host ports for sandbox containers.
// It is goroutine-safe via an internal mutex.
//
// Port range is inclusive on both ends: [min, max].
type Allocator struct {
	mu        sync.Mutex
	available map[int]bool
	allocated map[int]bool
	min       int
	max       int
}

// NewAllocator creates a port allocator for the range [min, max] inclusive.
// All ports start as available.
func NewAllocator(min, max int) *Allocator {
	available := make(map[int]bool, max-min+1)
	for p := min; p <= max; p++ {
		available[p] = true
	}

	return &Allocator{
		available: available,
		allocated: make(map[int]bool),
		min:       min,
		max:       max,
	}
}

// Allocate picks an available port, marks it as allocated, and returns it.
// Returns an error if no ports are available.
func (a *Allocator) Allocate() (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for port := range a.available {
		delete(a.available, port)
		a.allocated[port] = true
		return port, nil
	}

	return 0, fmt.Errorf("port allocator: no ports available in range [%d, %d]", a.min, a.max)
}

// AllocateSpecific reserves a specific port if it is available.
// Returns an error if the port is out of range or already allocated.
func (a *Allocator) AllocateSpecific(port int) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if port < a.min || port > a.max {
		return fmt.Errorf("port allocator: port %d is outside range [%d, %d]", port, a.min, a.max)
	}

	if a.allocated[port] {
		return fmt.Errorf("port allocator: port %d is already allocated", port)
	}

	if !a.available[port] {
		return fmt.Errorf("port allocator: port %d is not available", port)
	}

	delete(a.available, port)
	a.allocated[port] = true
	return nil
}

// Release returns an allocated port back to the available pool.
// Returns an error if the port was not previously allocated.
func (a *Allocator) Release(port int) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.allocated[port] {
		return fmt.Errorf("port allocator: port %d is not currently allocated", port)
	}

	delete(a.allocated, port)
	a.available[port] = true
	return nil
}

// InUse returns the number of currently allocated ports.
func (a *Allocator) InUse() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.allocated)
}

// Available returns the number of ports available for allocation.
func (a *Allocator) Available() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.available)
}
