package agent

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/appx/forge/agent/internal/config"
	"github.com/appx/forge/agent/internal/controlclient"
	"github.com/appx/forge/agent/internal/docker"
	"github.com/appx/forge/agent/internal/events"
	"github.com/appx/forge/agent/internal/filepush"
	"github.com/appx/forge/agent/internal/health"
	"github.com/appx/forge/agent/internal/ports"
)

// TestNewAgent verifies that the Agent constructor wires all components
// without requiring a running Docker daemon. We construct it manually
// using mocks rather than calling New() (which needs Docker).
func TestNewAgentManual(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	cfg := &config.Config{
		ControlURL:   "http://localhost:9000",
		Hostname:     "test-node",
		TailscaleIP:  "100.64.0.1",
		AgentPort:    8090,
		PortRangeMin: 40000,
		PortRangeMax: 40010,
		HMACSecret:   "test-secret",
		SandboxDir:   "/tmp/forge-test",
		AgentVersion: "0.1.0",
		SandboxImage: "appx/sandbox:v1",
	}

	dc := newMockDockerClient()
	portAlloc := ports.NewAllocator(cfg.PortRangeMin, cfg.PortRangeMax)
	regReq := controlclient.RegisterRequest{
		Hostname:        cfg.Hostname,
		TailscaleIP:     cfg.TailscaleIP,
		AgentListenPort: cfg.AgentPort,
		AgentVersion:    cfg.AgentVersion,
	}
	ctrlClient := controlclient.NewClient(cfg.ControlURL, regReq, logger)
	executor := NewCommandExecutor(dc, portAlloc, ctrlClient, cfg.SandboxDir, logger)
	watcher := events.NewWatcher(dc, logger)
	collector := &mockCollector{}
	heartbeatSender := health.NewHeartbeatSender(ctrlClient, collector, 15*time.Second, logger)
	puller := docker.NewImagePuller(dc, cfg.SandboxImage, logger)
	filePushHandler := filepush.NewHandler([]byte(cfg.HMACSecret), executor, logger)

	a := &Agent{
		cfg:        cfg,
		docker:     dc,
		ctrlClient: ctrlClient,
		executor:   executor,
		watcher:    watcher,
		heartbeat:  heartbeatSender,
		puller:     puller,
		filePush:   filePushHandler,
		ports:      portAlloc,
		logger:     logger,
	}

	if a.cfg.Hostname != "test-node" {
		t.Errorf("expected hostname test-node, got %s", a.cfg.Hostname)
	}
	if a.executor == nil {
		t.Error("executor is nil")
	}
	if a.watcher == nil {
		t.Error("watcher is nil")
	}
	if a.heartbeat == nil {
		t.Error("heartbeat is nil")
	}
	if a.filePush == nil {
		t.Error("file push handler is nil")
	}
}

// TestAgentShutdownClean verifies that Shutdown returns nil with a mocked client.
func TestAgentShutdownClean(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dc := newMockDockerClient()

	a := &Agent{
		cfg: &config.Config{
			Hostname: "test-node",
		},
		docker: dc,
		logger: logger,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := a.Shutdown(ctx)
	if err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}
}

// TestResolveSandboxID verifies sandbox ID lookup by app name.
func TestResolveSandboxID(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dc := newMockDockerClient()
	portAlloc := ports.NewAllocator(40000, 40010)
	ack := &mockAckReporter{}
	executor := NewCommandExecutor(dc, portAlloc, ack, "/tmp/test", logger)

	// Pre-populate
	executor.mu.Lock()
	executor.sandboxes["sandbox-abc"] = &sandboxInfo{
		ContainerID: "container-1",
		HostPort:    40001,
		AppName:     "my-app",
	}
	executor.mu.Unlock()

	a := &Agent{
		executor: executor,
		logger:   logger,
	}

	// Test found
	id := a.resolveSandboxID("my-app")
	if id != "sandbox-abc" {
		t.Errorf("expected sandbox-abc, got %s", id)
	}

	// Test not found (falls back to app name)
	id = a.resolveSandboxID("unknown-app")
	if id != "unknown-app" {
		t.Errorf("expected unknown-app fallback, got %s", id)
	}
}

// mockCollector is a no-op resource collector for tests.
type mockCollector struct{}

func (m *mockCollector) Collect() (usedMB int, runningContainers int) {
	return 0, 0
}
