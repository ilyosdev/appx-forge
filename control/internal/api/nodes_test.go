package api

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// --- Mock store for node handlers ---

// mockNodeStore implements NodeStore for testing registration and heartbeat.
type mockNodeStore struct {
	getByHostnameAndIPFn func(ctx context.Context, hostname string, ip netip.Addr) (nodeRecord, error)
	createNodeFn         func(ctx context.Context, arg createNodeArgs) (nodeRecord, error)
	updateNodeTokenFn    func(ctx context.Context, token string, agentVersion string, id pgtype.UUID) error
	getNodeFn            func(ctx context.Context, id pgtype.UUID) (nodeRecord, error)
	updateHeartbeatFn    func(ctx context.Context, id pgtype.UUID, usedMb int32, runningContainers int32) error
}

func (m *mockNodeStore) GetNodeByHostnameAndIP(ctx context.Context, hostname string, ip netip.Addr) (nodeRecord, error) {
	if m.getByHostnameAndIPFn != nil {
		return m.getByHostnameAndIPFn(ctx, hostname, ip)
	}
	return nodeRecord{}, pgx.ErrNoRows
}

func (m *mockNodeStore) CreateNode(ctx context.Context, arg createNodeArgs) (nodeRecord, error) {
	if m.createNodeFn != nil {
		return m.createNodeFn(ctx, arg)
	}
	return nodeRecord{}, nil
}

func (m *mockNodeStore) UpdateNodeToken(ctx context.Context, token string, agentVersion string, id pgtype.UUID) error {
	if m.updateNodeTokenFn != nil {
		return m.updateNodeTokenFn(ctx, token, agentVersion, id)
	}
	return nil
}

func (m *mockNodeStore) GetNode(ctx context.Context, id pgtype.UUID) (nodeRecord, error) {
	if m.getNodeFn != nil {
		return m.getNodeFn(ctx, id)
	}
	return nodeRecord{}, pgx.ErrNoRows
}

func (m *mockNodeStore) UpdateNodeHeartbeat(ctx context.Context, id pgtype.UUID, usedMb int32, runningContainers int32) error {
	if m.updateHeartbeatFn != nil {
		return m.updateHeartbeatFn(ctx, id, usedMb, runningContainers)
	}
	return nil
}

// testLogger returns a discard logger for tests.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// --- Registration tests ---

func TestRegisterNode_ValidRequest(t *testing.T) {
	store := &mockNodeStore{
		getByHostnameAndIPFn: func(ctx context.Context, hostname string, ip netip.Addr) (nodeRecord, error) {
			return nodeRecord{}, pgx.ErrNoRows
		},
		createNodeFn: func(ctx context.Context, arg createNodeArgs) (nodeRecord, error) {
			return nodeRecord{ID: arg.ID, Hostname: arg.Hostname}, nil
		},
	}

	srv := newTestServerWithNodeStore(store, 15)
	body := `{"hostname":"node-1","tailscale_ip":"100.64.1.5","agent_listen_port":8090,"capacity_mb":24000,"capacity_cpu":8.0,"agent_version":"0.1.0"}`

	req := httptest.NewRequest(http.MethodPost, "/v1/nodes/register", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp registerResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.NodeID == "" {
		t.Fatal("expected non-empty node_id")
	}
	if len(resp.AgentToken) != 64 {
		t.Fatalf("expected 64-char hex token, got %d chars: %q", len(resp.AgentToken), resp.AgentToken)
	}
	// Verify it's valid hex
	if _, err := hex.DecodeString(resp.AgentToken); err != nil {
		t.Fatalf("agent_token is not valid hex: %v", err)
	}
	if resp.HeartbeatIntervalSeconds != 15 {
		t.Fatalf("expected heartbeat_interval_seconds=15, got %d", resp.HeartbeatIntervalSeconds)
	}
}

func TestRegisterNode_ReRegistration(t *testing.T) {
	existingID := pgtype.UUID{Valid: true}
	copy(existingID.Bytes[:], []byte("existingnodeuuid"))

	var capturedToken string
	store := &mockNodeStore{
		getByHostnameAndIPFn: func(ctx context.Context, hostname string, ip netip.Addr) (nodeRecord, error) {
			return nodeRecord{ID: existingID, Hostname: hostname}, nil
		},
		updateNodeTokenFn: func(ctx context.Context, token string, agentVersion string, id pgtype.UUID) error {
			capturedToken = token
			if id != existingID {
				t.Fatalf("expected update for existing node ID, got different UUID")
			}
			return nil
		},
	}

	srv := newTestServerWithNodeStore(store, 15)
	body := `{"hostname":"node-1","tailscale_ip":"100.64.1.5","agent_listen_port":8090,"capacity_mb":24000,"capacity_cpu":8.0,"agent_version":"0.1.0"}`

	req := httptest.NewRequest(http.MethodPost, "/v1/nodes/register", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp registerResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Should return existing node_id
	expectedID := formatUUID(existingID)
	if resp.NodeID != expectedID {
		t.Fatalf("expected existing node_id %s, got %s", expectedID, resp.NodeID)
	}

	// Token should have been passed to UpdateNodeToken
	if capturedToken == "" {
		t.Fatal("expected UpdateNodeToken to be called with fresh token")
	}
	if len(capturedToken) != 64 {
		t.Fatalf("expected 64-char token, got %d chars", len(capturedToken))
	}
}

func TestRegisterNode_MissingHostname(t *testing.T) {
	store := &mockNodeStore{}
	srv := newTestServerWithNodeStore(store, 15)
	body := `{"tailscale_ip":"100.64.1.5","capacity_mb":24000,"agent_version":"0.1.0"}`

	req := httptest.NewRequest(http.MethodPost, "/v1/nodes/register", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRegisterNode_MissingTailscaleIP(t *testing.T) {
	store := &mockNodeStore{}
	srv := newTestServerWithNodeStore(store, 15)
	body := `{"hostname":"node-1","capacity_mb":24000,"agent_version":"0.1.0"}`

	req := httptest.NewRequest(http.MethodPost, "/v1/nodes/register", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRegisterNode_InvalidJSON(t *testing.T) {
	store := &mockNodeStore{}
	srv := newTestServerWithNodeStore(store, 15)

	req := httptest.NewRequest(http.MethodPost, "/v1/nodes/register", bytes.NewBufferString(`{invalid json`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRegisterNode_NoAuthRequired(t *testing.T) {
	// Registration should work without any auth token
	store := &mockNodeStore{
		getByHostnameAndIPFn: func(ctx context.Context, hostname string, ip netip.Addr) (nodeRecord, error) {
			return nodeRecord{}, pgx.ErrNoRows
		},
		createNodeFn: func(ctx context.Context, arg createNodeArgs) (nodeRecord, error) {
			return nodeRecord{ID: arg.ID, Hostname: arg.Hostname}, nil
		},
	}

	// Create server WITH auth configured -- registration should still not require it
	r := chi.NewRouter()
	s := &Server{
		router:                   r,
		config:                   &serverConfig{apiToken: "secret-token"},
		logger:                   testLogger(),
		nodeStore:                store,
		heartbeatIntervalSeconds: 15,
	}
	r.Post("/v1/nodes/register", s.handleRegisterNode)

	body := `{"hostname":"node-1","tailscale_ip":"100.64.1.5","capacity_mb":24000,"agent_version":"0.1.0"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/nodes/register", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 without auth, got %d: %s", w.Code, w.Body.String())
	}
}

// --- Test helpers ---

// newTestServerWithNodeStore creates a minimal Server for handler testing.
func newTestServerWithNodeStore(ns NodeStore, heartbeatInterval int) *Server {
	r := chi.NewRouter()
	s := &Server{
		router:                   r,
		config:                   &serverConfig{apiToken: "test-token"},
		logger:                   testLogger(),
		nodeStore:                ns,
		heartbeatIntervalSeconds: heartbeatInterval,
	}

	// Mount routes as they'll appear in production
	r.Post("/v1/nodes/register", s.handleRegisterNode)
	r.Group(func(r chi.Router) {
		r.Use(BearerAuth("test-token"))
		r.Post("/v1/nodes/{id}/heartbeat", s.handleHeartbeat)
	})

	return s
}
