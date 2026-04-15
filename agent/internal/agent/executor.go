package agent

import (
	"context"
	"log/slog"
	"sync"

	"github.com/appx/forge/agent/internal/controlclient"
	"github.com/appx/forge/agent/internal/docker"
	"github.com/appx/forge/agent/internal/ports"
)

// ackReporter is the interface for acknowledging commands with the control plane.
type ackReporter interface {
	AckCommand(ctx context.Context, cmdID string, ack controlclient.AckRequest) error
}

// sandboxInfo tracks running sandbox state in-memory.
type sandboxInfo struct {
	ContainerID string
	HostPort    int
	AppName     string
}

// CommandExecutor dispatches commands received from the control plane
// to the appropriate Docker operations and tracks sandbox state.
type CommandExecutor struct {
	docker     docker.Client
	ports      *ports.Allocator
	ctrlClient ackReporter
	sandboxDir string
	logger     *slog.Logger

	// In-memory map: sandboxID -> sandbox state
	sandboxes map[string]*sandboxInfo
	mu        sync.RWMutex
}

// NewCommandExecutor creates a CommandExecutor with the given dependencies.
func NewCommandExecutor(
	dockerClient docker.Client,
	portAlloc *ports.Allocator,
	ctrlClient ackReporter,
	sandboxDir string,
	logger *slog.Logger,
) *CommandExecutor {
	return &CommandExecutor{
		docker:     dockerClient,
		ports:      portAlloc,
		ctrlClient: ctrlClient,
		sandboxDir: sandboxDir,
		logger:     logger,
		sandboxes:  make(map[string]*sandboxInfo),
	}
}

// Execute dispatches a command to the appropriate handler.
// Stub: not yet implemented -- all commands will fail.
func (e *CommandExecutor) Execute(ctx context.Context, cmd controlclient.Command) error {
	// TODO: implement
	return nil
}
