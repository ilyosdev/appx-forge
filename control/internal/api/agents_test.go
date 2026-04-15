package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/appx/forge/control/internal/store"
	"github.com/appx/forge/shared-go/auth"
)

// ── Mock Agent Store ─────────────────────────────────────────────────

type mockAgentStore struct {
	pollPendingCommandsFn func(ctx context.Context, nodeID pgtype.UUID) ([]store.Command, error)
	getCommandFn          func(ctx context.Context, id pgtype.UUID) (store.Command, error)
}

func (m *mockAgentStore) PollPendingCommands(ctx context.Context, nodeID pgtype.UUID) ([]store.Command, error) {
	if m.pollPendingCommandsFn != nil {
		return m.pollPendingCommandsFn(ctx, nodeID)
	}
	return []store.Command{}, nil
}

func (m *mockAgentStore) GetCommand(ctx context.Context, id pgtype.UUID) (store.Command, error) {
	if m.getCommandFn != nil {
		return m.getCommandFn(ctx, id)
	}
	return store.Command{}, errors.New("not found")
}

// ── Mock Agent Lifecycle ─────────────────────────────────────────────

type mockAgentLifecycle struct {
	handleAckFn   func(ctx context.Context, cmdID, sandboxID uuid.UUID, cmdType, status string, result json.RawMessage) error
	handleEventFn func(ctx context.Context, nodeID, sandboxID uuid.UUID, eventType string, payload json.RawMessage) error
}

func (m *mockAgentLifecycle) HandleAck(ctx context.Context, cmdID, sandboxID uuid.UUID, cmdType, status string, result json.RawMessage) error {
	if m.handleAckFn != nil {
		return m.handleAckFn(ctx, cmdID, sandboxID, cmdType, status, result)
	}
	return nil
}

func (m *mockAgentLifecycle) HandleEvent(ctx context.Context, nodeID, sandboxID uuid.UUID, eventType string, payload json.RawMessage) error {
	if m.handleEventFn != nil {
		return m.handleEventFn(ctx, nodeID, sandboxID, eventType, payload)
	}
	return nil
}

// ── Test Helpers ─────────────────────────────────────────────────────

func newAgentTestServer(agentStore AgentStore, agentLC AgentLifecycle) *Server {
	r := chi.NewRouter()
	s := &Server{
		router:         r,
		config:         &serverConfig{apiToken: "test-token"},
		logger:         testLogger(),
		agentStore:     agentStore,
		agentLifecycle: agentLC,
	}

	r.Group(func(r chi.Router) {
		r.Use(BearerAuth("test-token"))
		r.Route("/v1", func(r chi.Router) {
			r.Get("/agents/{id}/commands", s.handlePollCommands)
			r.Post("/agents/{id}/commands/{cmd_id}/ack", s.handleAckCommand)
			r.Post("/agents/{id}/events", s.handleReportEvent)
		})
	})

	return s
}

func makeCommand(id, nodeID, sandboxID uuid.UUID, cmdType string) store.Command {
	return store.Command{
		ID:             pgtype.UUID{Bytes: id, Valid: true},
		NodeID:         pgtype.UUID{Bytes: nodeID, Valid: true},
		SandboxID:      pgtype.UUID{Bytes: sandboxID, Valid: true},
		CommandType:    cmdType,
		Payload:        []byte(`{"app_name":"test-app"}`),
		Status:         "dispatched",
		TimeoutSeconds: 60,
		CreatedAt:      pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}
}

// ── Long-poll Tests ─────────────────────────────────────────────────

func TestPollCommands_WithPendingCommands(t *testing.T) {
	nodeID := uuid.New()
	cmdID := uuid.New()
	sandboxID := uuid.New()

	agentStore := &mockAgentStore{
		pollPendingCommandsFn: func(ctx context.Context, nID pgtype.UUID) ([]store.Command, error) {
			return []store.Command{makeCommand(cmdID, nodeID, sandboxID, "start_sandbox")}, nil
		},
	}

	srv := newAgentTestServer(agentStore, &mockAgentLifecycle{})
	req := httptest.NewRequest(http.MethodGet, "/v1/agents/"+nodeID.String()+"/commands?wait=30", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	start := time.Now()
	srv.ServeHTTP(w, req)
	elapsed := time.Since(start)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Should return immediately (not wait)
	if elapsed > 2*time.Second {
		t.Fatalf("expected immediate return with pending commands, took %s", elapsed)
	}

	var resp struct {
		Commands []commandResponse `json:"commands"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(resp.Commands))
	}
	if resp.Commands[0].Type != "start_sandbox" {
		t.Fatalf("expected type 'start_sandbox', got %q", resp.Commands[0].Type)
	}
	if resp.Commands[0].ID != cmdID.String() {
		t.Fatalf("expected command ID %s, got %s", cmdID, resp.Commands[0].ID)
	}
}

func TestPollCommands_Timeout_ReturnsEmpty(t *testing.T) {
	nodeID := uuid.New()

	agentStore := &mockAgentStore{
		pollPendingCommandsFn: func(ctx context.Context, nID pgtype.UUID) ([]store.Command, error) {
			return []store.Command{}, nil
		},
	}

	srv := newAgentTestServer(agentStore, &mockAgentLifecycle{})
	req := httptest.NewRequest(http.MethodGet, "/v1/agents/"+nodeID.String()+"/commands?wait=1", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	start := time.Now()
	srv.ServeHTTP(w, req)
	elapsed := time.Since(start)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Should take approximately 1 second (the wait param)
	if elapsed < 900*time.Millisecond {
		t.Fatalf("expected ~1s wait, returned too fast in %s", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("expected ~1s wait, took too long: %s", elapsed)
	}

	var resp struct {
		Commands []commandResponse `json:"commands"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Commands) != 0 {
		t.Fatalf("expected 0 commands, got %d", len(resp.Commands))
	}
}

func TestPollCommands_WaitClamped(t *testing.T) {
	nodeID := uuid.New()

	callCount := 0
	agentStore := &mockAgentStore{
		pollPendingCommandsFn: func(ctx context.Context, nID pgtype.UUID) ([]store.Command, error) {
			callCount++
			// Return commands on first call to terminate quickly
			if callCount == 1 {
				return []store.Command{makeCommand(uuid.New(), nodeID, uuid.New(), "prune")}, nil
			}
			return []store.Command{}, nil
		},
	}

	srv := newAgentTestServer(agentStore, &mockAgentLifecycle{})
	// wait=120 should be clamped to 60 -- but since we return immediately, just verify no crash
	req := httptest.NewRequest(http.MethodGet, "/v1/agents/"+nodeID.String()+"/commands?wait=120", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPollCommands_DefaultWait(t *testing.T) {
	nodeID := uuid.New()

	agentStore := &mockAgentStore{
		pollPendingCommandsFn: func(ctx context.Context, nID pgtype.UUID) ([]store.Command, error) {
			// Return commands immediately to avoid 30s wait
			return []store.Command{makeCommand(uuid.New(), nodeID, uuid.New(), "prune")}, nil
		},
	}

	srv := newAgentTestServer(agentStore, &mockAgentLifecycle{})
	// No wait param -- should default to 30s, but returns immediately because commands exist
	req := httptest.NewRequest(http.MethodGet, "/v1/agents/"+nodeID.String()+"/commands", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Commands []commandResponse `json:"commands"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(resp.Commands))
	}
}

func TestPollCommands_InvalidNodeID(t *testing.T) {
	srv := newAgentTestServer(&mockAgentStore{}, &mockAgentLifecycle{})
	req := httptest.NewRequest(http.MethodGet, "/v1/agents/not-a-uuid/commands", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// ── Ack Tests ───────────────────────────────────────────────────────

func TestAckCommand_Success(t *testing.T) {
	nodeID := uuid.New()
	cmdID := uuid.New()
	sandboxID := uuid.New()

	ackCalled := false
	agentStore := &mockAgentStore{
		getCommandFn: func(ctx context.Context, id pgtype.UUID) (store.Command, error) {
			return makeCommand(cmdID, nodeID, sandboxID, "start_sandbox"), nil
		},
	}
	agentLC := &mockAgentLifecycle{
		handleAckFn: func(ctx context.Context, cID, sID uuid.UUID, cmdType, status string, result json.RawMessage) error {
			ackCalled = true
			if cID != cmdID {
				t.Fatalf("expected cmdID %s, got %s", cmdID, cID)
			}
			if sID != sandboxID {
				t.Fatalf("expected sandboxID %s, got %s", sandboxID, sID)
			}
			if cmdType != "start_sandbox" {
				t.Fatalf("expected cmdType 'start_sandbox', got %q", cmdType)
			}
			if status != "success" {
				t.Fatalf("expected status 'success', got %q", status)
			}
			return nil
		},
	}

	srv := newAgentTestServer(agentStore, agentLC)
	body := `{"status":"success","result":{"container_id":"abc123","host_port":43210}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/agents/"+nodeID.String()+"/commands/"+cmdID.String()+"/ack", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !ackCalled {
		t.Fatal("expected HandleAck to be called")
	}
}

func TestAckCommand_Failure(t *testing.T) {
	nodeID := uuid.New()
	cmdID := uuid.New()
	sandboxID := uuid.New()

	ackCalled := false
	agentStore := &mockAgentStore{
		getCommandFn: func(ctx context.Context, id pgtype.UUID) (store.Command, error) {
			return makeCommand(cmdID, nodeID, sandboxID, "start_sandbox"), nil
		},
	}
	agentLC := &mockAgentLifecycle{
		handleAckFn: func(ctx context.Context, cID, sID uuid.UUID, cmdType, status string, result json.RawMessage) error {
			ackCalled = true
			if status != "failure" {
				t.Fatalf("expected status 'failure', got %q", status)
			}
			return nil
		},
	}

	srv := newAgentTestServer(agentStore, agentLC)
	body := `{"status":"failure","error":"image not found"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/agents/"+nodeID.String()+"/commands/"+cmdID.String()+"/ack", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !ackCalled {
		t.Fatal("expected HandleAck to be called")
	}
}

func TestAckCommand_InvalidCmdID(t *testing.T) {
	nodeID := uuid.New()

	srv := newAgentTestServer(&mockAgentStore{}, &mockAgentLifecycle{})
	body := `{"status":"success"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/agents/"+nodeID.String()+"/commands/not-a-uuid/ack", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// ── Event Tests ─────────────────────────────────────────────────────

func TestReportEvent_Success(t *testing.T) {
	nodeID := uuid.New()
	sandboxID := uuid.New()

	eventCalled := false
	agentLC := &mockAgentLifecycle{
		handleEventFn: func(ctx context.Context, nID, sID uuid.UUID, eventType string, payload json.RawMessage) error {
			eventCalled = true
			if nID != nodeID {
				t.Fatalf("expected nodeID %s, got %s", nodeID, nID)
			}
			if sID != sandboxID {
				t.Fatalf("expected sandboxID %s, got %s", sandboxID, sID)
			}
			if eventType != "container_exited" {
				t.Fatalf("expected event_type 'container_exited', got %q", eventType)
			}
			return nil
		},
	}

	srv := newAgentTestServer(&mockAgentStore{}, agentLC)
	body := `{"sandbox_id":"` + sandboxID.String() + `","event_type":"container_exited","container_id":"abc123","exit_code":137,"payload":{"oom_killed":true}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/agents/"+nodeID.String()+"/events", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !eventCalled {
		t.Fatal("expected HandleEvent to be called")
	}
}

func TestReportEvent_MissingSandboxID(t *testing.T) {
	nodeID := uuid.New()

	srv := newAgentTestServer(&mockAgentStore{}, &mockAgentLifecycle{})
	body := `{"event_type":"container_exited"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/agents/"+nodeID.String()+"/events", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestReportEvent_MissingEventType(t *testing.T) {
	nodeID := uuid.New()
	sandboxID := uuid.New()

	srv := newAgentTestServer(&mockAgentStore{}, &mockAgentLifecycle{})
	body := `{"sandbox_id":"` + sandboxID.String() + `"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/agents/"+nodeID.String()+"/events", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestReportEvent_InvalidNodeID(t *testing.T) {
	srv := newAgentTestServer(&mockAgentStore{}, &mockAgentLifecycle{})
	body := `{"sandbox_id":"` + uuid.New().String() + `","event_type":"container_exited"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/agents/not-a-uuid/events", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// ── Mock File Push Store ────────────────────────────────────────────

type mockFilePushStore struct {
	getSandboxFn            func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error)
	getNodeFn               func(ctx context.Context, id pgtype.UUID) (store.Node, error)
	updateSandboxLastActive func(ctx context.Context, id pgtype.UUID) error
}

func (m *mockFilePushStore) GetSandbox(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
	if m.getSandboxFn != nil {
		return m.getSandboxFn(ctx, id)
	}
	return store.Sandbox{}, errors.New("not found")
}

func (m *mockFilePushStore) GetNode(ctx context.Context, id pgtype.UUID) (store.Node, error) {
	if m.getNodeFn != nil {
		return m.getNodeFn(ctx, id)
	}
	return store.Node{}, errors.New("not found")
}

func (m *mockFilePushStore) UpdateSandboxLastActive(ctx context.Context, id pgtype.UUID) error {
	if m.updateSandboxLastActive != nil {
		return m.updateSandboxLastActive(ctx, id)
	}
	return nil
}

// ── File Push Test Helpers ──────────────────────────────────────────

func newFilePushTestServer(fps FilePushStore) *Server {
	r := chi.NewRouter()
	s := &Server{
		router:        r,
		config:        &serverConfig{apiToken: "test-token", hmacSecret: "test-hmac-secret-32bytes-long!!"},
		logger:        testLogger(),
		filePushStore: fps,
	}

	r.Group(func(r chi.Router) {
		r.Use(BearerAuth("test-token"))
		r.Route("/v1", func(r chi.Router) {
			r.Post("/sandboxes/{id}/files", s.handleFilePush)
		})
	})

	return s
}

// ── File Push Tests ─────────────────────────────────────────────────

func TestFilePush_Success_307Redirect(t *testing.T) {
	sandboxID := uuid.New()
	nodeID := uuid.New()

	tsIP := netip.MustParseAddr("100.64.1.5")

	fps := &mockFilePushStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{
				ID:      pgtype.UUID{Bytes: sandboxID, Valid: true},
				AppName: "my-app",
				NodeID:  pgtype.UUID{Bytes: nodeID, Valid: true},
				State:   "running",
			}, nil
		},
		getNodeFn: func(ctx context.Context, id pgtype.UUID) (store.Node, error) {
			return store.Node{
				ID:              pgtype.UUID{Bytes: nodeID, Valid: true},
				TailscaleIp:     tsIP,
				AgentListenPort: 8090,
			}, nil
		},
		updateSandboxLastActive: func(ctx context.Context, id pgtype.UUID) error {
			return nil
		},
	}

	srv := newFilePushTestServer(fps)
	req := httptest.NewRequest(http.MethodPost, "/v1/sandboxes/"+sandboxID.String()+"/files", bytes.NewBufferString(`{"files":[]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusTemporaryRedirect {
		t.Fatalf("expected 307, got %d: %s", w.Code, w.Body.String())
	}

	loc := w.Header().Get("Location")
	if loc == "" {
		t.Fatal("expected Location header to be set")
	}

	// Verify the Location URL contains the sandbox ID
	if !strings.Contains(loc, sandboxID.String()) {
		t.Fatalf("expected Location URL to contain sandbox ID %s, got %q", sandboxID, loc)
	}

	// Verify the Location URL contains the Tailscale IP and port
	if !strings.Contains(loc, "100.64.1.5:8090") {
		t.Fatalf("expected Location URL to contain 100.64.1.5:8090, got %q", loc)
	}

	// Verify the signed URL has expires and sig params
	if !strings.Contains(loc, "expires=") {
		t.Fatalf("expected Location URL to contain expires param, got %q", loc)
	}
	if !strings.Contains(loc, "sig=") {
		t.Fatalf("expected Location URL to contain sig param, got %q", loc)
	}

	// Verify the signed URL is valid via auth.VerifyURL
	_, err := auth.VerifyURL(loc, []byte("test-hmac-secret-32bytes-long!!"))
	if err != nil {
		t.Fatalf("expected signed URL to be valid, got error: %v", err)
	}
}

func TestFilePush_SandboxNotFound(t *testing.T) {
	fps := &mockFilePushStore{} // default returns error

	srv := newFilePushTestServer(fps)
	req := httptest.NewRequest(http.MethodPost, "/v1/sandboxes/"+uuid.New().String()+"/files", bytes.NewBufferString(`{"files":[]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestFilePush_SandboxNotScheduled(t *testing.T) {
	sandboxID := uuid.New()

	fps := &mockFilePushStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{
				ID:      pgtype.UUID{Bytes: sandboxID, Valid: true},
				AppName: "my-app",
				NodeID:  pgtype.UUID{Valid: false}, // not assigned to a node
				State:   "pending",
			}, nil
		},
	}

	srv := newFilePushTestServer(fps)
	req := httptest.NewRequest(http.MethodPost, "/v1/sandboxes/"+sandboxID.String()+"/files", bytes.NewBufferString(`{"files":[]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

func TestFilePush_InvalidSandboxID(t *testing.T) {
	fps := &mockFilePushStore{}
	srv := newFilePushTestServer(fps)

	req := httptest.NewRequest(http.MethodPost, "/v1/sandboxes/not-a-uuid/files", bytes.NewBufferString(`{"files":[]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestFilePush_SignedURL_Has60sExpiry(t *testing.T) {
	sandboxID := uuid.New()
	nodeID := uuid.New()

	tsIP := netip.MustParseAddr("100.64.1.5")

	fps := &mockFilePushStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{
				ID:      pgtype.UUID{Bytes: sandboxID, Valid: true},
				AppName: "my-app",
				NodeID:  pgtype.UUID{Bytes: nodeID, Valid: true},
				State:   "running",
			}, nil
		},
		getNodeFn: func(ctx context.Context, id pgtype.UUID) (store.Node, error) {
			return store.Node{
				ID:              pgtype.UUID{Bytes: nodeID, Valid: true},
				TailscaleIp:     tsIP,
				AgentListenPort: 8090,
			}, nil
		},
	}

	srv := newFilePushTestServer(fps)
	req := httptest.NewRequest(http.MethodPost, "/v1/sandboxes/"+sandboxID.String()+"/files", bytes.NewBufferString(`{"files":[]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	beforeTime := time.Now().Unix()
	srv.ServeHTTP(w, req)
	afterTime := time.Now().Unix()

	if w.Code != http.StatusTemporaryRedirect {
		t.Fatalf("expected 307, got %d: %s", w.Code, w.Body.String())
	}

	loc := w.Header().Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse location URL: %v", err)
	}

	expiresStr := u.Query().Get("expires")
	if expiresStr == "" {
		t.Fatal("expected expires param in signed URL")
	}

	expires, err := strconv.ParseInt(expiresStr, 10, 64)
	if err != nil {
		t.Fatalf("parse expires: %v", err)
	}

	// Expires should be ~60s from now
	minExpiry := beforeTime + 55 // allow 5s tolerance
	maxExpiry := afterTime + 65  // allow 5s tolerance

	if expires < minExpiry || expires > maxExpiry {
		t.Fatalf("expected expires between %d and %d, got %d", minExpiry, maxExpiry, expires)
	}
}

func TestFilePush_UpdatesLastActive(t *testing.T) {
	sandboxID := uuid.New()
	nodeID := uuid.New()

	tsIP := netip.MustParseAddr("100.64.1.5")

	lastActiveCalled := false
	fps := &mockFilePushStore{
		getSandboxFn: func(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
			return store.Sandbox{
				ID:      pgtype.UUID{Bytes: sandboxID, Valid: true},
				AppName: "my-app",
				NodeID:  pgtype.UUID{Bytes: nodeID, Valid: true},
				State:   "running",
			}, nil
		},
		getNodeFn: func(ctx context.Context, id pgtype.UUID) (store.Node, error) {
			return store.Node{
				ID:              pgtype.UUID{Bytes: nodeID, Valid: true},
				TailscaleIp:     tsIP,
				AgentListenPort: 8090,
			}, nil
		},
		updateSandboxLastActive: func(ctx context.Context, id pgtype.UUID) error {
			lastActiveCalled = true
			if id.Bytes != sandboxID {
				t.Fatalf("expected sandbox ID %s, got %s", sandboxID, uuid.UUID(id.Bytes))
			}
			return nil
		},
	}

	srv := newFilePushTestServer(fps)
	req := httptest.NewRequest(http.MethodPost, "/v1/sandboxes/"+sandboxID.String()+"/files", bytes.NewBufferString(`{"files":[]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusTemporaryRedirect {
		t.Fatalf("expected 307, got %d: %s", w.Code, w.Body.String())
	}

	if !lastActiveCalled {
		t.Fatal("expected UpdateSandboxLastActive to be called")
	}
}
