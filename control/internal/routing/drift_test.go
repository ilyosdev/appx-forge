package routing

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/appx/forge/control/internal/store"
)

// ── Mock RouteListFetcher ──────────────────────────────────────────

type mockRouteListFetcher struct {
	listRoutesFn   func(ctx context.Context) ([]Route, error)
	addRouteFn     func(ctx context.Context, r Route) error
	removeRouteFn  func(ctx context.Context, appName string) error

	addedRoutes   []Route
	removedApps   []string
}

func (m *mockRouteListFetcher) ListRoutes(ctx context.Context) ([]Route, error) {
	if m.listRoutesFn != nil {
		return m.listRoutesFn(ctx)
	}
	return nil, nil
}

func (m *mockRouteListFetcher) AddRoute(ctx context.Context, r Route) error {
	m.addedRoutes = append(m.addedRoutes, r)
	if m.addRouteFn != nil {
		return m.addRouteFn(ctx, r)
	}
	return nil
}

func (m *mockRouteListFetcher) RemoveRoute(ctx context.Context, appName string) error {
	m.removedApps = append(m.removedApps, appName)
	if m.removeRouteFn != nil {
		return m.removeRouteFn(ctx, appName)
	}
	return nil
}

// ── Mock DriftStore ────────────────────────────────────────────────

type mockDriftStore struct {
	listSandboxesByStateFn func(ctx context.Context, state string) ([]store.Sandbox, error)
	getNodeFn              func(ctx context.Context, id pgtype.UUID) (store.Node, error)
}

func (m *mockDriftStore) ListSandboxesByState(ctx context.Context, state string) ([]store.Sandbox, error) {
	if m.listSandboxesByStateFn != nil {
		return m.listSandboxesByStateFn(ctx, state)
	}
	return nil, nil
}

func (m *mockDriftStore) GetNode(ctx context.Context, id pgtype.UUID) (store.Node, error) {
	if m.getNodeFn != nil {
		return m.getNodeFn(ctx, id)
	}
	return store.Node{}, errors.New("node not found")
}

// ── Test Helpers ────────────────────────────────────────────────────

func makePgUUID(u uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: u, Valid: true}
}

func makeSandbox(appName string, nodeID uuid.UUID, hostPort int32) store.Sandbox {
	id := uuid.New()
	return store.Sandbox{
		ID:       pgtype.UUID{Bytes: id, Valid: true},
		AppName:  appName,
		State:    "running",
		NodeID:   pgtype.UUID{Bytes: nodeID, Valid: true},
		HostPort: pgtype.Int4{Int32: hostPort, Valid: true},
	}
}

func makeNodeWithIP(id uuid.UUID, ip string) store.Node {
	return store.Node{
		ID:          pgtype.UUID{Bytes: id, Valid: true},
		Hostname:    "test-node",
		TailscaleIp: netip.MustParseAddr(ip),
	}
}

// ── Tests ───────────────────────────────────────────────────────────

func TestDrift_InSync_NoChanges(t *testing.T) {
	nodeID := uuid.New()
	sb := makeSandbox("app1", nodeID, 8081)

	proxy := &mockRouteListFetcher{
		listRoutesFn: func(ctx context.Context) ([]Route, error) {
			return []Route{
				{AppName: "app1", SandboxID: uuid.UUID(sb.ID.Bytes).String(), Upstream: "100.64.0.1:8081"},
			}, nil
		},
	}

	ds := &mockDriftStore{
		listSandboxesByStateFn: func(ctx context.Context, state string) ([]store.Sandbox, error) {
			if state == "running" {
				return []store.Sandbox{sb}, nil
			}
			return nil, nil
		},
	}

	dd := NewDriftDetector(proxy, ds, nil, 0)
	dd.detect(context.Background())

	if len(proxy.addedRoutes) != 0 {
		t.Errorf("expected 0 adds, got %d", len(proxy.addedRoutes))
	}
	if len(proxy.removedApps) != 0 {
		t.Errorf("expected 0 removes, got %d", len(proxy.removedApps))
	}
}

func TestDrift_StaleCaddyRoute_RemovesRoute(t *testing.T) {
	proxy := &mockRouteListFetcher{
		listRoutesFn: func(ctx context.Context) ([]Route, error) {
			return []Route{
				{AppName: "stale-app", SandboxID: "sbx-old", Upstream: "100.64.0.1:8081"},
			}, nil
		},
	}

	ds := &mockDriftStore{
		listSandboxesByStateFn: func(ctx context.Context, state string) ([]store.Sandbox, error) {
			return []store.Sandbox{}, nil // no running sandboxes
		},
	}

	dd := NewDriftDetector(proxy, ds, nil, 0)
	dd.detect(context.Background())

	if len(proxy.removedApps) != 1 {
		t.Fatalf("expected 1 remove, got %d", len(proxy.removedApps))
	}
	if proxy.removedApps[0] != "stale-app" {
		t.Errorf("expected removed app 'stale-app', got %q", proxy.removedApps[0])
	}
	if len(proxy.addedRoutes) != 0 {
		t.Errorf("expected 0 adds, got %d", len(proxy.addedRoutes))
	}
}

func TestDrift_MissingCaddyRoute_AddsRoute(t *testing.T) {
	nodeID := uuid.New()
	sb := makeSandbox("missing-app", nodeID, 9090)

	proxy := &mockRouteListFetcher{
		listRoutesFn: func(ctx context.Context) ([]Route, error) {
			return []Route{}, nil // empty Caddy
		},
	}

	ds := &mockDriftStore{
		listSandboxesByStateFn: func(ctx context.Context, state string) ([]store.Sandbox, error) {
			if state == "running" {
				return []store.Sandbox{sb}, nil
			}
			return nil, nil
		},
		getNodeFn: func(ctx context.Context, id pgtype.UUID) (store.Node, error) {
			return makeNodeWithIP(nodeID, "100.64.0.5"), nil
		},
	}

	dd := NewDriftDetector(proxy, ds, nil, 0)
	dd.detect(context.Background())

	if len(proxy.addedRoutes) != 1 {
		t.Fatalf("expected 1 add, got %d", len(proxy.addedRoutes))
	}
	added := proxy.addedRoutes[0]
	if added.AppName != "missing-app" {
		t.Errorf("expected add app 'missing-app', got %q", added.AppName)
	}
	expectedUpstream := "100.64.0.5:9090"
	if added.Upstream != expectedUpstream {
		t.Errorf("expected upstream %q, got %q", expectedUpstream, added.Upstream)
	}
	if added.SandboxID != uuid.UUID(sb.ID.Bytes).String() {
		t.Errorf("expected sandbox ID %q, got %q", uuid.UUID(sb.ID.Bytes).String(), added.SandboxID)
	}
	if len(proxy.removedApps) != 0 {
		t.Errorf("expected 0 removes, got %d", len(proxy.removedApps))
	}
}

func TestDrift_StaleAndMissing_FixesBoth(t *testing.T) {
	nodeID := uuid.New()
	sb := makeSandbox("new-app", nodeID, 7070)

	proxy := &mockRouteListFetcher{
		listRoutesFn: func(ctx context.Context) ([]Route, error) {
			return []Route{
				{AppName: "old-app", SandboxID: "sbx-gone", Upstream: "100.64.0.1:8081"},
			}, nil
		},
	}

	ds := &mockDriftStore{
		listSandboxesByStateFn: func(ctx context.Context, state string) ([]store.Sandbox, error) {
			if state == "running" {
				return []store.Sandbox{sb}, nil
			}
			return nil, nil
		},
		getNodeFn: func(ctx context.Context, id pgtype.UUID) (store.Node, error) {
			return makeNodeWithIP(nodeID, "100.64.0.10"), nil
		},
	}

	dd := NewDriftDetector(proxy, ds, nil, 0)
	dd.detect(context.Background())

	if len(proxy.removedApps) != 1 {
		t.Fatalf("expected 1 remove, got %d", len(proxy.removedApps))
	}
	if proxy.removedApps[0] != "old-app" {
		t.Errorf("expected removed 'old-app', got %q", proxy.removedApps[0])
	}

	if len(proxy.addedRoutes) != 1 {
		t.Fatalf("expected 1 add, got %d", len(proxy.addedRoutes))
	}
	if proxy.addedRoutes[0].AppName != "new-app" {
		t.Errorf("expected added 'new-app', got %q", proxy.addedRoutes[0].AppName)
	}
}

func TestDrift_ListRoutesFails_NoChanges(t *testing.T) {
	proxy := &mockRouteListFetcher{
		listRoutesFn: func(ctx context.Context) ([]Route, error) {
			return nil, errors.New("caddy unavailable")
		},
	}

	ds := &mockDriftStore{
		listSandboxesByStateFn: func(ctx context.Context, state string) ([]store.Sandbox, error) {
			t.Fatal("ListSandboxesByState should not be called when ListRoutes fails")
			return nil, nil
		},
	}

	dd := NewDriftDetector(proxy, ds, nil, 0)
	dd.detect(context.Background())

	if len(proxy.addedRoutes) != 0 {
		t.Errorf("expected 0 adds, got %d", len(proxy.addedRoutes))
	}
	if len(proxy.removedApps) != 0 {
		t.Errorf("expected 0 removes, got %d", len(proxy.removedApps))
	}
}

func TestDrift_ListSandboxesFails_NoChanges(t *testing.T) {
	proxy := &mockRouteListFetcher{
		listRoutesFn: func(ctx context.Context) ([]Route, error) {
			return []Route{
				{AppName: "some-app", Upstream: "100.64.0.1:8081"},
			}, nil
		},
	}

	ds := &mockDriftStore{
		listSandboxesByStateFn: func(ctx context.Context, state string) ([]store.Sandbox, error) {
			return nil, errors.New("database error")
		},
	}

	dd := NewDriftDetector(proxy, ds, nil, 0)
	dd.detect(context.Background())

	// Should not make any changes when Postgres data is unavailable
	if len(proxy.addedRoutes) != 0 {
		t.Errorf("expected 0 adds, got %d", len(proxy.addedRoutes))
	}
	if len(proxy.removedApps) != 0 {
		t.Errorf("expected 0 removes, got %d", len(proxy.removedApps))
	}
}

func TestDrift_NodeLookupFails_SkipsMissingRouteAdd(t *testing.T) {
	nodeID := uuid.New()
	sb := makeSandbox("orphan-app", nodeID, 5050)

	proxy := &mockRouteListFetcher{
		listRoutesFn: func(ctx context.Context) ([]Route, error) {
			return []Route{}, nil
		},
	}

	ds := &mockDriftStore{
		listSandboxesByStateFn: func(ctx context.Context, state string) ([]store.Sandbox, error) {
			if state == "running" {
				return []store.Sandbox{sb}, nil
			}
			return nil, nil
		},
		getNodeFn: func(ctx context.Context, id pgtype.UUID) (store.Node, error) {
			return store.Node{}, fmt.Errorf("node not found: %v", id)
		},
	}

	dd := NewDriftDetector(proxy, ds, nil, 0)
	dd.detect(context.Background())

	// Should not add route when node lookup fails
	if len(proxy.addedRoutes) != 0 {
		t.Errorf("expected 0 adds when node lookup fails, got %d", len(proxy.addedRoutes))
	}
}
