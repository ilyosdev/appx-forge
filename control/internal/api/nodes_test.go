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
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/appx/forge/control/internal/store"
)

// --- Mock store for node handlers ---

// mockNodeStore implements NodeStore for testing registration, heartbeat, and node management.
type mockNodeStore struct {
	getByHostnameAndIPFn       func(ctx context.Context, hostname string, ip netip.Addr) (NodeRecord, error)
	createNodeFn               func(ctx context.Context, arg CreateNodeArgs) (NodeRecord, error)
	updateNodeTokenFn          func(ctx context.Context, token string, agentVersion string, id pgtype.UUID) error
	getNodeFn                  func(ctx context.Context, id pgtype.UUID) (NodeRecord, error)
	updateHeartbeatFn          func(ctx context.Context, id pgtype.UUID, usedMb int32, runningContainers int32) error
	listNodesFn                func(ctx context.Context) ([]store.Node, error)
	updateNodeStatusFn         func(ctx context.Context, id pgtype.UUID, status string) error
	countActiveSandboxesFn     func(ctx context.Context, nodeID pgtype.UUID) (int32, error)
}

func (m *mockNodeStore) GetNodeByHostnameAndIP(ctx context.Context, hostname string, ip netip.Addr) (NodeRecord, error) {
	if m.getByHostnameAndIPFn != nil {
		return m.getByHostnameAndIPFn(ctx, hostname, ip)
	}
	return NodeRecord{}, pgx.ErrNoRows
}

func (m *mockNodeStore) CreateNode(ctx context.Context, arg CreateNodeArgs) (NodeRecord, error) {
	if m.createNodeFn != nil {
		return m.createNodeFn(ctx, arg)
	}
	return NodeRecord{}, nil
}

func (m *mockNodeStore) UpdateNodeToken(ctx context.Context, token string, agentVersion string, id pgtype.UUID) error {
	if m.updateNodeTokenFn != nil {
		return m.updateNodeTokenFn(ctx, token, agentVersion, id)
	}
	return nil
}

func (m *mockNodeStore) GetNode(ctx context.Context, id pgtype.UUID) (NodeRecord, error) {
	if m.getNodeFn != nil {
		return m.getNodeFn(ctx, id)
	}
	return NodeRecord{}, pgx.ErrNoRows
}

func (m *mockNodeStore) UpdateNodeHeartbeat(ctx context.Context, id pgtype.UUID, usedMb int32, runningContainers int32) error {
	if m.updateHeartbeatFn != nil {
		return m.updateHeartbeatFn(ctx, id, usedMb, runningContainers)
	}
	return nil
}

func (m *mockNodeStore) ListNodes(ctx context.Context) ([]store.Node, error) {
	if m.listNodesFn != nil {
		return m.listNodesFn(ctx)
	}
	return []store.Node{}, nil
}

func (m *mockNodeStore) UpdateNodeStatus(ctx context.Context, id pgtype.UUID, status string) error {
	if m.updateNodeStatusFn != nil {
		return m.updateNodeStatusFn(ctx, id, status)
	}
	return nil
}

func (m *mockNodeStore) CountActiveSandboxesByNode(ctx context.Context, nodeID pgtype.UUID) (int32, error) {
	if m.countActiveSandboxesFn != nil {
		return m.countActiveSandboxesFn(ctx, nodeID)
	}
	return 0, nil
}

// testLogger returns a discard logger for tests.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// --- Registration tests ---

func TestRegisterNode_ValidRequest(t *testing.T) {
	store := &mockNodeStore{
		getByHostnameAndIPFn: func(ctx context.Context, hostname string, ip netip.Addr) (NodeRecord, error) {
			return NodeRecord{}, pgx.ErrNoRows
		},
		createNodeFn: func(ctx context.Context, arg CreateNodeArgs) (NodeRecord, error) {
			return NodeRecord{ID: arg.ID, Hostname: arg.Hostname}, nil
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
		getByHostnameAndIPFn: func(ctx context.Context, hostname string, ip netip.Addr) (NodeRecord, error) {
			return NodeRecord{ID: existingID, Hostname: hostname}, nil
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
		getByHostnameAndIPFn: func(ctx context.Context, hostname string, ip netip.Addr) (NodeRecord, error) {
			return NodeRecord{}, pgx.ErrNoRows
		},
		createNodeFn: func(ctx context.Context, arg CreateNodeArgs) (NodeRecord, error) {
			return NodeRecord{ID: arg.ID, Hostname: arg.Hostname}, nil
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

// --- Heartbeat tests ---

func TestHeartbeat_ValidRequest(t *testing.T) {
	nodeID := pgtype.UUID{Valid: true}
	copy(nodeID.Bytes[:], []byte("heartbeatnodeuui"))

	var capturedUsedMb int32
	var capturedRunning int32
	store := &mockNodeStore{
		getNodeFn: func(ctx context.Context, id pgtype.UUID) (NodeRecord, error) {
			if id != nodeID {
				t.Fatalf("unexpected node ID in GetNode")
			}
			return NodeRecord{ID: nodeID, Hostname: "node-1"}, nil
		},
		updateHeartbeatFn: func(ctx context.Context, id pgtype.UUID, usedMb int32, runningContainers int32) error {
			capturedUsedMb = usedMb
			capturedRunning = runningContainers
			return nil
		},
	}

	srv := newTestServerWithNodeStore(store, 15)
	body := `{"used_mb":8500,"running_containers":12}`

	nodeIDStr := formatUUID(nodeID)
	req := httptest.NewRequest(http.MethodPost, "/v1/nodes/"+nodeIDStr+"/heartbeat", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if capturedUsedMb != 8500 {
		t.Fatalf("expected used_mb=8500, got %d", capturedUsedMb)
	}
	if capturedRunning != 12 {
		t.Fatalf("expected running_containers=12, got %d", capturedRunning)
	}
}

func TestHeartbeat_UnknownNode(t *testing.T) {
	store := &mockNodeStore{
		getNodeFn: func(ctx context.Context, id pgtype.UUID) (NodeRecord, error) {
			return NodeRecord{}, pgx.ErrNoRows
		},
	}

	srv := newTestServerWithNodeStore(store, 15)
	body := `{"used_mb":1000,"running_containers":2}`

	// Use a valid UUID that doesn't exist in the store
	req := httptest.NewRequest(http.MethodPost, "/v1/nodes/00000000-0000-0000-0000-000000000099/heartbeat", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHeartbeat_NoAuth(t *testing.T) {
	store := &mockNodeStore{}
	srv := newTestServerWithNodeStore(store, 15)
	body := `{"used_mb":1000,"running_containers":2}`

	req := httptest.NewRequest(http.MethodPost, "/v1/nodes/00000000-0000-0000-0000-000000000001/heartbeat", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	// No Authorization header
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHeartbeat_InvalidUUID(t *testing.T) {
	store := &mockNodeStore{}
	srv := newTestServerWithNodeStore(store, 15)
	body := `{"used_mb":1000,"running_containers":2}`

	req := httptest.NewRequest(http.MethodPost, "/v1/nodes/not-a-uuid/heartbeat", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// --- List Nodes tests ---

func TestListNodes_ReturnsNodeList(t *testing.T) {
	nodeID := pgtype.UUID{Valid: true}
	copy(nodeID.Bytes[:], []byte("listnodeuuid0001"))

	now := time.Now().UTC().Truncate(time.Microsecond)

	ns := &mockNodeStore{
		listNodesFn: func(ctx context.Context) ([]store.Node, error) {
			return []store.Node{
				{
					ID:                nodeID,
					Hostname:          "vds-1",
					TailscaleIp:       netip.MustParseAddr("100.64.1.5"),
					AgentListenPort:   8090,
					CapacityMb:        24000,
					UsedMb:            8500,
					RunningContainers: 12,
					Status:            "healthy",
					AgentVersion:      "0.1.0",
					LastSeenAt:        pgtype.Timestamptz{Time: now, Valid: true},
					RegisteredAt:      pgtype.Timestamptz{Time: now.Add(-24 * time.Hour), Valid: true},
					AgentToken:        "secret-token-should-never-appear",
				},
			}, nil
		},
	}

	srv := newTestServerWithNodeStore(ns, 15)
	req := httptest.NewRequest(http.MethodGet, "/v1/nodes", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Nodes []json.RawMessage `json:"nodes"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(resp.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(resp.Nodes))
	}

	// Verify agent_token is NEVER exposed (T-06-01)
	raw := string(resp.Nodes[0])
	if bytes.Contains([]byte(raw), []byte("agent_token")) {
		t.Fatal("agent_token MUST NOT be exposed in list nodes response")
	}
	if bytes.Contains([]byte(raw), []byte("secret-token-should-never-appear")) {
		t.Fatal("agent_token value leaked in response")
	}

	// Verify expected fields are present
	var node map[string]interface{}
	if err := json.Unmarshal(resp.Nodes[0], &node); err != nil {
		t.Fatalf("unmarshal node: %v", err)
	}
	for _, field := range []string{"id", "hostname", "tailscale_ip", "capacity_mb", "used_mb", "status", "running_sandboxes", "agent_version", "last_seen_at", "registered_at"} {
		if _, ok := node[field]; !ok {
			t.Errorf("missing expected field %q in node response", field)
		}
	}
}

func TestListNodes_EmptyList(t *testing.T) {
	ns := &mockNodeStore{
		listNodesFn: func(ctx context.Context) ([]store.Node, error) {
			return []store.Node{}, nil
		},
	}

	srv := newTestServerWithNodeStore(ns, 15)
	req := httptest.NewRequest(http.MethodGet, "/v1/nodes", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Nodes []json.RawMessage `json:"nodes"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(resp.Nodes) != 0 {
		t.Fatalf("expected 0 nodes, got %d", len(resp.Nodes))
	}
}

// --- Drain Node tests ---

func TestDrainNode_ValidRequest(t *testing.T) {
	nodeID := pgtype.UUID{Valid: true}
	copy(nodeID.Bytes[:], []byte("drainnodeuuid001"))

	var capturedStatus string
	ns := &mockNodeStore{
		getNodeFn: func(ctx context.Context, id pgtype.UUID) (NodeRecord, error) {
			if id != nodeID {
				t.Fatalf("unexpected node ID")
			}
			return NodeRecord{ID: nodeID, Hostname: "vds-1"}, nil
		},
		updateNodeStatusFn: func(ctx context.Context, id pgtype.UUID, status string) error {
			capturedStatus = status
			return nil
		},
	}

	srv := newTestServerWithNodeStore(ns, 15)
	nodeIDStr := formatUUID(nodeID)
	req := httptest.NewRequest(http.MethodPost, "/v1/nodes/"+nodeIDStr+"/drain", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if capturedStatus != "draining" {
		t.Fatalf("expected status 'draining', got %q", capturedStatus)
	}
}

func TestDrainNode_UnknownNode(t *testing.T) {
	ns := &mockNodeStore{
		getNodeFn: func(ctx context.Context, id pgtype.UUID) (NodeRecord, error) {
			return NodeRecord{}, pgx.ErrNoRows
		},
	}

	srv := newTestServerWithNodeStore(ns, 15)
	req := httptest.NewRequest(http.MethodPost, "/v1/nodes/00000000-0000-0000-0000-000000000099/drain", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// --- Remove Node tests ---

func TestRemoveNode_ZeroSandboxes(t *testing.T) {
	nodeID := pgtype.UUID{Valid: true}
	copy(nodeID.Bytes[:], []byte("removenodeuuid01"))

	var capturedStatus string
	ns := &mockNodeStore{
		getNodeFn: func(ctx context.Context, id pgtype.UUID) (NodeRecord, error) {
			return NodeRecord{ID: nodeID, Hostname: "vds-1"}, nil
		},
		countActiveSandboxesFn: func(ctx context.Context, nid pgtype.UUID) (int32, error) {
			return 0, nil
		},
		updateNodeStatusFn: func(ctx context.Context, id pgtype.UUID, status string) error {
			capturedStatus = status
			return nil
		},
	}

	srv := newTestServerWithNodeStore(ns, 15)
	nodeIDStr := formatUUID(nodeID)
	req := httptest.NewRequest(http.MethodDelete, "/v1/nodes/"+nodeIDStr, nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if capturedStatus != "removed" {
		t.Fatalf("expected status 'removed', got %q", capturedStatus)
	}
}

func TestRemoveNode_HasActiveSandboxes(t *testing.T) {
	nodeID := pgtype.UUID{Valid: true}
	copy(nodeID.Bytes[:], []byte("removenodeuuid02"))

	ns := &mockNodeStore{
		getNodeFn: func(ctx context.Context, id pgtype.UUID) (NodeRecord, error) {
			return NodeRecord{ID: nodeID, Hostname: "vds-1"}, nil
		},
		countActiveSandboxesFn: func(ctx context.Context, nid pgtype.UUID) (int32, error) {
			return 3, nil
		},
	}

	srv := newTestServerWithNodeStore(ns, 15)
	nodeIDStr := formatUUID(nodeID)
	req := httptest.NewRequest(http.MethodDelete, "/v1/nodes/"+nodeIDStr, nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRemoveNode_UnknownNode(t *testing.T) {
	ns := &mockNodeStore{
		getNodeFn: func(ctx context.Context, id pgtype.UUID) (NodeRecord, error) {
			return NodeRecord{}, pgx.ErrNoRows
		},
	}

	srv := newTestServerWithNodeStore(ns, 15)
	req := httptest.NewRequest(http.MethodDelete, "/v1/nodes/00000000-0000-0000-0000-000000000099", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
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
		r.Get("/v1/nodes", s.handleListNodes)
		r.Post("/v1/nodes/{id}/drain", s.handleDrainNode)
		r.Delete("/v1/nodes/{id}", s.handleRemoveNode)
	})

	return s
}
