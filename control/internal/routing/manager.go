package routing

import (
	"context"
	"errors"
	"log/slog"
)

// Enqueuer abstracts the Batcher.Enqueue method for testability.
type Enqueuer interface {
	Enqueue(change RouteChange)
}

// RouteManager translates sandbox lifecycle events into batched route changes.
// It implements lifecycle.RouteNotifier.
type RouteManager struct {
	batcher Enqueuer
	logger  *slog.Logger
}

// NewRouteManager creates a RouteManager that enqueues route changes via the given Enqueuer.
func NewRouteManager(batcher Enqueuer, logger *slog.Logger) *RouteManager {
	if logger == nil {
		logger = slog.Default()
	}
	return &RouteManager{batcher: batcher, logger: logger}
}

// OnSandboxRunning enqueues an "add" route change so Caddy starts proxying traffic
// to the sandbox's upstream address.
func (rm *RouteManager) OnSandboxRunning(_ context.Context, appName, sandboxID, upstream string) error {
	if upstream == "" {
		return errors.New("route manager: upstream must not be empty")
	}

	rm.logger.Info("route add enqueued",
		"app_name", appName,
		"sandbox_id", sandboxID,
		"upstream", upstream,
	)

	rm.batcher.Enqueue(RouteChange{
		Action: "add",
		Route: Route{
			AppName:   appName,
			SandboxID: sandboxID,
			Upstream:  upstream,
		},
	})
	return nil
}

// OnSandboxStopped enqueues a "remove" route change so Caddy stops proxying
// traffic to the sandbox.
func (rm *RouteManager) OnSandboxStopped(_ context.Context, appName string) error {
	rm.logger.Info("route remove enqueued", "app_name", appName)

	rm.batcher.Enqueue(RouteChange{
		Action:  "remove",
		AppName: appName,
	})
	return nil
}
