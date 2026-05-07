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
