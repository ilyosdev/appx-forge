// Package events provides Docker container event watching with
// reconnection and exponential backoff. The Watcher filters events
// to forge-managed containers and maps Docker actions to sandbox events.
package events

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/appx/forge/agent/internal/docker"
)

// containerPrefix is the naming convention for forge-managed containers.
// Only containers with this prefix are tracked.
const containerPrefix = "forge-"

// EventSource provides access to the Docker events stream.
// This interface enables mock injection for testing.
type EventSource interface {
	EventsStream(ctx context.Context, since time.Time) (<-chan docker.ContainerEvent, <-chan error)
}

// SandboxEvent is a processed event emitted by the Watcher.
type SandboxEvent struct {
	AppName     string
	ContainerID string
	EventType   string // "container_started", "container_exited", "container_oom"
	ExitCode    string
	Time        time.Time
	OOMKilled   bool
}

// Watcher watches Docker events and emits SandboxEvents for forge-managed
// containers. It reconnects with the last event timestamp on stream errors,
// using exponential backoff to prevent tight reconnection loops.
type Watcher struct {
	source      EventSource
	logger      *slog.Logger
	baseBackoff time.Duration
	maxBackoff  time.Duration
}

// NewWatcher creates a Watcher that filters Docker events to forge-managed
// containers and maps them to SandboxEvents.
func NewWatcher(source EventSource, logger *slog.Logger) *Watcher {
	return &Watcher{
		source:      source,
		logger:      logger,
		baseBackoff: 1 * time.Second,
		maxBackoff:  30 * time.Second,
	}
}

// Watch starts watching Docker events and returns a channel of SandboxEvents.
// The watcher reconnects automatically on stream errors, passing the last
// event timestamp as Since to avoid missing events. The returned channel is
// closed when ctx is cancelled.
func (w *Watcher) Watch(ctx context.Context) <-chan SandboxEvent {
	eventCh := make(chan SandboxEvent, 16)

	go w.watchLoop(ctx, eventCh)

	return eventCh
}

// watchLoop is the main event loop. It connects to the Docker events stream,
// processes events, and reconnects with exponential backoff on errors.
func (w *Watcher) watchLoop(ctx context.Context, out chan<- SandboxEvent) {
	defer close(out)

	var lastEventTime time.Time
	backoff := w.baseBackoff

	for {
		if ctx.Err() != nil {
			return
		}

		eventCh, errCh := w.source.EventsStream(ctx, lastEventTime)

		// Process events from the stream
		streamDone := w.processStream(ctx, eventCh, errCh, out, &lastEventTime)

		if ctx.Err() != nil {
			return
		}

		if !streamDone {
			// Stream closed cleanly without error -- no reconnect needed,
			// the source simply has no more events.
			return
		}

		// Stream ended with error -- reconnect with backoff
		w.logger.Warn("docker events stream error, reconnecting",
			"since", lastEventTime,
			"backoff", backoff,
		)

		select {
		case <-time.After(backoff):
			// Increase backoff for next attempt
			backoff = backoff * 2
			if backoff > w.maxBackoff {
				backoff = w.maxBackoff
			}
		case <-ctx.Done():
			return
		}
	}
}

// processStream reads events and errors from the Docker events stream.
// It returns true if the stream ended due to an error (reconnect needed),
// or false if the stream closed cleanly.
func (w *Watcher) processStream(
	ctx context.Context,
	eventCh <-chan docker.ContainerEvent,
	errCh <-chan error,
	out chan<- SandboxEvent,
	lastEventTime *time.Time,
) bool {
	hadError := false

	// Read all events first
	for ev := range eventCh {
		if ctx.Err() != nil {
			return false
		}

		sbEvent, ok := w.mapEvent(ev)
		if !ok {
			continue
		}

		// Track the latest event timestamp for reconnection
		*lastEventTime = ev.Time

		select {
		case out <- sbEvent:
		case <-ctx.Done():
			return false
		}
	}

	// Then drain errors
	for err := range errCh {
		if ctx.Err() != nil {
			return false
		}
		w.logger.Warn("docker events stream error", "error", err)
		hadError = true
	}

	return hadError
}

// mapEvent converts a Docker ContainerEvent to a SandboxEvent.
// Returns false if the event should be ignored (non-forge container or
// unknown action).
func (w *Watcher) mapEvent(ev docker.ContainerEvent) (SandboxEvent, bool) {
	// Filter: only forge-managed containers
	if !strings.HasPrefix(ev.ContainerName, containerPrefix) {
		return SandboxEvent{}, false
	}

	appName := strings.TrimPrefix(ev.ContainerName, containerPrefix)

	var eventType string
	var oomKilled bool

	switch ev.Action {
	case "die":
		eventType = "container_exited"
	case "oom":
		eventType = "container_oom"
		oomKilled = true
	case "start":
		eventType = "container_started"
	default:
		// Unknown action -- ignore
		return SandboxEvent{}, false
	}

	return SandboxEvent{
		AppName:     appName,
		ContainerID: ev.ContainerID,
		EventType:   eventType,
		ExitCode:    ev.ExitCode,
		Time:        ev.Time,
		OOMKilled:   oomKilled,
	}, true
}
