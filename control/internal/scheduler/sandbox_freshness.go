package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// ErrSandboxNotFound is returned by SandboxFreshnessService.GetSandbox when
// the agent confirms the container does not exist (read-through miss).
// Distinguished from a generic store "not found" so the API handler can
// translate to HTTP 404.
var ErrSandboxNotFound = errors.New("sandbox not found")

// AgentClient is the interface SandboxFreshnessService needs to ask the
// agent if a container exists. Implemented in production by an HTTP client
// that calls GET /v1/containers/{name} on the agent (T5).
type AgentClient interface {
	ContainerExists(ctx context.Context, name string) (exists bool, state string, err error)
}

// FreshnessStore is the per-name read + freshness write surface the
// SandboxFreshnessService needs from the store. Kept distinct from
// SandboxStore (T7) because the two services consume different slices
// of the store: one reads the per-node working set, the other does
// per-name lookups + a destroy mark.
type FreshnessStore interface {
	GetSandboxByName(ctx context.Context, name string) (*SandboxRow, time.Time, error)
	MarkSandboxVerified(ctx context.Context, appName, state string) error
	MarkSandboxDestroyed(ctx context.Context, appName, reason string) error
}

// SandboxFreshnessService wraps GetSandbox with a verified_at freshness
// check. On stale rows (or force_refresh), it synchronously asks the agent
// to confirm container existence before returning. On miss the row is
// marked destroyed and ErrSandboxNotFound is returned. Agent unreachable
// errors do NOT propagate — the cached row is returned with a warning so
// transient agent flaps don't manifest as user-visible 500s.
type SandboxFreshnessService struct {
	store     FreshnessStore
	agent     AgentClient
	freshness time.Duration
	logger    *slog.Logger
}

// NewSandboxFreshnessService constructs a SandboxFreshnessService with the
// configured freshness threshold (typical: 10s). Pass nil logger for slog.Default().
func NewSandboxFreshnessService(store FreshnessStore, agent AgentClient, freshness time.Duration, logger *slog.Logger) *SandboxFreshnessService {
	if logger == nil {
		logger = slog.Default()
	}
	return &SandboxFreshnessService{
		store:     store,
		agent:     agent,
		freshness: freshness,
		logger:    logger,
	}
}

// GetSandbox returns the sandbox row + its current verified_at.
//
// Behavior:
//   - Fresh row (verified_at within window) AND !forceRefresh → cached return.
//   - Stale row OR forceRefresh → synchronous agent.ContainerExists call.
//   - Agent confirms exists → MarkSandboxVerified, return row + now.
//   - Agent confirms missing → MarkSandboxDestroyed(reason="agent_lost_at_freshness_check"),
//     return ErrSandboxNotFound.
//   - Agent unreachable → log warning, return cached row + cached verified_at
//     (caller can choose to retry / surface staleness).
func (s *SandboxFreshnessService) GetSandbox(ctx context.Context, name string, forceRefresh bool) (*SandboxRow, time.Time, error) {
	row, verifiedAt, err := s.store.GetSandboxByName(ctx, name)
	if err != nil {
		return nil, time.Time{}, err
	}

	stale := time.Since(verifiedAt) > s.freshness
	if !stale && !forceRefresh {
		return row, verifiedAt, nil
	}

	exists, state, err := s.agent.ContainerExists(ctx, name)
	if err != nil {
		s.logger.Warn("agent ContainerExists failed; returning cached row",
			"name", name, "err", err)
		return row, verifiedAt, nil
	}
	if !exists {
		if mdErr := s.store.MarkSandboxDestroyed(ctx, name, "agent_lost_at_freshness_check"); mdErr != nil {
			s.logger.Warn("MarkSandboxDestroyed failed", "name", name, "err", mdErr)
		}
		return nil, time.Time{}, ErrSandboxNotFound
	}
	if mvErr := s.store.MarkSandboxVerified(ctx, name, state); mvErr != nil {
		s.logger.Warn("MarkSandboxVerified failed", "name", name, "err", mvErr)
	}
	return row, time.Now(), nil
}
