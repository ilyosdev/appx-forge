package filepush

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appx/forge/shared-go/auth"
)

// mockResolver implements SandboxResolver for tests.
type mockResolver struct {
	dirs map[string]string // sandboxID -> codeDir
}

func (m *mockResolver) CodeDir(sandboxID string) (string, error) {
	dir, ok := m.dirs[sandboxID]
	if !ok {
		return "", fmt.Errorf("sandbox %s not found on this node", sandboxID)
	}
	return dir, nil
}

// mockRestartResolver implements both SandboxResolver and SandboxRestarter,
// recording each restart so tests can assert the structural-push → fresh-crawl
// wiring (the production CommandExecutor satisfies both interfaces the same way).
type mockRestartResolver struct {
	mockResolver
	restarted  []string // sandboxIDs restarted, in order
	restartErr error    // when set, RestartSandbox returns it (fail-open test)
}

func (m *mockRestartResolver) RestartSandbox(sandboxID string) error {
	m.restarted = append(m.restarted, sandboxID)
	return m.restartErr
}

var testSecret = []byte("test-hmac-secret-key-32bytes!!!!!")

func TestHandler_ValidSignedURL_JSONBody(t *testing.T) {
	dir := t.TempDir()
	resolver := &mockResolver{dirs: map[string]string{"sbx-123": dir}}
	handler := NewHandler(testSecret, resolver, nil)

	body := `{"files":[{"path":"App.tsx","content":"` + b64("hello") + `","delete":false}]}`

	srv := httptest.NewServer(handler)
	defer srv.Close()

	// Build the URL path matching the handler's expected pattern
	targetURL := srv.URL + "/v1/sandboxes/sbx-123/files"
	signedURL, err := auth.SignURL(targetURL, testSecret, 60*time.Second)
	if err != nil {
		t.Fatalf("SignURL: %v", err)
	}

	resp, err := http.Post(signedURL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, respBody)
	}

	var result WriteResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if len(result.Written) != 1 || result.Written[0] != "App.tsx" {
		t.Errorf("Written = %v, want [App.tsx]", result.Written)
	}

	// Verify file was actually written
	got, err := os.ReadFile(filepath.Join(dir, "App.tsx"))
	if err != nil {
		t.Fatalf("reading written file: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("file content = %q, want %q", got, "hello")
	}
}

// TestHandler_FullSyncPrunesStale drives the W4 prune through the HTTP handler:
// a fullSync push whose manifest omits a pre-existing file deletes it and
// reports it in the `deleted` field.
func TestHandler_FullSyncPrunesStale(t *testing.T) {
	dir := t.TempDir()

	// Point the template index at an empty dir (so nothing is a seed) — must
	// exist or the prune fails open.
	tmpl := t.TempDir()
	prev := os.Getenv("FORGE_TEMPLATE_DIR")
	os.Setenv("FORGE_TEMPLATE_DIR", tmpl)
	defer os.Setenv("FORGE_TEMPLATE_DIR", prev)

	// Pre-existing ghost file not in the manifest.
	ghost := filepath.Join(dir, "app", "ghost.tsx")
	if err := os.MkdirAll(filepath.Dir(ghost), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(ghost, []byte("ghost"), 0644); err != nil {
		t.Fatalf("seed ghost: %v", err)
	}

	resolver := &mockResolver{dirs: map[string]string{"sbx-fs": dir}}
	handler := NewHandler(testSecret, resolver, nil)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	body := `{"fullSync":true,"manifest":["app/home.tsx"],"files":[{"path":"app/home.tsx","content":"` + b64("home") + `"}]}`
	targetURL := srv.URL + "/v1/sandboxes/sbx-fs/files"
	signedURL, err := auth.SignURL(targetURL, testSecret, 60*time.Second)
	if err != nil {
		t.Fatalf("SignURL: %v", err)
	}

	resp, err := http.Post(signedURL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, respBody)
	}

	var result WriteResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if len(result.Deleted) != 1 || result.Deleted[0] != "app/ghost.tsx" {
		t.Errorf("Deleted = %v, want [app/ghost.tsx]", result.Deleted)
	}
	if _, err := os.Stat(ghost); !os.IsNotExist(err) {
		t.Error("ghost file should have been pruned")
	}
	if _, err := os.Stat(filepath.Join(dir, "app", "home.tsx")); err != nil {
		t.Errorf("home.tsx should exist: %v", err)
	}
}

func TestHandler_MissingSigParameter(t *testing.T) {
	resolver := &mockResolver{dirs: map[string]string{"sbx-123": t.TempDir()}}
	handler := NewHandler(testSecret, resolver, nil)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	// No sig/expires params -- raw unsigned URL
	resp, err := http.Post(srv.URL+"/v1/sandboxes/sbx-123/files", "application/json", strings.NewReader(`{"files":[]}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

func TestHandler_InvalidSignature(t *testing.T) {
	resolver := &mockResolver{dirs: map[string]string{"sbx-123": t.TempDir()}}
	handler := NewHandler(testSecret, resolver, nil)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	// Sign with a different key
	wrongKey := []byte("wrong-hmac-secret-key-32bytes!!!!")
	signedURL, err := auth.SignURL(srv.URL+"/v1/sandboxes/sbx-123/files", wrongKey, 60*time.Second)
	if err != nil {
		t.Fatalf("SignURL: %v", err)
	}

	resp, err := http.Post(signedURL, "application/json", strings.NewReader(`{"files":[]}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

func TestHandler_ExpiredURL(t *testing.T) {
	resolver := &mockResolver{dirs: map[string]string{"sbx-123": t.TempDir()}}
	handler := NewHandler(testSecret, resolver, nil)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	// Sign with negative expiry (already expired)
	signedURL, err := auth.SignURL(srv.URL+"/v1/sandboxes/sbx-123/files", testSecret, -1*time.Second)
	if err != nil {
		t.Fatalf("SignURL: %v", err)
	}

	resp, err := http.Post(signedURL, "application/json", strings.NewReader(`{"files":[]}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

func TestHandler_PathTraversalInFilePath(t *testing.T) {
	dir := t.TempDir()
	resolver := &mockResolver{dirs: map[string]string{"sbx-123": dir}}
	handler := NewHandler(testSecret, resolver, nil)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	body := `{"files":[
		{"path":"good.tsx","content":"` + b64("good") + `","delete":false},
		{"path":"../evil.tsx","content":"` + b64("evil") + `","delete":false}
	]}`

	signedURL, err := auth.SignURL(srv.URL+"/v1/sandboxes/sbx-123/files", testSecret, 60*time.Second)
	if err != nil {
		t.Fatalf("SignURL: %v", err)
	}

	resp, err := http.Post(signedURL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result WriteResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if len(result.Written) != 1 || result.Written[0] != "good.tsx" {
		t.Errorf("Written = %v, want [good.tsx]", result.Written)
	}
	if len(result.Failed) != 1 || result.Failed[0] != "../evil.tsx" {
		t.Errorf("Failed = %v, want [../evil.tsx]", result.Failed)
	}
}

func TestHandler_TarContentType(t *testing.T) {
	dir := t.TempDir()
	resolver := &mockResolver{dirs: map[string]string{"sbx-123": dir}}
	handler := NewHandler(testSecret, resolver, nil)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	// Build tar.gz
	tarBuf := buildHandlerTarGz(t, map[string]string{
		"index.tsx": "index content",
	})

	signedURL, err := auth.SignURL(srv.URL+"/v1/sandboxes/sbx-123/files", testSecret, 60*time.Second)
	if err != nil {
		t.Fatalf("SignURL: %v", err)
	}

	resp, err := http.Post(signedURL, "application/x-tar", tarBuf)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, respBody)
	}

	var result WriteResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if len(result.Written) != 1 || result.Written[0] != "index.tsx" {
		t.Errorf("Written = %v, want [index.tsx]", result.Written)
	}

	got, err := os.ReadFile(filepath.Join(dir, "index.tsx"))
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	if string(got) != "index content" {
		t.Errorf("content = %q, want %q", got, "index content")
	}
}

func TestHandler_SandboxNotFound(t *testing.T) {
	// Empty resolver -- no sandboxes
	resolver := &mockResolver{dirs: map[string]string{}}
	handler := NewHandler(testSecret, resolver, nil)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	signedURL, err := auth.SignURL(srv.URL+"/v1/sandboxes/sbx-unknown/files", testSecret, 60*time.Second)
	if err != nil {
		t.Fatalf("SignURL: %v", err)
	}

	resp, err := http.Post(signedURL, "application/json", strings.NewReader(`{"files":[]}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestHandler_ExtractsSandboxIDFromPath(t *testing.T) {
	dir := t.TempDir()
	// Use a specific sandbox ID to verify extraction
	resolver := &mockResolver{dirs: map[string]string{"abc-def-ghi": dir}}
	handler := NewHandler(testSecret, resolver, nil)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	body := `{"files":[{"path":"test.tsx","content":"` + b64("test") + `","delete":false}]}`

	signedURL, err := auth.SignURL(srv.URL+"/v1/sandboxes/abc-def-ghi/files", testSecret, 60*time.Second)
	if err != nil {
		t.Fatalf("SignURL: %v", err)
	}

	resp, err := http.Post(signedURL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, respBody)
	}

	// Verify the file was written in the correct directory
	if _, err := os.Stat(filepath.Join(dir, "test.tsx")); err != nil {
		t.Error("file should exist in the sandbox directory")
	}
}

func TestHandler_NoBody(t *testing.T) {
	resolver := &mockResolver{dirs: map[string]string{"sbx-123": t.TempDir()}}
	handler := NewHandler(testSecret, resolver, nil)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	signedURL, err := auth.SignURL(srv.URL+"/v1/sandboxes/sbx-123/files", testSecret, 60*time.Second)
	if err != nil {
		t.Fatalf("SignURL: %v", err)
	}

	resp, err := http.Post(signedURL, "application/json", http.NoBody)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// postSigned signs and POSTs a JSON body to the handler, returning the response.
func postSigned(t *testing.T, srvURL, sandboxID, body string) *http.Response {
	t.Helper()
	target := srvURL + "/v1/sandboxes/" + sandboxID + "/files"
	signed, err := auth.SignURL(target, testSecret, 60*time.Second)
	if err != nil {
		t.Fatalf("SignURL: %v", err)
	}
	resp, err := http.Post(signed, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	return resp
}

// TestHandler_StructuralPushRestartsMetro proves the fresh-crawl wiring: a push
// that CREATES a new path triggers exactly one RestartSandbox for that sandbox.
func TestHandler_StructuralPushRestartsMetro(t *testing.T) {
	dir := t.TempDir()
	resolver := &mockRestartResolver{mockResolver: mockResolver{dirs: map[string]string{"sbx-1": dir}}}
	srv := httptest.NewServer(NewHandler(testSecret, resolver, nil))
	defer srv.Close()

	body := `{"files":[{"path":"components/New.tsx","content":"` + b64("export const New = 1") + `"}]}`
	resp := postSigned(t, srv.URL, "sbx-1", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if len(resolver.restarted) != 1 || resolver.restarted[0] != "sbx-1" {
		t.Errorf("expected one restart of sbx-1, got %v", resolver.restarted)
	}
}

// TestHandler_ContentOnlyPushDoesNotRestart proves a content-only edit to an
// existing file is left to the mtime-nudge — no needless container restart.
func TestHandler_ContentOnlyPushDoesNotRestart(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "App.tsx"), []byte("v1"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	resolver := &mockRestartResolver{mockResolver: mockResolver{dirs: map[string]string{"sbx-2": dir}}}
	srv := httptest.NewServer(NewHandler(testSecret, resolver, nil))
	defer srv.Close()

	body := `{"files":[{"path":"App.tsx","content":"` + b64("v2") + `"}]}`
	resp := postSigned(t, srv.URL, "sbx-2", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if len(resolver.restarted) != 0 {
		t.Errorf("content-only push must not restart, got %v", resolver.restarted)
	}
}

// TestHandler_RestartErrorIsFailOpen proves a restart failure does NOT fail the
// push — the files are written, so the response is still 200 and the new file is
// on disk (worst case the preview needs a manual refresh).
func TestHandler_RestartErrorIsFailOpen(t *testing.T) {
	dir := t.TempDir()
	resolver := &mockRestartResolver{
		mockResolver: mockResolver{dirs: map[string]string{"sbx-3": dir}},
		restartErr:   fmt.Errorf("docker daemon unreachable"),
	}
	srv := httptest.NewServer(NewHandler(testSecret, resolver, nil))
	defer srv.Close()

	body := `{"files":[{"path":"components/New.tsx","content":"` + b64("export const New = 1") + `"}]}`
	resp := postSigned(t, srv.URL, "sbx-3", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("restart error must not fail the push; expected 200, got %d", resp.StatusCode)
	}
	if len(resolver.restarted) != 1 {
		t.Errorf("expected restart attempt, got %v", resolver.restarted)
	}
	if _, err := os.Stat(filepath.Join(dir, "components/New.tsx")); err != nil {
		t.Errorf("file must be written despite restart error: %v", err)
	}
}

// --- helpers ---

func buildHandlerTarGz(t *testing.T, files map[string]string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for name, content := range files {
		data := []byte(content)
		if err := tw.WriteHeader(&tar.Header{
			Name:     name,
			Size:     int64(len(data)),
			Mode:     0644,
			Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}

	tw.Close()
	gz.Close()
	return &buf
}
