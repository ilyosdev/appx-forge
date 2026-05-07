package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/appx/forge/control/internal/store"
)

// --- Mock LogProxyStore ---

type mockLogProxyStore struct {
	getSandboxFn func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error)
	getNodeFn    func(ctx context.Context, id pgtype.UUID) (store.Node, error)
}

func (m *mockLogProxyStore) GetSandbox(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
	if m.getSandboxFn != nil {
		return m.getSandboxFn(ctx, id)
	}
	return store.Sandbox{}, pgx.ErrNoRows
}

func (m *mockLogProxyStore) GetNode(ctx context.Context, id pgtype.UUID) (store.Node, error) {
	if m.getNodeFn != nil {
		return m.getNodeFn(ctx, id)
	}
	return store.Node{}, pgx.ErrNoRows
}

// --- Tests ---

func TestGetLogs_ValidSandbox(t *testing.T) {
	sandboxID := pgtype.UUID{Valid: true}
	copy(sandboxID.Bytes[:], []byte("logssandboxuuid1"))

	nodeID := pgtype.UUID{Valid: true}
	copy(nodeID.Bytes[:], []byte("logsnodeuuid0001"))

	// Create a fake agent that returns log text
	agentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "line 1: app started\nline 2: listening on port 8081\n")
	}))
	defer agentServer.Close()

	// Parse agent server address to get IP and port
	agentAddr := strings.TrimPrefix(agentServer.URL, "http://")
	parts := strings.Split(agentAddr, ":")
	agentIP := netip.MustParseAddr(parts[0])
	var agentPort int32
	fmt.Sscanf(parts[1], "%d", &agentPort)

	lps := &mockLogProxyStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{
				ID:     sandboxID,
				NodeID: nodeID,
				State:  "running",
			}, nil
		},
		getNodeFn: func(ctx context.Context, id pgtype.UUID) (store.Node, error) {
			return store.Node{
				ID:              nodeID,
				TailscaleIp:     agentIP,
				AgentListenPort: agentPort,
			}, nil
		},
	}

	srv := newTestServerWithLogProxy(lps, agentServer.Client())
	req := httptest.NewRequest(http.MethodGet, "/v1/sandboxes/"+formatUUID(sandboxID)+"/logs", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	if !strings.Contains(body, "line 1: app started") {
		t.Fatalf("expected proxied log text, got: %s", body)
	}

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/plain") {
		t.Fatalf("expected Content-Type text/plain, got: %s", ct)
	}
}

func TestGetLogs_TailParam(t *testing.T) {
	sandboxID := pgtype.UUID{Valid: true}
	copy(sandboxID.Bytes[:], []byte("logstailuuid0001"))

	nodeID := pgtype.UUID{Valid: true}
	copy(nodeID.Bytes[:], []byte("logstailnode0001"))

	var capturedTail string
	agentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedTail = r.URL.Query().Get("tail")
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "tail output")
	}))
	defer agentServer.Close()

	agentAddr := strings.TrimPrefix(agentServer.URL, "http://")
	parts := strings.Split(agentAddr, ":")
	agentIP := netip.MustParseAddr(parts[0])
	var agentPort int32
	fmt.Sscanf(parts[1], "%d", &agentPort)

	lps := &mockLogProxyStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{ID: sandboxID, NodeID: nodeID, State: "running"}, nil
		},
		getNodeFn: func(ctx context.Context, id pgtype.UUID) (store.Node, error) {
			return store.Node{ID: nodeID, TailscaleIp: agentIP, AgentListenPort: agentPort}, nil
		},
	}

	srv := newTestServerWithLogProxy(lps, agentServer.Client())
	req := httptest.NewRequest(http.MethodGet, "/v1/sandboxes/"+formatUUID(sandboxID)+"/logs?tail=50", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if capturedTail != "50" {
		t.Fatalf("expected tail=50 forwarded to agent, got %q", capturedTail)
	}
}

func TestGetLogs_UnknownSandbox(t *testing.T) {
	lps := &mockLogProxyStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{}, pgx.ErrNoRows
		},
	}

	srv := newTestServerWithLogProxy(lps, http.DefaultClient)
	req := httptest.NewRequest(http.MethodGet, "/v1/sandboxes/00000000-0000-0000-0000-000000000099/logs", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetLogs_SandboxNotAssigned(t *testing.T) {
	sandboxID := pgtype.UUID{Valid: true}
	copy(sandboxID.Bytes[:], []byte("logsunassignedu1"))

	lps := &mockLogProxyStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			// NodeID is zero value (not valid) -- sandbox not assigned to any node
			return store.Sandbox{
				ID:    sandboxID,
				State: "pending",
			}, nil
		},
	}

	srv := newTestServerWithLogProxy(lps, http.DefaultClient)
	req := httptest.NewRequest(http.MethodGet, "/v1/sandboxes/"+formatUUID(sandboxID)+"/logs", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetLogs_ConstructsCorrectAgentURL(t *testing.T) {
	sandboxID := pgtype.UUID{Valid: true}
	copy(sandboxID.Bytes[:], []byte("logsurlcheckuui1"))

	nodeID := pgtype.UUID{Valid: true}
	copy(nodeID.Bytes[:], []byte("logsurlcheckno01"))

	var capturedURL string
	agentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedURL = r.URL.String()
		w.WriteHeader(http.StatusOK)
	}))
	defer agentServer.Close()

	agentAddr := strings.TrimPrefix(agentServer.URL, "http://")
	parts := strings.Split(agentAddr, ":")
	agentIP := netip.MustParseAddr(parts[0])
	var agentPort int32
	fmt.Sscanf(parts[1], "%d", &agentPort)

	sandboxUUIDStr := formatUUID(sandboxID)

	lps := &mockLogProxyStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{ID: sandboxID, NodeID: nodeID, State: "running"}, nil
		},
		getNodeFn: func(ctx context.Context, id pgtype.UUID) (store.Node, error) {
			return store.Node{ID: nodeID, TailscaleIp: agentIP, AgentListenPort: agentPort}, nil
		},
	}

	srv := newTestServerWithLogProxy(lps, agentServer.Client())
	req := httptest.NewRequest(http.MethodGet, "/v1/sandboxes/"+sandboxUUIDStr+"/logs?tail=25", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// The agent URL path should contain /sandboxes/{sandbox_id}/logs
	expectedPath := "/sandboxes/" + sandboxUUIDStr + "/logs"
	if !strings.Contains(capturedURL, expectedPath) {
		t.Fatalf("expected agent URL to contain %q, got %q", expectedPath, capturedURL)
	}

	// tail param should be forwarded
	if !strings.Contains(capturedURL, "tail=25") {
		t.Fatalf("expected agent URL to contain tail=25, got %q", capturedURL)
	}
}

// --- Test helper ---

func newTestServerWithLogProxy(lps LogProxyStore, httpClient httpDoer) *Server {
	r := chi.NewRouter()
	s := &Server{
		router:        r,
		config:        &serverConfig{apiToken: "test-token"},
		logger:        testLogger(),
		logProxyStore: lps,
		logHTTPClient: httpClient,
	}

	r.Group(func(r chi.Router) {
		r.Use(BearerAuth("test-token"))
		r.Get("/v1/sandboxes/{id}/logs", s.handleGetLogs)
	})

	return s
}

// readAll is a helper to safely read and close a response body.
func readAll(t *testing.T, body io.ReadCloser) string {
	t.Helper()
	defer body.Close()
	b, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}
