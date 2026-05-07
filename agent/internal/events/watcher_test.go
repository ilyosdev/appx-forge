package events

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/appx/forge/agent/internal/docker"
)

// ── Mock EventSource ────────────────────────────────────────────────────

// mockEventSource records calls to EventsStream and returns pre-configured
// channels on each call. This enables testing reconnection behavior.
type mockEventSource struct {
	mu    sync.Mutex
	calls []time.Time // `since` arg for each EventsStream call

	// scenarios is consumed in order: each EventsStream call pops the first entry.
	scenarios []scenario
}

type scenario struct {
	events []docker.ContainerEvent
	errs   []error
}

func (m *mockEventSource) EventsStream(ctx context.Context, since time.Time) (<-chan docker.ContainerEvent, <-chan error) {
	m.mu.Lock()
	m.calls = append(m.calls, since)

	eventCh := make(chan docker.ContainerEvent, 10)
	errCh := make(chan error, 1)

	var sc scenario
	if len(m.scenarios) > 0 {
		sc = m.scenarios[0]
		m.scenarios = m.scenarios[1:]
	}
	m.mu.Unlock()

	go func() {
		defer close(eventCh)
		defer close(errCh)

		for _, ev := range sc.events {
			select {
			case eventCh <- ev:
			case <-ctx.Done():
				return
			}
		}
		for _, err := range sc.errs {
			select {
			case errCh <- err:
			case <-ctx.Done():
				return
			}
		}
	}()

	return eventCh, errCh
}

func (m *mockEventSource) getCalls() []time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]time.Time, len(m.calls))
	copy(result, m.calls)
	return result
}

// ── Test Helpers ────────────────────────────────────────────────────────

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError}))
}

type discardWriter struct{}

func (d *discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func collectEvents(ch <-chan SandboxEvent, timeout time.Duration) []SandboxEvent {
	var events []SandboxEvent
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return events
			}
			events = append(events, ev)
		case <-timer.C:
			return events
		}
	}
}

// ── Tests ───────────────────────────────────────────────────────────────

func TestWatcher_DieEvent(t *testing.T) {
	eventTime := time.Now()
	source := &mockEventSource{
		scenarios: []scenario{
			{
				events: []docker.ContainerEvent{
					{
						ContainerID:   "abc123",
						ContainerName: "forge-myapp",
						Action:        "die",
						ExitCode:      "1",
						Time:          eventTime,
					},
				},
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := NewWatcher(source, testLogger())
	ch := w.Watch(ctx)

	events := collectEvents(ch, 2*time.Second)
	cancel()

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.EventType != "container_exited" {
		t.Errorf("expected EventType=container_exited, got %s", ev.EventType)
	}
	if ev.AppName != "myapp" {
		t.Errorf("expected AppName=myapp, got %s", ev.AppName)
	}
	if ev.ExitCode != "1" {
		t.Errorf("expected ExitCode=1, got %s", ev.ExitCode)
	}
	if ev.OOMKilled {
		t.Error("expected OOMKilled=false")
	}
}

func TestWatcher_OOMEvent(t *testing.T) {
	eventTime := time.Now()
	source := &mockEventSource{
		scenarios: []scenario{
			{
				events: []docker.ContainerEvent{
					{
						ContainerID:   "def456",
						ContainerName: "forge-oomapp",
						Action:        "oom",
						ExitCode:      "137",
						Time:          eventTime,
					},
				},
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := NewWatcher(source, testLogger())
	ch := w.Watch(ctx)

	events := collectEvents(ch, 2*time.Second)
	cancel()

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.EventType != "container_oom" {
		t.Errorf("expected EventType=container_oom, got %s", ev.EventType)
	}
	if ev.AppName != "oomapp" {
		t.Errorf("expected AppName=oomapp, got %s", ev.AppName)
	}
	if !ev.OOMKilled {
		t.Error("expected OOMKilled=true")
	}
}

func TestWatcher_IgnoresNonForgeContainers(t *testing.T) {
	eventTime := time.Now()
	source := &mockEventSource{
		scenarios: []scenario{
			{
				events: []docker.ContainerEvent{
					{
						ContainerID:   "other123",
						ContainerName: "redis-cache",
						Action:        "die",
						ExitCode:      "0",
						Time:          eventTime,
					},
					{
						ContainerID:   "forge123",
						ContainerName: "forge-kept",
						Action:        "die",
						ExitCode:      "0",
						Time:          eventTime.Add(time.Second),
					},
				},
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := NewWatcher(source, testLogger())
	ch := w.Watch(ctx)

	events := collectEvents(ch, 2*time.Second)
	cancel()

	if len(events) != 1 {
		t.Fatalf("expected 1 event (non-forge filtered), got %d", len(events))
	}
	if events[0].AppName != "kept" {
		t.Errorf("expected AppName=kept, got %s", events[0].AppName)
	}
}

func TestWatcher_ExtractsAppNameFromContainerName(t *testing.T) {
	tests := []struct {
		containerName string
		wantAppName   string
	}{
		{"forge-myapp", "myapp"},
		{"forge-test-app-123", "test-app-123"},
		{"forge-a", "a"},
	}

	for _, tt := range tests {
		t.Run(tt.containerName, func(t *testing.T) {
			source := &mockEventSource{
				scenarios: []scenario{
					{
						events: []docker.ContainerEvent{
							{
								ContainerID:   "id1",
								ContainerName: tt.containerName,
								Action:        "die",
								ExitCode:      "0",
								Time:          time.Now(),
							},
						},
					},
				},
			}

			ctx, cancel := context.WithCancel(context.Background())
			w := NewWatcher(source, testLogger())
			ch := w.Watch(ctx)

			events := collectEvents(ch, 2*time.Second)
			cancel()

			if len(events) != 1 {
				t.Fatalf("expected 1 event, got %d", len(events))
			}
			if events[0].AppName != tt.wantAppName {
				t.Errorf("expected AppName=%s, got %s", tt.wantAppName, events[0].AppName)
			}
		})
	}
}

func TestWatcher_ReconnectsWithSinceTimestamp(t *testing.T) {
	firstEventTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	source := &mockEventSource{
		scenarios: []scenario{
			// First connection: one event then error
			{
				events: []docker.ContainerEvent{
					{
						ContainerID:   "abc",
						ContainerName: "forge-app1",
						Action:        "die",
						ExitCode:      "1",
						Time:          firstEventTime,
					},
				},
				errs: []error{
					context.DeadlineExceeded,
				},
			},
			// Second connection (reconnect): one event, no error, then done
			{
				events: []docker.ContainerEvent{
					{
						ContainerID:   "def",
						ContainerName: "forge-app2",
						Action:        "start",
						ExitCode:      "",
						Time:          firstEventTime.Add(5 * time.Second),
					},
				},
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := NewWatcher(source, testLogger())
	// Override backoff for test speed
	w.baseBackoff = 10 * time.Millisecond
	w.maxBackoff = 50 * time.Millisecond
	ch := w.Watch(ctx)

	events := collectEvents(ch, 3*time.Second)

	if len(events) < 2 {
		t.Fatalf("expected at least 2 events (across reconnect), got %d", len(events))
	}

	// Verify second call used the first event's timestamp as Since
	calls := source.getCalls()
	if len(calls) < 2 {
		t.Fatalf("expected at least 2 EventsStream calls, got %d", len(calls))
	}

	// First call should use zero time (initial connection)
	if !calls[0].IsZero() {
		t.Errorf("first call Since should be zero, got %v", calls[0])
	}

	// Second call should use the first event's timestamp
	if !calls[1].Equal(firstEventTime) {
		t.Errorf("second call Since should be %v, got %v", firstEventTime, calls[1])
	}
}

func TestWatcher_ExponentialBackoff(t *testing.T) {
	// All scenarios return errors immediately to trigger rapid reconnection
	source := &mockEventSource{
		scenarios: []scenario{
			{errs: []error{context.DeadlineExceeded}},
			{errs: []error{context.DeadlineExceeded}},
			{errs: []error{context.DeadlineExceeded}},
			{errs: []error{context.DeadlineExceeded}},
			// Last scenario: no error, just close cleanly
			{},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := NewWatcher(source, testLogger())
	// Use small base for test speed, but verifiable ratios
	w.baseBackoff = 20 * time.Millisecond
	w.maxBackoff = 200 * time.Millisecond

	ch := w.Watch(ctx)

	// Wait for all reconnections to complete
	collectEvents(ch, 2*time.Second)

	calls := source.getCalls()
	if len(calls) < 4 {
		t.Fatalf("expected at least 4 EventsStream calls, got %d", len(calls))
	}

	// Verify increasing gaps between calls
	for i := 1; i < len(calls)-1; i++ {
		gap := calls[i+1].Sub(calls[i])
		prevGap := time.Duration(0)
		if i > 1 {
			prevGap = calls[i].Sub(calls[i-1])
		}
		// Each gap should be >= previous gap (exponential increase)
		// Allow 5ms tolerance for scheduling jitter
		if i > 1 && gap < prevGap-5*time.Millisecond {
			t.Errorf("gap %d (%v) should be >= gap %d (%v)", i, gap, i-1, prevGap)
		}
	}
}

func TestWatcher_StopsOnContextCancellation(t *testing.T) {
	// Scenario that would keep sending events if not cancelled
	source := &mockEventSource{
		scenarios: []scenario{
			{
				events: []docker.ContainerEvent{
					{
						ContainerID:   "id1",
						ContainerName: "forge-app",
						Action:        "start",
						ExitCode:      "",
						Time:          time.Now(),
					},
				},
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := NewWatcher(source, testLogger())
	ch := w.Watch(ctx)

	// Read one event
	select {
	case <-ch:
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for event")
	}

	// Cancel context
	cancel()

	// Channel should close
	select {
	case _, ok := <-ch:
		if ok {
			// Might get one more event before close, that's fine
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel not closed after context cancel")
	}
}

func TestWatcher_StartEvent(t *testing.T) {
	eventTime := time.Now()
	source := &mockEventSource{
		scenarios: []scenario{
			{
				events: []docker.ContainerEvent{
					{
						ContainerID:   "start123",
						ContainerName: "forge-startapp",
						Action:        "start",
						ExitCode:      "",
						Time:          eventTime,
					},
				},
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := NewWatcher(source, testLogger())
	ch := w.Watch(ctx)

	events := collectEvents(ch, 2*time.Second)
	cancel()

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.EventType != "container_started" {
		t.Errorf("expected EventType=container_started, got %s", ev.EventType)
	}
	if ev.AppName != "startapp" {
		t.Errorf("expected AppName=startapp, got %s", ev.AppName)
	}
	if ev.ContainerID != "start123" {
		t.Errorf("expected ContainerID=start123, got %s", ev.ContainerID)
	}
}
