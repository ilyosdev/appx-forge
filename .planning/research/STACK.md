# Technology Stack

**Project:** AppX Forge (Custom Container Orchestrator)
**Researched:** 2026-04-15

## Recommended Stack

### Language and Runtime

| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| Go | 1.25.x | All services (control, agent, proxy, cli) | Go 1.25.9 is latest patch (released Apr 7, 2026). Go 1.25 required by goose v3.27.0. Single binary deploys, native Docker SDK, stdlib slog (Go 1.21+), enhanced ServeMux routing (Go 1.22+). Go 1.26 is available but 1.25 has broader ecosystem testing. |

### HTTP Framework

| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| `go-chi/chi/v5` | v5.2.5 | Control plane HTTP router | Lightweight, 100% compatible with `net/http`. Provides middleware chain (RequestID, Logger, Recoverer, Timeout), route groups via `r.Route()`, URL params via `chi.URLParam()`, sub-router mounting via `r.Mount()`. Minimum Go 1.22. |
| stdlib `net/http` | (stdlib) | Agent HTTP server | Agent has roughly 5 endpoints. Stdlib is sufficient and keeps agent binary minimal. |

**Confidence:** HIGH (Context7 docs verified, GitHub releases confirmed v5.2.5 released 2025-02-05)

**Why chi over stdlib ServeMux:** Go 1.22+ ServeMux added `GET /path/{param}` routing, closing the gap significantly. However, the control plane has 15+ endpoints needing auth middleware, request logging, rate limiting, timeout enforcement, and route grouping. Chi provides `r.Use()` for middleware composition, `r.Route("/v1/sandboxes", ...)` for grouping, and custom 405/404 handlers. Without chi, you write 200+ lines of middleware plumbing by hand. Chi handlers are plain `http.HandlerFunc` -- any stdlib middleware works, and testing uses standard `httptest`.

**Why NOT Echo or Gin:** Both wrap `http.Handler` in custom context types (`echo.Context`, `gin.Context`), breaking stdlib compatibility. Chi stays in the `net/http` ecosystem, which matters for debuggability and for a solo dev who wants to use any stdlib-compatible library without adapters.

### Database Driver

| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| `jackc/pgx/v5` | v5.9.x | Postgres driver + connection pool | The de facto Go Postgres driver. Pure Go (no CGO). Connection pooling via `pgxpool`. Supports LISTEN/NOTIFY (useful for future event streaming), COPY protocol, comprehensive type system, prepared statements. |
| `pgxpool` | (included in pgx/v5) | Connection pooling | Built-in pool with configurable max conns, idle time, health checks, jitter. Parse pool config from DATABASE_URL with `pgxpool.ParseConfig()`. |

**Confidence:** HIGH (Context7 docs verified, pkg.go.dev confirmed v5.9.1)

**Key patterns:**
```go
// Pool creation from DATABASE_URL
pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))

// Pool config with overrides
config, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
config.MaxConns = 10
config.MinConns = 2
config.MaxConnLifetime = 30 * time.Minute
config.MaxConnLifetimeJitter = 5 * time.Minute
pool, err := pgxpool.NewWithConfig(ctx, config)
```

### SQL Layer

| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| `sqlc` | v1.30.0 | Type-safe query generation from SQL | Generates Go code from SQL queries. Write SQL, get typed Go functions. Zero runtime overhead (no reflection, no query building). Works with pgx/v5 natively (`sql_package: "pgx/v5"`). Catches SQL errors at compile time via managed database validation. |

**Confidence:** HIGH (Context7 docs verified, sqlc.dev docs confirmed v1.30.0)

**Why NOT GORM:** GORM adds ORM overhead (reflection, query building at runtime), hides SQL behind method chains, makes complex queries painful, and is a poor fit for a control plane that values predictable performance. The project spec uses raw SQL schemas -- GORM would fight this design.

**Why NOT raw pgx queries:** Raw pgx works for 5 queries. The control plane will have 30-50 queries across sandboxes, nodes, events, and routes. Without sqlc, every query requires manual `rows.Scan(&field1, &field2, ...)` -- tedious and error-prone. sqlc generates this boilerplate with type safety.

**Configuration:**
```yaml
# sqlc.yaml
version: "2"
sql:
  - engine: "postgresql"
    queries: "queries/"
    schema: "migrations/"
    gen:
      go:
        package: "store"
        out: "internal/store"
        sql_package: "pgx/v5"
        emit_json_tags: true
        emit_empty_slices: true
```

### Migration Tool

| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| `pressly/goose/v3` | v3.27.0 | Database migrations | Simple, battle-tested, supports SQL and Go migrations. CLI + library mode. Sequential numbering. Up/down migrations. Supports embedded migrations via `embed.FS`. `slog` integration via `WithSlog` option (v3.26.0+). Postgres locking support built-in. |

**Confidence:** HIGH (GitHub releases verified, v3.27.0 released 2025-02-22)

**Why NOT Atlas:** Atlas is more powerful (declarative schema diffing, automatic migration planning) but adds complexity and a SaaS dependency for its managed database feature. For a solo-dev project with a simple schema (3-4 tables), goose sequential SQL files are easier to reason about. Atlas shines at 50+ tables with team coordination.

**Why NOT golang-migrate:** golang-migrate enters a "dirty" state on partial failure with no automatic recovery. It does not automatically wrap individual migration files in transactions, so partial failures leave the database inconsistent. Goose handles this better and also provides library-mode for embedding migrations in the binary.

**Note:** goose v3.27.0 requires Go 1.25 minimum. This is why Go 1.25 is recommended over 1.24.

### Docker SDK

| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| `github.com/moby/moby/client` | v27.x | Agent: Docker Engine API interaction | Official Docker Go client for container lifecycle management. ContainerCreate, ContainerStart, ContainerStop, ContainerRemove, Events stream. Used by Docker CLI itself. |

**Confidence:** MEDIUM -- import path complications require careful handling

**CRITICAL GOTCHAS:**

1. **Import path confusion.** Three import paths exist:
   - `github.com/docker/docker/client` -- legacy path, still works via module redirect
   - `github.com/moby/moby/client` -- canonical Moby project path (recommended)
   - `github.com/moby/docker/client` -- newer modular path (v28.5.2 on pkg.go.dev, published Nov 2025)
   
   **Recommendation:** Use `github.com/moby/moby/client` pinned to v27.x. The `moby/docker` path is newer but less battle-tested. The `docker/docker` legacy path redirects to `moby/moby` anyway.

2. **v28 lazyregexp breakage.** Docker v28 moved an internal package (`lazyregexp`) to Go's `internal/` mechanism, which prevents external consumers from importing the client package. If you hit import errors with v28+, pin to v27.x.

3. **API version negotiation.** Always use `client.WithAPIVersionNegotiation()` when creating the client to handle version mismatches between SDK and Docker daemon on the host.

4. **docker/go-sdk (WIP).** Docker has a new high-level SDK at `github.com/docker/go-sdk` with a cleaner API (functional options like `container.Run(ctx, container.WithImage("nginx"))`), but it is WIP with no v1.0 release. Do NOT use for production. Monitor for v1.0.

**Pattern:**
```go
cli, err := client.NewClientWithOpts(
    client.FromEnv,
    client.WithAPIVersionNegotiation(),
)
defer cli.Close()
```

### Caddy Integration

| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| Caddy | v2.11.2 | L7 reverse proxy with dynamic config | Admin API for route management, auto TLS, HTTP/2 + WebSocket proxying. Config reload in roughly 50ms, no connection drops. |
| Raw `net/http` client | (stdlib) | Caddy Admin API interaction | Caddy Admin API is simple REST: GET/POST/PUT/DELETE on `/config/...`. A thin wrapper (roughly 100 lines) around net/http is cleaner than depending on a small community library. |

**Confidence:** HIGH (Caddy docs verified, v2.11.2 released 2026-03-06)

**Why NOT a Caddy Go client library:** Two community libraries exist (`raghavyuva/caddygo`, `multisig-labs/caddyapi`) but both have low adoption. The Caddy Admin API surface is 5 endpoints -- writing a thin wrapper eliminates a dependency risk for minimal effort.

**Key API endpoints:**
- `POST /load` -- set entire Caddy config
- `GET /config/[path]` -- read config at path
- `POST /config/[path]` -- set or replace config at path
- `PUT /config/[path]` -- create new config object
- `DELETE /config/[path]` -- delete config value
- Use `@id` fields in JSON config for stable path references

### Tailscale SDK

| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| `tailscale.com/client/tailscale/v2` | latest | Tailscale control plane API client | Official v2 Go client. Supports API keys, OAuth, and identity federation auth. Replaces deprecated v1 client at `github.com/tailscale/tailscale-client-go`. Requires Go 1.24+. |
| Tailscale CLI (via `os/exec`) | (system binary) | Node mesh management on bootstrap | For node setup operations (join/leave tailnet), shelling out to `tailscale` CLI in Ansible is simpler than programmatic API calls. |

**Confidence:** MEDIUM (v2 client confirmed on pkg.go.dev, import path verified, but limited community usage examples)

**Practical note:** For Forge v1, Tailscale interaction is minimal. Control plane verifies node Tailscale IPs during registration. Ansible handles `tailscale up` during node bootstrap. The Go SDK is needed mainly for listing devices and checking connectivity status. If this proves heavyweight, raw HTTP calls to `https://api.tailscale.com/api/v2/` with an API key work fine.

### CLI Framework

| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| `spf13/cobra` | v1.10.2 | forge-cli command structure | Industry standard: used by kubectl, docker, hugo, gh. Subcommand-based (`forge node list`, `forge sandbox inspect`). Auto-generated help, shell completions, man pages. 195K+ importers. |

**Confidence:** HIGH (GitHub releases verified, v1.10.2 released 2024-12-04)

**Why NOT urfave/cli:** Both are viable. Cobra has wider adoption, better subcommand nesting (important for `forge node add` vs `forge sandbox list` vs `forge routes verify`), and built-in completion generation for bash/zsh/fish. The `cobra-cli` scaffolding tool saves setup time.

### Config Management

| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| `kelseyhightower/envconfig` | v1.4.0 | Environment variable parsing | Simple, zero-dependency, struct-tag-based env parsing. Perfect for 12-factor apps. No config files needed (project spec mandates env vars only). Supports required fields, defaults, and nested structs. |

**Confidence:** HIGH (GitHub confirmed, stable since v1.4.0)

**Why NOT Viper:** Viper is a kitchen-sink config library (YAML, TOML, JSON, env, remote config, live reload). Forge uses env vars only -- Viper pulls in 10x the dependency surface for zero benefit. `envconfig` is roughly 500 lines with no transitive deps.

**Pattern:**
```go
type Config struct {
    Port           int    `envconfig:"PORT" default:"8080"`
    DatabaseURL    string `envconfig:"DATABASE_URL" required:"true"`
    LogLevel       string `envconfig:"LOG_LEVEL" default:"info"`
    HeartbeatSec   int    `envconfig:"HEARTBEAT_INTERVAL_SEC" default:"15"`
}

var cfg Config
envconfig.MustProcess("FORGE", &cfg)
// Reads: FORGE_PORT, FORGE_DATABASE_URL, FORGE_LOG_LEVEL, etc.
```

### Structured Logging

| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| `log/slog` | (stdlib, Go 1.21+) | Structured logging | Standard library since Go 1.21. JSON output for production, text for dev. Zero dependency. Context-aware via `slog.With()`. Handler interface for custom formatting. |

**Confidence:** HIGH (stdlib, extensively documented on go.dev/blog/slog)

**Pattern:**
```go
// Production: JSON handler
logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
    Level: slog.LevelInfo,
}))
slog.SetDefault(logger)

// Dev: text handler
logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
    Level: slog.LevelDebug,
}))

// Contextual logging -- always include sandbox_id, node_id when relevant
log := slog.With("sandbox_id", sandboxID, "node_id", nodeID)
log.Info("sandbox started", "port", port, "image", image)
log.Error("container exited", "exit_code", exitCode, "err", err)
```

**Best practices:**
- Switch handler by environment: JSON for production, text for dev
- Pass `*slog.Logger` into services (dependency injection), not global
- Use `sloglint` linter to enforce consistent key naming
- Message describes what happened, attributes describe context

### Testing

| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| stdlib `testing` | (stdlib) | Unit + integration tests | Standard Go testing. Table-driven tests. No assertion library needed. |
| `testcontainers-go` | v0.42.0 | Postgres integration tests | Spins up real Postgres in Docker for tests. Snapshot/restore for test isolation. Released Apr 2025. |
| `testcontainers-go/modules/postgres` | (included) | Postgres test container | Pre-configured with connection string generation, init scripts, wait strategies, and snapshot support. |

**Confidence:** HIGH (Context7 docs verified, v0.42.0 released 2025-04-09)

**Postgres test pattern:**
```go
func TestSandboxStore(t *testing.T) {
    ctx := context.Background()
    ctr, err := postgres.Run(ctx, "postgres:16-alpine",
        postgres.WithDatabase("forge_test"),
        postgres.WithUsername("test"),
        postgres.WithPassword("test"),
        postgres.BasicWaitStrategies(),
        postgres.WithSQLDriver("pgx"),
    )
    testcontainers.CleanupContainer(t, ctr)
    require.NoError(t, err)

    // Run migrations
    dbURL, _ := ctr.ConnectionString(ctx, "sslmode=disable")
    // ... run goose up against dbURL ...

    // Snapshot for test isolation
    err = ctr.Snapshot(ctx)
    require.NoError(t, err)

    t.Run("create sandbox", func(t *testing.T) {
        t.Cleanup(func() { ctr.Restore(ctx) })
        // ... test code ...
    })
}
```

**Docker mock pattern for agent tests:**
```go
// Define interface matching subset of client.ContainerAPIClient
type DockerClient interface {
    ContainerCreate(ctx context.Context, opts client.ContainerCreateOptions) (client.ContainerCreateResult, error)
    ContainerStart(ctx context.Context, id string, opts client.ContainerStartOptions) error
    ContainerStop(ctx context.Context, id string, opts client.ContainerStopOptions) error
    ContainerRemove(ctx context.Context, id string, opts client.ContainerRemoveOptions) error
    Events(ctx context.Context, opts events.ListOptions) (<-chan events.Message, <-chan error)
}
// Implement mock struct for testing -- no mock library needed
```

### Supporting Libraries

| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `google/uuid` | v1.6.0 | UUID generation | All ID fields (sandbox, node, event) |
| `prometheus/client_golang` | v1.20.x | Prometheus metrics | /v1/metrics endpoint, request duration histograms, sandbox counts |
| `go-chi/httprate` | v0.14.x | HTTP rate limiting | Control plane API rate limiting, Context7 verified |
| `samber/slog-chi` | latest | Chi middleware for slog | HTTP request logging via slog instead of chi default logger |
| `pashagolub/pgxmock` | latest | pgx mock driver | Unit testing store layer without real Postgres |

### Infrastructure

| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| Postgres | 16.x | State storage (managed: Neon or Supabase) | Source of truth for nodes, sandboxes, events, routes. Advisory locks for v1 HA. LISTEN/NOTIFY for future event streaming. |
| Caddy | v2.11.2 | L7 reverse proxy | Dynamic Admin API, auto TLS, WebSocket support. Routes *.myappx.live to correct node:port. |
| Tailscale | latest | Cross-node mesh networking | Encrypted, NAT-traversing, zero-config. DERP relay fallback if UDP blocked. |
| Docker Engine | 27.x (NOT 28.x or 29.x) | Container runtime | v27 is stable. v28+ has Go SDK import path breakage. v29 has the Swarm 60s restart bug. Pin Docker Engine to 27.5.x on all nodes. |
| Cloudflare | N/A | Edge DNS + CDN + SSL | Origin certificates (15-year validity), wildcard DNS for *.myappx.live. |

## Alternatives Considered

| Category | Recommended | Alternative | Why Not |
|----------|-------------|-------------|---------|
| HTTP Router | chi v5 | stdlib ServeMux | Lacks middleware composition, route groups, custom 405 handlers |
| HTTP Router | chi v5 | Echo/Gin | Non-stdlib handler signatures, custom context objects break compatibility |
| SQL Layer | sqlc | GORM | ORM overhead, hides SQL, reflection-based, poor for complex queries |
| SQL Layer | sqlc | Raw pgx | Manual Scan() for 30-50 queries is tedious and error-prone |
| Migrations | goose v3 | Atlas | Overkill for 3-4 tables, SaaS dependency for managed DB feature |
| Migrations | goose v3 | golang-migrate | "Dirty" state on failure, no automatic recovery, no library mode |
| Config | envconfig | Viper | Massive dependency for env-only config, zero benefit |
| CLI | Cobra | urfave/cli | Less subcommand nesting, smaller ecosystem |
| Logging | slog | zerolog/zap | Third-party deps for what stdlib does well since Go 1.21 |
| Docker SDK | moby/moby/client | docker/go-sdk | WIP, no v1.0 release, not production-ready |
| Caddy Client | Raw net/http | caddygo/caddyapi | Community libraries with low adoption for a 5-endpoint API |

## Installation

```bash
# Core dependencies (in each Go module directory)
go get github.com/go-chi/chi/v5@v5.2.5
go get github.com/jackc/pgx/v5@latest
go get github.com/kelseyhightower/envconfig@v1.4.0
go get github.com/spf13/cobra@v1.10.2
go get github.com/google/uuid@v1.6.0
go get github.com/prometheus/client_golang@latest

# Docker SDK (agent only)
go get github.com/moby/moby@v27.5.1

# Tailscale client (control plane only, if needed)
go get tailscale.com/client/tailscale/v2@latest

# Testing
go get github.com/testcontainers/testcontainers-go@v0.42.0
go get github.com/testcontainers/testcontainers-go/modules/postgres@v0.42.0

# CLI tools (installed globally)
go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0
go install github.com/pressly/goose/v3/cmd/goose@v3.27.0

# Linting
go install github.com/go-simpler/sloglint/cmd/sloglint@latest
```

## Go Module Structure

```
appx-forge/
  go.work              # workspace linking all modules
  control/go.mod       # chi, pgx, sqlc-generated, envconfig, prometheus
  agent/go.mod         # moby/moby/client, envconfig (minimal deps)
  cli/go.mod           # cobra, envconfig
  shared-go/go.mod     # models only, zero external deps
```

Each module has its own go.mod to keep dependency trees isolated. Agent binary should be roughly 15-20MB (Docker SDK + stdlib). Control plane roughly 30MB (pgx + chi + prometheus).

## Version Pinning Strategy

| Dependency | Pin Strategy | Rationale |
|------------|-------------|-----------|
| Go | 1.25.x exact | Required by goose v3.27.0, actively supported |
| chi | v5.2.5 exact | Stable, infrequent releases |
| pgx | v5.9.x minor | Active development, minor bumps are safe |
| sqlc | v1.30.0 exact | Code generator -- pin to avoid unexpected output changes |
| goose | v3.27.0 exact | Migration tool -- pin strictly |
| cobra | v1.10.2 exact | CLI framework, stable |
| Docker SDK | v27.x pinned | Avoid v28+ import path issues, avoid v29 Swarm bugs |
| testcontainers-go | v0.42.x minor | Test-only dependency, minor bumps fine |
| envconfig | v1.4.0 exact | Stable, no new releases expected |

## Sources

- [go-chi/chi GitHub](https://github.com/go-chi/chi) -- v5.2.5 released 2025-02-05, Context7 docs verified
- [jackc/pgx pkg.go.dev](https://pkg.go.dev/github.com/jackc/pgx/v5) -- v5.9.x, Context7 docs verified
- [sqlc.dev documentation](https://docs.sqlc.dev/) -- v1.30.0, Context7 docs verified
- [pressly/goose GitHub releases](https://github.com/pressly/goose/releases) -- v3.27.0 released 2025-02-22
- [moby/moby/client pkg.go.dev](https://pkg.go.dev/github.com/moby/moby/client) -- Context7 docs verified
- [Docker Engine v27 release notes](https://docs.docker.com/engine/release-notes/27/)
- [Docker SDK import issues](https://github.com/moby/moby/issues/49712) -- v28 lazyregexp breakage
- [docker/go-sdk GitHub](https://github.com/docker/go-sdk) -- WIP, no v1.0
- [Caddy API documentation](https://caddyserver.com/docs/api) -- v2.11.2 released 2026-03-06
- [tailscale-client-go-v2 GitHub](https://github.com/tailscale/tailscale-client-go-v2) -- import path verified
- [spf13/cobra GitHub releases](https://github.com/spf13/cobra/releases) -- v1.10.2 released 2024-12-04
- [kelseyhightower/envconfig GitHub](https://github.com/kelseyhightower/envconfig) -- v1.4.0
- [testcontainers-go GitHub releases](https://github.com/testcontainers/testcontainers-go/releases) -- v0.42.0 released 2025-04-09
- [Go release history](https://go.dev/doc/devel/release) -- Go 1.26.2 latest, 1.25.9 latest for 1.25 line
- [slog official blog post](https://go.dev/blog/slog)
- [Chi vs ServeMux comparison](https://www.calhoun.io/go-servemux-vs-chi/)
- [Goose vs Atlas vs golang-migrate comparison](https://dev.to/shrsv/best-database-migration-tools-for-golang-ajf)
