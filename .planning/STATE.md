---
gsd_state_version: 1.0
milestone: v1.0
milestone_name: milestone
status: executing
stopped_at: Completed 03-02-PLAN.md (bin-packing scheduler)
last_updated: "2026-04-15T22:13:09.386Z"
last_activity: 2026-04-15
progress:
  total_phases: 7
  completed_phases: 2
  total_plans: 17
  completed_plans: 13
  percent: 76
---

# Project State

## Project Reference

See: .planning/PROJECT.md (updated 2026-04-15)

**Core value:** Sub-second sandbox claim, ~5s cold start, zero spurious restarts -- reliable container orchestration simple enough to explain on one whiteboard
**Current focus:** Phase 03 — Control Plane API

## Current Position

Phase: 03 (Control Plane API) — EXECUTING
Plan: 3 of 6
Status: Ready to execute
Last activity: 2026-04-15

Progress: [░░░░░░░░░░] 0%

## Performance Metrics

**Velocity:**

- Total plans completed: 0
- Average duration: --
- Total execution time: 0 hours

**By Phase:**

| Phase | Plans | Total | Avg/Plan |
|-------|-------|-------|----------|
| - | - | - | - |

**Recent Trend:**

- Last 5 plans: --
- Trend: --

*Updated after each plan completion*
| Phase 01 P01 | 7min | 2 tasks | 11 files |
| Phase 01 P03 | 6min | 2 tasks | 4 files |
| Phase 01 P04 | 3min | 2 tasks | 4 files |
| Phase 01 P05 | 3min | 2 tasks | 9 files |
| Phase 01 P02 | 12min | 2 tasks | 20 files |
| Phase 02 P01 | 4min | 2 tasks | 6 files |
| Phase 02 P02 | 18min | 2 tasks | 7 files |
| Phase 02 P04 | 5min | 2 tasks | 5 files |
| Phase 02 P05 | 23min | 2 tasks | 6 files |
| Phase 02 P03 | 2min | 2 tasks | 6 files |
| Phase 02 P06 | 5min | 2 tasks | 6 files |
| Phase 03 P01 | 6min | 2 tasks | 15 files |
| Phase 03 P02 | 2min | 2 tasks | 2 files |

## Accumulated Context

### Decisions

Decisions are logged in PROJECT.md Key Decisions table.
Recent decisions affecting current work:

- [Roadmap]: OpenAPI spec must be written first -- gates agent, SDK, CLI
- [Roadmap]: Agent before control plane (Docker SDK has most gotchas)
- [Roadmap]: Single-node proven before multi-node attempted
- [Roadmap]: appx-api integration is the final phase before multi-node
- [Phase 01]: Go 1.26.2 installed (Homebrew latest) with go 1.25.0 workspace directive for compatibility
- [Phase 01]: HMAC URL signing uses full URL path+query rather than sandboxID-based approach for stronger security
- [Phase 01]: Error responses use RFC 7807 application/problem+json with type/title/status/detail/instance fields
- [Phase 01]: Agent registration endpoint is unauthenticated (security: []) since it provides the token
- [Phase 01]: File push HMAC uses full URL path+query canonical message for stronger security
- [Phase 01]: DNS verification uses dig with nslookup fallback for portability
- [Phase 01]: npm install without --production flag -- babel-preset-expo and typescript needed at runtime for Metro bundling
- [Phase 01]: Shared deps at /opt/expo-shared-deps with symlink into app -- separates heavy Docker layer from app template for cache efficiency
- [Phase 01]: Used runtime.Caller for migration path resolution instead of go:embed (embed cannot traverse parent dirs)
- [Phase 01]: CAS pattern: UPDATE WHERE state=$expected returns pgx.ErrNoRows on rejection -- proven with integration tests
- [Phase 01]: Close database/sql connection before testcontainers Snapshot to avoid template DB lock
- [Phase 02]: Empty prefix for envconfig.Process since struct tags contain full FORGE_ names
- [Phase 02]: Port allocator uses map[int]bool for O(1) allocation/release operations
- [Phase 02]: rawDockerClient interface enables mock injection for testing container creation params without Docker daemon
- [Phase 02]: Docker SDK v0.4.0 uses new modular import path github.com/moby/moby/client (not docker/docker)
- [Phase 02]: chownFunc package-level var allows test override since os.Chown to UID 1000 requires root
- [Phase 02]: PollCommands creates dedicated http.Client per call with wait+5s timeout rather than modifying shared client
- [Phase 02]: Heartbeat sender uses interface-based HeartbeatClient and ResourceCollector for testability
- [Phase 02]: Agent token stored in memory only, protected by RWMutex, never logged (T-02-13 mitigation)
- [Phase 02]: SandboxResolver interface decouples handler from sandbox storage implementation
- [Phase 02]: Partial failures return 200 with failed array rather than 500, per protocol spec
- [Phase 02]: EventSource interface decouples Watcher from Docker client for testability
- [Phase 02]: Narrow single-method interfaces (ImagePullClient, LogClient) for focused mocking per interface segregation
- [Phase 02]: Watcher tracks lastEventTime and passes as Since on reconnect -- prevents missed events
- [Phase 02]: Executor implements filepush.SandboxResolver directly via CodeDir method
- [Phase 02]: Sequential command execution per agent-protocol.md -- no concurrent command handling
- [Phase 02]: Resource collector returns placeholder (0,0) in v1 -- future reads /proc/meminfo
- [Phase 03]: PoolPinger interface decouples health handler from pgxpool.Pool for testability
- [Phase 03]: serverConfig struct allows nil config in tests -- only need public routes
- [Phase 03]: Empty envconfig prefix with full FORGE_ names in struct tags -- matches Phase 02 convention
- [Phase 03]: Single-pass O(n) scan for scheduler instead of sort -- simpler for max-find
- [Phase 03]: Deterministic tiebreak by input order (first wins) for scheduler node selection

### Pending Todos

None yet.

### Blockers/Concerns

- [Research]: Docker SDK v28+ import path breakage -- pin to v27.x
- [Research]: Caddy drops ALL WebSocket connections on config reload -- needs 500ms debounce batching
- [Research]: Tailscale UDP connectivity on Contabo unverified -- Phase 1 will validate

## Deferred Items

Items acknowledged and carried forward from previous milestone close:

| Category | Item | Status | Deferred At |
|----------|------|--------|-------------|
| *(none)* | | | |

## Session Continuity

Last session: 2026-04-15T22:13:09.383Z
Stopped at: Completed 03-02-PLAN.md (bin-packing scheduler)
Resume file: None
