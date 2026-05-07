package routing

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/appx/forge/control/internal/store"
)

// RouteListFetcher abstracts the proxy operations needed by the drift detector.
type RouteListFetcher interface {
	ListRoutes(ctx context.Context) ([]Route, error)
	AddRoute(ctx context.Context, r Route) error
	RemoveRoute(ctx context.Context, appName string) error
}

// DriftStore abstracts the database operations needed by the drift detector.
type DriftStore interface {
	ListSandboxesByState(ctx context.Context, state string) ([]store.Sandbox, error)
	GetNode(ctx context.Context, id pgtype.UUID) (store.Node, error)
}

// DriftDetector periodically compares Caddy routes with Postgres sandbox state
// and fixes discrepancies. It is the safety net for any route add/remove that
// failed during normal lifecycle operations.
type DriftDetector struct {
	proxy    RouteListFetcher
	store    DriftStore
	logger   *slog.Logger
	interval time.Duration
}

// NewDriftDetector creates a new DriftDetector.
// interval is how often detect() is called; 0 defaults to 60s.
func NewDriftDetector(proxy RouteListFetcher, store DriftStore, logger *slog.Logger, interval time.Duration) *DriftDetector {
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = 60 * time.Second
	}
	return &DriftDetector{
		proxy:    proxy,
		store:    store,
		logger:   logger,
		interval: interval,
	}
}

// Run starts the drift detector ticker loop. It calls detect() on each tick
// and returns when ctx is cancelled.
func (d *DriftDetector) Run(ctx context.Context) {
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	d.logger.Info("drift detector started", "interval", d.interval)

	for {
		select {
		case <-ctx.Done():
			d.logger.Info("drift detector stopped")
			return
		case <-ticker.C:
			d.detect(ctx)
		}
	}
}

// detect compares Caddy routes against Postgres running sandboxes and fixes
// discrepancies. If either data source fails, it returns early without making
// partial changes.
func (d *DriftDetector) detect(ctx context.Context) {
	// 1. Fetch current Caddy routes
	caddyRoutes, err := d.proxy.ListRoutes(ctx)
	if err != nil {
		d.logger.Error("drift: failed to list Caddy routes", "error", err)
		return
	}

	// 2. Fetch running sandboxes from Postgres
	sandboxes, err := d.store.ListSandboxesByState(ctx, "running")
	if err != nil {
		d.logger.Error("drift: failed to list running sandboxes", "error", err)
		return
	}

	// Build maps for comparison
	caddyMap := make(map[string]Route, len(caddyRoutes))
	for _, r := range caddyRoutes {
		caddyMap[r.AppName] = r
	}

	pgMap := make(map[string]store.Sandbox, len(sandboxes))
	for _, sb := range sandboxes {
		pgMap[sb.AppName] = sb
	}

	added := 0
	removed := 0

	// 3. Stale routes: in Caddy but not in Postgres running set
	for appName := range caddyMap {
		if _, exists := pgMap[appName]; !exists {
			if err := d.proxy.RemoveRoute(ctx, appName); err != nil {
				d.logger.Warn("drift: failed to remove stale route",
					"error", err,
					"app_name", appName,
				)
				continue
			}
			removed++
		}
	}

	// 4. Missing routes: in Postgres running set but not in Caddy
	for appName, sb := range pgMap {
		if _, exists := caddyMap[appName]; exists {
			continue
		}

		// Look up node to build upstream address
		if !sb.NodeID.Valid {
			d.logger.Warn("drift: sandbox has no node_id, skipping route add",
				"app_name", appName,
				"sandbox_id", uuid.UUID(sb.ID.Bytes).String(),
			)
			continue
		}

		node, err := d.store.GetNode(ctx, sb.NodeID)
		if err != nil {
			d.logger.Warn("drift: failed to get node for missing route",
				"error", err,
				"app_name", appName,
				"node_id", uuid.UUID(sb.NodeID.Bytes).String(),
			)
			continue
		}

		if !sb.HostPort.Valid {
			d.logger.Warn("drift: sandbox has no host_port, skipping route add",
				"app_name", appName,
				"sandbox_id", uuid.UUID(sb.ID.Bytes).String(),
			)
			continue
		}

		upstream := fmt.Sprintf("%s:%d", node.TailscaleIp.String(), sb.HostPort.Int32)
		route := Route{
			AppName:   appName,
			SandboxID: uuid.UUID(sb.ID.Bytes).String(),
			Upstream:  upstream,
		}

		if err := d.proxy.AddRoute(ctx, route); err != nil {
			d.logger.Warn("drift: failed to add missing route",
				"error", err,
				"app_name", appName,
				"upstream", upstream,
			)
			continue
		}
		added++
	}

	if added > 0 || removed > 0 {
		d.logger.Info("drift check complete", "added", added, "removed", removed)
	}
}
