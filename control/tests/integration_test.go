package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/appx/forge/control/internal/api"
	"github.com/appx/forge/control/internal/lifecycle"
	"github.com/appx/forge/control/internal/scheduler"
	"github.com/appx/forge/control/internal/store"
	"github.com/appx/forge/control/tests/testhelpers"
	"github.com/appx/forge/shared-go/models"
)

// ── Shared test helpers ────────────────────────────────────────────────

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// integrationAdapter bridges store.Queries to the interfaces needed by
// api.Server and lifecycle.LifecycleService for integration tests.
type integrationAdapter struct {
	q    *store.Queries
	pool *pgxpool.Pool
}

// ── api.NodeStore ──────────────────────────────────────────────────────

func (a *integrationAdapter) GetNodeByHostnameAndIP(ctx context.Context, hostname string, ip netip.Addr) (api.NodeRecord, error) {
	n, err := a.q.GetNodeByHostnameAndIP(ctx, store.GetNodeByHostnameAndIPParams{
		Hostname:    hostname,
		TailscaleIp: ip,
	})
	if err != nil {
		return api.NodeRecord{}, err
	}
	return api.NodeRecord{ID: n.ID, Hostname: n.Hostname}, nil
}

func (a *integrationAdapter) CreateNode(ctx context.Context, arg api.CreateNodeArgs) (api.NodeRecord, error) {
	// pgtype.Numeric.Scan does not accept float64; construct directly
	scaled := int64(arg.CapacityCPU * 1000)
	cpuNum := pgtype.Numeric{Int: big.NewInt(scaled), Exp: -3, Valid: true}

	n, err := a.q.CreateNode(ctx, store.CreateNodeParams{
		ID:              arg.ID,
		Hostname:        arg.Hostname,
		TailscaleIp:     arg.TailscaleIP,
		AgentListenPort: arg.AgentListenPort,
		CapacityMb:      arg.CapacityMb,
		CapacityCpu:     cpuNum,
		AgentVersion:    arg.AgentVersion,
		Metadata:        arg.Metadata,
	})
	if err != nil {
		return api.NodeRecord{}, err
	}
	if arg.AgentToken != "" {
		_ = a.q.UpdateNodeToken(ctx, store.UpdateNodeTokenParams{
			AgentToken:   arg.AgentToken,
			AgentVersion: arg.AgentVersion,
			ID:           arg.ID,
		})
	}
	return api.NodeRecord{ID: n.ID, Hostname: n.Hostname}, nil
}

func (a *integrationAdapter) UpdateNodeToken(ctx context.Context, token, agentVersion string, id pgtype.UUID) error {
	return a.q.UpdateNodeToken(ctx, store.UpdateNodeTokenParams{
		AgentToken:   token,
		AgentVersion: agentVersion,
		ID:           id,
	})
}

func (a *integrationAdapter) GetNode(ctx context.Context, id pgtype.UUID) (api.NodeRecord, error) {
	n, err := a.q.GetNode(ctx, id)
	if err != nil {
		return api.NodeRecord{}, err
	}
	return api.NodeRecord{ID: n.ID, Hostname: n.Hostname}, nil
}

func (a *integrationAdapter) UpdateNodeHeartbeat(ctx context.Context, id pgtype.UUID, usedMb, runningContainers int32) error {
	return a.q.UpdateNodeHeartbeat(ctx, store.UpdateNodeHeartbeatParams{
		ID:                id,
		UsedMb:            usedMb,
		RunningContainers: runningContainers,
	})
}

func (a *integrationAdapter) ListNodes(ctx context.Context) ([]store.Node, error) {
	return a.q.ListNodes(ctx)
}

func (a *integrationAdapter) UpdateNodeStatus(ctx context.Context, id pgtype.UUID, status string) error {
	return a.q.UpdateNodeStatus(ctx, store.UpdateNodeStatusParams{
		ID:     id,
		Status: status,
	})
}

func (a *integrationAdapter) CountActiveSandboxesByNode(ctx context.Context, nodeID pgtype.UUID) (int32, error) {
	return a.q.CountActiveSandboxesByNode(ctx, nodeID)
}

func (a *integrationAdapter) CountSchedulableSandboxesByNode(ctx context.Context, nodeID pgtype.UUID) (int32, error) {
	return a.q.CountSchedulableSandboxesByNode(ctx, nodeID)
}

// ── api.SandboxReader ──────────────────────────────────────────────────

func (a *integrationAdapter) GetSandbox(ctx context.Context, id pgtype.UUID) (store.Sandbox, error) {
	return a.q.GetSandbox(ctx, id)
}

func (a *integrationAdapter) GetSandboxByAppName(ctx context.Context, appName string) (store.Sandbox, error) {
	return a.q.GetSandboxByAppName(ctx, appName)
}

func (a *integrationAdapter) ListSandboxes(ctx context.Context, limit int32) ([]store.Sandbox, error) {
	return a.q.ListSandboxes(ctx, limit)
}

func (a *integrationAdapter) ListSandboxesByState(ctx context.Context, state string) ([]store.Sandbox, error) {
	return a.q.ListSandboxesByState(ctx, state)
}

func (a *integrationAdapter) ListSandboxesByNode(ctx context.Context, nodeID pgtype.UUID) ([]store.Sandbox, error) {
	return a.q.ListSandboxesByNode(ctx, nodeID)
}

func (a *integrationAdapter) ListSandboxesByUser(ctx context.Context, userID string) ([]store.Sandbox, error) {
	return a.q.ListSandboxesByUser(ctx, userID)
}

// ── api.AgentStore ─────────────────────────────────────────────────────

func (a *integrationAdapter) PollPendingCommands(ctx context.Context, nodeID pgtype.UUID) ([]store.Command, error) {
	return a.q.PollPendingCommands(ctx, nodeID)
}

func (a *integrationAdapter) GetCommand(ctx context.Context, id pgtype.UUID) (store.Command, error) {
	return a.q.GetCommand(ctx, id)
}

// ── lifecycle.Store ────────────────────────────────────────────────────

func (a *integrationAdapter) CreateSandbox(ctx context.Context, arg store.CreateSandboxParams) (store.Sandbox, error) {
	return a.q.CreateSandbox(ctx, arg)
}

func (a *integrationAdapter) DeleteSandbox(ctx context.Context, id pgtype.UUID) error {
	return a.q.DeleteSandbox(ctx, id)
}

func (a *integrationAdapter) ListHealthyNodes(ctx context.Context) ([]store.Node, error) {
	return a.q.ListHealthyNodes(ctx)
}

func (a *integrationAdapter) AssignSandboxToNode(ctx context.Context, arg store.AssignSandboxToNodeParams) (store.Sandbox, error) {
	return a.q.AssignSandboxToNode(ctx, arg)
}

func (a *integrationAdapter) AssignSandboxToNodeUnderCap(ctx context.Context, nodeID, sandboxID pgtype.UUID, cap int32) (bool, store.Sandbox, error) {
	return store.AssignPendingSandboxUnderCap(ctx, a.pool, nodeID, sandboxID, cap)
}

func (a *integrationAdapter) TransitionSandboxState(ctx context.Context, arg store.TransitionSandboxStateParams) (store.Sandbox, error) {
	return a.q.TransitionSandboxState(ctx, arg)
}

func (a *integrationAdapter) UpdateSandboxRuntime(ctx context.Context, arg store.UpdateSandboxRuntimeParams) error {
	return a.q.UpdateSandboxRuntime(ctx, arg)
}

func (a *integrationAdapter) CreateCommand(ctx context.Context, arg store.CreateCommandParams) (store.Command, error) {
	return a.q.CreateCommand(ctx, arg)
}

func (a *integrationAdapter) AckCommand(ctx context.Context, arg store.AckCommandParams) error {
	return a.q.AckCommand(ctx, arg)
}

func (a *integrationAdapter) RecordEvent(ctx context.Context, arg store.RecordEventParams) (store.Event, error) {
	return a.q.RecordEvent(ctx, arg)
}

func (a *integrationAdapter) DeleteCommandsForSandbox(ctx context.Context, sandboxID pgtype.UUID) error {
	return a.q.DeleteCommandsForSandbox(ctx, sandboxID)
}

func (a *integrationAdapter) GetNodeByID(ctx context.Context, id pgtype.UUID) (store.Node, error) {
	return a.q.GetNode(ctx, id)
}

// ── scheduler.SandboxStore (Phase 30 reconciler) ───────────────────────
//
// Mirrors main.go's reconcilerStoreAdapter so the integration server gets
// the same wiring path the production binary uses. Lets T21+ heartbeat
// reconcile tests exercise the real bump/agent-lost code paths against
// real Postgres rather than a fake store.

func (a *integrationAdapter) ListSandboxesForNode(ctx context.Context, nodeID pgtype.UUID) ([]scheduler.SandboxRow, error) {
	rows, err := a.q.ListSandboxesForNode(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	out := make([]scheduler.SandboxRow, len(rows))
	for i, r := range rows {
		out[i] = scheduler.SandboxRow{
			AppName:   r.AppName,
			State:     r.State,
			CreatedAt: r.CreatedAt.Time,
		}
	}
	return out, nil
}

func (a *integrationAdapter) MarkSandboxVerified(ctx context.Context, appName, state string) error {
	return a.q.MarkSandboxVerified(ctx, store.MarkSandboxVerifiedParams{
		AppName: appName,
		State:   state,
	})
}

func (a *integrationAdapter) MarkSandboxAgentLost(ctx context.Context, appName string, nodeID pgtype.UUID) error {
	return a.q.MarkSandboxAgentLost(ctx, store.MarkSandboxAgentLostParams{
		AppName: appName,
		NodeID:  nodeID,
	})
}

// Phase 33-Real-7 — terminal-row containers the agent still observes.
func (a *integrationAdapter) ListTerminalSandboxesForNode(ctx context.Context, nodeID pgtype.UUID) ([]scheduler.TerminalSandboxRow, error) {
	rows, err := a.q.ListTerminalSandboxesForNode(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	out := make([]scheduler.TerminalSandboxRow, 0, len(rows))
	for _, r := range rows {
		if !r.ContainerID.Valid || r.ContainerID.String == "" {
			continue
		}
		out = append(out, scheduler.TerminalSandboxRow{
			ID:          r.ID,
			AppName:     r.AppName,
			ContainerID: r.ContainerID.String,
		})
	}
	return out, nil
}

// DispatchStopSandbox creates a stop_sandbox command targeted at the
// node so the agent destroys the orphan container. Mirrors main.go's
// reconcilerStoreAdapter.DispatchStopSandbox.
func (a *integrationAdapter) DispatchStopSandbox(ctx context.Context, sandboxID, nodeID pgtype.UUID, containerID, reason string) error {
	cmdPayload, err := json.Marshal(map[string]interface{}{
		"container_id": containerID,
		"reason":       reason,
	})
	if err != nil {
		return err
	}
	cmdID := uuid.New()
	_, err = a.q.CreateCommand(ctx, store.CreateCommandParams{
		ID:             pgtype.UUID{Bytes: cmdID, Valid: true},
		NodeID:         nodeID,
		SandboxID:      sandboxID,
		CommandType:    string(models.CmdStopSandbox),
		Payload:        cmdPayload,
		TimeoutSeconds: 60,
	})
	return err
}

// reconcilerAPIAdapter satisfies api.Reconciler by translating
// api.ContainerInfo → scheduler.ContainerInfo. Identical pattern to
// main.go's reconcilerAdapter; redeclared here because the production
// adapter is a private type in cmd/forge-control.
type reconcilerAPIAdapter struct {
	r *scheduler.HeartbeatReconciler
}

func (a *reconcilerAPIAdapter) Reconcile(ctx context.Context, nodeID pgtype.UUID, containers []api.ContainerInfo) error {
	out := make([]scheduler.ContainerInfo, len(containers))
	for i, c := range containers {
		out[i] = scheduler.ContainerInfo{
			AppName:     c.AppName,
			State:       c.State,
			HostPort:    c.HostPort,
			ContainerID: c.ContainerID,
		}
	}
	return a.r.Reconcile(ctx, nodeID, out)
}

// ── Setup helper ───────────────────────────────────────────────────────

const testToken = "integration-test-token"

// setupIntegrationServer creates a fully wired api.Server backed by a real
// Postgres container. Returns the httptest server URL and the queries handle.
//
// Tests that need raw SQL (e.g. seeding controlled created_at / verified_at
// timestamps for reconciler tests) should use setupIntegrationServerWithPool
// below instead.
func setupIntegrationServer(t *testing.T) (string, *store.Queries) {
	url, queries, _ := setupIntegrationServerWithPool(t)
	return url, queries
}

// setupIntegrationServerWithPool is identical to setupIntegrationServer but
// also returns the underlying pgxpool so callers can issue raw SQL. Phase 30
// reconciler integration tests need this for fixture timestamps that the
// sqlc API can't set directly.
func setupIntegrationServerWithPool(t *testing.T) (string, *store.Queries, *pgxpool.Pool) {
	t.Helper()

	connStr, ctr := testhelpers.SetupTestDB(t)
	ctx := context.Background()

	if err := ctr.Restore(ctx); err != nil {
		t.Fatalf("restoring snapshot: %v", err)
	}

	pool := testhelpers.ConnectPool(t, connStr)
	queries := store.New(pool)
	adapter := &integrationAdapter{q: queries, pool: pool}

	lc := lifecycle.New(adapter, discardLogger())

	srv := api.NewServer(
		api.NewServerConfig(testToken, "test-hmac-secret"),
		pool,
		discardLogger(),
		adapter,
		lc,
		adapter,
		15,
	)
	srv.SetAgentDeps(adapter, lc)

	// Phase 30 — wire heartbeat reconciler. Backwards compat for legacy
	// (non-rich) heartbeats: handler still 200s when req.Containers is
	// nil; the reconciler is only invoked on rich heartbeats. Existing
	// integration tests that send legacy heartbeats are unaffected.
	heartbeatReconciler := scheduler.NewHeartbeatReconciler(adapter, discardLogger())
	srv.SetReconciler(&reconcilerAPIAdapter{r: heartbeatReconciler})

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	return ts.URL, queries, pool
}

// ── Integration Tests ──────────────────────────────────────────────────

// TestIntegration_HealthzReturnsOK verifies that the healthz endpoint returns
// 200 OK when connected to a real Postgres database.
func TestIntegration_HealthzReturnsOK(t *testing.T) {
	baseURL, _ := setupIntegrationServer(t)

	resp, err := http.Get(baseURL + "/v1/healthz")
	if err != nil {
		t.Fatalf("GET /v1/healthz: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(body))
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode healthz response: %v", err)
	}

	if status, ok := result["status"].(string); !ok || status != "ok" {
		t.Fatalf("expected status 'ok', got %v", result["status"])
	}
	if pg, ok := result["postgres"].(string); !ok || pg != "ok" {
		t.Fatalf("expected postgres 'ok', got %v", result["postgres"])
	}
}

// TestIntegration_CreateSandbox_DispatchesCommand verifies the full sandbox
// creation flow: create sandbox -> schedule to node -> dispatch start_sandbox
// command -> sandbox in "starting" state with command in commands table.
func TestIntegration_CreateSandbox_DispatchesCommand(t *testing.T) {
	baseURL, queries := setupIntegrationServer(t)
	ctx := context.Background()

	// Insert a healthy node directly in the DB
	nodeID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	_, err := queries.CreateNode(ctx, store.CreateNodeParams{
		ID:              nodeID,
		Hostname:        "integration-node-1",
		TailscaleIp:     mustParseAddr("100.64.1.10"),
		AgentListenPort: 8090,
		CapacityMb:      24000,
		CapacityCpu:     pgtype.Numeric{Int: big.NewInt(8), Exp: 0, Valid: true},
		AgentVersion:    "v0.1.0",
		Metadata:        []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("creating test node: %v", err)
	}

	// POST /v1/sandboxes
	createBody := `{
		"app_name": "integ-test-app",
		"user_id": "user-integ",
		"image": "appx/sandbox:v1",
		"resources": {"cpu_cores": 0.5, "memory_mb": 512},
		"env": {"NODE_ENV": "test"}
	}`

	req, _ := http.NewRequest(http.MethodPost, baseURL+"/v1/sandboxes", bytes.NewBufferString(createBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/sandboxes: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, string(body))
	}

	var createResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	sandboxID, ok := createResp["id"].(string)
	if !ok || sandboxID == "" {
		t.Fatalf("expected non-empty sandbox id, got %v", createResp["id"])
	}

	state, ok := createResp["state"].(string)
	if !ok || state != "starting" {
		t.Fatalf("expected state 'starting', got %v", createResp["state"])
	}

	// Verify a start_sandbox command was created for the node
	cmds, err := queries.PollPendingCommands(ctx, nodeID)
	if err != nil {
		t.Fatalf("polling commands: %v", err)
	}
	// Commands are already dispatched by lifecycle, so poll returns dispatched ones.
	// Instead, check directly via raw query.
	// The lifecycle service creates commands with status 'pending', then PollPendingCommands
	// atomically marks them 'dispatched'. Since we haven't polled yet, they should be pending.
	// Actually PollPendingCommands above just polled them, so they're dispatched now.
	// The fact that we got them at all proves the command was created.
	if len(cmds) == 0 {
		t.Fatal("expected at least 1 command for the node, got 0")
	}

	foundStartCmd := false
	for _, cmd := range cmds {
		if cmd.CommandType == "start_sandbox" {
			foundStartCmd = true
			break
		}
	}
	if !foundStartCmd {
		t.Fatal("expected a start_sandbox command, none found")
	}

	// GET /v1/sandboxes/{id} -> verify state is "starting"
	getReq, _ := http.NewRequest(http.MethodGet, baseURL+"/v1/sandboxes/"+sandboxID, nil)
	getReq.Header.Set("Authorization", "Bearer "+testToken)

	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatalf("GET /v1/sandboxes/%s: %v", sandboxID, err)
	}
	defer getResp.Body.Close()

	if getResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(getResp.Body)
		t.Fatalf("expected 200, got %d: %s", getResp.StatusCode, string(body))
	}

	var getResult map[string]interface{}
	if err := json.NewDecoder(getResp.Body).Decode(&getResult); err != nil {
		t.Fatalf("decode get response: %v", err)
	}

	if getState, ok := getResult["state"].(string); !ok || getState != "starting" {
		t.Fatalf("expected state 'starting' on GET, got %v", getResult["state"])
	}
}

// TestIntegration_RegisterAndHeartbeat verifies the full node registration
// and heartbeat flow against a real Postgres database.
func TestIntegration_RegisterAndHeartbeat(t *testing.T) {
	baseURL, _ := setupIntegrationServer(t)

	// POST /v1/nodes/register
	registerBody := `{
		"hostname": "integ-heartbeat-node",
		"tailscale_ip": "100.64.2.20",
		"agent_listen_port": 8090,
		"capacity_mb": 16000,
		"capacity_cpu": 4.0,
		"agent_version": "v0.1.0"
	}`

	req, _ := http.NewRequest(http.MethodPost, baseURL+"/v1/nodes/register", bytes.NewBufferString(registerBody))
	req.Header.Set("Content-Type", "application/json")
	// Registration is unauthenticated

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/nodes/register: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, string(body))
	}

	var registerResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&registerResp); err != nil {
		t.Fatalf("decode register response: %v", err)
	}

	nodeID, ok := registerResp["node_id"].(string)
	if !ok || nodeID == "" {
		t.Fatalf("expected non-empty node_id, got %v", registerResp["node_id"])
	}

	agentToken, ok := registerResp["agent_token"].(string)
	if !ok || len(agentToken) != 64 {
		t.Fatalf("expected 64-char agent_token, got %v", registerResp["agent_token"])
	}

	heartbeatInterval, ok := registerResp["heartbeat_interval_seconds"].(float64)
	if !ok || heartbeatInterval != 15 {
		t.Fatalf("expected heartbeat_interval_seconds=15, got %v", registerResp["heartbeat_interval_seconds"])
	}

	// POST /v1/nodes/{id}/heartbeat with auth
	heartbeatBody := `{"used_mb": 4000, "running_containers": 5}`
	hbReq, _ := http.NewRequest(http.MethodPost,
		fmt.Sprintf("%s/v1/nodes/%s/heartbeat", baseURL, nodeID),
		bytes.NewBufferString(heartbeatBody),
	)
	hbReq.Header.Set("Content-Type", "application/json")
	hbReq.Header.Set("Authorization", "Bearer "+testToken)

	hbResp, err := http.DefaultClient.Do(hbReq)
	if err != nil {
		t.Fatalf("POST /v1/nodes/{id}/heartbeat: %v", err)
	}
	defer hbResp.Body.Close()

	if hbResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(hbResp.Body)
		t.Fatalf("expected 200, got %d: %s", hbResp.StatusCode, string(body))
	}
}
