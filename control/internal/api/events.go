package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/appx/forge/control/internal/store"
)

// EventStore abstracts the database operations needed by the events handler.
type EventStore interface {
	ListEventsBySandbox(ctx context.Context, arg store.ListEventsBySandboxParams) ([]store.Event, error)
	ListEventsByType(ctx context.Context, arg store.ListEventsByTypeParams) ([]store.Event, error)
	ListRecentEvents(ctx context.Context, limit int32) ([]store.Event, error)
}

// ── Response Types ──────────────────────────────────────────────────

type eventResponse struct {
	ID        int64           `json:"id"`
	SandboxID string          `json:"sandbox_id,omitempty"`
	NodeID    string          `json:"node_id,omitempty"`
	EventType string          `json:"event_type"`
	Actor     string          `json:"actor"`
	PrevState string          `json:"prev_state,omitempty"`
	NextState string          `json:"next_state,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt string          `json:"created_at,omitempty"`
}

type eventListResponse struct {
	Events []eventResponse `json:"events"`
}

// ── Handlers ────────────────────────────────────────────────────────

// handleListEvents handles GET /v1/events.
// Supports filtering by sandbox_id or event_type. Without filters, returns
// recent events with a default limit of 100.
func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	if s.eventStore == nil {
		ServiceUnavailable(w, "event service not configured")
		return
	}

	q := r.URL.Query()

	// Parse limit (default 100, max 1000)
	limit := int32(100)
	if limitStr := q.Get("limit"); limitStr != "" {
		l, err := strconv.Atoi(limitStr)
		if err != nil || l < 1 {
			BadRequest(w, "limit must be a positive integer")
			return
		}
		if l > 1000 {
			l = 1000
		}
		limit = int32(l)
	}

	var events []store.Event
	var err error

	switch {
	case q.Get("sandbox_id") != "":
		sandboxID, parseErr := parseUUID(q.Get("sandbox_id"))
		if parseErr != nil {
			BadRequest(w, "invalid sandbox_id: must be a valid UUID")
			return
		}
		events, err = s.eventStore.ListEventsBySandbox(r.Context(), store.ListEventsBySandboxParams{
			SandboxID: sandboxID,
			Limit:     limit,
		})

	case q.Get("event_type") != "":
		events, err = s.eventStore.ListEventsByType(r.Context(), store.ListEventsByTypeParams{
			EventType: q.Get("event_type"),
			Limit:     limit,
		})

	default:
		events, err = s.eventStore.ListRecentEvents(r.Context(), limit)
	}

	if err != nil {
		s.logger.Error("failed to list events", "error", err)
		WriteProblem(w, http.StatusInternalServerError,
			"urn:forge:error:internal", "Internal Server Error", "failed to list events")
		return
	}

	resp := make([]eventResponse, len(events))
	for i, e := range events {
		resp[i] = storeEventToResponse(e)
	}

	writeJSON(w, http.StatusOK, eventListResponse{Events: resp})
}

// storeEventToResponse maps a store.Event to the API response type.
func storeEventToResponse(e store.Event) eventResponse {
	resp := eventResponse{
		ID:        e.ID,
		EventType: e.EventType,
		Actor:     e.Actor,
	}

	if e.SandboxID.Valid {
		resp.SandboxID = formatUUID(e.SandboxID)
	}
	if e.NodeID.Valid {
		resp.NodeID = formatUUID(e.NodeID)
	}
	if e.PrevState.Valid {
		resp.PrevState = e.PrevState.String
	}
	if e.NextState.Valid {
		resp.NextState = e.NextState.String
	}
	if len(e.Payload) > 0 {
		resp.Payload = e.Payload
	}
	if e.CreatedAt.Valid {
		resp.CreatedAt = e.CreatedAt.Time.Format("2006-01-02T15:04:05Z")
	}

	return resp
}
