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
// heartbeat.
//
// Phase 33-Audit-6: when the snapshot times out (Docker slow under load),
// fall back to a LIVENESS-ONLY heartbeat (Containers=nil) instead of
// SKIPPING the tick entirely. The control-plane heartbeat handler
// (`api/nodes.go:handleHeartbeat`) updates last_seen_at unconditionally
// and only triggers T7 reconciler when `req.Containers != nil`, so a
// liveness heartbeat:
//
//   - keeps the node 'healthy' on the control side (no missed-beat flap)
//   - does NOT trigger reconcile against an empty list (no mass-destroy
//     of Forge sandboxes — the T7 protection the prior SKIPPING was
//     trying to preserve)
//
// Previously, SKIPPING ticks under Docker pressure tripped the
// missed-heartbeat alarm (4-streak / 6-missed → status='unhealthy'),
// which removed the node from `ListHealthyNodes` and caused user
// `createApp` calls to 503 with "no nodes available with sufficient
// capacity" — even though the agent process was alive and Docker was
// merely momentarily slow. Beta incident 2026-05-08, project
// cb39efa6-ddd9-489c-b806-14ea3c7021bf.
//
// Phase 32 Wave 2 Bug 5 — each tick also gets a per-call timeout
// (heartbeatTickTimeout). On a host where dockerd has stalled,
// ListContainers can block for tens of seconds; without this bound
// the heartbeat loop itself stalls.
func (h *HeartbeatSender) sendHeartbeat(ctx context.Context) {
	tickCtx, cancel := context.WithTimeout(ctx, heartbeatTickTimeout)
	defer cancel()

	snapshots, err := h.snapshotter.Snapshot(tickCtx)
	if err != nil {
		// Distinguish "agent shutting down" from "Docker stalled" —
		// parent-context cancellation is a normal stop, drop quietly.
		if ctx.Err() != nil {
			return
		}

		// Docker timeout or transient failure → liveness-only heartbeat.
		// This is the failure-mode path; we deliberately leave Containers
		// empty so the reconciler does NOT fire on an unobserved snapshot
		// (T7 mass-destroy protection still in place).
		h.skippedTicks.Add(1)
		if errors.Is(err, context.DeadlineExceeded) {
			h.logger.Warn(
				"heartbeat snapshot deadline exceeded; sending LIVENESS-ONLY heartbeat (agent alive, Docker slow)",
				"timeout", heartbeatTickTimeout,
				"skipped_snapshots_total", h.skippedTicks.Load(),
			)
		} else {
			h.logger.Warn(
				"heartbeat snapshot failed; sending LIVENESS-ONLY heartbeat (Containers omitted to avoid tripping T7 mass-destroy)",
				"error", err,
				"skipped_snapshots_total", h.skippedTicks.Load(),
			)
		}
		usedMB, runningContainers := h.collector.Collect()
		req := controlclient.HeartbeatRequest{
			UsedMB:            usedMB,
			RunningContainers: runningContainers,
			// Containers intentionally nil — keeps reconciler silent.
		}
		if hbErr := h.client.Heartbeat(ctx, req); hbErr != nil {
			h.logger.Warn("liveness-only heartbeat send failed", "error", hbErr)
		}
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
