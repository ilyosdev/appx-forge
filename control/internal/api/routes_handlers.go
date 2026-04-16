package api

import (
	"context"
	"net/http"

	"github.com/appx/forge/control/internal/routing"
)

// RouteListFetcher abstracts the Caddy route listing operation.
// In production, *routing.CaddyClient satisfies this interface.
type RouteListFetcher interface {
	ListRoutes(ctx context.Context) ([]routing.Route, error)
}

// ── Response Types ──────────────────────────────────────────────────

type routeResponse struct {
	AppName   string `json:"app_name"`
	SandboxID string `json:"sandbox_id"`
	Upstream  string `json:"upstream"`
}

type routeListResponse struct {
	Routes []routeResponse `json:"routes"`
}

// ── Handlers ────────────────────────────────────────────────────────

// handleListRoutes handles GET /v1/routes.
// Returns active routes from Caddy for ops/debug visibility.
func (s *Server) handleListRoutes(w http.ResponseWriter, r *http.Request) {
	if s.routeFetcher == nil {
		ServiceUnavailable(w, "route service not configured")
		return
	}

	routes, err := s.routeFetcher.ListRoutes(r.Context())
	if err != nil {
		s.logger.Error("failed to list routes", "error", err)
		WriteProblem(w, http.StatusInternalServerError,
			"urn:forge:error:internal", "Internal Server Error", "failed to list routes")
		return
	}

	resp := make([]routeResponse, len(routes))
	for i, rt := range routes {
		resp[i] = routeResponse{
			AppName:   rt.AppName,
			SandboxID: rt.SandboxID,
			Upstream:  rt.Upstream,
		}
	}

	writeJSON(w, http.StatusOK, routeListResponse{Routes: resp})
}
