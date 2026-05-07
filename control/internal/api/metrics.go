package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/appx/forge/control/internal/store"
)

// MetricsStore abstracts the database operations needed for the /metrics endpoint.
type MetricsStore interface {
	CountSandboxesByState(ctx context.Context) ([]store.CountSandboxesByStateRow, error)
	ListNodes(ctx context.Context) ([]store.Node, error)
}

// handleMetrics serves Prometheus text format metrics.
// This endpoint is unauthenticated (like /healthz) and returns:
//   - forge_sandbox_count{state="..."} -- sandbox count per state
//   - forge_node_utilization_ratio{node="..."} -- memory utilization per node
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if s.metricsStore == nil {
		http.Error(w, "metrics not configured", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	var b strings.Builder

	// ── Sandbox counts by state ────────────────────────────────────────
	b.WriteString("# HELP forge_sandbox_count Number of sandboxes by state\n")
	b.WriteString("# TYPE forge_sandbox_count gauge\n")

	counts, err := s.metricsStore.CountSandboxesByState(ctx)
	if err != nil {
		s.logger.Warn("metrics: failed to count sandboxes", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	for _, row := range counts {
		b.WriteString(fmt.Sprintf("forge_sandbox_count{state=%q} %d\n", row.State, row.Count))
	}

	// ── Node utilization ───────────────────────────────────────────────
	b.WriteString("# HELP forge_node_utilization_ratio Memory utilization ratio per node (used_mb / capacity_mb)\n")
	b.WriteString("# TYPE forge_node_utilization_ratio gauge\n")

	nodes, err := s.metricsStore.ListNodes(ctx)
	if err != nil {
		s.logger.Warn("metrics: failed to list nodes", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	for _, n := range nodes {
		ratio := 0.0
		if n.CapacityMb > 0 {
			ratio = float64(n.UsedMb) / float64(n.CapacityMb)
		}
		b.WriteString(fmt.Sprintf("forge_node_utilization_ratio{node=%q} %.4f\n", n.Hostname, ratio))
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(b.String()))
}
