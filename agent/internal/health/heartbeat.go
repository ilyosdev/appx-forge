// Package health provides heartbeat and health monitoring for the Forge agent.
package health

import (
	"context"
	"log/slog"
	"time"

	"github.com/appx/forge/agent/internal/controlclient"
	"github.com/appx/forge/agent/internal/docker"
)

// HeartbeatClient defines the interface for sending heartbeats to the control plane.
type HeartbeatClient interface {
	Heartbeat(ctx context.Context, req controlclient.HeartbeatRequest) error
}

// ResourceCollector collects current resource usage from the node.
type ResourceCollector interface {
	Collect() (usedMB int, runningContainers int)
}

// SnapshotProvider is the interface HeartbeatSender uses to fetch the full
// container list per tick. Implemented by docker.Snapshotter (Phase 30 T3).
//
// Returns docker.ContainerSnapshot (the docker package's protocol-free type);
// HeartbeatSender converts each entry to controlclient.ContainerInfo before
// putting it on the wire.
type SnapshotProvider interface {
	Snapshot(ctx context.Context) ([]docker.ContainerSnapshot, error)
}

// HeartbeatSender periodically sends heartbeats to the control plane.
// It runs as a goroutine and stops when the context is cancelled.
//
// Phase 30 — also fetches a full container snapshot per tick and includes it
// in the heartbeat payload so the control plane can reconcile its DB against
// agent truth continuously.
type HeartbeatSender struct {
	client      HeartbeatClient
	collector   ResourceCollector
	snapshotter SnapshotProvider
	interval    time.Duration
	logger      *slog.Logger
}

// NewHeartbeatSender creates a new HeartbeatSender.
func NewHeartbeatSender(
	client HeartbeatClient,
	collector ResourceCollector,
	snapshotter SnapshotProvider,
	interval time.Duration,
	logger *slog.Logger,
) *HeartbeatSender {
	return &HeartbeatSender{
		client:      client,
		collector:   collector,
		snapshotter: snapshotter,
		interval:    interval,
		logger:      logger,
	}
}

// Start runs the heartbeat loop. It blocks until ctx is cancelled.
// On each tick, it collects resource usage and sends a heartbeat.
// Errors are logged but do not stop the loop (per agent-protocol.md).
func (h *HeartbeatSender) Start(ctx context.Context) {
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()

	h.logger.Info("heartbeat sender started", "interval", h.interval)

	for {
		select {
		case <-ctx.Done():
			h.logger.Info("heartbeat sender stopped")
			return
		case <-ticker.C:
			h.sendHeartbeat(ctx)
		}
	}
}

// sendHeartbeat collects resources, snapshots containers, and sends a single
// heartbeat. On snapshot failure, the heartbeat is SKIPPED entirely rather
// than sent with an empty container list. Sending an empty list would, over
// a multi-tick failure (>60s), trip T7 reconciler's grace window and cause
// it to mass-mark every Forge sandbox as 'agent_lost' — trading correctness
// for liveness. Skipping ticks lets the control plane's existing
// missed-heartbeat alarm surface the real signal: this node is in trouble.
func (h *HeartbeatSender) sendHeartbeat(ctx context.Context) {
	snapshots, err := h.snapshotter.Snapshot(ctx)
	if err != nil {
		h.logger.Warn("heartbeat snapshot failed; SKIPPING this tick to avoid sending an empty list (would trip T7 reconciler's mass-destroy grace window if Docker stays down)", "error", err)
		return
	}

	usedMB, runningContainers := h.collector.Collect()

	containers := make([]controlclient.ContainerInfo, 0, len(snapshots))
	for _, s := range snapshots {
		containers = append(containers, controlclient.ContainerInfo{
			AppName:     s.AppName,
			State:       s.State,
			HostPort:    s.HostPort,
			ContainerID: s.ContainerID,
		})
	}

	req := controlclient.HeartbeatRequest{
		UsedMB:            usedMB,
		RunningContainers: runningContainers,
		Containers:        containers,
	}

	if err := h.client.Heartbeat(ctx, req); err != nil {
		h.logger.Warn("heartbeat failed", "error", err)
		return
	}

	h.logger.Debug("heartbeat sent",
		"used_mb", usedMB,
		"running_containers", runningContainers,
		"containers_in_payload", len(containers),
	)
}
