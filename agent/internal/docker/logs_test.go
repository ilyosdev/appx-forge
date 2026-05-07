package docker

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
)

// ── Mock Client for Logs ────────────────────────────────────────────────

type mockLogClient struct {
	mu         sync.Mutex
	logCalls   []logCall
	logResult  io.ReadCloser
	logErr     error
}

type logCall struct {
	containerID string
	tail        int
	follow      bool
}

func (m *mockLogClient) GetLogs(ctx context.Context, containerID string, tail int, follow bool) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logCalls = append(m.logCalls, logCall{
		containerID: containerID,
		tail:        tail,
		follow:      follow,
	})
	return m.logResult, m.logErr
}

func (m *mockLogClient) getLogCalls() []logCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]logCall, len(m.logCalls))
	copy(result, m.logCalls)
	return result
}

// ── Tests ───────────────────────────────────────────────────────────────

func TestLogReader_WithTail(t *testing.T) {
	body := io.NopCloser(strings.NewReader("line1\nline2\n"))
	mock := &mockLogClient{logResult: body}
	reader := NewLogReader(mock, testLogger())

	result, err := reader.ReadLogs(context.Background(), "container-abc", 100, false)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	defer result.Close()

	calls := mock.getLogCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 GetLogs call, got %d", len(calls))
	}
	if calls[0].containerID != "container-abc" {
		t.Errorf("expected containerID=container-abc, got %s", calls[0].containerID)
	}
	if calls[0].tail != 100 {
		t.Errorf("expected tail=100, got %d", calls[0].tail)
	}
	if calls[0].follow {
		t.Error("expected follow=false")
	}
}

func TestLogReader_WithFollow(t *testing.T) {
	body := io.NopCloser(strings.NewReader("streaming..."))
	mock := &mockLogClient{logResult: body}
	reader := NewLogReader(mock, testLogger())

	result, err := reader.ReadLogs(context.Background(), "container-xyz", 0, true)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	defer result.Close()

	calls := mock.getLogCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 GetLogs call, got %d", len(calls))
	}
	if !calls[0].follow {
		t.Error("expected follow=true")
	}
	if calls[0].tail != 0 {
		t.Errorf("expected tail=0, got %d", calls[0].tail)
	}
}

func TestLogReader_ReturnsReadCloser(t *testing.T) {
	content := "log line 1\nlog line 2\nlog line 3\n"
	body := io.NopCloser(strings.NewReader(content))
	mock := &mockLogClient{logResult: body}
	reader := NewLogReader(mock, testLogger())

	result, err := reader.ReadLogs(context.Background(), "container-logs", 50, false)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Read content from the returned reader
	data, err := io.ReadAll(result)
	if err != nil {
		t.Fatalf("failed to read logs: %v", err)
	}
	result.Close()

	if string(data) != content {
		t.Errorf("expected content %q, got %q", content, string(data))
	}
}
