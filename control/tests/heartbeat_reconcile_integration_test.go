package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/appx/forge/control/internal/store"
)

// Phase 30 (T21) — Heartbeat reconcile integration tests against a real
// Postgres container. These exercise the full HeartbeatReconciler →
// store.MarkSandboxVerified / MarkSandboxAgentLost path that ships in
// production via main.go's reconcilerStoreAdapter; the integration
// adapter in integration_test.go satisfies the same scheduler.SandboxStore
// contract so we drive it through the real HTTP heartbeat endpoint.

// seedSandboxRunning writes a sandbox row, assigns it to the node, drives
// it to state='running', and then pins created_at + verified_at to
// controlled offsets via raw SQL. Returns the sandbox UUID.
//
// `createdAtOffset` and `verifiedAtOffset` are added to NOW() — pass
// negative durations to make the row look "old" or "stale".
func seedSandboxRunning(
	t *testing.T,
	queries *store.Queries,
	pool *pgxpool.Pool,
	appName string,
	nodeID pgtype.UUID,
	createdAtOffset time.Duration,
	verifiedAtOffset time.Duration,
) pgtype.UUID {
	t.Helper()
	ctx := context.Background()

	id := pgtype.UUID{Bytes: uuid.New(), Valid: true}

	// Direct INSERT bypasses the lifecycle service. CreateSandbox sets
	// state='pending'; we transition it through to 'running' below.
	sb, err := queries.CreateSandbox(ctx, store.CreateSandboxParams{
		ID:                 id,
		AppName:            appName,
		UserID:             "user-recon",
		Image:              "appx/sandbox:v1",
		Resources:          []byte(`{"cpu_cores":0.5,"memory_mb":512}`),
		Env:                []byte(`{}`),
		IdleTimeoutSeconds: 1800,
		Metadata:           []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("CreateSandbox(%s): %v", appName, err)
	}

	// Assign to node so ListSandboxesForNode finds it. AssignSandboxToNode
	// transitions pending→starting.
	if _, err := queries.AssignSandboxToNode(ctx, store.AssignSandboxToNodeParams{
		ID:          sb.ID,
		NodeID:      nodeID,
		HostPort:    pgtype.Int4{Int32: 8081, Valid: true},
		ContainerID: pgtype.Text{String: "ctr-recon", Valid: true},
	}); err != nil {
		t.Fatalf("AssignSandboxToNode(%s): %v", appName, err)
	}

	// starting → running so MarkSandboxAgentLost (which only fires for
	// pending|starting|running|restarting rows) sees the row in scope.
	if _, err := queries.TransitionSandboxState(ctx, store.TransitionSandboxStateParams{
		State:   "running",
		ID:      sb.ID,
		State_2: "starting",
	}); err != nil {
		t.Fatalf("TransitionSandboxState(%s) starting→running: %v", appName, err)
	}

	// Pin created_at + verified_at via raw SQL. Neither is settable
	// through the sqlc API and the test wants precise control over the
	// 60s grace window and the staleness threshold.
	now := time.Now()
	if _, err := pool.Exec(ctx,
		`UPDATE sandboxes SET created_at = $1, verified_at = $2 WHERE id = $3`,
		now.Add(createdAtOffset), now.Add(verifiedAtOffset), id,
	); err != nil {
		t.Fatalf("UPDATE sandboxes timestamps for %s: %v", appName, err)
	}
	return id
}

// registerNodeForHeartbeat drives POST /v1/nodes/register and returns
// both the string ID (for URL building) and the typed pgtype.UUID (for
// direct DB seeding).
func registerNodeForHeartbeat(
	t *testing.T,
	baseURL string,
	hostname string,
	tailscaleIP string,
) (string, pgtype.UUID) {
	t.Helper()
	body := fmt.Sprintf(`{
		"hostname": %q,
		"tailscale_ip": %q,
		"agent_listen_port": 8090,
		"capacity_mb": 24000,
		"capacity_cpu": 4.0,
		"agent_version": "v0.1.0"
	}`, hostname, tailscaleIP)

	req, _ := http.NewRequest(http.MethodPost,
		baseURL+"/v1/nodes/register", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/nodes/register: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		bs, _ := io.ReadAll(resp.Body)
		t.Fatalf("register: expected 201, got %d: %s", resp.StatusCode, string(bs))
	}

	var registerResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&registerResp); err != nil {
		t.Fatalf("decode register: %v", err)
	}
	idStr, _ := registerResp["node_id"].(string)
	parsed, err := uuid.Parse(idStr)
	if err != nil {
		t.Fatalf("parse node_id: %v", err)
	}
	return idStr, pgtype.UUID{Bytes: parsed, Valid: true}
}

// postRichHeartbeat sends a Phase 30 heartbeat (with explicit container
// list). Returns the response status code; non-200 logs the body.
func postRichHeartbeat(
	t *testing.T,
	baseURL string,
	nodeIDStr string,
	containers []map[string]interface{},
) int {
	t.Helper()
	body := map[string]interface{}{
		"used_mb":            1024,
		"running_containers": len(containers),
		"containers":         containers,
	}
	bs, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost,
		fmt.Sprintf("%s/v1/nodes/%s/heartbeat", baseURL, nodeIDStr),
		bytes.NewBuffer(bs))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/nodes/{id}/heartbeat: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		bs, _ := io.ReadAll(resp.Body)
		t.Logf("heartbeat body: %s", string(bs))
	}
	return resp.StatusCode
}

// TestIntegration_HeartbeatReconcile_PresentBumpsVerifiedAt verifies that
// when the agent's heartbeat reports a container we already track, the
// reconciler bumps verified_at to NOW().
func TestIntegration_HeartbeatReconcile_PresentBumpsVerifiedAt(t *testing.T) {
	baseURL, queries, pool := setupIntegrationServerWithPool(t)
	ctx := context.Background()

	nodeIDStr, nodeID := registerNodeForHeartbeat(
		t, baseURL, "recon-bump-node", "100.64.30.1",
	)

	// 5min old, verified_at 60s stale — exactly the case the reconciler
	// should refresh on a heartbeat that confirms presence.
	seedSandboxRunning(t, queries, pool, "pool-bump", nodeID,
		-5*time.Minute, -60*time.Second)

	if status := postRichHeartbeat(t, baseURL, nodeIDStr,
		[]map[string]interface{}{
			{"app_name": "pool-bump", "state": "running",
				"host_port": 8081, "container_id": "c-bump"},
		}); status != http.StatusOK {
		t.Fatalf("rich heartbeat returned %d", status)
	}

	row, err := queries.GetSandboxByAppName(ctx, "pool-bump")
	if err != nil {
		t.Fatalf("GetSandboxByAppName: %v", err)
	}
	if !row.VerifiedAt.Valid {
		t.Fatal("expected verified_at to be set after rich heartbeat")
	}
	if age := time.Since(row.VerifiedAt.Time); age > 10*time.Second {
		t.Errorf("verified_at not bumped — still %v old", age)
	}
	if row.State != "running" {
		t.Errorf("expected state running, got %q", row.State)
	}
}

// TestIntegration_HeartbeatReconcile_AgentLost_MarksDestroyed verifies
// that DB rows past the 60s grace window get marked destroyed when the
// agent's heartbeat omits them, while rows still inside the window are
// left running.
func TestIntegration_HeartbeatReconcile_AgentLost_MarksDestroyed(t *testing.T) {
	baseURL, queries, pool := setupIntegrationServerWithPool(t)
	ctx := context.Background()

	nodeIDStr, nodeID := registerNodeForHeartbeat(
		t, baseURL, "recon-lost-node", "100.64.30.2",
	)

	// pool-LOST: 5min old → eligible for agent-lost.
	// pool-FRESH: 10s old → still inside the 60s grace window.
	seedSandboxRunning(t, queries, pool, "pool-LOST", nodeID,
		-5*time.Minute, -60*time.Second)
	seedSandboxRunning(t, queries, pool, "pool-FRESH", nodeID,
		-10*time.Second, -10*time.Second)

	// Empty container list — both are missing from the agent's view.
	if status := postRichHeartbeat(t, baseURL, nodeIDStr,
		[]map[string]interface{}{}); status != http.StatusOK {
		t.Fatalf("rich heartbeat returned %d", status)
	}

	lost, err := queries.GetSandboxByAppName(ctx, "pool-LOST")
	if err != nil {
		t.Fatalf("GetSandboxByAppName(pool-LOST): %v", err)
	}
	if lost.State != "destroyed" {
		t.Errorf("pool-LOST: expected state=destroyed, got %q", lost.State)
	}

	fresh, err := queries.GetSandboxByAppName(ctx, "pool-FRESH")
	if err != nil {
		t.Fatalf("GetSandboxByAppName(pool-FRESH): %v", err)
	}
	if fresh.State != "running" {
		t.Errorf("pool-FRESH: expected state=running (grace window), got %q",
			fresh.State)
	}
}
