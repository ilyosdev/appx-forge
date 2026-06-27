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
	"github.com/appx/forge/shared-go/models"
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
	DispatchBuildExport(ctx context.Context, sandboxID uuid.UUID, req lifecycle.BuildExportRequest) (string, error)
	DispatchStartHmr(ctx context.Context, sandboxID uuid.UUID, req lifecycle.StartHmrRequest) (string, error)
	DispatchStopHmr(ctx context.Context, sandboxID uuid.UUID, req lifecycle.StopHmrRequest) (string, error)
}

// SandboxMetadataWriter abstracts the metadata-merge store write
// (sleep-not-destroy, 2026-06-11). Satisfied by *store.Queries.
type SandboxMetadataWriter interface {
	MergeSandboxMetadata(ctx context.Context, arg store.MergeSandboxMetadataParams) (store.Sandbox, error)
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
	AppName            string                 `json:"app_name"`
	UserID             string                 `json:"user_id"`
	Image              string                 `json:"image"`
	Resources          *resourcesRequest      `json:"resources,omitempty"`
	Env                map[string]string      `json:"env,omitempty"`
	IdleTimeoutSeconds *int32                 `json:"idle_timeout_seconds,omitempty"`
	Metadata           map[string]interface{} `json:"metadata,omitempty"`
}

type resourcesRequest struct {
	CPUCores float64 `json:"cpu_cores"`
	MemoryMB int32   `json:"memory_mb"`
}

// SandboxResponse is the API response for a sandbox.
type SandboxResponse struct {
	ID           string          `json:"id"`
	AppName      string          `json:"app_name"`
	UserID       string          `json:"user_id"`
	NodeID       *string         `json:"node_id,omitempty"`
	ContainerID  *string         `json:"container_id,omitempty"`
	Image        string          `json:"image"`
	State        string          `json:"state"`
	URL          string          `json:"url"`
	Resources    json.RawMessage `json:"resources,omitempty"`
	HostPort     *int32          `json:"host_port,omitempty"`
	CreatedAt    string          `json:"created_at,omitempty"`
	UpdatedAt    string          `json:"updated_at,omitempty"`
	LastActiveAt string          `json:"last_active_at,omitempty"`
	FailureCount int32           `json:"failure_count"`
	Metadata     json.RawMessage `json:"metadata,omitempty"`
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

// handleMergeSandboxMetadata handles PUT /v1/sandboxes/{id}/metadata.
// Body: a flat JSON object merged into the sandbox's metadata (jsonb ||).
// The backend calls this at pool-claim time to tag appx.projectId so the
// idle reaper sleeps (rather than destroys) claimed sandboxes.
func (s *Server) handleMergeSandboxMetadata(w http.ResponseWriter, r *http.Request) {
	if s.sandboxMetaWriter == nil {
		ServiceUnavailable(w, "sandbox metadata writer not configured")
		return
	}
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		BadRequest(w, "invalid sandbox ID: must be a valid UUID")
		return
	}
	var patch map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil || len(patch) == 0 {
		BadRequest(w, "body must be a non-empty JSON object")
		return
	}
	raw, err := json.Marshal(patch)
	if err != nil {
		BadRequest(w, "unmarshalable patch")
		return
	}
	sb, err := s.sandboxMetaWriter.MergeSandboxMetadata(r.Context(), store.MergeSandboxMetadataParams{
		ID:    pgtype.UUID{Bytes: id, Valid: true},
		Patch: raw,
	})
	if err != nil {
		// Mirrors handleGetSandbox: a no-rows UPDATE and a real store error
		// are both surfaced as 404 (the row either isn't there or isn't
		// reachable); the caller treats the tag as best-effort.
		NotFound(w, "sandbox not found")
		return
	}

	// Wave-2 (2026-06-23): setting appx.projectId is the pool-CLAIM signal.
	// A just-claimed warm sandbox still carries last_active_at from its idle
	// warm life, so the idle reaper could sleep/destroy it in the window
	// between claim and the project's first code push. Mirror the wake handler
	// (handleWakeSandbox) and bump last_active_at on claim so the freshly
	// claimed sandbox gets a full idle window to become active. Best-effort —
	// a failed bump only risks an earlier reap, never a failed tag.
	if claimsProject(patch) && s.filePushStore != nil {
		if err := s.filePushStore.UpdateSandboxLastActive(r.Context(), pgtype.UUID{Bytes: id, Valid: true}); err != nil {
			s.logger.Warn("metadata-merge: failed to bump last_active_at on claim",
				"error", err, "sandbox_id", idStr)
		}
	}

	writeJSON(w, http.StatusOK, sandboxToResponse(sb))
}

// claimsProject reports whether a metadata patch sets a non-empty
// appx.projectId — i.e. it's a pool-claim tag (vs any other metadata merge).
func claimsProject(patch map[string]interface{}) bool {
	v, ok := patch["appx.projectId"]
	if !ok {
		return false
	}
	s, isStr := v.(string)
	return isStr && s != ""
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

	// Viewer keep-alive (2026-06-11): a successful wake must also bump
	// last_active_at. WakeSandbox itself only transitions state; if the row's
	// timestamp is already older than idle_timeout (the usual case for a just-
	// woken sandbox), the idle reaper would re-reap it on its next tick before
	// any file push lands (observed live: re-reaped 110s after wake). Handler-
	// level and best-effort — the wake already succeeded, a failed bump only
	// risks an earlier re-sleep, never a failed wake.
	if s.filePushStore != nil {
		pgID := pgtype.UUID{Bytes: id, Valid: true}
		if err := s.filePushStore.UpdateSandboxLastActive(r.Context(), pgID); err != nil {
			s.logger.Warn("wake: failed to bump last_active_at", "error", err, "sandbox_id", idStr)
		}
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

// handleTouchSandboxActivity handles POST /v1/sandboxes/{id}/activity.
// Pure last_active_at bump — no state transition, no event, no agent command.
//
// Why: the idle reaper's predicate (ListIdleSandboxes: state='running' AND
// last_active_at < NOW() - idle_timeout) previously saw activity ONLY from
// the file-push handler. Viewer HTTP traffic goes Server2-Caddy → container
// directly, so a user watching a live preview generated zero bumps and the
// reaper slept the container under them. The backend's viewer keep-alive
// cron calls this every ~5min for each project room with connected sockets.
//
// Idempotent and safe in any state: second-tier destroy keys off updated_at
// (ListStoppedExpired), so touching a stopped row never extends the 24h
// destroy retention. UUID-only, no app-name alias — consistent with
// wake/sleep/restart; the backend resolves aliases before calling.
func (s *Server) handleTouchSandboxActivity(w http.ResponseWriter, r *http.Request) {
	if s.filePushStore == nil {
		ServiceUnavailable(w, "sandbox service not configured")
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		BadRequest(w, "invalid sandbox ID: must be a valid UUID")
		return
	}

	pgID := pgtype.UUID{Bytes: id, Valid: true}
	sb, err := s.filePushStore.GetSandbox(r.Context(), pgID)
	if err != nil {
		// No-rows and store errors both surface as 404 (mirrors
		// handleMergeSandboxMetadata) — gives the backend a clean
		// phantom-row signal.
		NotFound(w, "sandbox not found")
		return
	}

	if err := s.filePushStore.UpdateSandboxLastActive(r.Context(), pgID); err != nil {
		s.logger.Error("touch sandbox activity failed", "error", err, "sandbox_id", idStr)
		WriteProblem(w, http.StatusInternalServerError,
			"urn:forge:error:internal", "Internal Server Error", "failed to update sandbox activity")
		return
	}

	// state lets the backend detect "forge slept it while viewers connected"
	// drift (logged as warn caller-side, never fatal).
	writeJSON(w, http.StatusOK, map[string]string{
		"id":    idStr,
		"state": sb.State,
	})
}

// ── Exec Handlers ────────────────────────────────────────────────────

// execRequest is the JSON body for POST /v1/sandboxes/{id}/exec.
type execRequest struct {
	Command        string            `json:"command"`
	Cwd            string            `json:"cwd,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	// CPUBurst, when true, asks the agent to temporarily raise the sandbox CPU
	// cap for this exec. Optional (omitted = false). Pass-through only — control
	// forwards it to the agent verbatim via lifecycle.ExecRequest.
	CPUBurst bool `json:"cpu_burst,omitempty"`
	// User runs the exec as a specific user (empty = agent default appuser).
	// Optional pass-through — forwarded verbatim via lifecycle.ExecRequest.
	User string `json:"user,omitempty"`
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
	// BuildID is set only for build_export results — it identifies the build
	// snapshot whose dist/ the backend then fetches via ?build=<id>.
	BuildID string `json:"build_id,omitempty"`
	// HmrContainerID / HostPort / NodeID are set only for start_hmr results —
	// the backend builds the ephemeral Caddy upstream dial (<nodeIp>:<HostPort>)
	// from NodeID + HostPort. spinUp rejects the box if NodeID is missing.
	HmrContainerID string `json:"hmr_container_id,omitempty"`
	HostPort       *int   `json:"host_port,omitempty"`
	NodeID         string `json:"node_id,omitempty"`
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
		CPUBurst:       req.CPUBurst,
		User:           req.User,
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

// buildExportRequest is the JSON body for POST /v1/sandboxes/{id}/build-export.
// It mirrors execRequest (the export command + env are owned by the backend);
// the image is resolved from the dev sandbox row server-side.
type buildExportRequest struct {
	Command        string            `json:"command"`
	Cwd            string            `json:"cwd,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	CPUBurst       bool              `json:"cpu_burst,omitempty"`
	User           string            `json:"user,omitempty"`
}

// handleBuildExport handles POST /v1/sandboxes/{id}/build-export.
// Dispatches an isolated cold web export (build_export command) to the agent
// hosting the sandbox and returns 202 Accepted with the new command_id. The
// caller polls GET /v1/sandboxes/{id}/exec/{cmd_id} for the result (which
// carries build_id once complete), then fetches the dist via
// GET /v1/sandboxes/{id}/dist?build=<build_id>.
//
// Unlike exec, this does NOT require the sandbox to be running: the worker
// snapshots the on-disk code dir, which persists across a slept sandbox.
func (s *Server) handleBuildExport(w http.ResponseWriter, r *http.Request) {
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

	var req buildExportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		BadRequest(w, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Command) == "" {
		BadRequest(w, "command is required")
		return
	}
	if req.TimeoutSeconds < 0 {
		BadRequest(w, "timeout_seconds must be non-negative")
		return
	}
	if req.TimeoutSeconds > 300 {
		req.TimeoutSeconds = 300
	}

	// Verify the sandbox exists (cleaner 404) before dispatching; lifecycle
	// validates node assignment.
	pgID := pgtype.UUID{Bytes: sandboxID, Valid: true}
	if _, err := s.sandboxReader.GetSandbox(r.Context(), pgID); err != nil {
		NotFound(w, "sandbox not found")
		return
	}

	cmdID, err := s.lifecycle.DispatchBuildExport(r.Context(), sandboxID, lifecycle.BuildExportRequest{
		Command:        req.Command,
		Cwd:            req.Cwd,
		Env:            req.Env,
		TimeoutSeconds: req.TimeoutSeconds,
		CPUBurst:       req.CPUBurst,
		User:           req.User,
	})
	if err != nil {
		switch {
		case errors.Is(err, lifecycle.ErrNotFound):
			NotFound(w, "sandbox not found")
		case errors.Is(err, lifecycle.ErrSandboxNotAssigned):
			ServiceUnavailable(w, "sandbox not assigned to a node")
		default:
			s.logger.Error("build_export dispatch failed", "error", err, "sandbox_id", idStr)
			WriteProblem(w, http.StatusInternalServerError,
				"urn:forge:error:internal", "Internal Server Error", "failed to dispatch build_export command")
		}
		return
	}

	writeJSON(w, http.StatusAccepted, execAcceptResponse{
		CommandID: cmdID,
		Status:    "queued",
	})
}

// startHmrRequest is the JSON body for POST /v1/sandboxes/{id}/hmr. The image
// is resolved from the dev sandbox row server-side.
type startHmrRequest struct {
	TurnID string            `json:"turn_id"`
	Env    map[string]string `json:"env,omitempty"`
}

// handleStartHmr handles POST /v1/sandboxes/{id}/hmr.
// Dispatches a start_hmr command (per-turn ephemeral dev Metro box) to the agent
// hosting the sandbox and returns 202 Accepted with the new command_id. The
// caller polls GET /v1/sandboxes/{id}/exec/{cmd_id} for the result, which
// carries {hmr_container_id, host_port} once complete; the backend then adds the
// ephemeral Caddy route and emits preview:hmr-ready.
//
// Unlike exec, this does NOT require the sandbox to be running: the box binds
// the on-disk live code dir, which persists across a slept sandbox.
func (s *Server) handleStartHmr(w http.ResponseWriter, r *http.Request) {
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

	var req startHmrRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		BadRequest(w, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.TurnID) == "" {
		BadRequest(w, "turn_id is required")
		return
	}

	// Verify the sandbox exists (cleaner 404) before dispatching; lifecycle
	// validates node assignment.
	pgID := pgtype.UUID{Bytes: sandboxID, Valid: true}
	if _, err := s.sandboxReader.GetSandbox(r.Context(), pgID); err != nil {
		NotFound(w, "sandbox not found")
		return
	}

	cmdID, err := s.lifecycle.DispatchStartHmr(r.Context(), sandboxID, lifecycle.StartHmrRequest{
		TurnID: req.TurnID,
		Env:    req.Env,
	})
	if err != nil {
		switch {
		case errors.Is(err, lifecycle.ErrNotFound):
			NotFound(w, "sandbox not found")
		case errors.Is(err, lifecycle.ErrSandboxNotAssigned):
			ServiceUnavailable(w, "sandbox not assigned to a node")
		default:
			s.logger.Error("start_hmr dispatch failed", "error", err, "sandbox_id", idStr)
			WriteProblem(w, http.StatusInternalServerError,
				"urn:forge:error:internal", "Internal Server Error", "failed to dispatch start_hmr command")
		}
		return
	}

	writeJSON(w, http.StatusAccepted, execAcceptResponse{
		CommandID: cmdID,
		Status:    "queued",
	})
}

// handleStopHmr handles DELETE /v1/sandboxes/{id}/hmr/{turn}.
// Dispatches a stop_hmr command (force-remove the per-turn box + release its
// port) to the agent hosting the sandbox and returns 202 Accepted with the new
// command_id. Idempotent agent-side: a turn whose box is already gone acks
// success.
func (s *Server) handleStopHmr(w http.ResponseWriter, r *http.Request) {
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

	turnID := chi.URLParam(r, "turn")
	if strings.TrimSpace(turnID) == "" {
		BadRequest(w, "turn is required")
		return
	}

	pgID := pgtype.UUID{Bytes: sandboxID, Valid: true}
	if _, err := s.sandboxReader.GetSandbox(r.Context(), pgID); err != nil {
		NotFound(w, "sandbox not found")
		return
	}

	cmdID, err := s.lifecycle.DispatchStopHmr(r.Context(), sandboxID, lifecycle.StopHmrRequest{
		TurnID: turnID,
	})
	if err != nil {
		switch {
		case errors.Is(err, lifecycle.ErrNotFound):
			NotFound(w, "sandbox not found")
		case errors.Is(err, lifecycle.ErrSandboxNotAssigned):
			ServiceUnavailable(w, "sandbox not assigned to a node")
		default:
			s.logger.Error("stop_hmr dispatch failed", "error", err, "sandbox_id", idStr)
			WriteProblem(w, http.StatusInternalServerError,
				"urn:forge:error:internal", "Internal Server Error", "failed to dispatch stop_hmr command")
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

	// Only return results for exec + build_export + start_hmr commands (the
	// latter two reuse this poll endpoint so waitForExec is shared verbatim).
	// Other command types (start_sandbox, stop_sandbox, restart_sandbox,
	// stop_hmr) have their own UX and shouldn't leak through this endpoint shape.
	if cmd.CommandType != "exec" &&
		cmd.CommandType != string(models.CmdBuildExport) &&
		cmd.CommandType != string(models.CmdStartHmr) {
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
				if s, ok := resultMap["build_id"].(string); ok {
					resp.BuildID = s
				}
				if s, ok := resultMap["hmr_container_id"].(string); ok {
					resp.HmrContainerID = s
				}
				if f, ok := resultMap["host_port"].(float64); ok {
					hp := int(f)
					resp.HostPort = &hp
				}
				if s, ok := resultMap["node_id"].(string); ok {
					resp.NodeID = s
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
