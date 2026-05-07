// Package health provides heartbeat and health monitoring for the Forge agent.
package health

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/appx/forge/agent/internal/controlclient"
	"github.com/appx/forge/agent/internal/docker"
)

// heartbeatTickTimeout bounds a single heartbeat tick — specifically the
// snapshotter.Snapshot call, which under the hood hits Docker's
// containerList endpoint. On an overloaded host (swap-thrashing dockerd,
// high CPU pressure) ListContainers can block for tens of seconds; without
// a per-tick deadline that block propagates into the heartbeat loop and
// the control plane sees us as gone.
//
// Phase 32 Wave 2 Bug 5 — pair this with the control-side debounce window
// (Bug 4): a few skipped ticks no longer cascade into reschedule.
const heartbeatTickTimeout = 5 * time.Second

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

	// skippedTicks counts heartbeat ticks dropped because snapshot timed
	// out (Phase 32 Wave 2 Bug 5). Exposed via SkippedTicks() so operators
	// and the metrics endpoint can surface "agent alive but Docker slow"
	// — a distinct signal from "agent crashed" or "node down".
	skippedTicks atomic.Uint64
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

// SkippedTicks returns the cumulative number of heartbeat ticks dropped
// because the per-tick snapshot deadline expired. A non-zero, growing
// value means the agent is alive but Docker is too slow to enumerate
// containers within the tick budget — typically swap pressure or
// dockerd CPU starvation on the host. (Phase 32 Wave 2 Bug 5.)
func (h *HeartbeatSender) SkippedTicks() uint64 {
	return h.skippedTicks.Load()
}

// sendHeartbeat collects resources, snapshots containers, and sends a single
// heartbeat. On snapshot failure, the heartbeat is SKIPPED entirely rather
// than sent with an empty container list. Sending an empty list would, over
// a multi-tick failure (>60s), trip T7 reconciler's grace window and cause
// it to mass-mark every Forge sandbox as 'agent_lost' — trading correctness
// for liveness. Skipping ticks lets the control plane's existing
// missed-heartbeat alarm surface the real signal: this node is in trouble.
//
// Phase 32 Wave 2 Bug 5 — each tick also gets a per-call timeout
// (heartbeatTickTimeout). On a host where dockerd has stalled, ListContainers
// can block for tens of seconds; without this bound the heartbeat loop
// itself stalls and the control plane sees the node as gone. On timeout
// we still skip (relying on the control-side debounce window from Bug 4)
// but increment skippedTicks so operators see "agent alive, Docker slow"
// as a distinct signal from "agent crashed".
func (h *HeartbeatSender) sendHeartbeat(ctx context.Context) {
	tickCtx, cancel := context.WithTimeout(ctx, heartbeatTickTimeout)
	defer cancel()

	snapshots, err := h.snapshotter.Snapshot(tickCtx)
	if err != nil {
		// Distinguish "Docker stalled past our tick budget" from "Docker
		// returned a real error" — only the former implies the daemon
		// is healthy enough to eventually recover. We check both the
		// per-tick deadline AND the parent context: if the parent was
		// cancelled (agent shutting down), drop quietly without
		// counting a skip — that's a normal stop, not a Docker stall.
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, context.DeadlineExceeded) {
			h.skippedTicks.Add(1)
			h.logger.Warn(
				"heartbeat snapshot deadline exceeded; SKIPPING this tick (agent alive, Docker slow)",
				"timeout", heartbeatTickTimeout,
				"skipped_total", h.skippedTicks.Load(),
			)
			return
		}
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
