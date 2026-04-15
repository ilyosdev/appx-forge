package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/appx/forge/control/internal/store"
	"github.com/appx/forge/shared-go/auth"
)

// ── Interfaces ──────────────────────────────────────────────────────

// AgentStore abstracts the database operations needed by agent handlers.
type AgentStore interface {
	PollPendingCommands(ctx context.Context, nodeID pgtype.UUID) ([]store.Command, error)
	GetCommand(ctx context.Context, id pgtype.UUID) (store.Command, error)
}

// AgentLifecycle abstracts lifecycle operations triggered by agent callbacks.
type AgentLifecycle interface {
	HandleAck(ctx context.Context, cmdID, sandboxID uuid.UUID, cmdType, status string, result json.RawMessage) error
	HandleEvent(ctx context.Context, nodeID, sandboxID uuid.UUID, eventType string, payload json.RawMessage) error
}

// FilePushStore abstracts the database operations needed by the file push handler.
type FilePushStore interface {
	GetSandbox(ctx context.Context, id pgtype.UUID) (store.Sandbox, error)
	GetNode(ctx context.Context, id pgtype.UUID) (store.Node, error)
	UpdateSandboxLastActive(ctx context.Context, id pgtype.UUID) error
}

// ── Response Types ──────────────────────────────────────────────────

type commandResponse struct {
	ID             string          `json:"id"`
	Type           string          `json:"type"`
	SandboxID      *string         `json:"sandbox_id"`
	Payload        json.RawMessage `json:"payload"`
	IssuedAt       string          `json:"issued_at"`
	TimeoutSeconds int32           `json:"timeout_seconds"`
}

type pollResponse struct {
	Commands []commandResponse `json:"commands"`
}

// ── Request Types ───────────────────────────────────────────────────

type ackRequest struct {
	Status string          `json:"status"`
	Error  string          `json:"error,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

type eventRequest struct {
	SandboxID   string          `json:"sandbox_id"`
	EventType   string          `json:"event_type"`
	ContainerID string          `json:"container_id,omitempty"`
	ExitCode    *int            `json:"exit_code,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`
}

// ── Handlers ────────────────────────────────────────────────────────

// handlePollCommands handles GET /v1/agents/{id}/commands.
// Long-polls for up to `wait` seconds (default 30, max 60, min 1).
// Returns immediately if pending commands exist. Returns empty array on timeout.
func (s *Server) handlePollCommands(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	nodeID, err := parseUUID(idStr)
	if err != nil {
		BadRequest(w, "invalid node ID: must be a valid UUID")
		return
	}

	// Parse wait param: default 30, min 1, max 60
	waitSec := 30
	if waitStr := r.URL.Query().Get("wait"); waitStr != "" {
		w64, err := strconv.Atoi(waitStr)
		if err == nil {
			waitSec = w64
		}
	}
	if waitSec < 1 {
		waitSec = 1
	}
	if waitSec > 60 {
		waitSec = 60
	}

	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(waitSec)*time.Second)
	defer cancel()

	// Poll loop: check for commands, sleep 1s, repeat until timeout
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	// First check immediately (before any sleep)
	cmds, err := s.agentStore.PollPendingCommands(ctx, nodeID)
	if err != nil {
		s.logger.Error("poll pending commands failed", "error", err, "node_id", idStr)
		WriteProblem(w, http.StatusInternalServerError,
			"urn:forge:error:internal", "Internal Server Error", "failed to poll commands")
		return
	}

	if len(cmds) > 0 {
		writeJSON(w, http.StatusOK, pollResponse{Commands: mapCommands(cmds)})
		return
	}

	// Poll loop with 1s ticks
	for {
		select {
		case <-ctx.Done():
			// Timeout: return empty array
			writeJSON(w, http.StatusOK, pollResponse{Commands: []commandResponse{}})
			return
		case <-ticker.C:
			cmds, err = s.agentStore.PollPendingCommands(ctx, nodeID)
			if err != nil {
				// Context cancelled is expected on timeout
				if ctx.Err() != nil {
					writeJSON(w, http.StatusOK, pollResponse{Commands: []commandResponse{}})
					return
				}
				s.logger.Error("poll pending commands failed", "error", err, "node_id", idStr)
				WriteProblem(w, http.StatusInternalServerError,
					"urn:forge:error:internal", "Internal Server Error", "failed to poll commands")
				return
			}
			if len(cmds) > 0 {
				writeJSON(w, http.StatusOK, pollResponse{Commands: mapCommands(cmds)})
				return
			}
		}
	}
}

// handleAckCommand handles POST /v1/agents/{id}/commands/{cmd_id}/ack.
// Records the command acknowledgment and triggers lifecycle state transition.
func (s *Server) handleAckCommand(w http.ResponseWriter, r *http.Request) {
	// Parse node ID (validate but we primarily need cmd_id)
	idStr := chi.URLParam(r, "id")
	if _, err := parseUUID(idStr); err != nil {
		BadRequest(w, "invalid node ID: must be a valid UUID")
		return
	}

	cmdIDStr := chi.URLParam(r, "cmd_id")
	cmdID, err := uuid.Parse(cmdIDStr)
	if err != nil {
		BadRequest(w, "invalid command ID: must be a valid UUID")
		return
	}

	var req ackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		BadRequest(w, "invalid JSON body")
		return
	}

	// Look up the command to get sandbox_id and command_type
	pgCmdID := pgtype.UUID{Bytes: cmdID, Valid: true}
	cmd, err := s.agentStore.GetCommand(r.Context(), pgCmdID)
	if err != nil {
		NotFound(w, "command not found")
		return
	}

	sandboxID := uuid.UUID(cmd.SandboxID.Bytes)

	// Build result payload for lifecycle
	var resultPayload json.RawMessage
	if req.Result != nil {
		resultPayload = req.Result
	} else if req.Error != "" {
		resultPayload, _ = json.Marshal(map[string]string{"error": req.Error})
	}

	if err := s.agentLifecycle.HandleAck(r.Context(), cmdID, sandboxID, cmd.CommandType, req.Status, resultPayload); err != nil {
		s.logger.Error("handle ack failed", "error", err, "cmd_id", cmdIDStr)
		WriteProblem(w, http.StatusInternalServerError,
			"urn:forge:error:internal", "Internal Server Error", "failed to process acknowledgment")
		return
	}

	w.WriteHeader(http.StatusOK)
}

// handleReportEvent handles POST /v1/agents/{id}/events.
// Records the container event and triggers lifecycle state transition.
func (s *Server) handleReportEvent(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	nodeIDParsed, err := uuid.Parse(idStr)
	if err != nil {
		BadRequest(w, "invalid node ID: must be a valid UUID")
		return
	}

	var req eventRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		BadRequest(w, "invalid JSON body")
		return
	}

	// Validate required fields
	if req.SandboxID == "" {
		BadRequest(w, "sandbox_id is required")
		return
	}
	if req.EventType == "" {
		BadRequest(w, "event_type is required")
		return
	}

	sandboxID, err := uuid.Parse(req.SandboxID)
	if err != nil {
		BadRequest(w, "sandbox_id must be a valid UUID")
		return
	}

	if err := s.agentLifecycle.HandleEvent(r.Context(), nodeIDParsed, sandboxID, req.EventType, req.Payload); err != nil {
		s.logger.Error("handle event failed", "error", err, "node_id", idStr, "sandbox_id", req.SandboxID)
		WriteProblem(w, http.StatusInternalServerError,
			"urn:forge:error:internal", "Internal Server Error", "failed to process event")
		return
	}

	w.WriteHeader(http.StatusOK)
}

// ── File Push Handler ───────────────────────────────────────────────

// handleFilePush handles POST /v1/sandboxes/{id}/files.
// Returns 307 Temporary Redirect with an HMAC-signed URL pointing at the
// agent hosting the sandbox. The client follows the redirect to push files
// directly to the agent (T-03-14, T-03-15).
func (s *Server) handleFilePush(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	pgID, err := parseUUID(idStr)
	if err != nil {
		BadRequest(w, "invalid sandbox ID: must be a valid UUID")
		return
	}

	// Look up sandbox
	sandbox, err := s.filePushStore.GetSandbox(r.Context(), pgID)
	if err != nil {
		NotFound(w, "sandbox not found")
		return
	}

	// Sandbox must be assigned to a node
	if !sandbox.NodeID.Valid {
		ServiceUnavailable(w, "sandbox not yet scheduled to a node")
		return
	}

	// Get the node to find tailscale_ip and agent_listen_port
	node, err := s.filePushStore.GetNode(r.Context(), sandbox.NodeID)
	if err != nil {
		s.logger.Error("failed to get node for file push", "error", err, "node_id", formatUUID(sandbox.NodeID))
		WriteProblem(w, http.StatusInternalServerError,
			"urn:forge:error:internal", "Internal Server Error", "failed to resolve sandbox node")
		return
	}

	// Construct agent URL
	sandboxUUID := uuid.UUID(pgID.Bytes)
	agentURL := fmt.Sprintf("http://%s:%d/v1/sandboxes/%s/files",
		node.TailscaleIp.String(), node.AgentListenPort, sandboxUUID.String())

	// Sign URL with HMAC (60s expiry)
	signedURL, err := auth.SignURL(agentURL, []byte(s.config.hmacSecret), 60*time.Second)
	if err != nil {
		s.logger.Error("failed to sign file push URL", "error", err)
		WriteProblem(w, http.StatusInternalServerError,
			"urn:forge:error:internal", "Internal Server Error", "failed to generate signed URL")
		return
	}

	// Update last_active_at to prevent idle reaping
	if err := s.filePushStore.UpdateSandboxLastActive(r.Context(), pgID); err != nil {
		s.logger.Warn("failed to update sandbox last_active_at", "error", err, "sandbox_id", idStr)
		// Non-fatal: continue with redirect
	}

	// Return 307 Temporary Redirect
	w.Header().Set("Location", signedURL)
	w.WriteHeader(http.StatusTemporaryRedirect)
}

// ── Helpers ─────────────────────────────────────────────────────────

// mapCommands converts store.Command slice to API response format.
func mapCommands(cmds []store.Command) []commandResponse {
	result := make([]commandResponse, len(cmds))
	for i, c := range cmds {
		cmdResp := commandResponse{
			ID:             formatUUID(c.ID),
			Type:           c.CommandType,
			Payload:        c.Payload,
			TimeoutSeconds: c.TimeoutSeconds,
		}

		if c.SandboxID.Valid {
			sid := formatUUID(c.SandboxID)
			cmdResp.SandboxID = &sid
		}

		if c.CreatedAt.Valid {
			cmdResp.IssuedAt = c.CreatedAt.Time.Format(time.RFC3339)
		}

		result[i] = cmdResp
	}
	return result
}
