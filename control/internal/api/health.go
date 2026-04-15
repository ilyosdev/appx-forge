package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// healthResponse is the JSON response for /v1/healthz.
type healthResponse struct {
	Status        string `json:"status"`
	Postgres      string `json:"postgres"`
	UptimeSeconds int    `json:"uptime_seconds"`
}

// handleHealthz checks database connectivity and returns health status.
// Public endpoint -- no auth required (security: [] in OpenAPI spec).
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	pgStatus := "ok"
	httpStatus := http.StatusOK
	overallStatus := "ok"

	if err := s.pinger.Ping(ctx); err != nil {
		pgStatus = "unreachable"
		httpStatus = http.StatusServiceUnavailable
		overallStatus = "error"
		s.logger.Warn("healthz: postgres ping failed", "error", err)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	json.NewEncoder(w).Encode(healthResponse{
		Status:        overallStatus,
		Postgres:      pgStatus,
		UptimeSeconds: int(time.Since(s.startTime).Seconds()),
	})
}
