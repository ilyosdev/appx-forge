// Package controlclient provides an HTTP client for communication with the
// Forge control plane. It handles registration, heartbeats, command polling,
// command acknowledgment, and event reporting.
package controlclient

import (
	"encoding/json"
	"time"
)

// RegisterRequest is sent to POST /v1/nodes/register on agent startup.
type RegisterRequest struct {
	Hostname        string  `json:"hostname"`
	TailscaleIP     string  `json:"tailscale_ip"`
	AgentListenPort int     `json:"agent_listen_port"`
	CapacityMB      int     `json:"capacity_mb"`
	CapacityCPU     float64 `json:"capacity_cpu"`
	AgentVersion    string  `json:"agent_version"`
}

// RegisterResponse is returned from POST /v1/nodes/register.
type RegisterResponse struct {
	NodeID                   string `json:"node_id"`
	AgentToken               string `json:"agent_token"`
	HeartbeatIntervalSeconds int    `json:"heartbeat_interval_seconds"`
}

// HeartbeatRequest is sent to POST /v1/nodes/{id}/heartbeat.
//
// Phase 30 — gains Containers (full list, not just count) so the control
// plane can reconcile its DB against agent truth on every tick. The legacy
// RunningContainers count is kept for transition (older control planes that
// don't read Containers still get the count they expect).
type HeartbeatRequest struct {
	UsedMB            int             `json:"used_mb"`
	RunningContainers int             `json:"running_containers"`
	Containers        []ContainerInfo `json:"containers"`
}

// ContainerInfo is the protocol-side representation of a single container
// reported by the agent in its heartbeat.
//
// This mirrors agent/internal/docker.ContainerSnapshot field-for-field. The
// duplication is intentional — keeping the controlclient package free of a
// docker dependency means the protocol types stay light and the dependency
// graph stays tidy. Conversion happens in HeartbeatSender.
type ContainerInfo struct {
	AppName     string `json:"app_name"`
	State       string `json:"state"`
	HostPort    int    `json:"host_port"`
	ContainerID string `json:"container_id"`
}

// Command represents a command received from the control plane via long-poll.
type Command struct {
	ID             string          `json:"id"`
	Type           string          `json:"type"`
	SandboxID      string          `json:"sandbox_id"`
	Payload        json.RawMessage `json:"payload"`
	IssuedAt       time.Time       `json:"issued_at"`
	TimeoutSeconds int             `json:"timeout_seconds"`
}

// CommandsResponse wraps the list of commands from GET /v1/agents/{id}/commands.
type CommandsResponse struct {
	Commands []Command `json:"commands"`
}

// AckRequest is sent to POST /v1/agents/{id}/commands/{cmd_id}/ack.
type AckRequest struct {
	Status string      `json:"status"`
	Error  string      `json:"error,omitempty"`
	Result interface{} `json:"result,omitempty"`
}

// EventReport is sent to POST /v1/agents/{id}/events for container lifecycle events.
type EventReport struct {
	SandboxID   string      `json:"sandbox_id"`
	EventType   string      `json:"event_type"`
	ContainerID string      `json:"container_id,omitempty"`
	ExitCode    int         `json:"exit_code,omitempty"`
	Payload     interface{} `json:"payload,omitempty"`
}
