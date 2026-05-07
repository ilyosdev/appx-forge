# Phase 1: Infrastructure & Contracts - Research

**Researched:** 2026-04-15
**Domain:** Go infrastructure setup, Postgres schema/migrations, OpenAPI contracts, Docker sandbox image, state machine design
**Confidence:** HIGH

## Summary

Phase 1 is the foundation-laying phase: validate infrastructure assumptions (Tailscale, Docker Engine, Cloudflare DNS, inotify limits), codify all protocol contracts (OpenAPI, agent, file-push, proxy), establish the Postgres schema with migrations, implement the sandbox state machine with compare-and-swap enforcement, and produce a trimmed sandbox Docker image.

This phase has zero code dependencies on other phases -- it is the root of the dependency graph. Every subsequent phase (agent, control plane, proxy, CLI, SDK) builds against the contracts and schemas defined here. Getting contracts wrong here forces cascading changes downstream; getting them right means parallel work across tracks becomes safe.

The TDD mandate requires tests to be written first: testcontainers-go for Postgres integration tests (migration up/down, state machine transitions, compare-and-swap race conditions), property-based tests for the state machine transition table, and a smoke test for the sandbox Docker image.

**Primary recommendation:** Start with contracts (move and finalize OpenAPI spec), then schema + migrations with tests, then state machine with CAS tests, then sandbox image -- in that order. Contracts gate everything; schema gates state machine; image is independent.

<user_constraints>
## User Constraints (from CONTEXT.md)

### Locked Decisions
All implementation choices are at Claude's discretion -- pure infrastructure phase. Use ROADMAP phase goal, success criteria, and the STARTER_PLAN.md + control-api.openapi.yaml as the source of truth.

**TDD mandate from user:** Write concrete tests FIRST for all Go code (state machine, migrations). Tests must pass before moving to next phase. Use testcontainers-go for Postgres integration tests.

### Claude's Discretion
All implementation choices.

### Deferred Ideas (OUT OF SCOPE)
None -- infrastructure phase.
</user_constraints>

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| INFRA-01 | Tailscale UDP connectivity verified between Contabo VDS nodes (direct peering, not DERP relay) | Tailscale netcheck verification script; DERP fallback detection pattern; Pitfall 8 mitigation |
| INFRA-02 | Docker Engine 27.x installed and confirmed on target nodes | Docker SDK v27.x pinning rationale; v28+ import breakage; v29 Swarm 60s bug; Docker version verification |
| INFRA-03 | Kernel inotify watch limit set to support 80+ containers with Metro file watchers | sysctl settings (524288 watches, 8192 instances); per-container budget calculation |
| INFRA-04 | Cloudflare wildcard DNS configured for `*.myappx.live` pointing at proxy node(s) | Cloudflare Origin CA cert pattern; DNS record shape; manual vs Terraform options |
| CNTR-01 | OpenAPI 3.1 spec defines all v1 endpoints | Existing `control-api.openapi.yaml` at repo root; needs review, possible additions, move to `docs/contracts/` |
| CNTR-02 | Agent protocol documented | Long-poll command dispatch pattern; idempotent ack pattern; command schema |
| CNTR-03 | File push protocol documented | Signed-URL redirect pattern; HMAC-SHA256 + 60s expiry; agent direct endpoint |
| CNTR-04 | Proxy routing protocol documented | Caddy Admin API shape; `@id` route access; batch update pattern; drift detection |
| CNTR-05 | Postgres schema with migrations (nodes, sandboxes, events, commands) | goose v3.27.0 embedded migrations; pgx/v5 driver; sqlc v1.30.0 codegen; schema from STARTER_PLAN |
| CNTR-06 | Sandbox state machine with compare-and-swap | Map-based transition table; CAS SQL pattern; state_version column; event logging |
| IMG-01 | Dockerfile produces <500MB image with Metro/Expo pre-installed | Existing Dockerfile.v2 baseline; node:20-slim base; shared deps at /opt/expo-shared-deps/ |
| IMG-02 | Runs on port 8081, accepts code via bind-mount at /app/code | Existing pattern from bundle-server; bind-mount path standardization |
| IMG-03 | Pre-installed node_modules for common Expo dependencies | Existing app/package.json with 40+ Expo packages; ~500MB shared deps layer |
| IMG-04 | Documents required env vars (APP_NAME, PORT) | Environment variable inventory from existing Dockerfiles |
| IMG-05 | Cold start to Metro responding in <10s | Dockerfile.v2 uses persistent Metro (no pre-build); health check with start-period=30s |
</phase_requirements>

## Project Constraints (from CLAUDE.md)

- **Stack**: Go 1.25.x for all services, TypeScript for SDK only
- **No docker.sock mounts**: Agent runs on host as systemd service
- **No Swarm**: Plain `docker run` via Docker SDK only
- **No reconciliation loop**: State changes only on explicit events
- **Contracts first**: OpenAPI spec is sacred -- changes require ADR
- **Postgres is truth**: No agent-local state that can't be reconstructed
- **Error handling**: `fmt.Errorf("doing X: %w", err)`, no `panic` outside `main`
- **Logging**: `slog` with structured context (`sandbox_id`, `node_id`, `app_name`)
- **Config**: env vars only via `envconfig`, no config files
- **Migrations**: one file per migration, sequentially numbered, up + down SQL, never edit committed
- **API**: timestamps RFC3339 UTC, IDs are UUIDs, `application/problem+json` for errors
- **Tests**: `_test.go` next to source, testcontainers-go for Postgres
- **No clever abstractions**: repeat code twice before extracting

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| Tailscale connectivity verification | Infrastructure / Ops | -- | Host-level networking, validated via CLI tooling, not application code |
| Docker Engine setup | Infrastructure / Ops | -- | Node-level package installation, sysctl tuning -- Ansible/manual ops |
| Cloudflare DNS | CDN / Static (Edge) | -- | DNS configuration in Cloudflare dashboard or Terraform |
| inotify watch limits | Infrastructure / Ops | -- | Kernel parameter on host nodes |
| OpenAPI spec | Contract / Documentation | -- | Static artifact consumed by all tiers; not runtime code |
| Agent protocol doc | Contract / Documentation | -- | Defines control-to-agent communication contract |
| File push protocol doc | Contract / Documentation | -- | Defines SDK-to-agent file push contract |
| Proxy routing protocol doc | Contract / Documentation | -- | Defines control-to-Caddy route management contract |
| Postgres schema + migrations | Database / Storage | -- | DDL lives in `control/migrations/`, runs against Postgres |
| Sandbox state machine | API / Backend (control) | Database / Storage | Business logic in Go, enforced via CAS in Postgres |
| Sandbox Docker image | Container Runtime | -- | Dockerfile producing the Metro/Expo base image |

## Standard Stack

### Core (Phase 1 specific)

| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| Go | 1.25.x | Language runtime | Required by goose v3.27.0; Go 1.25.9 latest patch [VERIFIED: STACK.md research] |
| `pressly/goose/v3` | v3.27.0 | Database migrations | Embedded migrations via `embed.FS`, sequential numbering, up/down SQL, slog integration [VERIFIED: Context7 docs] |
| `jackc/pgx/v5` | v5.9.x | Postgres driver | Pure Go, connection pooling via pgxpool, LISTEN/NOTIFY support [VERIFIED: Context7 docs] |
| `sqlc` | v1.30.0 | Type-safe SQL codegen | Generates Go from SQL queries, works with pgx/v5 natively [VERIFIED: Context7 docs] |
| `testcontainers-go` | v0.42.0 | Postgres test containers | Snapshot/restore for test isolation, postgres module with pgx driver support [VERIFIED: Context7 docs] |
| `google/uuid` | v1.6.0 | UUID generation | All ID fields (sandbox, node, event, command) [VERIFIED: STACK.md research] |

### Supporting (Phase 1 specific)

| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `kelseyhightower/envconfig` | v1.4.0 | Env var parsing | Configuration for any Go binary bootstrapping in this phase |
| `log/slog` | stdlib | Structured logging | All Go code in this phase |
| stdlib `testing` | stdlib | Test framework | All unit and integration tests |
| `crypto/hmac` + `crypto/sha256` | stdlib | HMAC-SHA256 | Signed URL generation/verification (shared-go/auth) |

### Alternatives Considered

| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| goose | Atlas | More powerful declarative diffing, but overkill for 3-4 tables and adds SaaS dependency |
| goose | golang-migrate | "Dirty" state on partial failure with no auto-recovery; no library mode for embedding |
| sqlc | Raw pgx queries | Manual `rows.Scan()` for 30-50 queries is tedious and error-prone |
| sqlc | GORM | ORM overhead, hides SQL, reflection-based -- fights the raw-SQL-first design |
| testcontainers-go | dockertest | Less maintained, no snapshot/restore, less ergonomic API |

**Installation (Phase 1 Go modules):**

```bash
# In control/ directory
go mod init github.com/appx/forge/control
go get github.com/jackc/pgx/v5@latest
go get github.com/google/uuid@v1.6.0
go get github.com/kelseyhightower/envconfig@v1.4.0
go get github.com/testcontainers/testcontainers-go@v0.42.0
go get github.com/testcontainers/testcontainers-go/modules/postgres@v0.42.0

# In shared-go/ directory
go mod init github.com/appx/forge/shared-go
go get github.com/google/uuid@v1.6.0

# CLI tools (installed globally)
go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0
go install github.com/pressly/goose/v3/cmd/goose@v3.27.0
```

## Architecture Patterns

### System Architecture Diagram

```
Phase 1 scope (what gets built):

    control-api.openapi.yaml ─────── gates all downstream phases
              |
    ┌─────────┴──────────┐
    |                    |
    v                    v
 agent-protocol.md   filepush-protocol.md
 proxy-routing.md
              |
              v
    ┌─── Postgres ────────────────────────────────┐
    |  nodes table                                 |
    |  sandboxes table (state machine + CAS)       |
    |  events table (append-only audit log)        |
    |  commands table (dispatch + ack tracking)    |
    └──────────────────────────────────────────────┘
              |
              v
    State Machine (Go)
    ┌──────────────────────────────┐
    |  (currentState, event)       |
    |       -> (newState, side     |
    |           effects)           |
    |  CAS: UPDATE WHERE state=$exp|
    └──────────────────────────────┘
              |
              v
    Sandbox Docker Image
    ┌──────────────────────────────┐
    |  node:20-slim                |
    |  + Expo SDK 54               |
    |  + Metro bundler             |
    |  + 40+ expo packages         |
    |  Port 8081, bind-mount /app/code |
    └──────────────────────────────┘

Infrastructure verification (manual/scripted):
    - Tailscale direct peering ── tailscale netcheck
    - Docker Engine 27.x ──────── docker version
    - inotify 524288 ──────────── sysctl -p
    - Cloudflare *.myappx.live ── dig/nslookup
```

### Recommended Project Structure (Phase 1 deliverables)

```
appx-forge/
├── go.work                          # Go workspace root
├── docs/
│   ├── contracts/
│   │   ├── control-api.openapi.yaml # Moved from repo root, finalized
│   │   ├── agent-protocol.md        # NEW: command/ack protocol
│   │   ├── filepush-protocol.md     # NEW: signed URL redirect
│   │   └── proxy-routing.md         # NEW: Caddy Admin API usage
│   └── adr/                         # Architecture Decision Records
├── control/
│   ├── go.mod
│   ├── internal/
│   │   ├── lifecycle/               # State machine
│   │   │   ├── state.go             # State + Event types, transition table
│   │   │   ├── machine.go           # Transition logic + CAS + event emit
│   │   │   ├── state_test.go        # TDD: property tests for transitions
│   │   │   └── machine_test.go      # TDD: CAS integration tests
│   │   └── store/                   # sqlc-generated
│   │       ├── queries/
│   │       │   ├── sandboxes.sql    # CAS transition queries
│   │       │   ├── nodes.sql        # Node CRUD
│   │       │   ├── events.sql       # Event recording
│   │       │   └── commands.sql     # Command dispatch/ack
│   │       ├── models.go            # Generated
│   │       ├── db.go                # Generated
│   │       └── querier.go           # Generated
│   ├── migrations/
│   │   ├── 00001_create_nodes.sql
│   │   ├── 00002_create_sandboxes.sql
│   │   ├── 00003_create_events.sql
│   │   └── 00004_create_commands.sql
│   ├── sqlc.yaml
│   └── tests/
│       ├── migration_test.go        # TDD: up/down/up cycle
│       ├── store_test.go            # TDD: CAS, CRUD, queries
│       └── testhelpers/
│           └── postgres.go          # Shared testcontainers setup
├── shared-go/
│   ├── go.mod
│   ├── models/
│   │   ├── sandbox.go               # State, Event types
│   │   ├── node.go                  # Node model
│   │   ├── command.go               # Command types
│   │   └── route.go                 # Route model
│   └── auth/
│       ├── hmac.go                  # Signed URL generation
│       └── hmac_test.go             # TDD: signature verification
├── sandbox-image/
│   ├── Dockerfile                   # Trimmed from existing bundle-server
│   ├── app/
│   │   ├── package.json             # Expo deps
│   │   └── ... template files
│   └── smoke-test.sh               # Verify Metro responds in <10s
└── deploy/
    └── scripts/
        ├── verify-tailscale.sh      # INFRA-01 validation
        ├── verify-docker.sh         # INFRA-02 validation
        ├── setup-inotify.sh         # INFRA-03 setup
        └── verify-dns.sh            # INFRA-04 validation
```

### Pattern 1: Compare-and-Swap State Transitions

**What:** Every sandbox state transition uses Postgres CAS: `UPDATE sandboxes SET state = $new WHERE id = $id AND state = $expected RETURNING *`. Zero rows affected means the transition was rejected (concurrent modification). [VERIFIED: ARCHITECTURE.md research, PITFALLS.md Pitfall 4]

**When to use:** Every state change in the sandbox lifecycle. No exceptions.

**Example:**
```sql
-- Source: STARTER_PLAN.md pattern + CAS enforcement from PITFALLS.md
-- queries/sandboxes.sql (sqlc)

-- name: TransitionSandboxState :one
UPDATE sandboxes
SET state = $1,
    updated_at = NOW(),
    state_version = state_version + 1
WHERE id = $2
  AND state = $3
RETURNING *;
```

```go
// Source: ARCHITECTURE.md state machine pattern
func (m *Machine) Transition(ctx context.Context, sandboxID uuid.UUID, event Event) (*Sandbox, error) {
    current, err := m.store.GetSandbox(ctx, sandboxID)
    if err != nil {
        return nil, fmt.Errorf("getting sandbox %s: %w", sandboxID, err)
    }

    transitions, ok := validTransitions[current.State]
    if !ok {
        return nil, fmt.Errorf("no transitions defined from state %s", current.State)
    }

    nextState, ok := transitions[event]
    if !ok {
        return nil, fmt.Errorf("invalid event %s in state %s for sandbox %s", event, current.State, sandboxID)
    }

    // CAS: only succeeds if state hasn't changed since we read it
    updated, err := m.store.TransitionSandboxState(ctx, nextState, sandboxID, current.State)
    if err != nil {
        return nil, fmt.Errorf("CAS transition %s->%s for sandbox %s: %w", current.State, nextState, sandboxID, err)
    }

    // Record event for audit trail
    m.store.RecordEvent(ctx, sandboxID, event, current.State, nextState)

    return updated, nil
}
```

### Pattern 2: Goose Embedded Migrations with testcontainers-go

**What:** SQL migrations are embedded in the Go binary via `embed.FS`. Tests use testcontainers-go Postgres module with snapshot/restore for isolation. [VERIFIED: Context7 docs for both goose and testcontainers-go]

**When to use:** All migration and store tests in this phase.

**Example:**
```go
// Source: Context7 testcontainers-go docs + goose embed docs
package testhelpers

import (
    "context"
    "database/sql"
    "embed"
    "testing"

    "github.com/pressly/goose/v3"
    "github.com/testcontainers/testcontainers-go"
    "github.com/testcontainers/testcontainers-go/modules/postgres"
)

//go:embed migrations/*.sql
var migrations embed.FS

func SetupTestDB(t *testing.T) (string, *postgres.PostgresContainer) {
    t.Helper()
    ctx := context.Background()

    ctr, err := postgres.Run(ctx, "postgres:16-alpine",
        postgres.WithDatabase("forge_test"),
        postgres.WithUsername("test"),
        postgres.WithPassword("test"),
        postgres.BasicWaitStrategies(),
        postgres.WithSQLDriver("pgx"),
    )
    if err != nil {
        t.Fatalf("starting postgres container: %v", err)
    }
    testcontainers.CleanupContainer(t, ctr)

    dbURL, err := ctr.ConnectionString(ctx, "sslmode=disable")
    if err != nil {
        t.Fatalf("getting connection string: %v", err)
    }

    // Run migrations
    db, err := sql.Open("pgx", dbURL)
    if err != nil {
        t.Fatalf("opening db: %v", err)
    }
    defer db.Close()

    goose.SetBaseFS(migrations)
    if err := goose.SetDialect("postgres"); err != nil {
        t.Fatalf("setting dialect: %v", err)
    }
    if err := goose.Up(db, "migrations"); err != nil {
        t.Fatalf("running migrations: %v", err)
    }

    // Snapshot for test isolation
    if err := ctr.Snapshot(ctx); err != nil {
        t.Fatalf("creating snapshot: %v", err)
    }

    return dbURL, ctr
}
```

### Pattern 3: sqlc with pgx/v5 for Type-Safe Queries

**What:** Write SQL queries in `.sql` files with sqlc annotations. sqlc generates type-safe Go code compatible with pgx/v5. [VERIFIED: Context7 sqlc docs]

**When to use:** All database queries in the store layer.

**Example:**
```yaml
# Source: Context7 sqlc docs
# control/sqlc.yaml
version: "2"
sql:
  - engine: "postgresql"
    queries: "internal/store/queries/"
    schema: "migrations/"
    gen:
      go:
        package: "store"
        out: "internal/store"
        sql_package: "pgx/v5"
        emit_json_tags: true
        emit_empty_slices: true
```

```sql
-- Source: STARTER_PLAN.md schema + CAS pattern
-- internal/store/queries/sandboxes.sql

-- name: CreateSandbox :one
INSERT INTO sandboxes (
    id, app_name, user_id, image, state, resources, metadata
) VALUES ($1, $2, $3, $4, 'pending', $5, $6)
RETURNING *;

-- name: GetSandbox :one
SELECT * FROM sandboxes WHERE id = $1;

-- name: GetSandboxByAppName :one
SELECT * FROM sandboxes WHERE app_name = $1;

-- name: TransitionSandboxState :one
UPDATE sandboxes
SET state = $1, updated_at = NOW(), state_version = state_version + 1
WHERE id = $2 AND state = $3
RETURNING *;

-- name: AssignSandboxToNode :one
UPDATE sandboxes
SET node_id = $1, state = 'starting', updated_at = NOW(), state_version = state_version + 1
WHERE id = $2 AND state = 'pending'
RETURNING *;
```

### Anti-Patterns to Avoid

- **No `UPDATE sandboxes SET state = $new` without `WHERE state = $expected`:** Every state transition MUST be compare-and-swap. Without CAS, concurrent writers (API + agent report) can create impossible states. [VERIFIED: PITFALLS.md Pitfall 4]
- **No editing committed migrations:** Once a migration file is committed, it is immutable. Create a new migration to alter. [VERIFIED: CLAUDE_CODE_KICKOFF.md conventions]
- **No hand-rolled state machine libraries:** The state machine is <50 LOC with a map-based transition table. No external library needed at this scale. [VERIFIED: ARCHITECTURE.md Pattern 1]
- **No goose global state in production code:** Use `goose.NewProvider()` (v3.27.0+) for library mode in tests instead of global `goose.SetBaseFS()` which has race conditions in parallel tests. [ASSUMED]

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Database migrations | Custom migration runner | goose v3.27.0 | Handles locking, up/down, sequential ordering, embedded FS, transaction wrapping |
| Type-safe SQL queries | Manual `rows.Scan()` boilerplate | sqlc v1.30.0 | Generates from SQL, catches type errors at build time, zero runtime overhead |
| UUID generation | `math/rand` or `crypto/rand` formatting | `google/uuid` v1.6.0 | RFC 4122 compliant, tested, v7 time-sortable option available |
| Test database containers | Manual Docker setup/teardown | testcontainers-go v0.42.0 | Lifecycle management, snapshot/restore, wait strategies, cleanup |
| HMAC signing | Custom crypto | stdlib `crypto/hmac` + `crypto/sha256` | Stdlib is audited, constant-time comparison via `hmac.Equal()` |
| Postgres connection pooling | Custom pool | pgxpool (in pgx/v5) | Health checks, jitter, max conn lifetime, configurable from URL |

**Key insight:** Phase 1 is infrastructure -- every component here is a well-solved problem. The only novel code is the state machine transition table (~50 LOC) and the contract documents (prose).

## Common Pitfalls

### Pitfall 1: State Machine Race Between Agent Reports and API Calls
**What goes wrong:** Concurrent state transitions without CAS create impossible states (e.g., `running` and `destroying` simultaneously). [VERIFIED: PITFALLS.md Pitfall 4]
**Why it happens:** Plain `UPDATE SET state = $new` without checking current state.
**How to avoid:** CAS enforcement: `UPDATE WHERE state = $expected RETURNING *`. Zero rows = rejected transition. Add `state_version` integer for optimistic concurrency.
**Warning signs:** Events table shows impossible sequences (`running -> running`, `destroyed -> restarting`).

### Pitfall 2: Goose Migration Down Script Missing or Broken
**What goes wrong:** `goose down` fails because the down migration drops tables in wrong order (FK constraint violations) or is simply empty.
**Why it happens:** Developers write `up` and skip `down`. Down migrations are only tested when something goes wrong in production.
**How to avoid:** TDD: write a test that runs `up -> down -> up` cycle and verifies clean state. Test this for every migration file.
**Warning signs:** `goose down` fails with FK constraint error; migration state gets stuck.

### Pitfall 3: sqlc Generated Code Breaks on Schema Change
**What goes wrong:** Someone modifies a migration SQL file but forgets to re-run `sqlc generate`. Generated Go code is out of sync with the actual schema.
**How to avoid:** Add `sqlc vet` to CI/pre-commit. Consider `sqlc diff` to detect drift. Run `sqlc generate` as part of the build process.
**Warning signs:** Runtime SQL errors that don't show up in `go build`.

### Pitfall 4: testcontainers-go Snapshot Doesn't Restore Sequences
**What goes wrong:** After `ctr.Restore()`, auto-increment sequences (like `events.id BIGSERIAL`) may not reset to the pre-snapshot value, causing unexpected ID gaps or conflicts in tests that assert on specific IDs.
**Why it happens:** PostgreSQL `pg_dump`-based snapshots may handle sequences differently than full data.
**How to avoid:** Never assert on specific auto-generated IDs in tests. Assert on row counts, column values, or use UUID primary keys (which this schema does for nodes and sandboxes). The `events` table uses BIGSERIAL -- test by checking row existence, not ID values.
**Warning signs:** Tests pass individually but fail when run together due to sequence state leaking.

### Pitfall 5: Bind Mount UID/GID Mismatch in Sandbox Image
**What goes wrong:** Agent creates directory as root (UID 0), container runs Node.js as UID 1000. Metro can't read files, inotify fails silently. [VERIFIED: PITFALLS.md Pitfall 6]
**Why it happens:** Linux bind mounts preserve host UID/GID.
**How to avoid:** Define fixed user in Dockerfile (`RUN useradd -u 1000 -m appuser`). Agent must `chown` bind-mount directories to match.
**Warning signs:** Metro logs `EACCES: permission denied`; HMR doesn't trigger after file push.

### Pitfall 6: Sandbox Image Exceeds 500MB Due to node_modules
**What goes wrong:** Expo shared deps layer alone is ~500MB. Adding server code, git, build tools pushes past target.
**Why it happens:** The existing `app/package.json` has 40+ Expo packages including heavy ones (maps, camera, av, reanimated).
**How to avoid:** Multi-stage build. Use `--production` flag. Remove dev-only packages from the shared deps. Consider whether all 40+ packages are needed in the sandbox (vs. installed on-demand).
**Warning signs:** `docker images` shows image >500MB; pull times on nodes >30s.

## Code Examples

### Migration: Create Sandboxes Table with State Machine Support

```sql
-- Source: STARTER_PLAN.md schema + CAS additions
-- control/migrations/00002_create_sandboxes.sql

-- +goose Up
CREATE TABLE sandboxes (
    id              UUID PRIMARY KEY,
    app_name        TEXT NOT NULL UNIQUE,
    user_id         TEXT NOT NULL,
    node_id         UUID REFERENCES nodes(id),
    container_id    TEXT,
    host_port       INT,
    image           TEXT NOT NULL,
    state           TEXT NOT NULL DEFAULT 'pending'
                    CHECK (state IN ('pending','starting','running','restarting',
                                     'stopped','destroying','destroyed','failed')),
    state_version   INT NOT NULL DEFAULT 0,
    resources       JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_active_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    failure_count   INT NOT NULL DEFAULT 0,
    metadata        JSONB NOT NULL DEFAULT '{}'
);

CREATE INDEX idx_sandboxes_app_name ON sandboxes(app_name);
CREATE INDEX idx_sandboxes_state ON sandboxes(state);
CREATE INDEX idx_sandboxes_node ON sandboxes(node_id);
CREATE INDEX idx_sandboxes_user ON sandboxes(user_id);

-- +goose Down
DROP TABLE IF EXISTS sandboxes;
```

### Migration: Create Events Table (Audit Log)

```sql
-- Source: STARTER_PLAN.md schema
-- control/migrations/00003_create_events.sql

-- +goose Up
CREATE TABLE events (
    id          BIGSERIAL PRIMARY KEY,
    sandbox_id  UUID,
    node_id     UUID,
    event_type  TEXT NOT NULL,
    actor       TEXT NOT NULL DEFAULT 'system',
    prev_state  TEXT,
    next_state  TEXT,
    payload     JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_events_sandbox ON events(sandbox_id, created_at DESC);
CREATE INDEX idx_events_type ON events(event_type, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS events;
```

### Migration: Create Commands Table

```sql
-- Source: ARCHITECTURE.md Pattern 2 + PITFALLS.md Pitfall 10 (idempotency)
-- control/migrations/00004_create_commands.sql

-- +goose Up
CREATE TABLE commands (
    id            UUID PRIMARY KEY,
    node_id       UUID NOT NULL REFERENCES nodes(id),
    sandbox_id    UUID REFERENCES sandboxes(id),
    command_type  TEXT NOT NULL
                  CHECK (command_type IN ('start_sandbox','stop_sandbox',
                                          'restart_sandbox','get_logs','prune')),
    payload       JSONB NOT NULL DEFAULT '{}',
    status        TEXT NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending','dispatched','completed','failed')),
    dispatched_at TIMESTAMPTZ,
    acked_at      TIMESTAMPTZ,
    result        JSONB,
    timeout_seconds INT NOT NULL DEFAULT 60,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_commands_node_pending ON commands(node_id, status)
    WHERE status IN ('pending', 'dispatched');

-- +goose Down
DROP TABLE IF EXISTS commands;
```

### State Machine Transition Table (Go)

```go
// Source: ARCHITECTURE.md Pattern 1, validated
// shared-go/models/sandbox.go

package models

type SandboxState string

const (
    StatePending    SandboxState = "pending"
    StateStarting   SandboxState = "starting"
    StateRunning    SandboxState = "running"
    StateRestarting SandboxState = "restarting"
    StateStopped    SandboxState = "stopped"
    StateDestroying SandboxState = "destroying"
    StateDestroyed  SandboxState = "destroyed"
    StateFailed     SandboxState = "failed"
)

type SandboxEvent string

const (
    EventScheduled       SandboxEvent = "scheduled"
    EventStarted         SandboxEvent = "started"
    EventContainerExited SandboxEvent = "container_exited"
    EventIdleTimeout     SandboxEvent = "idle_timeout"
    EventDestroyRequest  SandboxEvent = "destroy_requested"
    EventDestroyed       SandboxEvent = "destroyed"
    EventRestartAttempt  SandboxEvent = "restart_attempt"
    EventNodeFailed      SandboxEvent = "node_failed"
    EventStartFailed     SandboxEvent = "start_failed"
)

// ValidTransitions defines the complete state machine.
// Key: current state. Value: map of event -> next state.
var ValidTransitions = map[SandboxState]map[SandboxEvent]SandboxState{
    StatePending: {
        EventScheduled:      StateStarting,
        EventDestroyRequest: StateDestroyed,
    },
    StateStarting: {
        EventStarted:         StateRunning,
        EventContainerExited: StateFailed,
        EventStartFailed:     StateFailed,
        EventDestroyRequest:  StateDestroying,
    },
    StateRunning: {
        EventContainerExited: StateRestarting,
        EventDestroyRequest:  StateDestroying,
        EventIdleTimeout:     StateStopped,
        EventNodeFailed:      StatePending,
    },
    StateRestarting: {
        EventRestartAttempt: StateStarting,
        EventDestroyRequest: StateDestroying,
    },
    StateStopped: {
        EventScheduled:      StateStarting,
        EventDestroyRequest: StateDestroyed,
    },
    StateDestroying: {
        EventDestroyed: StateDestroyed,
    },
    StateFailed: {
        EventRestartAttempt: StateStarting,
        EventDestroyRequest: StateDestroyed,
    },
}

// NextState returns the target state for a given current state and event,
// or false if the transition is invalid.
func NextState(current SandboxState, event SandboxEvent) (SandboxState, bool) {
    transitions, ok := ValidTransitions[current]
    if !ok {
        return "", false
    }
    next, ok := transitions[event]
    return next, ok
}
```

### TDD Test: State Machine Property Tests

```go
// Source: pattern from ARCHITECTURE.md, TDD-first per user mandate
// shared-go/models/sandbox_test.go

package models

import "testing"

func TestAllStatesHaveTransitions(t *testing.T) {
    allStates := []SandboxState{
        StatePending, StateStarting, StateRunning, StateRestarting,
        StateStopped, StateDestroying, StateDestroyed, StateFailed,
    }
    for _, state := range allStates {
        if state == StateDestroyed {
            continue // terminal state, no outgoing transitions expected
        }
        transitions, ok := ValidTransitions[state]
        if !ok || len(transitions) == 0 {
            t.Errorf("state %q has no outgoing transitions", state)
        }
    }
}

func TestDestroyedIsTerminal(t *testing.T) {
    _, hasTransitions := ValidTransitions[StateDestroyed]
    if hasTransitions {
        t.Error("StateDestroyed should be terminal (no outgoing transitions)")
    }
}

func TestEveryStateCanReachDestroyed(t *testing.T) {
    // Every non-terminal state should have a path to StateDestroyed
    reachable := map[SandboxState]bool{StateDestroyed: true}
    changed := true
    for changed {
        changed = false
        for state, transitions := range ValidTransitions {
            if reachable[state] {
                continue
            }
            for _, next := range transitions {
                if reachable[next] {
                    reachable[state] = true
                    changed = true
                    break
                }
            }
        }
    }

    allStates := []SandboxState{
        StatePending, StateStarting, StateRunning, StateRestarting,
        StateStopped, StateDestroying, StateFailed,
    }
    for _, state := range allStates {
        if !reachable[state] {
            t.Errorf("state %q cannot reach StateDestroyed", state)
        }
    }
}

func TestDestroyRequestAlwaysAccepted(t *testing.T) {
    // Destroy should work from any active state
    activeStates := []SandboxState{
        StatePending, StateStarting, StateRunning, StateRestarting,
        StateStopped, StateFailed,
    }
    for _, state := range activeStates {
        next, ok := NextState(state, EventDestroyRequest)
        if !ok {
            t.Errorf("EventDestroyRequest not accepted in state %q", state)
        }
        if next != StateDestroying && next != StateDestroyed {
            t.Errorf("EventDestroyRequest in %q should lead to destroying/destroyed, got %q", state, next)
        }
    }
}

func TestInvalidTransitionsRejected(t *testing.T) {
    tests := []struct {
        state SandboxState
        event SandboxEvent
    }{
        {StateDestroyed, EventStarted},
        {StateDestroyed, EventScheduled},
        {StatePending, EventStarted},
        {StateRunning, EventScheduled},
        {StateStarting, EventIdleTimeout},
    }
    for _, tt := range tests {
        _, ok := NextState(tt.state, tt.event)
        if ok {
            t.Errorf("transition %q + %q should be invalid", tt.state, tt.event)
        }
    }
}
```

### TDD Test: CAS Integration Test with testcontainers-go

```go
// Source: testcontainers-go Context7 docs + CAS pattern
// control/tests/store_test.go

func TestTransitionSandboxState_CASRejectsConcurrentWrite(t *testing.T) {
    dbURL, ctr := testhelpers.SetupTestDB(t)
    t.Cleanup(func() { ctr.Restore(context.Background()) })

    ctx := context.Background()
    pool, err := pgxpool.New(ctx, dbURL)
    require.NoError(t, err)
    defer pool.Close()

    queries := store.New(pool)

    // Create a sandbox in 'pending' state
    sandboxID := uuid.New()
    _, err = queries.CreateSandbox(ctx, store.CreateSandboxParams{
        ID:      sandboxID,
        AppName: "test-app",
        UserID:  "user-1",
        Image:   "appx/sandbox:v1",
    })
    require.NoError(t, err)

    // First CAS: pending -> starting (should succeed)
    updated, err := queries.TransitionSandboxState(ctx, "starting", sandboxID, "pending")
    require.NoError(t, err)
    require.Equal(t, "starting", string(updated.State))

    // Second CAS with stale state: pending -> starting (should fail -- state is now 'starting')
    _, err = queries.TransitionSandboxState(ctx, "starting", sandboxID, "pending")
    require.Error(t, err) // pgx.ErrNoRows -- zero rows affected
}
```

### HMAC Signed URL (shared-go)

```go
// Source: ARCHITECTURE.md Pattern 4 (signed-URL file push)
// shared-go/auth/hmac.go

package auth

import (
    "crypto/hmac"
    "crypto/sha256"
    "encoding/hex"
    "fmt"
    "strconv"
    "time"
)

// SignURL generates an HMAC-SHA256 signature for a file push URL.
func SignURL(secret []byte, sandboxID string, expiresAt time.Time) string {
    msg := fmt.Sprintf("%s:%d", sandboxID, expiresAt.Unix())
    mac := hmac.New(sha256.New, secret)
    mac.Write([]byte(msg))
    return hex.EncodeToString(mac.Sum(nil))
}

// VerifyURL checks the HMAC signature and expiry.
func VerifyURL(secret []byte, sandboxID string, expiresStr string, signature string) error {
    expires, err := strconv.ParseInt(expiresStr, 10, 64)
    if err != nil {
        return fmt.Errorf("parsing expiry: %w", err)
    }

    if time.Now().Unix() > expires {
        return fmt.Errorf("signed URL expired")
    }

    expected := SignURL(secret, sandboxID, time.Unix(expires, 0))
    if !hmac.Equal([]byte(expected), []byte(signature)) {
        return fmt.Errorf("invalid signature")
    }

    return nil
}
```

### Sandbox Dockerfile (Trimmed from Existing)

```dockerfile
# Source: Existing bundle-server-spike/Dockerfile.v2, trimmed for Forge
# sandbox-image/Dockerfile

FROM node:20-slim

# Only git needed (some metro deps require it)
RUN apt-get update && apt-get install -y --no-install-recommends git \
    && rm -rf /var/lib/apt/lists/*

# Create non-root user (UID 1000 -- agent must chown bind-mount dirs to match)
RUN useradd -u 1000 -m -s /bin/sh appuser

WORKDIR /app

# Layer 1: Install shared Expo/RN dependencies (~500MB, heavily cached)
COPY app/package.json /opt/expo-shared-deps/package.json
RUN cd /opt/expo-shared-deps && npm install --legacy-peer-deps --production \
    && echo "Shared deps: $(du -sh /opt/expo-shared-deps/node_modules | cut -f1)"

# Layer 2: Copy app template
COPY app/ ./app/

# Symlink shared deps into app directory
RUN ln -sf /opt/expo-shared-deps/node_modules /app/app/node_modules

# Create directories
RUN mkdir -p /app/code /app/cache \
    && chown -R appuser:appuser /app/code /app/cache

# Make expo CLI available
ENV NODE_PATH=/opt/expo-shared-deps/node_modules
ENV PATH="/opt/expo-shared-deps/node_modules/.bin:${PATH}"
ENV PORT=8081
ENV NODE_ENV=development

# Bind-mount point for user code
VOLUME ["/app/code"]

EXPOSE 8081

HEALTHCHECK --interval=30s --timeout=10s --retries=3 --start-period=30s \
    CMD node -e "fetch('http://localhost:${PORT}/status').then(r=>r.ok?process.exit(0):process.exit(1)).catch(()=>process.exit(1))"

USER appuser

# Metro persistent dev server -- detects file changes via inotify
CMD ["npx", "expo", "start", "--port", "8081", "--no-dev-client"]
```

### Infrastructure Verification Script (Tailscale)

```bash
#!/usr/bin/env bash
# Source: PITFALLS.md Pitfall 8 (DERP relay detection)
# deploy/scripts/verify-tailscale.sh

set -euo pipefail

echo "=== Tailscale Connectivity Verification ==="

# Check tailscale is running
if ! tailscale status &>/dev/null; then
    echo "FAIL: tailscale is not running"
    exit 1
fi

echo "Tailscale status:"
tailscale status

echo ""
echo "Network check:"
tailscale netcheck

echo ""
echo "Checking for DERP relay usage..."
# If any peer shows 'relay' instead of 'direct', warn
if tailscale status | grep -q "relay"; then
    echo "WARNING: Some connections are using DERP relay (higher latency)"
    echo "Check UDP port 41641 is open in Contabo firewall"
    exit 1
else
    echo "OK: All connections are direct (no DERP relay)"
fi
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| Docker Swarm reconciler | Event-driven state machine | This project (2026) | Eliminates 60s restart bug, deterministic state changes |
| GORM / manual SQL | sqlc v1.30.0 codegen | 2024-2025 ecosystem shift | Type-safe queries with zero runtime overhead |
| golang-migrate | goose v3.27.0 | 2025 (goose v3.26+ slog support) | Better failure handling, embedded FS, no "dirty" state |
| `docker/docker/client` import | `moby/moby/client` v27.x | 2025 | Canonical import path, avoids v28 lazyregexp breakage |
| Custom test DB setup | testcontainers-go v0.42.0 | 2024-2025 | Snapshot/restore, automatic cleanup, pgx driver support |

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | goose v3.27.0 `NewProvider()` avoids global state race conditions in parallel tests | Anti-Patterns | Tests may need to use `sync.Mutex` around global goose functions; medium impact |
| A2 | `npm install --production` on the Expo deps will keep shared deps under 500MB | Sandbox Image / IMG-01 | May need to trim package list or use multi-stage build to meet <500MB target |
| A3 | Metro `npx expo start` command works with bind-mounted `/app/code` and inotify | Sandbox Image / IMG-05 | May need custom metro.config.js with explicit watchFolders configuration |
| A4 | Existing bundle-server app/package.json packages are all needed for sandbox image | Sandbox Image / IMG-03 | Some packages (camera, contacts, calendar) may be removable to reduce image size |
| A5 | sqlc handles pgx `ErrNoRows` correctly for CAS queries returning zero rows | CAS Pattern | May need custom error handling wrapper around sqlc-generated code |

## Open Questions (RESOLVED)

1. **Sandbox image CMD: `expo start` vs custom server?**
   - What we know: Dockerfile.v2 uses `npx tsx src/server-v2.ts` (custom Express+Metro server). Expo CLI `expo start` is simpler but may lack custom endpoints (`/status`, `/update`).
   - What's unclear: Whether the sandbox needs a custom HTTP server (for file push direct endpoint, status checks) or if Metro's built-in dev server is sufficient.
   - RESOLVED: Use `expo start` for simplicity. Agent provides file push and health endpoints — container only needs Metro.

2. **Should `commands` table be in Phase 1 or Phase 3?**
   - What we know: Phase 1 defines contracts and schema. The `commands` table supports long-poll dispatch which is Phase 3 (control plane) functionality.
   - What's unclear: Whether to create the table now (complete schema) or defer to Phase 3.
   - RESOLVED: Create it now in Phase 1. Schema is cheap. Complete schema in Phase 1 means downstream phases don't need migration changes for core tables.

3. **Postgres hosting: local Docker or managed service?**
   - What we know: STARTER_PLAN mentions Neon/Supabase. Local development uses Docker Compose.
   - What's unclear: Whether to set up managed Postgres now or defer.
   - RESOLVED: Local Docker for development (docker-compose.dev.yml). Managed Postgres is a deployment concern for Phase 6/7.

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Go | All Go code | No | -- | Must install: `brew install go` (will get 1.25.x or 1.26.x) |
| Docker | testcontainers-go, sandbox image | Yes | 29.4.0 (local Mac) | Local Docker 29.x works for dev; nodes need 27.x |
| sqlc | Code generation | No | -- | `go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0` |
| goose | Migration CLI | No | -- | `go install github.com/pressly/goose/v3/cmd/goose@v3.27.0` |
| Tailscale | INFRA-01 | No (local) | -- | Verify on Contabo nodes, not local dev machine |
| Postgres | Schema testing | Via Docker | 16-alpine (container) | testcontainers-go spins up ephemeral instances |
| Node.js | Sandbox image build | Yes | 22.21.1 | Not needed on dev machine; Docker handles it |
| npm | Sandbox image build | Yes | 10.9.4 | Not needed on dev machine; Docker handles it |

**Missing dependencies with no fallback:**
- Go must be installed before any Go code can be written. This is the first task.

**Missing dependencies with fallback:**
- sqlc and goose are installable via `go install` once Go is available.
- Docker 29.x on dev machine works fine for local development and testcontainers-go. The Docker 27.x requirement only applies to production nodes.

## Validation Architecture

### Test Framework

| Property | Value |
|----------|-------|
| Framework | Go stdlib `testing` + `testcontainers-go` v0.42.0 |
| Config file | None needed (Go convention: `_test.go` next to source) |
| Quick run command | `go test ./... -short` |
| Full suite command | `go test ./... -v -count=1` |

### Phase Requirements to Test Map

| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| INFRA-01 | Tailscale direct peering | manual-only | `bash deploy/scripts/verify-tailscale.sh` (on node) | Wave 0 |
| INFRA-02 | Docker Engine 27.x on nodes | manual-only | `bash deploy/scripts/verify-docker.sh` (on node) | Wave 0 |
| INFRA-03 | inotify watch limit 524288 | manual-only | `cat /proc/sys/fs/inotify/max_user_watches` (on node) | Wave 0 |
| INFRA-04 | Cloudflare wildcard DNS | manual-only | `dig +short '*.myappx.live'` | Wave 0 |
| CNTR-01 | OpenAPI spec completeness | manual-only | Review spec against REQUIREMENTS.md | -- |
| CNTR-02 | Agent protocol documented | manual-only | Review doc against STARTER_PLAN | -- |
| CNTR-03 | File push protocol documented | manual-only | Review doc against STARTER_PLAN | -- |
| CNTR-04 | Proxy routing protocol documented | manual-only | Review doc against STARTER_PLAN | -- |
| CNTR-05 | Postgres schema + migrations | integration | `go test ./control/tests/ -run TestMigrationUpDown -v` | Wave 0 |
| CNTR-06 | State machine CAS transitions | unit+integration | `go test ./shared-go/models/ -v && go test ./control/tests/ -run TestCAS -v` | Wave 0 |
| IMG-01 | Dockerfile <500MB | smoke | `docker build -t forge-sandbox:test . && docker images forge-sandbox:test --format '{{.Size}}'` | Wave 0 |
| IMG-02 | Port 8081, bind-mount /app/code | smoke | `bash sandbox-image/smoke-test.sh` | Wave 0 |
| IMG-03 | Pre-installed node_modules | smoke | `docker run --rm forge-sandbox:test ls /opt/expo-shared-deps/node_modules/expo` | Wave 0 |
| IMG-04 | Required env vars documented | manual-only | Review Dockerfile ENV directives | -- |
| IMG-05 | Cold start <10s | smoke | `bash sandbox-image/smoke-test.sh` (times Metro startup) | Wave 0 |

### Sampling Rate
- **Per task commit:** `go test ./... -short` (skips integration tests)
- **Per wave merge:** `go test ./... -v -count=1` (full suite including testcontainers)
- **Phase gate:** Full suite green + infrastructure verification scripts pass on at least one node

### Wave 0 Gaps
- [ ] `control/tests/migration_test.go` -- covers CNTR-05 (up/down/up cycle)
- [ ] `control/tests/store_test.go` -- covers CNTR-06 (CAS integration)
- [ ] `shared-go/models/sandbox_test.go` -- covers CNTR-06 (state machine properties)
- [ ] `shared-go/auth/hmac_test.go` -- covers CNTR-03 (signed URL verification)
- [ ] `control/tests/testhelpers/postgres.go` -- shared test fixture
- [ ] `sandbox-image/smoke-test.sh` -- covers IMG-01, IMG-02, IMG-05
- [ ] Go installation: `brew install go` or `go install` equivalent
- [ ] sqlc installation: `go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0`
- [ ] goose installation: `go install github.com/pressly/goose/v3/cmd/goose@v3.27.0`

## Security Domain

### Applicable ASVS Categories

| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | No (Phase 1 is infrastructure, auth is Phase 3) | -- |
| V3 Session Management | No | -- |
| V4 Access Control | No (bearer token auth is Phase 3) | -- |
| V5 Input Validation | Yes (schema CHECK constraints, sqlc type safety) | Postgres CHECK constraints + sqlc compile-time validation |
| V6 Cryptography | Yes (HMAC-SHA256 for signed URLs) | stdlib `crypto/hmac` + `crypto/sha256` -- never hand-roll |

### Known Threat Patterns for Go + Postgres

| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| SQL injection via string interpolation | Tampering | sqlc generates parameterized queries ($1, $2) -- injection impossible in generated code |
| Timing attack on HMAC verification | Information Disclosure | `hmac.Equal()` provides constant-time comparison |
| Weak signed URL tokens | Elevation of Privilege | HMAC-SHA256 with 256-bit secret, 60s expiry, include sandbox_id in payload |
| Migration applied out of order | Tampering | goose sequential numbering + lock table prevents concurrent migration runs |

## Sources

### Primary (HIGH confidence)
- [testcontainers-go Context7 docs] `/websites/golang_testcontainers` -- Postgres snapshot/restore pattern, pgx driver support
- [sqlc Context7 docs] `/websites/sqlc_dev_en` -- pgx/v5 configuration, query annotations
- [goose Context7 docs] `/pressly/goose` -- embedded migrations via embed.FS, slog integration
- STARTER_PLAN.md (repo root) -- Postgres schema, state machine diagram, repo structure, service descriptions
- control-api.openapi.yaml (repo root) -- Full OpenAPI 3.1 spec with all v1 endpoints
- CLAUDE_CODE_KICKOFF.md (repo root) -- Coding conventions, hard rules
- .planning/research/STACK.md -- Verified library versions and rationale
- .planning/research/ARCHITECTURE.md -- Architecture patterns, data flows, anti-patterns
- .planning/research/PITFALLS.md -- 10 critical pitfalls with prevention strategies

### Secondary (MEDIUM confidence)
- Existing bundle-server-spike/Dockerfile.v2 -- baseline for sandbox image structure
- Existing bundle-server-spike/app/package.json -- Expo dependency list for sandbox image

### Tertiary (LOW confidence)
- None -- all claims verified against project artifacts or Context7 docs

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH -- all libraries verified via Context7 docs and project research artifacts
- Architecture: HIGH -- patterns come from verified ARCHITECTURE.md research with source citations
- Pitfalls: HIGH -- all 10 pitfalls in PITFALLS.md verified against official docs and community reports
- Sandbox image: MEDIUM -- based on existing Dockerfile, but <500MB target needs build verification

**Research date:** 2026-04-15
**Valid until:** 2026-05-15 (stable infrastructure stack, 30-day validity)
