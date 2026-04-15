<!-- GSD:project-start source:PROJECT.md -->
## Project

**AppX Forge**

A custom container orchestrator built in Go that replaces Railover/CapRover/Docker Swarm for running AppX sandboxes (React Native + Metro) across a fleet of Contabo VDS nodes. Five services — control plane, per-node agent, Caddy-based proxy, CLI, and TypeScript SDK — coordinated via Postgres as source of truth, Tailscale for cross-node mesh, and Cloudflare for edge routing. No Swarm, no docker.sock mounts, no reconciliation loops.

**Core Value:** Sub-second sandbox claim and ~5s cold start with zero spurious restarts — a sandbox orchestrator simple enough to explain on one whiteboard, reliable enough to never wake you up at night.

### Constraints

- **Stack**: Go 1.23+ for all services, TypeScript for SDK only — locked by team decision
- **No docker.sock mounts**: Agent runs on host as systemd, talks to Docker locally — non-negotiable security boundary
- **No Swarm**: Plain `docker run` via Docker SDK only
- **No reconciliation loop**: State changes only on explicit events (API call, container exit, heartbeat timeout)
- **Contracts first**: OpenAPI spec is sacred — changes require ADR
- **Postgres is truth**: No agent-local state that can't be reconstructed
- **Budget**: Contabo VDS nodes (~$15/mo each), no expensive managed services
- **Solo dev**: One developer, AI-assisted parallel execution via Claude Code + GSD
<!-- GSD:project-end -->

<!-- GSD:stack-start source:research/STACK.md -->
## Technology Stack

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
### Database Driver
| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| `jackc/pgx/v5` | v5.9.x | Postgres driver + connection pool | The de facto Go Postgres driver. Pure Go (no CGO). Connection pooling via `pgxpool`. Supports LISTEN/NOTIFY (useful for future event streaming), COPY protocol, comprehensive type system, prepared statements. |
| `pgxpool` | (included in pgx/v5) | Connection pooling | Built-in pool with configurable max conns, idle time, health checks, jitter. Parse pool config from DATABASE_URL with `pgxpool.ParseConfig()`. |
### SQL Layer
| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| `sqlc` | v1.30.0 | Type-safe query generation from SQL | Generates Go code from SQL queries. Write SQL, get typed Go functions. Zero runtime overhead (no reflection, no query building). Works with pgx/v5 natively (`sql_package: "pgx/v5"`). Catches SQL errors at compile time via managed database validation. |
# sqlc.yaml
### Migration Tool
| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| `pressly/goose/v3` | v3.27.0 | Database migrations | Simple, battle-tested, supports SQL and Go migrations. CLI + library mode. Sequential numbering. Up/down migrations. Supports embedded migrations via `embed.FS`. `slog` integration via `WithSlog` option (v3.26.0+). Postgres locking support built-in. |
### Docker SDK
| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| `github.com/moby/moby/client` | v27.x | Agent: Docker Engine API interaction | Official Docker Go client for container lifecycle management. ContainerCreate, ContainerStart, ContainerStop, ContainerRemove, Events stream. Used by Docker CLI itself. |
### Caddy Integration
| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| Caddy | v2.11.2 | L7 reverse proxy with dynamic config | Admin API for route management, auto TLS, HTTP/2 + WebSocket proxying. Config reload in roughly 50ms, no connection drops. |
| Raw `net/http` client | (stdlib) | Caddy Admin API interaction | Caddy Admin API is simple REST: GET/POST/PUT/DELETE on `/config/...`. A thin wrapper (roughly 100 lines) around net/http is cleaner than depending on a small community library. |
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
### CLI Framework
| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| `spf13/cobra` | v1.10.2 | forge-cli command structure | Industry standard: used by kubectl, docker, hugo, gh. Subcommand-based (`forge node list`, `forge sandbox inspect`). Auto-generated help, shell completions, man pages. 195K+ importers. |
### Config Management
| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| `kelseyhightower/envconfig` | v1.4.0 | Environment variable parsing | Simple, zero-dependency, struct-tag-based env parsing. Perfect for 12-factor apps. No config files needed (project spec mandates env vars only). Supports required fields, defaults, and nested structs. |
### Structured Logging
| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| `log/slog` | (stdlib, Go 1.21+) | Structured logging | Standard library since Go 1.21. JSON output for production, text for dev. Zero dependency. Context-aware via `slog.With()`. Handler interface for custom formatting. |
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
# Core dependencies (in each Go module directory)
# Docker SDK (agent only)
# Tailscale client (control plane only, if needed)
# Testing
# CLI tools (installed globally)
# Linting
## Go Module Structure
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
<!-- GSD:stack-end -->

<!-- GSD:conventions-start source:CONVENTIONS.md -->
## Conventions

Conventions not yet established. Will populate as patterns emerge during development.
<!-- GSD:conventions-end -->

<!-- GSD:architecture-start source:ARCHITECTURE.md -->
## Architecture

Architecture not yet mapped. Follow existing patterns found in the codebase.
<!-- GSD:architecture-end -->

<!-- GSD:skills-start source:skills/ -->
## Project Skills

No project skills found. Add skills to any of: `.claude/skills/`, `.agents/skills/`, `.cursor/skills/`, or `.github/skills/` with a `SKILL.md` index file.
<!-- GSD:skills-end -->

<!-- GSD:workflow-start source:GSD defaults -->
## GSD Workflow Enforcement

Before using Edit, Write, or other file-changing tools, start work through a GSD command so planning artifacts and execution context stay in sync.

Use these entry points:
- `/gsd-quick` for small fixes, doc updates, and ad-hoc tasks
- `/gsd-debug` for investigation and bug fixing
- `/gsd-execute-phase` for planned phase work

Do not make direct repo edits outside a GSD workflow unless the user explicitly asks to bypass it.
<!-- GSD:workflow-end -->



<!-- GSD:profile-start -->
## Developer Profile

> Profile not yet configured. Run `/gsd-profile-user` to generate your developer profile.
> This section is managed by `generate-claude-profile` -- do not edit manually.
<!-- GSD:profile-end -->
