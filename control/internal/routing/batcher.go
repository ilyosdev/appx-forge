package routing

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// RouteChange represents a pending route add or remove operation.
type RouteChange struct {
	Action  string // "add" or "remove"
	Route   Route  // populated for "add" actions
	AppName string // populated for "remove" actions (Route.AppName used for "add")
}

// Flusher applies batched route changes to the proxy.
type Flusher interface {
	Apply(ctx context.Context, adds []Route, removes []string) error
}

const (
	defaultDebounce = 500 * time.Millisecond
	defaultMaxBuf   = 50
)

// Batcher collects route changes and flushes them after a debounce window
// or when the buffer reaches maxBuf, whichever comes first.
type Batcher struct {
	flusher  Flusher
	debounce time.Duration
	maxBuf   int
	logger   *slog.Logger

	mu      sync.Mutex
	pending map[string]RouteChange // keyed by app name, last write wins
	timer   *time.Timer
	stopped bool
}

// NewBatcher creates a Batcher with default 500ms debounce and 50-item buffer.
func NewBatcher(f Flusher, logger *slog.Logger) *Batcher {
	return &Batcher{
		flusher:  f,
		debounce: defaultDebounce,
		maxBuf:   defaultMaxBuf,
		logger:   logger,
		pending:  make(map[string]RouteChange),
	}
}

// NewBatcherWithDebounce creates a Batcher with a custom debounce duration (for tests).
func NewBatcherWithDebounce(f Flusher, debounce time.Duration, logger *slog.Logger) *Batcher {
	return &Batcher{
		flusher:  f,
		debounce: debounce,
		maxBuf:   defaultMaxBuf,
		logger:   logger,
		pending:  make(map[string]RouteChange),
	}
}

// Enqueue adds a route change to the pending buffer. If the buffer reaches
// maxBuf, it flushes immediately. Otherwise it resets the debounce timer.
func (b *Batcher) Enqueue(change RouteChange) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.stopped {
		return
	}

	// Determine the key for dedup: last write wins per app name.
	key := change.AppName
	if change.Action == "add" {
		key = change.Route.AppName
	}
	b.pending[key] = change

	// Buffer overflow: flush immediately.
	if len(b.pending) >= b.maxBuf {
		b.stopTimerLocked()
		b.flushLocked()
		return
	}

	// Reset or start debounce timer.
	if b.timer != nil {
		b.timer.Reset(b.debounce)
	} else {
		b.timer = time.AfterFunc(b.debounce, func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if !b.stopped {
				b.flushLocked()
			}
		})
	}
}

// Stop drains any pending changes and prevents further enqueues.
func (b *Batcher) Stop() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.stopped {
		return
	}
	b.stopped = true
	b.stopTimerLocked()

	if len(b.pending) > 0 {
		b.flushLocked()
	}
}

// flushLocked sends all pending changes to the Flusher. Must be called with mu held.
func (b *Batcher) flushLocked() {
	if len(b.pending) == 0 {
		return
	}

	// Collect adds and removes from pending map.
	adds := make([]Route, 0)
	removes := make([]string, 0)

	for _, change := range b.pending {
		switch change.Action {
		case "add":
			adds = append(adds, change.Route)
		case "remove":
			removes = append(removes, change.AppName)
		}
	}

	// Clear pending before calling Apply (outside lock scope below).
	b.pending = make(map[string]RouteChange)
	b.timer = nil

	// Release lock during Apply to avoid holding it during I/O.
	b.mu.Unlock()
	err := b.flusher.Apply(context.Background(), adds, removes)
	b.mu.Lock()

	if err != nil {
		b.logger.Error("batcher flush failed", "error", err, "adds", len(adds), "removes", len(removes))
	}
}

// stopTimerLocked stops the debounce timer if active. Must be called with mu held.
func (b *Batcher) stopTimerLocked() {
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
}
