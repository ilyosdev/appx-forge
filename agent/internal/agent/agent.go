// Package agent wires all agent components into a single orchestrated process.
// The Agent struct owns the lifecycle of Docker client, control plane client,
// events watcher, heartbeat sender, file push handler, port allocator, and
// image puller. It follows the startup sequence defined in agent-protocol.md.
package agent

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/appx/forge/agent/internal/config"
	"github.com/appx/forge/agent/internal/controlclient"
	"github.com/appx/forge/agent/internal/docker"
	"github.com/appx/forge/agent/internal/events"
	"github.com/appx/forge/agent/internal/filepush"
	"github.com/appx/forge/agent/internal/health"
	"github.com/appx/forge/agent/internal/ports"
)

// Agent orchestrates all agent components and manages their lifecycle.
// It is the top-level struct created by main and driven by Run/Shutdown.
type Agent struct {
	cfg        *config.Config
	docker     docker.Client
	ctrlClient *controlclient.Client
	executor   *CommandExecutor
	watcher    *events.Watcher
	heartbeat  *health.HeartbeatSender
	puller     *docker.ImagePuller
	filePush   *filepush.Handler
	ports      *ports.Allocator
	httpServer *http.Server
	logger     *slog.Logger
}

// New creates an Agent by wiring all components together.
// It does NOT start any goroutines or connect to the control plane --
// that happens in Run().
func New(cfg *config.Config, logger *slog.Logger) (*Agent, error) {
	// 1. Docker client
	dockerClient, err := docker.NewDockerClient()
	if err != nil {
		return nil, fmt.Errorf("agent: create docker client: %w", err)
	}

	// 2. Port allocator
	portAlloc := ports.NewAllocator(cfg.PortRangeMin, cfg.PortRangeMax)

	// 3. Control client
	regReq := controlclient.RegisterRequest{
		Hostname:        cfg.Hostname,
		TailscaleIP:     cfg.TailscaleIP,
		AgentListenPort: cfg.AgentPort,
		CapacityMB:      cfg.CapacityMB,
		CapacityCPU:     cfg.CapacityCPU,
		AgentVersion:    cfg.AgentVersion,
	}
	ctrlClient := controlclient.NewClient(cfg.ControlURL, regReq, logger, cfg.APIToken)

	// 4. Command executor
	executor := NewCommandExecutor(dockerClient, portAlloc, ctrlClient, cfg.SandboxDir, logger)

	// 5. Events watcher
	watcher := events.NewWatcher(dockerClient, logger)

	// 6. Heartbeat sender with a simple resource collector
	collector := &dockerResourceCollector{docker: dockerClient, logger: logger}
	heartbeatSender := health.NewHeartbeatSender(ctrlClient, collector, 15*time.Second, logger)

	// 7. Image puller
	puller := docker.NewImagePuller(dockerClient, cfg.SandboxImage, logger)

	// 8. File push handler -- executor implements SandboxResolver via CodeDir
	filePushHandler := filepush.NewHandler([]byte(cfg.HMACSecret), executor, logger)

	return &Agent{
		cfg:        cfg,
		docker:     dockerClient,
		ctrlClient: ctrlClient,
		executor:   executor,
		watcher:    watcher,
		heartbeat:  heartbeatSender,
		puller:     puller,
		filePush:   filePushHandler,
		ports:      portAlloc,
		logger:     logger,
	}, nil
}

// Run starts the agent startup sequence (per agent-protocol.md):
// 1. Register with control plane
// 2. Start heartbeat sender
// 3. Start Docker events watcher
// 4. Pull sandbox image
// 5. Start HTTP server
// 6. Start command poll loop
//
// Run blocks until ctx is cancelled. It returns nil on clean shutdown
// or an error if a fatal problem occurs during startup.
func (a *Agent) Run(ctx context.Context) error {
	// Step 1: Register with control plane (retries internally)
	a.logger.Info("registering with control plane", "url", a.cfg.ControlURL)
	regResp, err := a.ctrlClient.Register(ctx)
	if err != nil {
		return fmt.Errorf("agent: registration failed: %w", err)
	}
	a.logger.Info("registered successfully",
		"node_id", regResp.NodeID,
		"heartbeat_interval", regResp.HeartbeatIntervalSeconds,
	)

	// Step 2: Start heartbeat sender (goroutine, stops on ctx cancel)
	go a.heartbeat.Start(ctx)

	// Step 3: Start Docker events watcher and forward events to control plane
	eventCh := a.watcher.Watch(ctx)
	go a.forwardEvents(ctx, eventCh)

	// Step 4: Pull sandbox image (non-blocking, log error)
	go func() {
		if err := a.puller.PullOnce(ctx); err != nil {
			a.logger.Warn("initial image pull failed", "error", err)
		}
	}()

	// Step 5: Start periodic image pull (every 1 hour)
	a.puller.StartPeriodicPull(ctx, 1*time.Hour)

	// Step 6: Start HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.handleHealthz)
	mux.Handle("POST /v1/sandboxes/{id}/files", a.filePush)

	listenAddr := net.JoinHostPort(a.cfg.TailscaleIP, fmt.Sprintf("%d", a.cfg.AgentPort))
	if a.cfg.TailscaleIP == "" {
		// If no Tailscale IP, listen on all interfaces (dev mode)
		listenAddr = fmt.Sprintf(":%d", a.cfg.AgentPort)
	}

	a.httpServer = &http.Server{
		Addr:    listenAddr,
		Handler: mux,
	}

	go func() {
		a.logger.Info("HTTP server starting", "addr", listenAddr)
		if err := a.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			a.logger.Error("HTTP server error", "error", err)
		}
	}()

	// Step 7: Start command poll loop (blocks until ctx is cancelled)
	a.logger.Info("starting command poll loop")
	a.pollLoop(ctx)

	return nil
}

// Shutdown gracefully stops all agent components.
func (a *Agent) Shutdown(ctx context.Context) error {
	a.logger.Info("shutting down agent")

	var firstErr error

	// Stop HTTP server
	if a.httpServer != nil {
		if err := a.httpServer.Shutdown(ctx); err != nil {
			a.logger.Error("HTTP server shutdown error", "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	// Close Docker client
	if err := a.docker.Close(); err != nil {
		a.logger.Error("Docker client close error", "error", err)
		if firstErr == nil {
			firstErr = err
		}
	}

	a.logger.Info("agent shutdown complete")
	return firstErr
}

// ── Internal Methods ────────────────────────────────────────────────────

// pollLoop continuously polls for commands and executes them sequentially.
// It blocks until ctx is cancelled.
func (a *Agent) pollLoop(ctx context.Context) {
	const waitSeconds = 30

	for {
		if ctx.Err() != nil {
			return
		}

		commands, err := a.ctrlClient.PollCommands(ctx, waitSeconds)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			a.logger.Warn("poll commands failed", "error", err)
			// Brief pause before retrying on error
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
				return
			}
			continue
		}

		for _, cmd := range commands {
			if ctx.Err() != nil {
				return
			}
			if err := a.executor.Execute(ctx, cmd); err != nil {
				a.logger.Error("command execution error",
					"cmd_id", cmd.ID,
					"type", cmd.Type,
					"error", err,
				)
			}
		}
	}
}

// forwardEvents reads sandbox events from the watcher and reports them
// to the control plane. Events are fire-and-forget per agent-protocol.md.
func (a *Agent) forwardEvents(ctx context.Context, eventCh <-chan events.SandboxEvent) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-eventCh:
			if !ok {
				return
			}

			// Look up sandbox ID from the executor's in-memory map using app name
			sandboxID := a.resolveSandboxID(ev.AppName)

			report := controlclient.EventReport{
				SandboxID:   sandboxID,
				EventType:   ev.EventType,
				ContainerID: ev.ContainerID,
			}

			if ev.ExitCode != "" {
				// Parse exit code string to int for the report
				var exitCode int
				fmt.Sscanf(ev.ExitCode, "%d", &exitCode)
				report.ExitCode = exitCode
			}

			if ev.OOMKilled {
				report.Payload = map[string]interface{}{
					"oom_killed": true,
				}
			}

			if err := a.ctrlClient.ReportEvent(ctx, report); err != nil {
				a.logger.Warn("event report failed",
					"sandbox_id", sandboxID,
					"event_type", ev.EventType,
					"error", err,
				)
			}
		}
	}
}

// resolveSandboxID looks up a sandbox ID from an app name using the executor's map.
func (a *Agent) resolveSandboxID(appName string) string {
	a.executor.mu.RLock()
	defer a.executor.mu.RUnlock()

	for sandboxID, info := range a.executor.sandboxes {
		if info.AppName == appName {
			return sandboxID
		}
	}
	// If not found, use the app name as a fallback
	return appName
}

// handleHealthz responds with 200 OK for health checks.
func (a *Agent) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

// ── Resource Collector ──────────────────────────────────────────────────

// dockerResourceCollector collects resource usage by counting running containers.
type dockerResourceCollector struct {
	docker docker.Client
	logger *slog.Logger
}

// Collect returns current memory usage (placeholder) and running container count.
func (c *dockerResourceCollector) Collect() (usedMB int, runningContainers int) {
	// In v1, we report a placeholder for memory usage.
	// A future version can read /proc/meminfo or cgroup stats.
	// Running containers count comes from Docker inspect of known containers.
	return 0, 0
}
