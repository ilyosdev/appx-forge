package controlclient

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTestLogger returns a no-op logger for tests.
func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// testRegReq returns a standard registration request for tests.
func testRegReq() RegisterRequest {
	return RegisterRequest{
		Hostname:        "test-node",
		TailscaleIP:     "100.64.1.5",
		AgentListenPort: 8090,
		CapacityMB:      24000,
		CapacityCPU:     8.0,
		AgentVersion:    "0.1.0",
	}
}

func TestRegister_SendsCorrectJSON(t *testing.T) {
	var received RegisterRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/nodes/register" {
			t.Errorf("expected /v1/nodes/register, got %s", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", ct)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &received); err != nil {
			t.Fatalf("failed to unmarshal request body: %v", err)
		}

		resp := RegisterResponse{
			NodeID:                   "node-uuid-123",
			AgentToken:               "forge_agt_test_token",
			HeartbeatIntervalSeconds: 15,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, testRegReq(), newTestLogger())
	resp, err := c.Register(context.Background())
	if err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	// Verify request body
	if received.Hostname != "test-node" {
		t.Errorf("hostname: got %q, want %q", received.Hostname, "test-node")
	}
	if received.TailscaleIP != "100.64.1.5" {
		t.Errorf("tailscale_ip: got %q, want %q", received.TailscaleIP, "100.64.1.5")
	}
	if received.AgentListenPort != 8090 {
		t.Errorf("agent_listen_port: got %d, want %d", received.AgentListenPort, 8090)
	}
	if received.CapacityMB != 24000 {
		t.Errorf("capacity_mb: got %d, want %d", received.CapacityMB, 24000)
	}
	if received.CapacityCPU != 8.0 {
		t.Errorf("capacity_cpu: got %f, want %f", received.CapacityCPU, 8.0)
	}
	if received.AgentVersion != "0.1.0" {
		t.Errorf("agent_version: got %q, want %q", received.AgentVersion, "0.1.0")
	}

	// Verify response parsing
	if resp.NodeID != "node-uuid-123" {
		t.Errorf("node_id: got %q, want %q", resp.NodeID, "node-uuid-123")
	}
	if resp.AgentToken != "forge_agt_test_token" {
		t.Errorf("agent_token: got %q, want %q", resp.AgentToken, "forge_agt_test_token")
	}
	if resp.HeartbeatIntervalSeconds != 15 {
		t.Errorf("heartbeat_interval_seconds: got %d, want %d", resp.HeartbeatIntervalSeconds, 15)
	}
}

func TestRegister_RetriesOn5xx(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		resp := RegisterResponse{
			NodeID:                   "node-uuid-retry",
			AgentToken:               "forge_agt_retry_token",
			HeartbeatIntervalSeconds: 15,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, testRegReq(), newTestLogger())
	resp, err := c.Register(context.Background())
	if err != nil {
		t.Fatalf("Register returned error after retries: %v", err)
	}
	if resp.NodeID != "node-uuid-retry" {
		t.Errorf("node_id: got %q, want %q", resp.NodeID, "node-uuid-retry")
	}
	if got := int(attempts.Load()); got != 3 {
		t.Errorf("expected 3 attempts, got %d", got)
	}
}

func TestHeartbeat_SendsCorrectJSON(t *testing.T) {
	var received HeartbeatRequest
	var authHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/nodes/register" {
			resp := RegisterResponse{
				NodeID:                   "hb-node-id",
				AgentToken:               "hb-token",
				HeartbeatIntervalSeconds: 15,
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(resp)
			return
		}
		if r.URL.Path != "/v1/nodes/hb-node-id/heartbeat" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		authHeader = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, testRegReq(), newTestLogger())
	c.Register(context.Background())

	err := c.Heartbeat(context.Background(), HeartbeatRequest{
		UsedMB:            8500,
		RunningContainers: 12,
	})
	if err != nil {
		t.Fatalf("Heartbeat returned error: %v", err)
	}
	if received.UsedMB != 8500 {
		t.Errorf("used_mb: got %d, want %d", received.UsedMB, 8500)
	}
	if received.RunningContainers != 12 {
		t.Errorf("running_containers: got %d, want %d", received.RunningContainers, 12)
	}
	if authHeader != "Bearer hb-token" {
		t.Errorf("Authorization: got %q, want %q", authHeader, "Bearer hb-token")
	}
}

func TestHeartbeat_ReRegistersOn404(t *testing.T) {
	var registerCalls atomic.Int32
	var mu sync.Mutex
	nodeID := "old-node-id"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/nodes/register" {
			n := registerCalls.Add(1)
			mu.Lock()
			if n == 1 {
				nodeID = "old-node-id"
			} else {
				nodeID = "new-node-id"
			}
			mu.Unlock()
			resp := RegisterResponse{
				NodeID:                   nodeID,
				AgentToken:               "token-" + nodeID,
				HeartbeatIntervalSeconds: 15,
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(resp)
			return
		}

		// 404 on heartbeat with old node ID
		if strings.Contains(r.URL.Path, "old-node-id") {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		// Success on heartbeat with new node ID
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, testRegReq(), newTestLogger())
	c.Register(context.Background())

	err := c.Heartbeat(context.Background(), HeartbeatRequest{UsedMB: 100, RunningContainers: 1})
	if err != nil {
		t.Fatalf("Heartbeat returned error: %v", err)
	}

	got := int(registerCalls.Load())
	if got != 2 {
		t.Errorf("expected 2 register calls (initial + re-register on 404), got %d", got)
	}
}

func TestPollCommands_SendsCorrectRequest(t *testing.T) {
	var capturedPath string
	var capturedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/nodes/register" {
			resp := RegisterResponse{NodeID: "poll-node", AgentToken: "poll-token", HeartbeatIntervalSeconds: 15}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(resp)
			return
		}
		capturedPath = r.URL.RequestURI()
		capturedAuth = r.Header.Get("Authorization")
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		resp := CommandsResponse{Commands: nil}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, testRegReq(), newTestLogger())
	c.Register(context.Background())

	_, err := c.PollCommands(context.Background(), 30)
	if err != nil {
		t.Fatalf("PollCommands error: %v", err)
	}
	if capturedPath != "/v1/agents/poll-node/commands?wait=30" {
		t.Errorf("path: got %q, want %q", capturedPath, "/v1/agents/poll-node/commands?wait=30")
	}
	if capturedAuth != "Bearer poll-token" {
		t.Errorf("auth: got %q, want %q", capturedAuth, "Bearer poll-token")
	}
}

func TestPollCommands_ReturnsEmptyOnTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/nodes/register" {
			resp := RegisterResponse{NodeID: "empty-node", AgentToken: "empty-token", HeartbeatIntervalSeconds: 15}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(resp)
			return
		}
		resp := CommandsResponse{Commands: []Command{}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, testRegReq(), newTestLogger())
	c.Register(context.Background())

	cmds, err := c.PollCommands(context.Background(), 30)
	if err != nil {
		t.Fatalf("PollCommands error: %v", err)
	}
	if len(cmds) != 0 {
		t.Errorf("expected 0 commands, got %d", len(cmds))
	}
}

func TestPollCommands_ParsesCommandList(t *testing.T) {
	issuedAt := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/nodes/register" {
			resp := RegisterResponse{NodeID: "parse-node", AgentToken: "parse-token", HeartbeatIntervalSeconds: 15}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(resp)
			return
		}
		resp := CommandsResponse{
			Commands: []Command{
				{
					ID:             "cmd-1",
					Type:           "start_sandbox",
					SandboxID:      "sb-1",
					Payload:        json.RawMessage(`{"image":"appx/sandbox:v1"}`),
					IssuedAt:       issuedAt,
					TimeoutSeconds: 60,
				},
				{
					ID:             "cmd-2",
					Type:           "stop_sandbox",
					SandboxID:      "sb-2",
					Payload:        json.RawMessage(`{"container_id":"abc"}`),
					IssuedAt:       issuedAt,
					TimeoutSeconds: 30,
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, testRegReq(), newTestLogger())
	c.Register(context.Background())

	cmds, err := c.PollCommands(context.Background(), 30)
	if err != nil {
		t.Fatalf("PollCommands error: %v", err)
	}
	if len(cmds) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(cmds))
	}
	if cmds[0].ID != "cmd-1" {
		t.Errorf("cmd[0].ID: got %q, want %q", cmds[0].ID, "cmd-1")
	}
	if cmds[0].Type != "start_sandbox" {
		t.Errorf("cmd[0].Type: got %q, want %q", cmds[0].Type, "start_sandbox")
	}
	if cmds[0].SandboxID != "sb-1" {
		t.Errorf("cmd[0].SandboxID: got %q, want %q", cmds[0].SandboxID, "sb-1")
	}
	if cmds[0].TimeoutSeconds != 60 {
		t.Errorf("cmd[0].TimeoutSeconds: got %d, want %d", cmds[0].TimeoutSeconds, 60)
	}
	if !cmds[0].IssuedAt.Equal(issuedAt) {
		t.Errorf("cmd[0].IssuedAt: got %v, want %v", cmds[0].IssuedAt, issuedAt)
	}
	if cmds[1].Type != "stop_sandbox" {
		t.Errorf("cmd[1].Type: got %q, want %q", cmds[1].Type, "stop_sandbox")
	}
}

func TestAckCommand_SendsCorrectJSON(t *testing.T) {
	var received AckRequest
	var capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/nodes/register" {
			resp := RegisterResponse{NodeID: "ack-node", AgentToken: "ack-token", HeartbeatIntervalSeconds: 15}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(resp)
			return
		}
		capturedPath = r.URL.Path
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, testRegReq(), newTestLogger())
	c.Register(context.Background())

	err := c.AckCommand(context.Background(), "cmd-abc", AckRequest{
		Status: "success",
		Result: map[string]interface{}{"container_id": "abc123", "host_port": float64(43210)},
	})
	if err != nil {
		t.Fatalf("AckCommand error: %v", err)
	}
	if capturedPath != "/v1/agents/ack-node/commands/cmd-abc/ack" {
		t.Errorf("path: got %q, want %q", capturedPath, "/v1/agents/ack-node/commands/cmd-abc/ack")
	}
	if received.Status != "success" {
		t.Errorf("status: got %q, want %q", received.Status, "success")
	}
}

func TestReportEvent_SendsCorrectJSON(t *testing.T) {
	var received EventReport
	var capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/nodes/register" {
			resp := RegisterResponse{NodeID: "evt-node", AgentToken: "evt-token", HeartbeatIntervalSeconds: 15}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(resp)
			return
		}
		capturedPath = r.URL.Path
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, testRegReq(), newTestLogger())
	c.Register(context.Background())

	err := c.ReportEvent(context.Background(), EventReport{
		SandboxID:   "sb-uuid-1",
		EventType:   "container_exited",
		ContainerID: "abc123",
		ExitCode:    137,
	})
	if err != nil {
		t.Fatalf("ReportEvent error: %v", err)
	}
	if capturedPath != "/v1/agents/evt-node/events" {
		t.Errorf("path: got %q, want %q", capturedPath, "/v1/agents/evt-node/events")
	}
	if received.SandboxID != "sb-uuid-1" {
		t.Errorf("sandbox_id: got %q, want %q", received.SandboxID, "sb-uuid-1")
	}
	if received.EventType != "container_exited" {
		t.Errorf("event_type: got %q, want %q", received.EventType, "container_exited")
	}
	if received.ContainerID != "abc123" {
		t.Errorf("container_id: got %q, want %q", received.ContainerID, "abc123")
	}
	if received.ExitCode != 137 {
		t.Errorf("exit_code: got %d, want %d", received.ExitCode, 137)
	}
}

func TestAllAuthenticatedRequests_IncludeBearerHeader(t *testing.T) {
	var authHeaders []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/nodes/register" {
			resp := RegisterResponse{NodeID: "auth-node", AgentToken: "auth-token-123", HeartbeatIntervalSeconds: 15}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(resp)
			return
		}
		mu.Lock()
		authHeaders = append(authHeaders, r.Header.Get("Authorization"))
		mu.Unlock()
		if strings.Contains(r.URL.Path, "commands") && r.Method == http.MethodGet {
			resp := CommandsResponse{Commands: nil}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, testRegReq(), newTestLogger())
	c.Register(context.Background())

	// Make requests to all authenticated endpoints
	c.Heartbeat(context.Background(), HeartbeatRequest{UsedMB: 100, RunningContainers: 1})
	c.PollCommands(context.Background(), 30)
	c.AckCommand(context.Background(), "cmd-1", AckRequest{Status: "success"})
	c.ReportEvent(context.Background(), EventReport{SandboxID: "sb-1", EventType: "container_started"})

	mu.Lock()
	defer mu.Unlock()

	if len(authHeaders) != 4 {
		t.Fatalf("expected 4 auth headers, got %d", len(authHeaders))
	}
	for i, h := range authHeaders {
		if h != "Bearer auth-token-123" {
			t.Errorf("request %d: auth header got %q, want %q", i, h, "Bearer auth-token-123")
		}
	}
}

func TestClient_ReRegistersOn401(t *testing.T) {
	var registerCalls atomic.Int32
	var tokenVersion atomic.Int32
	tokenVersion.Store(1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/nodes/register" {
			n := registerCalls.Add(1)
			_ = n
			v := tokenVersion.Add(1)
			resp := RegisterResponse{
				NodeID:                   "reauth-node",
				AgentToken:               "token-v" + string(rune('0'+v)),
				HeartbeatIntervalSeconds: 15,
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(resp)
			return
		}

		auth := r.Header.Get("Authorization")
		// First attempt with old token gets 401, retry with new token succeeds
		if auth == "Bearer token-v2" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		if strings.Contains(r.URL.Path, "commands") && r.Method == http.MethodGet {
			resp := CommandsResponse{Commands: nil}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, testRegReq(), newTestLogger())
	c.Register(context.Background())

	// This should trigger 401, re-register, and retry
	cmds, err := c.PollCommands(context.Background(), 30)
	if err != nil {
		t.Fatalf("PollCommands after re-register: %v", err)
	}
	if cmds == nil {
		// nil is acceptable for empty commands
	}
	got := int(registerCalls.Load())
	if got < 2 {
		t.Errorf("expected at least 2 register calls (initial + re-register on 401), got %d", got)
	}
}

func TestPollCommands_HTTPTimeoutIsWaitPlusFive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/nodes/register" {
			resp := RegisterResponse{NodeID: "timeout-node", AgentToken: "timeout-token", HeartbeatIntervalSeconds: 15}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(resp)
			return
		}

		// Hold the connection for longer than wait+5 to trigger timeout
		// Use wait=1 for the test so timeout = 6s.
		// We hold for 10s which should cause a client-side timeout.
		time.Sleep(10 * time.Second)
		resp := CommandsResponse{Commands: nil}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, testRegReq(), newTestLogger())
	c.Register(context.Background())

	start := time.Now()
	_, err := c.PollCommands(context.Background(), 1) // timeout = 1+5 = 6s
	elapsed := time.Since(start)

	// Should timeout (error expected)
	if err == nil {
		t.Error("expected timeout error from PollCommands, got nil")
	}

	// Should timeout around 6s (with some tolerance)
	if elapsed < 5*time.Second || elapsed > 8*time.Second {
		t.Errorf("expected timeout around 6s, took %v", elapsed)
	}
}
