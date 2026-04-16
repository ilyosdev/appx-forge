package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/appx/forge/control/internal/store"
)

// LogProxyStore abstracts the database operations needed by the log proxy handler.
type LogProxyStore interface {
	GetSandbox(ctx context.Context, id pgtype.UUID) (store.Sandbox, error)
	GetNode(ctx context.Context, id pgtype.UUID) (store.Node, error)
}

// handleGetLogs handles GET /v1/sandboxes/{id}/logs.
// Proxies the log request to the agent hosting the sandbox. Supports:
//   - tail: number of lines to return (forwarded to agent)
//   - follow: stream logs continuously (forwarded to agent)
//
// Returns 404 if sandbox not found, 503 if sandbox not assigned to a node.
// Uses a 60-second timeout on the proxy request to prevent indefinite holds (T-06-02).
func (s *Server) handleGetLogs(w http.ResponseWriter, r *http.Request) {
	if s.logProxyStore == nil {
		ServiceUnavailable(w, "log proxy not configured")
		return
	}

	idStr := chi.URLParam(r, "id")

	// Resolve sandbox (support UUID and app:{name} prefix per OpenAPI)
	var sandbox store.Sandbox
	var err error

	if strings.HasPrefix(idStr, "app:") {
		// app:name lookup not supported for logs -- need sandbox ID for agent URL
		BadRequest(w, "log endpoint requires sandbox UUID, not app:{name}")
		return
	}

	pgID, parseErr := parseUUID(idStr)
	if parseErr != nil {
		BadRequest(w, "invalid sandbox ID: must be a valid UUID")
		return
	}

	sandbox, err = s.logProxyStore.GetSandbox(r.Context(), pgID)
	if err != nil {
		NotFound(w, "sandbox not found")
		return
	}

	// Sandbox must be assigned to a node
	if !sandbox.NodeID.Valid {
		ServiceUnavailable(w, "sandbox not assigned to a node")
		return
	}

	// Get node to find tailscale_ip and agent_listen_port
	node, err := s.logProxyStore.GetNode(r.Context(), sandbox.NodeID)
	if err != nil {
		s.logger.Error("failed to get node for log proxy", "error", err, "node_id", formatUUID(sandbox.NodeID))
		WriteProblem(w, http.StatusInternalServerError,
			"urn:forge:error:internal", "Internal Server Error", "failed to resolve sandbox node")
		return
	}

	// Construct agent URL
	sandboxUUID := uuid.UUID(pgID.Bytes)
	agentURL := fmt.Sprintf("http://%s:%d/sandboxes/%s/logs",
		node.TailscaleIp.String(), node.AgentListenPort, sandboxUUID.String())

	// Forward query parameters (tail, follow)
	q := r.URL.Query()
	params := []string{}
	if tail := q.Get("tail"); tail != "" {
		params = append(params, "tail="+tail)
	}
	if follow := q.Get("follow"); follow != "" {
		params = append(params, "follow="+follow)
	}
	if len(params) > 0 {
		agentURL += "?" + strings.Join(params, "&")
	}

	// Create proxy request to agent
	httpClient := s.logHTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	proxyReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, agentURL, nil)
	if err != nil {
		s.logger.Error("failed to create log proxy request", "error", err)
		WriteProblem(w, http.StatusInternalServerError,
			"urn:forge:error:internal", "Internal Server Error", "failed to create proxy request")
		return
	}

	resp, err := httpClient.Do(proxyReq)
	if err != nil {
		s.logger.Error("log proxy request failed", "error", err, "agent_url", agentURL)
		WriteProblem(w, http.StatusBadGateway,
			"urn:forge:error:bad-gateway", "Bad Gateway", "failed to reach agent")
		return
	}
	defer resp.Body.Close()

	// Forward response headers and body
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
