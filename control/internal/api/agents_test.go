package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/appx/forge/control/internal/store"
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
