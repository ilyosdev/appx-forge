package agent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeLogsResolver struct {
	containerID string
	ok          bool
	gotSandbox  string
}

func (f *fakeLogsResolver) ResolveContainerID(sandboxID string) (string, bool) {
	f.gotSandbox = sandboxID
	return f.containerID, f.ok
}

type fakeLogsReader struct {
	body      string
	err       error
	gotID     string
	gotTail   int
	gotFollow bool
}

func (f *fakeLogsReader) GetLogs(ctx context.Context, containerID string, tail int, follow bool) (io.ReadCloser, error) {
	f.gotID = containerID
	f.gotTail = tail
	f.gotFollow = follow
	if f.err != nil {
		return nil, f.err
	}
	return io.NopCloser(strings.NewReader(f.body)), nil
}

func TestLogs_ReturnsBodyForRunningSandbox(t *testing.T) {
	resolver := &fakeLogsResolver{containerID: "ctr-abc", ok: true}
	reader := &fakeLogsReader{body: "line 1\nline 2\n"}
	h := newLogsHandler(resolver, reader)

	req := httptest.NewRequest("GET", "/sandboxes/sb-1/logs", nil)
	req.SetPathValue("id", "sb-1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "line 1\nline 2\n" {
		t.Errorf("unexpected body: %q", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("expected text/plain, got %s", ct)
	}
	if reader.gotID != "ctr-abc" {
		t.Errorf("expected container ctr-abc, got %s", reader.gotID)
	}
	// follow must be forced off — the control proxy is one-shot.
	if reader.gotFollow {
		t.Errorf("expected follow=false")
	}
}

// The regression this fix targets: a sandbox the agent doesn't have mapped
// (e.g. created before its last restart) must surface as 404 from the handler
// — and the resolver's docker-label fallback (resolveSandbox) is what keeps a
// real running sandbox from landing here. With the route absent entirely, the
// mux returned 404 for EVERY sandbox, which the UI rendered as "unreachable".
func TestLogs_Returns404WhenSandboxNotOnNode(t *testing.T) {
	resolver := &fakeLogsResolver{ok: false}
	reader := &fakeLogsReader{}
	h := newLogsHandler(resolver, reader)

	req := httptest.NewRequest("GET", "/sandboxes/sb-missing/logs", nil)
	req.SetPathValue("id", "sb-missing")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
	if reader.gotID != "" {
		t.Errorf("docker should not be hit when sandbox unresolved")
	}
}

func TestLogs_Returns502WhenDockerFails(t *testing.T) {
	resolver := &fakeLogsResolver{containerID: "ctr-x", ok: true}
	reader := &fakeLogsReader{err: errors.New("docker daemon down")}
	h := newLogsHandler(resolver, reader)

	req := httptest.NewRequest("GET", "/sandboxes/sb-1/logs", nil)
	req.SetPathValue("id", "sb-1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rec.Code)
	}
}

func TestLogs_Returns400WhenIDMissing(t *testing.T) {
	h := newLogsHandler(&fakeLogsResolver{}, &fakeLogsReader{})

	req := httptest.NewRequest("GET", "/sandboxes//logs", nil)
	// PathValue("id") returns "" when not set
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestLogs_ForwardsTailParam(t *testing.T) {
	resolver := &fakeLogsResolver{containerID: "ctr-abc", ok: true}
	reader := &fakeLogsReader{body: "tail output"}
	h := newLogsHandler(resolver, reader)

	req := httptest.NewRequest("GET", "/sandboxes/sb-1/logs?tail=42", nil)
	req.SetPathValue("id", "sb-1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if reader.gotTail != 42 {
		t.Errorf("expected tail=42 forwarded, got %d", reader.gotTail)
	}
}

func TestLogs_IgnoresInvalidTail(t *testing.T) {
	resolver := &fakeLogsResolver{containerID: "ctr-abc", ok: true}
	reader := &fakeLogsReader{body: "x"}
	h := newLogsHandler(resolver, reader)

	req := httptest.NewRequest("GET", "/sandboxes/sb-1/logs?tail=not-a-number", nil)
	req.SetPathValue("id", "sb-1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if reader.gotTail != 0 {
		t.Errorf("expected tail=0 (all) for invalid input, got %d", reader.gotTail)
	}
}
