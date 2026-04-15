// Package health provides heartbeat and health monitoring for the Forge agent.
package health

import (
	"context"
	"log/slog"
	"time"

	"github.com/appx/forge/agent/internal/controlclient"
)

// HeartbeatClient defines the interface for sending heartbeats to the control plane.
type HeartbeatClient interface {
	Heartbeat(ctx context.Context, req controlclient.HeartbeatRequest) error
}

// ResourceCollector collects current resource usage from the node.
type ResourceCollector interface {
	Collect() (usedMB int, runningContainers int)
}

// HeartbeatSender periodically sends heartbeats to the control plane.
// It runs as a goroutine and stops when the context is cancelled.
type HeartbeatSender struct {
	client    HeartbeatClient
	collector ResourceCollector
	interval  time.Duration
	logger    *slog.Logger
}

// NewHeartbeatSender creates a new HeartbeatSender.
func NewHeartbeatSender(
	client HeartbeatClient,
	collector ResourceCollector,
	interval time.Duration,
	logger *slog.Logger,
) *HeartbeatSender {
	return &HeartbeatSender{
		client:    client,
		collector: collector,
		interval:  interval,
		logger:    logger,
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

// sendHeartbeat collects resources and sends a single heartbeat.
func (h *HeartbeatSender) sendHeartbeat(ctx context.Context) {
	usedMB, runningContainers := h.collector.Collect()

	req := controlclient.HeartbeatRequest{
		UsedMB:            usedMB,
		RunningContainers: runningContainers,
	}

	if err := h.client.Heartbeat(ctx, req); err != nil {
		h.logger.Warn("heartbeat failed", "error", err)
		return
	}

	h.logger.Debug("heartbeat sent", "used_mb", usedMB, "running_containers", runningContainers)
}
