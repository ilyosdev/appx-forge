package docker

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// ── Mock Client for Image Pull ──────────────────────────────────────────

type mockImageClient struct {
	mu        sync.Mutex
	pullCalls []string // image refs passed to PullImage
	pullErrs  []error  // errors to return, consumed in order
}

func (m *mockImageClient) PullImage(ctx context.Context, imageRef string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pullCalls = append(m.pullCalls, imageRef)
	if len(m.pullErrs) > 0 {
		err := m.pullErrs[0]
		m.pullErrs = m.pullErrs[1:]
		return err
	}
	return nil
}

func (m *mockImageClient) getPullCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]string, len(m.pullCalls))
	copy(result, m.pullCalls)
	return result
}

// ── Tests ───────────────────────────────────────────────────────────────

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError}))
}

type discardWriter struct{}

func (d *discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestImagePuller_PullOnce_Success(t *testing.T) {
	mock := &mockImageClient{}
	puller := NewImagePuller(mock, "appx/sandbox:v1", testLogger())

	err := puller.PullOnce(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	calls := mock.getPullCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 PullImage call, got %d", len(calls))
	}
	if calls[0] != "appx/sandbox:v1" {
		t.Errorf("expected image=appx/sandbox:v1, got %s", calls[0])
	}
}

func TestImagePuller_PullOnce_RetriesOnFailure(t *testing.T) {
	mock := &mockImageClient{
		pullErrs: []error{
			errors.New("network timeout"),
			errors.New("network timeout"),
			nil, // third attempt succeeds
		},
	}
	puller := NewImagePuller(mock, "appx/sandbox:v1", testLogger())
	// Use fast backoff for tests
	puller.baseBackoff = 5 * time.Millisecond
	puller.maxRetries = 3

	err := puller.PullOnce(context.Background())
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}

	calls := mock.getPullCalls()
	if len(calls) != 3 {
		t.Fatalf("expected 3 PullImage calls (2 failures + 1 success), got %d", len(calls))
	}
}

func TestImagePuller_StartPeriodicPull(t *testing.T) {
	mock := &mockImageClient{}
	puller := NewImagePuller(mock, "appx/sandbox:v1", testLogger())
	puller.baseBackoff = 5 * time.Millisecond
	puller.maxRetries = 1

	ctx, cancel := context.WithCancel(context.Background())

	// Start periodic pull with a short interval
	puller.StartPeriodicPull(ctx, 50*time.Millisecond)

	// Wait for at least 2 periodic pulls
	time.Sleep(150 * time.Millisecond)
	cancel()

	// Allow goroutine to clean up
	time.Sleep(20 * time.Millisecond)

	calls := mock.getPullCalls()
	if len(calls) < 2 {
		t.Fatalf("expected at least 2 periodic pull calls, got %d", len(calls))
	}
}
