package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/appx/forge/control/internal/lifecycle"
	"github.com/appx/forge/control/internal/scheduler"
	"github.com/appx/forge/control/internal/store"
)

// appNameRegex validates app_name per OpenAPI spec: ^[a-z0-9][a-z0-9-]{1,62}$
var appNameRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

// ── Interfaces ───────────────────────────────────────────────────────

// SandboxLifecycle abstracts the lifecycle service for sandbox handlers.
type SandboxLifecycle interface {
	CreateSandbox(ctx context.Context, req lifecycle.CreateRequest) (*lifecycle.SandboxResult, error)
	DestroySandbox(ctx context.Context, id uuid.UUID) error
	RestartSandbox(ctx context.Context, id uuid.UUID) error
	WakeSandbox(ctx context.Context, id uuid.UUID) error
	SleepSandbox(ctx context.Context, id uuid.UUID) error
	DispatchExec(ctx context.Context, sandboxID uuid.UUID, req lifecycle.ExecRequest) (string, error)
}

// SandboxReader abstracts the read-only store operations for sandbox handlers.
type SandboxReader interface {
	GetSandbox(ctx context.Context, id pgtype.UUID) (store.Sandbox, error)
	GetSandboxByAppName(ctx context.Context, appName string) (store.Sandbox, error)
	ListSandboxes(ctx context.Context, limit int32) ([]store.Sandbox, error)
	ListSandboxesByState(ctx context.Context, state string) ([]store.Sandbox, error)
	ListSandboxesByNode(ctx context.Context, nodeID pgtype.UUID) ([]store.Sandbox, error)
	ListSandboxesByUser(ctx context.Context, userID string) ([]store.Sandbox, error)
}

// ── Request/Response Types ───────────────────────────────────────────

type createSandboxRequest struct {
	AppName            string            `json:"app_name"`
	UserID             string            `json:"user_id"`
	Image              string            `json:"image"`
	Resources          *resourcesRequest `json:"resources,omitempty"`
	Env                map[string]string `json:"env,omitempty"`
	IdleTimeoutSeconds *int32            `json:"idle_timeout_seconds,omitempty"`
	Metadata           map[string]interface{} `json:"metadata,omitempty"`
}

type resourcesRequest struct {
	CPUCores float64 `json:"cpu_cores"`
	MemoryMB int32   `json:"memory_mb"`
}

// SandboxResponse is the API response for a sandbox.
type SandboxResponse struct {
	ID           string           `json:"id"`
	AppName      string           `json:"app_name"`
	UserID       string           `json:"user_id"`
	NodeID       *string          `json:"node_id,omitempty"`
	ContainerID  *string          `json:"container_id,omitempty"`
	Image        string           `json:"image"`
	State        string           `json:"state"`
	URL          string           `json:"url"`
	Resources    json.RawMessage  `json:"resources,omitempty"`
	HostPort     *int32           `json:"host_port,omitempty"`
	CreatedAt    string           `json:"created_at,omitempty"`
	UpdatedAt    string           `json:"updated_at,omitempty"`
	LastActiveAt string           `json:"last_active_at,omitempty"`
	FailureCount int32            `json:"failure_count"`
	Metadata     json.RawMessage  `json:"metadata,omitempty"`
	// Phase 30 — last time control plane verified this row against agent truth.
	// ISO8601 (RFC3339). Empty when the row predates the verified_at column
	// or has never been touched by reconciler / freshness check.
	VerifiedAt string `json:"verified_at,omitempty"`
}

type sandboxListResponse struct {
	Sandboxes []SandboxResponse `json:"sandboxes"`
	// Phase 30 — oldest verified_at across the returned rows. Lets the
	// caller (backend ContainerStateService) decide whether the cached
	// fleet snapshot is fresh enough to use, or whether to issue a
	// per-row force_refresh follow-up. Empty when no rows have a
	// verified_at (legacy data).
	FleetVerifiedAt string `json:"fleet_verified_at,omitempty"`
}

// ── Handlers ─────────────────────────────────────────────────────────

// handleCreateSandbox handles POST /v1/sandboxes.
func (s *Server) handleCreateSandbox(w http.ResponseWriter, r *http.Request) {
	if s.lifecycle == nil {
		ServiceUnavailable(w, "sandbox service not configured")
		return
	}

	var req createSandboxRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		BadRequest(w, "invalid JSON body")
		return
	}

	// Validate required fields
	if req.AppName == "" {
		BadRequest(w, "app_name is required")
		return
	}
	if !appNameRegex.MatchString(req.AppName) {
		BadRequest(w, "app_name must match pattern ^[a-z0-9][a-z0-9-]{1,62}$")
		return
	}
	if req.UserID == "" {
		BadRequest(w, "user_id is required")
		return
	}
	if req.Image == "" {
		BadRequest(w, "image is required")
		return
	}

	// Apply defaults
	resources := lifecycle.Resources{CPUCores: 0.5, MemoryMB: 512}
	if req.Resources != nil {
		if req.Resources.CPUCores > 0 {
			resources.CPUCores = req.Resources.CPUCores
		}
		if req.Resources.MemoryMB > 0 {
			resources.MemoryMB = req.Resources.MemoryMB
		}
	}

	idleTimeout := int32(1800)
	if req.IdleTimeoutSeconds != nil {
		idleTimeout = *req.IdleTimeoutSeconds
	}

	result, err := s.lifecycle.CreateSandbox(r.Context(), lifecycle.CreateRequest{
		AppName:            req.AppName,
		UserID:             req.UserID,
		Image:              req.Image,
		Resources:          resources,
		Env:                req.Env,
		IdleTimeoutSeconds: idleTimeout,
		Metadata:           req.Metadata,
	})
	if err != nil {
		switch {
		case errors.Is(err, lifecycle.ErrConflict):
			Conflict(w, "app_name already exists")
		case errors.Is(err, lifecycle.ErrNoCapacity):
			ServiceUnavailable(w, "no nodes available with sufficient capacity")
		default:
			s.logger.Error("create sandbox failed", "error", err)
			WriteProblem(w, http.StatusInternalServerError,
				"urn:forge:error:internal", "Internal Server Error", "failed to create sandbox")
		}
		return
	}

	resp := sandboxResultToResponse(result)
	writeJSON(w, http.StatusCreated, resp)
}

// handleGetSandbox handles GET /v1/sandboxes/{id}.
// Supports UUID lookup and app:name lookup.
//
// Phase 30 — when freshness is wired AND the row's verified_at is stale
// (or ?force_refresh=true), the handler calls SandboxFreshnessService to
// confirm container existence against the agent before returning. On
// confirmed agent miss the row is marked destroyed and the handler returns
// 404. On agent unreachable the freshness impl returns the cached row;
// the handler stays available with the stored verified_at.
func (s *Server) handleGetSandbox(w http.ResponseWriter, r *http.Request) {
	if s.sandboxReader == nil {
		ServiceUnavailable(w, "sandbox service not configured")
		return
	}

	idStr := chi.URLParam(r, "id")

	var sandbox store.Sandbox
	var err error

	if strings.HasPrefix(idStr, "app:") {
		appName := strings.TrimPrefix(idStr, "app:")
		sandbox, err = s.sandboxReader.GetSandboxByAppName(r.Context(), appName)
	} else {
		pgID, parseErr := parseUUID(idStr)
		if parseErr != nil {
			BadRequest(w, "invalid sandbox ID: must be a valid UUID or app:{name}")
			return
		}
		sandbox, err = s.sandboxReader.GetSandbox(r.Context(), pgID)
	}

	if err != nil {
		NotFound(w, "sandbox not found")
		return
	}

	resp := sandboxToResponse(sandbox)

	// Phase 30 — freshness check. Skipped when the service is not wired
	// or when the row has no app_name (defensive — should never happen).
	forceRefresh := r.URL.Query().Get("force_refresh") == "true"
	if s.freshness != nil && sandbox.AppName != "" {
		_, refreshedAt, refErr := s.freshness.GetSandbox(r.Context(), sandbox.AppName, forceRefresh)
		if errors.Is(refErr, scheduler.ErrSandboxNotFound) {
			NotFound(w, "sandbox not found")
			return
		}
		if refErr != nil {
			s.logger.Warn("freshness check failed; serving cached row",
				"app_name", sandbox.AppName, "err", refErr)
		} else if !refreshedAt.IsZero() {
			resp.VerifiedAt = refreshedAt.Format(time.RFC3339)
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleListSandboxes handles GET /v1/sandboxes.
// Supports query filters: app_name, user_id, state, node_id, limit.
func (s *Server) handleListSandboxes(w http.ResponseWriter, r *http.Request) {
	if s.sandboxReader == nil {
		ServiceUnavailable(w, "sandbox service not configured")
		return
	}

	q := r.URL.Query()

	var sandboxes []store.Sandbox
	var err error

	switch {
	case q.Get("app_name") != "":
		sb, singleErr := s.sandboxReader.GetSandboxByAppName(r.Context(), q.Get("app_name"))
		if singleErr != nil {
			writeJSON(w, http.StatusOK, sandboxListResponse{Sandboxes: []SandboxResponse{}})
			return
		}
		sandboxes = []store.Sandbox{sb}

	case q.Get("user_id") != "":
		sandboxes, err = s.sandboxReader.ListSandboxesByUser(r.Context(), q.Get("user_id"))

	case q.Get("state") != "":
		sandboxes, err = s.sandboxReader.ListSandboxesByState(r.Context(), q.Get("state"))

	case q.Get("node_id") != "":
		nodeID, parseErr := parseUUID(q.Get("node_id"))
		if parseErr != nil {
			BadRequest(w, "invalid node_id: must be a valid UUID")
			return
		}
		sandboxes, err = s.sandboxReader.ListSandboxesByNode(r.Context(), nodeID)

	default:
		limit := int32(50)
		if limitStr := q.Get("limit"); limitStr != "" {
			l, parseErr := strconv.Atoi(limitStr)
			if parseErr != nil || l < 1 {
				BadRequest(w, "limit must be a positive integer")
				return
			}
			if l > 500 {
				l = 500
			}
			limit = int32(l)
		}
		sandboxes, err = s.sandboxReader.ListSandboxes(r.Context(), limit)
	}

	if err != nil {
		s.logger.Error("list sandboxes failed", "error", err)
		WriteProblem(w, http.StatusInternalServerError,
			"urn:forge:error:internal", "Internal Server Error", "failed to list sandboxes")
		return
	}

	resp := make([]SandboxResponse, len(sandboxes))
	// Phase 30 — track the oldest verified_at to surface fleet freshness.
	var oldestVerified time.Time
	for i, sb := range sandboxes {
		resp[i] = sandboxToResponse(sb)
		if sb.VerifiedAt.Valid {
			t := sb.VerifiedAt.Time
			if oldestVerified.IsZero() || t.Before(oldestVerified) {
				oldestVerified = t
			}
		}
	}

	listResp := sandboxListResponse{Sandboxes: resp}
	if !oldestVerified.IsZero() {
		listResp.FleetVerifiedAt = oldestVerified.Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, listResp)
}

// handleDestroySandbox handles DELETE /v1/sandboxes/{id}.
func (s *Server) handleDestroySandbox(w http.ResponseWriter, r *http.Request) {
	if s.lifecycle == nil {
		ServiceUnavailable(w, "sandbox service not configured")
		return
	}

	idStr := chi.URLParam(r, "id")

	id, err := uuid.Parse(idStr)
	if err != nil {
		BadRequest(w, "invalid sandbox ID: must be a valid UUID")
		return
	}

	if err := s.lifecycle.DestroySandbox(r.Context(), id); err != nil {
		if errors.Is(err, lifecycle.ErrNotFound) {
			NotFound(w, "sandbox not found")
			return
		}
		s.logger.Error("destroy sandbox failed", "error", err, "sandbox_id", idStr)
		WriteProblem(w, http.StatusInternalServerError,
			"urn:forge:error:internal", "Internal Server Error", "failed to destroy sandbox")
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// handleRestartSandbox handles POST /v1/sandboxes/{id}/restart.
func (s *Server) handleRestartSandbox(w http.ResponseWriter, r *http.Request) {
	if s.lifecycle == nil {
		ServiceUnavailable(w, "sandbox service not configured")
		return
	}

	idStr := chi.URLParam(r, "id")

	id, err := uuid.Parse(idStr)
	if err != nil {
		BadRequest(w, "invalid sandbox ID: must be a valid UUID")
		return
	}

	if err := s.lifecycle.RestartSandbox(r.Context(), id); err != nil {
		if errors.Is(err, lifecycle.ErrNotFound) {
			NotFound(w, "sandbox not found")
			return
		}
		s.logger.Error("restart sandbox failed", "error", err, "sandbox_id", idStr)
		WriteProblem(w, http.StatusInternalServerError,
			"urn:forge:error:internal", "Internal Server Error", "failed to restart sandbox")
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// handleWakeSandbox handles POST /v1/sandboxes/{id}/wake.
// Re-starts a stopped (idle-reaped) sandbox without creating a new one.
func (s *Server) handleWakeSandbox(w http.ResponseWriter, r *http.Request) {
	if s.lifecycle == nil {
		ServiceUnavailable(w, "sandbox service not configured")
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		BadRequest(w, "invalid sandbox ID: must be a valid UUID")
		return
	}

	if err := s.lifecycle.WakeSandbox(r.Context(), id); err != nil {
		if errors.Is(err, lifecycle.ErrNotFound) {
			NotFound(w, "sandbox not found")
			return
		}
		if errors.Is(err, lifecycle.ErrInvalidState) {
			WriteProblem(w, http.StatusConflict, "urn:forge:error:conflict", "Conflict", err.Error())
			return
		}
		s.logger.Error("wake sandbox failed", "error", err, "sandbox_id", idStr)
		WriteProblem(w, http.StatusInternalServerError,
			"urn:forge:error:internal", "Internal Server Error", "failed to wake sandbox")
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// handleSleepSandbox handles POST /v1/sandboxes/{id}/sleep.
// Stops a running sandbox without destroying it, leaving it in StateStopped so
// a subsequent wake can revive it. Idempotent on already-stopped sandboxes.
func (s *Server) handleSleepSandbox(w http.ResponseWriter, r *http.Request) {
	if s.lifecycle == nil {
		ServiceUnavailable(w, "sandbox service not configured")
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		BadRequest(w, "invalid sandbox ID: must be a valid UUID")
		return
	}

	if err := s.lifecycle.SleepSandbox(r.Context(), id); err != nil {
		if errors.Is(err, lifecycle.ErrNotFound) {
			NotFound(w, "sandbox not found")
			return
		}
		if errors.Is(err, lifecycle.ErrInvalidState) {
			WriteProblem(w, http.StatusConflict, "urn:forge:error:conflict", "Conflict", err.Error())
			return
		}
		s.logger.Error("sleep sandbox failed", "error", err, "sandbox_id", idStr)
		WriteProblem(w, http.StatusInternalServerError,
			"urn:forge:error:internal", "Internal Server Error", "failed to sleep sandbox")
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// ── Exec Handlers ────────────────────────────────────────────────────

// execRequest is the JSON body for POST /v1/sandboxes/{id}/exec.
type execRequest struct {
	Command        string            `json:"command"`
	Cwd            string            `json:"cwd,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
}

// execAcceptResponse is the 202 Accepted body returned by handleExecSandbox.
type execAcceptResponse struct {
	CommandID string `json:"command_id"`
	Status    string `json:"status"`
}

// execResultResponse is the 200 OK body returned by handleGetExecResult.
// status mirrors the command lifecycle:
//
//	queued    — agent has not yet picked up the command (status=pending)
//	running   — agent is executing (status=dispatched, no ack yet)
//	complete  — agent acked success (status=completed)
//	failed    — agent acked failure (status=failed)
type execResultResponse struct {
	Status          string `json:"status"`
	ExitCode        *int   `json:"exit_code,omitempty"`
	Stdout          string `json:"stdout,omitempty"`
	Stderr          string `json:"stderr,omitempty"`
	StdoutTruncated bool   `json:"stdout_truncated,omitempty"`
	StderrTruncated bool   `json:"stderr_truncated,omitempty"`
	DurationMs      int64  `json:"duration_ms,omitempty"`
}

// handleExecSandbox handles POST /v1/sandboxes/{id}/exec.
// Dispatches a shell command to the agent hosting the sandbox and returns
// 202 Accepted with the new command_id. Caller polls
// GET /v1/sandboxes/{id}/exec/{cmd_id} for the result, or subscribes to
// the exec_completed webhook to skip polling entirely.
func (s *Server) handleExecSandbox(w http.ResponseWriter, r *http.Request) {
	if s.lifecycle == nil {
		ServiceUnavailable(w, "sandbox service not configured")
		return
	}
	if s.sandboxReader == nil {
		ServiceUnavailable(w, "sandbox service not configured")
		return
	}

	idStr := chi.URLParam(r, "id")
	sandboxID, err := uuid.Parse(idStr)
	if err != nil {
		BadRequest(w, "invalid sandbox ID: must be a valid UUID")
		return
	}

	var req execRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		BadRequest(w, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Command) == "" {
		BadRequest(w, "command is required")
		return
	}
	// Clamp timeout into [1, 300] up front so the API contract matches the
	// lifecycle layer. Callers passing 0 get the 120s default from lifecycle.
	if req.TimeoutSeconds < 0 {
		BadRequest(w, "timeout_seconds must be non-negative")
		return
	}
	if req.TimeoutSeconds > 300 {
		req.TimeoutSeconds = 300
	}

	// Verify sandbox exists + is running before dispatching. lifecycle's
	// DispatchExec also validates, but checking here lets us return 404 vs
	// 409 vs 500 with clean separation.
	pgID := pgtype.UUID{Bytes: sandboxID, Valid: true}
	sandbox, err := s.sandboxReader.GetSandbox(r.Context(), pgID)
	if err != nil {
		NotFound(w, "sandbox not found")
		return
	}
	if sandbox.State != "running" {
		WriteProblem(w, http.StatusConflict, "urn:forge:error:conflict",
			"Conflict", fmt.Sprintf("sandbox must be running to exec, current state: %s", sandbox.State))
		return
	}

	cmdID, err := s.lifecycle.DispatchExec(r.Context(), sandboxID, lifecycle.ExecRequest{
		Command:        req.Command,
		Cwd:            req.Cwd,
		Env:            req.Env,
		TimeoutSeconds: req.TimeoutSeconds,
	})
	if err != nil {
		switch {
		case errors.Is(err, lifecycle.ErrNotFound):
			NotFound(w, "sandbox not found")
		case errors.Is(err, lifecycle.ErrSandboxNotAssigned):
			ServiceUnavailable(w, "sandbox not assigned to a node")
		case errors.Is(err, lifecycle.ErrSandboxNotRunning):
			WriteProblem(w, http.StatusConflict, "urn:forge:error:conflict", "Conflict", err.Error())
		default:
			s.logger.Error("exec dispatch failed", "error", err, "sandbox_id", idStr)
			WriteProblem(w, http.StatusInternalServerError,
				"urn:forge:error:internal", "Internal Server Error", "failed to dispatch exec command")
		}
		return
	}

	writeJSON(w, http.StatusAccepted, execAcceptResponse{
		CommandID: cmdID,
		Status:    "queued",
	})
}

// handleGetExecResult handles GET /v1/sandboxes/{id}/exec/{cmd_id}.
// Returns the current status of the exec command and, once acked, the
// stdout/stderr/exit_code/duration extracted from the agent's ack result.
//
// Authorization: the cmd_id must belong to the sandbox in the path — a
// caller authorized for sandbox A cannot read exec results from sandbox B
// even if they know the cmd_id.
func (s *Server) handleGetExecResult(w http.ResponseWriter, r *http.Request) {
	if s.agentStore == nil {
		ServiceUnavailable(w, "command service not configured")
		return
	}

	idStr := chi.URLParam(r, "id")
	sandboxID, err := uuid.Parse(idStr)
	if err != nil {
		BadRequest(w, "invalid sandbox ID: must be a valid UUID")
		return
	}

	cmdIDStr := chi.URLParam(r, "cmd_id")
	cmdID, err := uuid.Parse(cmdIDStr)
	if err != nil {
		BadRequest(w, "invalid command ID: must be a valid UUID")
		return
	}

	cmd, err := s.agentStore.GetCommand(r.Context(), pgtype.UUID{Bytes: cmdID, Valid: true})
	if err != nil {
		NotFound(w, "command not found")
		return
	}

	// Authorization check: refuse cross-sandbox reads. Compare the bytes
	// directly rather than re-formatting both sides as strings.
	if !cmd.SandboxID.Valid || cmd.SandboxID.Bytes != sandboxID {
		NotFound(w, "command not found")
		return
	}

	// Only return results for exec commands. Other command types (start_sandbox,
	// stop_sandbox, restart_sandbox) have their own UX and shouldn't leak through
	// this endpoint shape.
	if cmd.CommandType != "exec" {
		NotFound(w, "command not found")
		return
	}

	resp := execResultResponse{}
	switch cmd.Status {
	case "pending":
		resp.Status = "queued"
	case "dispatched":
		resp.Status = "running"
	case "completed":
		resp.Status = "complete"
	case "failed":
		resp.Status = "failed"
	default:
		// Defensive: unknown status — treat as failed so caller knows
		// the row is no longer in flight.
		resp.Status = "failed"
	}

	// Parse the ack result for terminal states. We don't fail the request
	// if parsing fails — surface the status alone, log the parse error.
	if cmd.Status == "completed" || cmd.Status == "failed" {
		if len(cmd.Result) > 0 {
			var resultMap map[string]interface{}
			if err := json.Unmarshal(cmd.Result, &resultMap); err == nil {
				if v, ok := resultMap["exit_code"]; ok {
					if f, isNum := v.(float64); isNum {
						exit := int(f)
						resp.ExitCode = &exit
					}
				}
				if s, ok := resultMap["stdout"].(string); ok {
					resp.Stdout = s
				}
				if s, ok := resultMap["stderr"].(string); ok {
					resp.Stderr = s
				}
				if b, ok := resultMap["stdout_truncated"].(bool); ok {
					resp.StdoutTruncated = b
				}
				if b, ok := resultMap["stderr_truncated"].(bool); ok {
					resp.StderrTruncated = b
				}
				if f, ok := resultMap["duration_ms"].(float64); ok {
					resp.DurationMs = int64(f)
				}
			} else {
				s.logger.Warn("exec result parse failed",
					"err", err, "cmd_id", cmdIDStr, "sandbox_id", idStr)
			}
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// ── Response Mappers ─────────────────────────────────────────────────

// sandboxToResponse converts a store.Sandbox to the API response type.
func sandboxToResponse(s store.Sandbox) SandboxResponse {
	resp := SandboxResponse{
		ID:           formatUUID(s.ID),
		AppName:      s.AppName,
		UserID:       s.UserID,
		Image:        s.Image,
		State:        s.State,
		URL:          fmt.Sprintf("https://%s.myappx.live", s.AppName),
		Resources:    s.Resources,
		FailureCount: s.FailureCount,
		Metadata:     s.Metadata,
	}

	if s.NodeID.Valid {
		nodeIDStr := formatUUID(s.NodeID)
		resp.NodeID = &nodeIDStr
	}

	if s.ContainerID.Valid {
		resp.ContainerID = &s.ContainerID.String
	}

	if s.HostPort.Valid {
		resp.HostPort = &s.HostPort.Int32
	}

	if s.CreatedAt.Valid {
		resp.CreatedAt = s.CreatedAt.Time.Format("2006-01-02T15:04:05Z")
	}

	if s.UpdatedAt.Valid {
		resp.UpdatedAt = s.UpdatedAt.Time.Format("2006-01-02T15:04:05Z")
	}

	if s.LastActiveAt.Valid {
		resp.LastActiveAt = s.LastActiveAt.Time.Format("2006-01-02T15:04:05Z")
	}

	if s.VerifiedAt.Valid {
		resp.VerifiedAt = s.VerifiedAt.Time.Format(time.RFC3339)
	}

	return resp
}

// sandboxResultToResponse converts a lifecycle.SandboxResult to the API response type.
func sandboxResultToResponse(r *lifecycle.SandboxResult) SandboxResponse {
	resp := SandboxResponse{
		ID:      r.ID.String(),
		AppName: r.AppName,
		UserID:  r.UserID,
		Image:   r.Image,
		State:   r.State,
		URL:     r.URL,
	}

	if r.NodeID != nil {
		nodeIDStr := r.NodeID.String()
		resp.NodeID = &nodeIDStr
	}

	return resp
}
