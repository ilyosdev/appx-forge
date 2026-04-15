// forge-agent is the per-node agent binary for the Forge container orchestrator.
// It registers with the control plane, heartbeats resource usage, watches Docker
// events, serves file push HTTP endpoints, and polls for commands to execute.
//
// Configuration is via environment variables (see config.Config).
// Run as a systemd service via deploy/forge-agent.service.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/appx/forge/agent/internal/agent"
	"github.com/appx/forge/agent/internal/config"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	logger.Info("forge-agent starting")

	cfg, err := config.Load()
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	a, err := agent.New(cfg, logger)
	if err != nil {
		logger.Error("failed to create agent", "error", err)
		os.Exit(1)
	}

	// Listen for SIGTERM (systemd) and SIGINT (Ctrl+C)
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// Run blocks until ctx is cancelled
	if err := a.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("agent exited with error", "error", err)
		os.Exit(1)
	}

	// Graceful shutdown with 10s timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := a.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", "error", err)
	}

	logger.Info("agent shutdown complete")
}
