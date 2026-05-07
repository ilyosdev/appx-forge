package routing

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// ── Mock Flusher ────────────────────────────────────────────────────

type mockFlusher struct {
	mu       sync.Mutex
	calls    []applyCall
	err      error
	waitCh   chan struct{} // signals when Apply is called
}

type applyCall struct {
	Adds    []Route
	Removes []string
}

func newMockFlusher() *mockFlusher {
	return &mockFlusher{
		waitCh: make(chan struct{}, 100),
	}
}

func (m *mockFlusher) Apply(_ context.Context, adds []Route, removes []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, applyCall{Adds: adds, Removes: removes})
	select {
	case m.waitCh <- struct{}{}:
	default:
	}
	return m.err
}

func (m *mockFlusher) setErr(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

func (m *mockFlusher) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *mockFlusher) lastCall() applyCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		return applyCall{}
	}
	return m.calls[len(m.calls)-1]
}

func (m *mockFlusher) allCalls() []applyCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]applyCall, len(m.calls))
	copy(out, m.calls)
	return out
}

// waitForCall blocks until Apply is called or timeout elapses.
func (m *mockFlusher) waitForCall(t *testing.T, timeout time.Duration) {
	t.Helper()
	select {
	case <-m.waitCh:
	case <-time.After(timeout):
		t.Fatal("timed out waiting for Apply call")
	}
}

// ── Batcher Tests ───────────────────────────────────────────────────

func TestBatcherSingleAdd_FlushedAfterDebounce(t *testing.T) {
	f := newMockFlusher()
	logger := slog.Default()
	b := NewBatcherWithDebounce(f, 50*time.Millisecond, logger)
	defer b.Stop()

	b.Enqueue(RouteChange{
		Action: "add",
		Route:  Route{AppName: "app1", SandboxID: "sbx-1", Upstream: "100.64.0.1:8081"},
	})

	f.waitForCall(t, 200*time.Millisecond)

	if f.callCount() != 1 {
		t.Fatalf("call count = %d, want 1", f.callCount())
	}
	call := f.lastCall()
	if len(call.Adds) != 1 || call.Adds[0].AppName != "app1" {
		t.Errorf("adds = %v, want [app1]", call.Adds)
	}
	if len(call.Removes) != 0 {
		t.Errorf("removes = %v, want []", call.Removes)
	}
}

func TestBatcherMultipleAdds_FlushedInSingleCall(t *testing.T) {
	f := newMockFlusher()
	logger := slog.Default()
	b := NewBatcherWithDebounce(f, 50*time.Millisecond, logger)
	defer b.Stop()

	b.Enqueue(RouteChange{
		Action: "add",
		Route:  Route{AppName: "app1", SandboxID: "sbx-1", Upstream: "100.64.0.1:8081"},
	})
	b.Enqueue(RouteChange{
		Action: "add",
		Route:  Route{AppName: "app2", SandboxID: "sbx-2", Upstream: "100.64.0.2:8081"},
	})
	b.Enqueue(RouteChange{
		Action: "remove",
		AppName: "app3",
	})

	f.waitForCall(t, 200*time.Millisecond)

	if f.callCount() != 1 {
		t.Fatalf("call count = %d, want 1 (batch)", f.callCount())
	}
	call := f.lastCall()
	if len(call.Adds) != 2 {
		t.Errorf("adds count = %d, want 2", len(call.Adds))
	}
	if len(call.Removes) != 1 || call.Removes[0] != "app3" {
		t.Errorf("removes = %v, want [app3]", call.Removes)
	}
}

func TestBatcherAddThenRemoveSameApp_Noop(t *testing.T) {
	f := newMockFlusher()
	logger := slog.Default()
	b := NewBatcherWithDebounce(f, 50*time.Millisecond, logger)
	defer b.Stop()

	// Add then remove same app within debounce window
	b.Enqueue(RouteChange{
		Action: "add",
		Route:  Route{AppName: "app1", SandboxID: "sbx-1", Upstream: "100.64.0.1:8081"},
	})
	b.Enqueue(RouteChange{
		Action:  "remove",
		AppName: "app1",
	})

	f.waitForCall(t, 200*time.Millisecond)

	call := f.lastCall()
	// Net result: remove wins (last write). The pending map overwrites add with remove.
	if len(call.Adds) != 0 {
		t.Errorf("adds = %v, want [] (add cancelled by remove)", call.Adds)
	}
	if len(call.Removes) != 1 || call.Removes[0] != "app1" {
		t.Errorf("removes = %v, want [app1]", call.Removes)
	}
}

func TestBatcherBufferOverflow_ImmediateFlush(t *testing.T) {
	f := newMockFlusher()
	logger := slog.Default()
	// Long debounce to prove buffer overflow triggers flush, not timer
	b := NewBatcherWithDebounce(f, 10*time.Second, logger)
	defer b.Stop()

	// Enqueue 50 distinct adds to hit maxBuf
	for i := 0; i < 50; i++ {
		b.Enqueue(RouteChange{
			Action: "add",
			Route:  Route{AppName: fmt.Sprintf("app-%d", i), SandboxID: fmt.Sprintf("sbx-%d", i), Upstream: fmt.Sprintf("100.64.0.%d:8081", i)},
		})
	}

	// Should flush immediately -- not wait for 10s debounce
	f.waitForCall(t, 500*time.Millisecond)

	if f.callCount() < 1 {
		t.Fatal("expected at least 1 flush from buffer overflow")
	}
	call := f.lastCall()
	if len(call.Adds) != 50 {
		t.Errorf("adds count = %d, want 50", len(call.Adds))
	}
}

func TestBatcherApplyError_DoesNotPanicOrBlock(t *testing.T) {
	f := newMockFlusher()
	f.setErr(fmt.Errorf("simulated apply failure"))
	logger := slog.Default()
	b := NewBatcherWithDebounce(f, 50*time.Millisecond, logger)
	defer b.Stop()

	b.Enqueue(RouteChange{
		Action: "add",
		Route:  Route{AppName: "fail-app", SandboxID: "sbx-1", Upstream: "100.64.0.1:8081"},
	})

	f.waitForCall(t, 200*time.Millisecond)

	// Should not panic. Now enqueue another -- it should still work.
	f.setErr(nil)
	b.Enqueue(RouteChange{
		Action: "add",
		Route:  Route{AppName: "ok-app", SandboxID: "sbx-2", Upstream: "100.64.0.2:8081"},
	})

	f.waitForCall(t, 200*time.Millisecond)

	if f.callCount() != 2 {
		t.Errorf("call count = %d, want 2 (error + success)", f.callCount())
	}
}

func TestBatcherStop_DrainsPendingChanges(t *testing.T) {
	f := newMockFlusher()
	logger := slog.Default()
	// Long debounce so timer doesn't fire before Stop()
	b := NewBatcherWithDebounce(f, 10*time.Second, logger)

	b.Enqueue(RouteChange{
		Action: "add",
		Route:  Route{AppName: "drain-app", SandboxID: "sbx-1", Upstream: "100.64.0.1:8081"},
	})

	// Stop should drain pending changes
	b.Stop()

	// Give a moment for the drain flush
	time.Sleep(20 * time.Millisecond)

	if f.callCount() != 1 {
		t.Fatalf("call count = %d, want 1 (drained on stop)", f.callCount())
	}
	call := f.lastCall()
	if len(call.Adds) != 1 || call.Adds[0].AppName != "drain-app" {
		t.Errorf("drained adds = %v, want [drain-app]", call.Adds)
	}
}
