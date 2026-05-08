package health

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/appx/forge/agent/internal/controlclient"
	"github.com/appx/forge/agent/internal/docker"
)

// mockSnapshotter returns a fixed list of container snapshots (Phase 30 T4).
type mockSnapshotter struct {
	mu         sync.Mutex
	containers []docker.ContainerSnapshot
	err        error
}

func (m *mockSnapshotter) Snapshot(ctx context.Context) ([]docker.ContainerSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	out := make([]docker.ContainerSnapshot, len(m.containers))
	copy(out, m.containers)
	return out, nil
}

func (m *mockSnapshotter) setContainers(c []docker.ContainerSnapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.containers = c
}

// emptySnapshotter is a minimal SnapshotProvider for tests that don't care
// about the container-list payload.
type emptySnapshotter struct{}

func (emptySnapshotter) Snapshot(ctx context.Context) ([]docker.ContainerSnapshot, error) {
	return []docker.ContainerSnapshot{}, nil
}

// newTestLogger returns a no-op logger for tests.
func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// mockHeartbeatClient records calls to Heartbeat.
type mockHeartbeatClient struct {
	mu    sync.Mutex
	calls []controlclient.HeartbeatRequest
	err   error
}

func (m *mockHeartbeatClient) Heartbeat(ctx context.Context, req controlclient.HeartbeatRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, req)
	return m.err
}

func (m *mockHeartbeatClient) getCalls() []controlclient.HeartbeatRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]controlclient.HeartbeatRequest, len(m.calls))
	copy(result, m.calls)
	return result
}

// mockCollector returns fixed resource usage values.
type mockCollector struct {
	mu                sync.Mutex
	usedMB            int
	runningContainers int
	collectCalls      atomic.Int32
}

func (m *mockCollector) Collect() (usedMB int, runningContainers int) {
	m.collectCalls.Add(1)
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.usedMB, m.runningContainers
}

func (m *mockCollector) setValues(usedMB, runningContainers int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.usedMB = usedMB
	m.runningContainers = runningContainers
}

func TestHeartbeatSender_CallsAtInterval(t *testing.T) {
	client := &mockHeartbeatClient{}
	collector := &mockCollector{usedMB: 100, runningContainers: 1}

	sender := NewHeartbeatSender(client, collector, emptySnapshotter{}, 20*time.Millisecond, newTestLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go sender.Start(ctx)

	// Wait for at least 3 heartbeats (20ms interval -> 60ms minimum + buffer)
	time.Sleep(80 * time.Millisecond)
	cancel()

	// Give goroutine time to stop
	time.Sleep(10 * time.Millisecond)

	calls := client.getCalls()
	if len(calls) < 3 {
		t.Errorf("expected at least 3 heartbeat calls, got %d", len(calls))
	}
}

func TestHeartbeatSender_PassesCurrentResourceUsage(t *testing.T) {
	client := &mockHeartbeatClient{}
	collector := &mockCollector{usedMB: 8500, runningContainers: 12}

	sender := NewHeartbeatSender(client, collector, emptySnapshotter{}, 15*time.Millisecond, newTestLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go sender.Start(ctx)

	// Wait for at least 1 heartbeat
	time.Sleep(30 * time.Millisecond)

	// Change values
	collector.setValues(9000, 15)

	// Wait for another heartbeat
	time.Sleep(30 * time.Millisecond)
	cancel()
	time.Sleep(10 * time.Millisecond)

	calls := client.getCalls()
	if len(calls) < 2 {
		t.Fatalf("expected at least 2 heartbeat calls, got %d", len(calls))
	}

	// First call should have initial values
	if calls[0].UsedMB != 8500 {
		t.Errorf("first call used_mb: got %d, want %d", calls[0].UsedMB, 8500)
	}
	if calls[0].RunningContainers != 12 {
		t.Errorf("first call running_containers: got %d, want %d", calls[0].RunningContainers, 12)
	}

	// Later calls should have updated values
	last := calls[len(calls)-1]
	if last.UsedMB != 9000 {
		t.Errorf("last call used_mb: got %d, want %d", last.UsedMB, 9000)
	}
	if last.RunningContainers != 15 {
		t.Errorf("last call running_containers: got %d, want %d", last.RunningContainers, 15)
	}
}

func TestHeartbeatSender_ContinuesOnError(t *testing.T) {
	client := &mockHeartbeatClient{err: context.DeadlineExceeded}
	collector := &mockCollector{usedMB: 100, runningContainers: 1}

	sender := NewHeartbeatSender(client, collector, emptySnapshotter{}, 15*time.Millisecond, newTestLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go sender.Start(ctx)

	// Wait for several ticks -- should continue despite errors
	time.Sleep(60 * time.Millisecond)
	cancel()
	time.Sleep(10 * time.Millisecond)

	calls := client.getCalls()
	if len(calls) < 3 {
		t.Errorf("expected at least 3 calls despite errors, got %d", len(calls))
	}
}

func TestHeartbeatSender_StopsOnContextCancel(t *testing.T) {
	client := &mockHeartbeatClient{}
	collector := &mockCollector{usedMB: 100, runningContainers: 1}

	sender := NewHeartbeatSender(client, collector, emptySnapshotter{}, 10*time.Millisecond, newTestLogger())

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		sender.Start(ctx)
		close(done)
	}()

	// Let a few heartbeats fire
	time.Sleep(40 * time.Millisecond)
	cancel()

	// Start() should return promptly after cancel
	select {
	case <-done:
		// OK -- Start returned
	case <-time.After(200 * time.Millisecond):
		t.Error("Start did not return after context cancellation")
	}

	// Count calls after cancellation -- should not increase
	callsAfterCancel := len(client.getCalls())
	time.Sleep(30 * time.Millisecond)
	callsLater := len(client.getCalls())
	if callsLater > callsAfterCancel {
		t.Errorf("heartbeat continued after cancel: before=%d, after=%d", callsAfterCancel, callsLater)
	}
}

func TestHeartbeatSender_CallsResourceCollector(t *testing.T) {
	client := &mockHeartbeatClient{}
	collector := &mockCollector{usedMB: 500, runningContainers: 3}

	sender := NewHeartbeatSender(client, collector, emptySnapshotter{}, 15*time.Millisecond, newTestLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go sender.Start(ctx)

	// Wait for several ticks
	time.Sleep(60 * time.Millisecond)
	cancel()
	time.Sleep(10 * time.Millisecond)

	collectCount := int(collector.collectCalls.Load())
	if collectCount < 3 {
		t.Errorf("expected Collect() called at least 3 times, got %d", collectCount)
	}

	heartbeatCalls := len(client.getCalls())
	if collectCount != heartbeatCalls {
		t.Errorf("Collect calls (%d) should equal Heartbeat calls (%d)", collectCount, heartbeatCalls)
	}
}

// Phase 30 T4 — heartbeat carries the full container list, not just a count.
func TestHeartbeatSender_IncludesContainerList(t *testing.T) {
	client := &mockHeartbeatClient{}
	collector := &mockCollector{usedMB: 1234, runningContainers: 2}
	snapshotter := &mockSnapshotter{
		containers: []docker.ContainerSnapshot{
			{AppName: "pool-X", State: "running", HostPort: 8081, ContainerID: "c1"},
			{AppName: "app-Y", State: "running", HostPort: 8082, ContainerID: "c2"},
		},
	}

	sender := NewHeartbeatSender(client, collector, snapshotter, 15*time.Millisecond, newTestLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go sender.Start(ctx)

	time.Sleep(40 * time.Millisecond)
	cancel()
	time.Sleep(10 * time.Millisecond)

	calls := client.getCalls()
	if len(calls) == 0 {
		t.Fatal("expected at least one heartbeat call")
	}

	first := calls[0]
	if len(first.Containers) != 2 {
		t.Fatalf("expected 2 containers in heartbeat payload, got %d", len(first.Containers))
	}
	if first.Containers[0].AppName != "pool-X" {
		t.Errorf("first container app_name: got %q, want pool-X", first.Containers[0].AppName)
	}
	if first.Containers[0].State != "running" {
		t.Errorf("first container state: got %q, want running", first.Containers[0].State)
	}
	if first.Containers[0].HostPort != 8081 {
		t.Errorf("first container host_port: got %d, want 8081", first.Containers[0].HostPort)
	}
	if first.Containers[0].ContainerID != "c1" {
		t.Errorf("first container container_id: got %q, want c1", first.Containers[0].ContainerID)
	}
	if first.Containers[1].AppName != "app-Y" {
		t.Errorf("second container app_name: got %q, want app-Y", first.Containers[1].AppName)
	}
	if first.UsedMB != 1234 {
		t.Errorf("used_mb: got %d, want 1234", first.UsedMB)
	}
	if first.RunningContainers != 2 {
		t.Errorf("running_containers (legacy): got %d, want 2", first.RunningContainers)
	}
}

// Phase 32 Wave 2 Bug 5 — sendHeartbeat must bound the snapshot call so a
// stalled Docker daemon (overloaded host, swap-thrashing, hung dockerd)
// cannot block heartbeats indefinitely. Without a per-tick deadline,
// snapshotter.Snapshot inherits the long-lived Start() context and blocks
// until Docker eventually responds — which on a degraded host can be
// minutes. The control plane's missed-heartbeat alarm then trips the node
// unhealthy, cascading into mass reschedule.
//
// Fix: each tick gets a 5s budget. If Snapshot doesn't return in time, the
// tick is skipped (relying on Bug 4's control-side debounce window) and a
// structured counter is incremented so operators can see "agent is alive
// but Docker is too slow to snapshot."
type slowSnapshotter struct {
	delay time.Duration
}

func (s *slowSnapshotter) Snapshot(ctx context.Context) ([]docker.ContainerSnapshot, error) {
	select {
	case <-time.After(s.delay):
		return []docker.ContainerSnapshot{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestHeartbeatSender_DockerSlowDoesNotHangPastTimeout(t *testing.T) {
	// Snapshot would take 30s; sendHeartbeat must return at the 5s mark.
	slow := &slowSnapshotter{delay: 30 * time.Second}
	client := &mockHeartbeatClient{}
	collector := &mockCollector{}
	sender := NewHeartbeatSender(client, collector, slow, time.Hour, newTestLogger())

	start := time.Now()
	sender.sendHeartbeat(context.Background())
	elapsed := time.Since(start)

	// 5s timeout + small scheduling jitter; allow up to 6.5s.
	if elapsed > 6500*time.Millisecond {
		t.Fatalf("sendHeartbeat blocked for %v; expected <6.5s with %v timeout",
			elapsed, heartbeatTickTimeout)
	}
	// Must also have actually waited ~5s (proves the bound is real, not
	// a cancel-immediately bug).
	if elapsed < 4500*time.Millisecond {
		t.Fatalf("sendHeartbeat returned in %v; expected ~%v wait before timeout",
			elapsed, heartbeatTickTimeout)
	}

	// Phase 33-Audit-6 — snapshot timeout sends a liveness-only heartbeat
	// (Containers field empty) so the control plane keeps the node healthy.
	// The skipped-snapshot counter still increments to surface "Docker slow"
	// to operators.
	calls := client.getCalls()
	if len(calls) != 1 {
		t.Errorf("expected exactly 1 liveness heartbeat on snapshot timeout, got %d", len(calls))
	} else if len(calls[0].Containers) != 0 {
		t.Errorf("liveness heartbeat must omit Containers; got %d entries", len(calls[0].Containers))
	}
	// Counter records the skipped-snapshot.
	if got := sender.SkippedTicks(); got != 1 {
		t.Errorf("expected SkippedTicks=1 after one timeout, got %d", got)
	}
}

func TestHeartbeatSender_FastSnapshotRecordsNoSkip(t *testing.T) {
	client := &mockHeartbeatClient{}
	collector := &mockCollector{usedMB: 1, runningContainers: 0}
	sender := NewHeartbeatSender(client, collector, emptySnapshotter{}, time.Hour, newTestLogger())

	sender.sendHeartbeat(context.Background())

	if got := len(client.getCalls()); got != 1 {
		t.Errorf("expected 1 heartbeat call on fast snapshot, got %d", got)
	}
	if got := sender.SkippedTicks(); got != 0 {
		t.Errorf("expected SkippedTicks=0 on fast path, got %d", got)
	}
}

func TestHeartbeatSender_ParentContextCancelStillReturns(t *testing.T) {
	// If the parent context is cancelled mid-snapshot, sendHeartbeat must
	// not wait the full 5s — it should propagate the cancel and return.
	slow := &slowSnapshotter{delay: 10 * time.Second}
	client := &mockHeartbeatClient{}
	collector := &mockCollector{}
	sender := NewHeartbeatSender(client, collector, slow, time.Hour, newTestLogger())

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	sender.sendHeartbeat(ctx)
	elapsed := time.Since(start)

	if elapsed > 1500*time.Millisecond {
		t.Fatalf("sendHeartbeat blocked for %v after parent cancel; expected fast return",
			elapsed)
	}
}

// Phase 33-Audit-6 — snapshot failure now sends a LIVENESS-ONLY heartbeat
// (Containers field omitted) instead of skipping the tick. The control-plane
// heartbeat handler updates last_seen_at unconditionally and only triggers
// the T7 reconciler when Containers is non-nil, so:
//
//   - liveness heartbeat keeps the node 'healthy' on the control side
//     (no missed-beat flap that breaks user provisioning)
//   - liveness heartbeat does NOT trigger reconcile against an empty list
//     (T7 mass-destroy protection still in place)
//
// Replaces the prior SKIPPING behavior that tripped the missed-heartbeat
// alarm under sustained Docker pressure (beta incident 2026-05-08).
func TestHeartbeatSender_SnapshotErrorSendsLivenessOnlyHeartbeat(t *testing.T) {
	client := &mockHeartbeatClient{}
	collector := &mockCollector{usedMB: 100, runningContainers: 0}
	snapshotter := &mockSnapshotter{err: errors.New("docker daemon unreachable")}

	sender := NewHeartbeatSender(client, collector, snapshotter, 15*time.Millisecond, newTestLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go sender.Start(ctx)

	// Wait several tick intervals; expect ≥1 liveness heartbeat sent.
	time.Sleep(60 * time.Millisecond)
	cancel()
	time.Sleep(10 * time.Millisecond)

	calls := client.getCalls()
	if len(calls) == 0 {
		t.Fatalf("expected at least one liveness heartbeat on snapshot failure; got 0 calls")
	}

	// Every call must be liveness-only — Containers field empty/nil — so
	// the control-plane reconciler does not fire on an unobserved snapshot.
	for i, call := range calls {
		if len(call.Containers) != 0 {
			t.Errorf("call[%d]: liveness heartbeat must omit Containers (got %d entries)",
				i, len(call.Containers))
		}
	}

	// Skipped-snapshot counter must increment (operators rely on this signal
	// to distinguish "agent alive, Docker slow" from "agent crashed").
	if got := sender.SkippedTicks(); got == 0 {
		t.Errorf("SkippedTicks should have incremented; got %d", got)
	}
}
