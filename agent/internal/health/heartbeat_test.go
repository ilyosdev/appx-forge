package health

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/appx/forge/agent/internal/controlclient"
)

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

	sender := NewHeartbeatSender(client, collector, 20*time.Millisecond, newTestLogger())

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

	sender := NewHeartbeatSender(client, collector, 15*time.Millisecond, newTestLogger())

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

	sender := NewHeartbeatSender(client, collector, 15*time.Millisecond, newTestLogger())

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

	sender := NewHeartbeatSender(client, collector, 10*time.Millisecond, newTestLogger())

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

	sender := NewHeartbeatSender(client, collector, 15*time.Millisecond, newTestLogger())

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
