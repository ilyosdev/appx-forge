package ports_test

import (
	"sync"
	"testing"

	"github.com/appx/forge/agent/internal/ports"
)

func TestNewAllocator_AvailablePorts(t *testing.T) {
	a := ports.NewAllocator(40000, 40005)

	if got := a.Available(); got != 6 {
		t.Errorf("Available() = %d, want 6", got)
	}
	if got := a.InUse(); got != 0 {
		t.Errorf("InUse() = %d, want 0", got)
	}
}

func TestAllocate_ReturnsPortInRange(t *testing.T) {
	a := ports.NewAllocator(40000, 40005)

	port, err := a.Allocate()
	if err != nil {
		t.Fatalf("Allocate() error: %v", err)
	}

	if port < 40000 || port > 40005 {
		t.Errorf("Allocate() = %d, want port in range [40000, 40005]", port)
	}
}

func TestAllocate_ExhaustsRange(t *testing.T) {
	a := ports.NewAllocator(40000, 40005)

	allocated := make(map[int]bool)
	for i := 0; i < 6; i++ {
		port, err := a.Allocate()
		if err != nil {
			t.Fatalf("Allocate() #%d error: %v", i+1, err)
		}
		if allocated[port] {
			t.Fatalf("Allocate() returned duplicate port %d on call #%d", port, i+1)
		}
		allocated[port] = true
	}

	// 7th allocation should fail
	_, err := a.Allocate()
	if err == nil {
		t.Fatal("expected error on 7th Allocate() after exhausting range, got nil")
	}
}

func TestRelease_MakesPortAvailableAgain(t *testing.T) {
	a := ports.NewAllocator(40000, 40002)

	// Allocate all 3
	var ports3 []int
	for i := 0; i < 3; i++ {
		p, err := a.Allocate()
		if err != nil {
			t.Fatalf("Allocate() error: %v", err)
		}
		ports3 = append(ports3, p)
	}

	if a.Available() != 0 {
		t.Fatalf("Available() = %d, want 0 after exhausting range", a.Available())
	}

	// Release one
	if err := a.Release(ports3[0]); err != nil {
		t.Fatalf("Release(%d) error: %v", ports3[0], err)
	}

	if a.Available() != 1 {
		t.Errorf("Available() = %d, want 1 after releasing one port", a.Available())
	}

	// Should be able to allocate again
	port, err := a.Allocate()
	if err != nil {
		t.Fatalf("Allocate() after Release error: %v", err)
	}
	if port != ports3[0] {
		t.Logf("Re-allocated port %d (released %d) -- acceptable, just different order", port, ports3[0])
	}
}

func TestRelease_UnallocatedPort_ReturnsError(t *testing.T) {
	a := ports.NewAllocator(40000, 40005)

	err := a.Release(40002)
	if err == nil {
		t.Fatal("expected error when releasing unallocated port, got nil")
	}
}

func TestConcurrentAllocate_NoDuplicates(t *testing.T) {
	a := ports.NewAllocator(40000, 40009)

	var (
		mu        sync.Mutex
		wg        sync.WaitGroup
		allocated = make(map[int]bool)
		errors    []error
	)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			port, err := a.Allocate()
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errors = append(errors, err)
				return
			}
			if allocated[port] {
				t.Errorf("concurrent Allocate() returned duplicate port %d", port)
			}
			allocated[port] = true
		}()
	}

	wg.Wait()

	if len(errors) > 0 {
		t.Fatalf("got %d allocation errors from 10 goroutines (expected 0): %v", len(errors), errors)
	}
	if len(allocated) != 10 {
		t.Errorf("allocated %d unique ports, want 10", len(allocated))
	}
}

func TestAllocateSpecific_ReservesExactPort(t *testing.T) {
	a := ports.NewAllocator(40000, 40005)

	err := a.AllocateSpecific(40002)
	if err != nil {
		t.Fatalf("AllocateSpecific(40002) error: %v", err)
	}

	if a.InUse() != 1 {
		t.Errorf("InUse() = %d, want 1", a.InUse())
	}
	if a.Available() != 5 {
		t.Errorf("Available() = %d, want 5", a.Available())
	}
}

func TestAllocateSpecific_AlreadyAllocated_ReturnsError(t *testing.T) {
	a := ports.NewAllocator(40000, 40005)

	if err := a.AllocateSpecific(40002); err != nil {
		t.Fatalf("first AllocateSpecific(40002) error: %v", err)
	}

	err := a.AllocateSpecific(40002)
	if err == nil {
		t.Fatal("expected error when allocating already-allocated port, got nil")
	}
}

func TestAllocateSpecific_OutOfRange_ReturnsError(t *testing.T) {
	a := ports.NewAllocator(40000, 40005)

	err := a.AllocateSpecific(50000)
	if err == nil {
		t.Fatal("expected error when allocating port outside range, got nil")
	}
}

func TestInUse_ReturnsCorrectCount(t *testing.T) {
	a := ports.NewAllocator(40000, 40009)

	for i := 0; i < 5; i++ {
		if _, err := a.Allocate(); err != nil {
			t.Fatalf("Allocate() error: %v", err)
		}
	}

	if got := a.InUse(); got != 5 {
		t.Errorf("InUse() = %d, want 5", got)
	}
}

func TestAvailable_ReturnsCorrectCount(t *testing.T) {
	a := ports.NewAllocator(40000, 40009)

	for i := 0; i < 3; i++ {
		if _, err := a.Allocate(); err != nil {
			t.Fatalf("Allocate() error: %v", err)
		}
	}

	if got := a.Available(); got != 7 {
		t.Errorf("Available() = %d, want 7", got)
	}
}
