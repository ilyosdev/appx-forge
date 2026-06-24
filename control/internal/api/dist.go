package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/appx/forge/shared-go/auth"
)

// ── Dist Fetch Handler ──────────────────────────────────────────────
//
// handleDistFetch handles GET /v1/sandboxes/{id}/dist.
// It is the read-out twin of handleFilePush: it returns a 307 Temporary
// Redirect with an HMAC-signed URL pointing at the agent hosting the sandbox,
// and the client follows it to stream the sandbox's built `dist/` directory as
// gzip-tar (for static-preview publish to R2, ADR-0003). Same node-resolution
// and HMAC-sign scheme as filepush — but with a longer signature TTL because
// the backend reads a multi-MB body after following the redirect.
func (s *Server) handleDistFetch(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	pgID, err := parseUUID(idStr)
	if err != nil {
		BadRequest(w, "invalid sandbox ID: must be a valid UUID")
		return
	}

	sandbox, err := s.filePushStore.GetSandbox(r.Context(), pgID)
	if err != nil {
		NotFound(w, "sandbox not found")
		return
	}

	if !sandbox.NodeID.Valid {
		ServiceUnavailable(w, "sandbox not yet scheduled to a node")
		return
	}

	node, err := s.filePushStore.GetNode(r.Context(), sandbox.NodeID)
	if err != nil {
		s.logger.Error("failed to get node for dist fetch", "error", err, "node_id", formatUUID(sandbox.NodeID))
		WriteProblem(w, http.StatusInternalServerError,
			"urn:forge:error:internal", "Internal Server Error", "failed to resolve sandbox node")
		return
	}

	sandboxUUID := uuid.UUID(pgID.Bytes)
	agentURL := fmt.Sprintf("http://%s:%d/v1/sandboxes/%s/dist",
		node.TailscaleIp.String(), node.AgentListenPort, sandboxUUID.String())

	// 120s TTL (vs file-push 60s): VerifyURL checks expiry only at request
	// start, but the backend follows this redirect and then reads a multi-MB
	// gzip-tar body — give comfortable headroom over a slow transfer.
	signedURL, err := auth.SignURL(agentURL, []byte(s.config.hmacSecret), 120*time.Second)
	if err != nil {
		s.logger.Error("failed to sign dist fetch URL", "error", err)
		WriteProblem(w, http.StatusInternalServerError,
			"urn:forge:error:internal", "Internal Server Error", "failed to generate signed URL")
		return
	}

	// Touch last_active_at so a build-time fetch does not race the idle reaper.
	if err := s.filePushStore.UpdateSandboxLastActive(r.Context(), pgID); err != nil {
		s.logger.Warn("failed to update sandbox last_active_at", "error", err, "sandbox_id", idStr)
		// Non-fatal: continue with redirect
	}

	w.Header().Set("Location", signedURL)
	w.WriteHeader(http.StatusTemporaryRedirect)
}
