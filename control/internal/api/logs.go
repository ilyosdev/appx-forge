package api

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/appx/forge/control/internal/store"
)

// LogProxyStore abstracts the database operations needed by the log proxy handler.
type LogProxyStore interface {
	GetSandbox(ctx context.Context, id pgtype.UUID) (store.Sandbox, error)
	GetNode(ctx context.Context, id pgtype.UUID) (store.Node, error)
}

// handleGetLogs handles GET /v1/sandboxes/{id}/logs.
// Proxies the request to the agent hosting the sandbox.
// Full implementation in Task 2.
func (s *Server) handleGetLogs(w http.ResponseWriter, r *http.Request) {
	WriteProblem(w, http.StatusNotImplemented,
		"urn:forge:error:not-implemented", "Not Implemented", "log proxy not yet implemented")
}
