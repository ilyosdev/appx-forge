package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/netip"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// NodeStore abstracts the database operations needed by node handlers.
// In production, a wrapper around *store.Queries satisfies this interface.
// In tests, a mock implementation is injected.
type NodeStore interface {
	GetNodeByHostnameAndIP(ctx context.Context, hostname string, ip netip.Addr) (NodeRecord, error)
	CreateNode(ctx context.Context, arg CreateNodeArgs) (NodeRecord, error)
	UpdateNodeToken(ctx context.Context, token string, agentVersion string, id pgtype.UUID) error
	GetNode(ctx context.Context, id pgtype.UUID) (NodeRecord, error)
	UpdateNodeHeartbeat(ctx context.Context, id pgtype.UUID, usedMb int32, runningContainers int32) error
}

// NodeRecord is the minimal node data handlers need. It decouples handlers from
// the sqlc-generated store.Node type.
type NodeRecord struct {
	ID       pgtype.UUID
	Hostname string
}

// CreateNodeArgs are the parameters for creating a new node.
type CreateNodeArgs struct {
	ID              pgtype.UUID
	Hostname        string
	TailscaleIP     netip.Addr
	AgentListenPort int32
	CapacityMb      int32
	CapacityCPU     float64
	AgentVersion    string
	Metadata        []byte
	AgentToken      string
}

// ── Request/response types ────────────────────────────────────────────

type registerRequest struct {
	Hostname        string           `json:"hostname"`
	TailscaleIP     string           `json:"tailscale_ip"`
	AgentListenPort int32            `json:"agent_listen_port"`
	CapacityMb      int32            `json:"capacity_mb"`
	CapacityCPU     float64          `json:"capacity_cpu"`
	AgentVersion    string           `json:"agent_version"`
	Metadata        *json.RawMessage `json:"metadata,omitempty"`
}

type registerResponse struct {
	NodeID                    string `json:"node_id"`
	AgentToken                string `json:"agent_token"`
	HeartbeatIntervalSeconds  int    `json:"heartbeat_interval_seconds"`
}

type heartbeatRequest struct {
	UsedMb            int32 `json:"used_mb"`
	RunningContainers int32 `json:"running_containers"`
}

// ── Handlers ──────────────────────────────────────────────────────────

// handleRegisterNode handles POST /v1/nodes/register.
// This is a public (unauthenticated) endpoint -- it is how the agent obtains
// its token. Re-registration with the same hostname+tailscale_ip is idempotent:
// returns the existing node_id with a fresh agent_token.
func (s *Server) handleRegisterNode(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		BadRequest(w, "invalid JSON body")
		return
	}

	// Validate required fields
	if req.Hostname == "" {
		BadRequest(w, "hostname is required")
		return
	}
	if req.TailscaleIP == "" {
		BadRequest(w, "tailscale_ip is required")
		return
	}
	if req.CapacityMb == 0 {
		BadRequest(w, "capacity_mb is required")
		return
	}
	if req.AgentVersion == "" {
		BadRequest(w, "agent_version is required")
		return
	}

	ip, err := netip.ParseAddr(req.TailscaleIP)
	if err != nil {
		BadRequest(w, "tailscale_ip must be a valid IP address")
		return
	}

	// Generate cryptographically random agent token (32 bytes -> 64-char hex)
	token, err := generateAgentToken()
	if err != nil {
		s.logger.Error("failed to generate agent token", "error", err)
		WriteProblem(w, http.StatusInternalServerError,
			"urn:forge:error:internal", "Internal Server Error", "failed to generate agent token")
		return
	}

	ctx := r.Context()

	// Check if node already exists (idempotent re-registration)
	existing, err := s.nodeStore.GetNodeByHostnameAndIP(ctx, req.Hostname, ip)
	if err == nil {
		// Node exists -- update token and return existing ID
		if err := s.nodeStore.UpdateNodeToken(ctx, token, req.AgentVersion, existing.ID); err != nil {
			s.logger.Error("failed to update node token", "error", err, "node_id", formatUUID(existing.ID))
			WriteProblem(w, http.StatusInternalServerError,
				"urn:forge:error:internal", "Internal Server Error", "failed to update node token")
			return
		}

		s.logger.Info("node re-registered",
			"node_id", formatUUID(existing.ID),
			"hostname", req.Hostname,
		)

		writeJSON(w, http.StatusCreated, registerResponse{
			NodeID:                   formatUUID(existing.ID),
			AgentToken:               token,
			HeartbeatIntervalSeconds: s.heartbeatIntervalSeconds,
		})
		return
	}

	if err != pgx.ErrNoRows {
		s.logger.Error("failed to check existing node", "error", err)
		WriteProblem(w, http.StatusInternalServerError,
			"urn:forge:error:internal", "Internal Server Error", "failed to check existing node")
		return
	}

	// Node does not exist -- create new
	newID := uuid.New()
	pgID := pgtype.UUID{Valid: true}
	copy(pgID.Bytes[:], newID[:])

	listenPort := req.AgentListenPort
	if listenPort == 0 {
		listenPort = 8090 // default per agent-protocol.md
	}

	metadata := []byte(`{}`)
	if req.Metadata != nil {
		metadata = []byte(*req.Metadata)
	}

	_, err = s.nodeStore.CreateNode(ctx, CreateNodeArgs{
		ID:              pgID,
		Hostname:        req.Hostname,
		TailscaleIP:     ip,
		AgentListenPort: listenPort,
		CapacityMb:      req.CapacityMb,
		CapacityCPU:     req.CapacityCPU,
		AgentVersion:    req.AgentVersion,
		Metadata:        metadata,
		AgentToken:      token,
	})
	if err != nil {
		s.logger.Error("failed to create node", "error", err)
		WriteProblem(w, http.StatusInternalServerError,
			"urn:forge:error:internal", "Internal Server Error", "failed to create node")
		return
	}

	s.logger.Info("node registered",
		"node_id", formatUUID(pgID),
		"hostname", req.Hostname,
		"tailscale_ip", req.TailscaleIP,
	)

	writeJSON(w, http.StatusCreated, registerResponse{
		NodeID:                   formatUUID(pgID),
		AgentToken:               token,
		HeartbeatIntervalSeconds: s.heartbeatIntervalSeconds,
	})
}

// handleHeartbeat handles POST /v1/nodes/{id}/heartbeat.
// Updates the node's resource usage and last_seen_at timestamp.
// Returns 404 if the node does not exist.
func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")

	nodeID, err := parseUUID(idStr)
	if err != nil {
		BadRequest(w, "invalid node ID: must be a valid UUID")
		return
	}

	var req heartbeatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		BadRequest(w, "invalid JSON body")
		return
	}

	ctx := r.Context()

	// Verify node exists before updating
	if _, err := s.nodeStore.GetNode(ctx, nodeID); err != nil {
		if err == pgx.ErrNoRows {
			NotFound(w, "node not found")
			return
		}
		s.logger.Error("failed to get node", "error", err, "node_id", idStr)
		WriteProblem(w, http.StatusInternalServerError,
			"urn:forge:error:internal", "Internal Server Error", "failed to get node")
		return
	}

	if err := s.nodeStore.UpdateNodeHeartbeat(ctx, nodeID, req.UsedMb, req.RunningContainers); err != nil {
		s.logger.Error("failed to update heartbeat", "error", err, "node_id", idStr)
		WriteProblem(w, http.StatusInternalServerError,
			"urn:forge:error:internal", "Internal Server Error", "failed to update heartbeat")
		return
	}

	w.WriteHeader(http.StatusOK)
}

// ── Helpers ───────────────────────────────────────────────────────────

// generateAgentToken creates a cryptographically random 64-character hex string
// (32 random bytes hex-encoded). Uses crypto/rand for security (T-03-09).
func generateAgentToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// parseUUID parses a string into a pgtype.UUID.
func parseUUID(s string) (pgtype.UUID, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return pgtype.UUID{}, err
	}
	return pgtype.UUID{Bytes: u, Valid: true}, nil
}

// formatUUID converts a pgtype.UUID to its string representation.
func formatUUID(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	id := uuid.UUID(u.Bytes)
	return id.String()
}

// writeJSON encodes v as JSON and writes it with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
