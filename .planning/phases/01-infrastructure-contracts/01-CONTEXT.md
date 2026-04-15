# Phase 1: Infrastructure & Contracts - Context

**Gathered:** 2026-04-15
**Status:** Ready for planning
**Mode:** Auto-generated (infrastructure phase — discuss skipped)

<domain>
## Phase Boundary

All infrastructure assumptions validated and every protocol/schema documented so downstream phases can build against stable contracts. This includes: Tailscale connectivity verification, Docker Engine setup, Cloudflare DNS, OpenAPI spec, Postgres schema with migrations, sandbox state machine with compare-and-swap, and the sandbox Docker image.

</domain>

<decisions>
## Implementation Decisions

### Claude's Discretion
All implementation choices are at Claude's discretion — pure infrastructure phase. Use ROADMAP phase goal, success criteria, and the STARTER_PLAN.md + control-api.openapi.yaml as the source of truth.

**TDD mandate from user:** Write concrete tests FIRST for all Go code (state machine, migrations). Tests must pass before moving to next phase. Use testcontainers-go for Postgres integration tests.

</decisions>

<code_context>
## Existing Code Insights

### Reusable Assets
- `control-api.openapi.yaml` — Full OpenAPI 3.1 spec already exists at repo root
- `STARTER_PLAN.md` — Contains Postgres schema, state machine diagram, repo structure
- `CLAUDE_CODE_KICKOFF.md` — Coding conventions (slog, pgx/v5, sqlc, chi, envconfig)

### Established Patterns
- Go workspace layout: `control/`, `agent/`, `proxy/`, `cli/`, `sdk-ts/`, `shared-go/`
- Postgres: pgx/v5 + sqlc for type-safe queries
- Migrations: goose (sequentially numbered, up + down SQL)
- Tests: `_test.go` next to source, testcontainers-go for Postgres

### Integration Points
- OpenAPI spec gates: agent protocol, SDK, CLI
- Postgres schema gates: control plane, agent state reporting
- State machine gates: all lifecycle operations

</code_context>

<specifics>
## Specific Ideas

- Move existing `control-api.openapi.yaml` to `docs/contracts/`
- Move `STARTER_PLAN.md` to `docs/`
- Write agent-protocol.md, filepush-protocol.md, proxy-routing.md contracts
- Postgres schema from STARTER_PLAN.md (nodes, sandboxes, events tables)
- State machine with compare-and-swap: `UPDATE sandboxes SET state = $new WHERE id = $id AND state = $expected`
- Sandbox image: trim current bundle-server image, target <500MB

</specifics>

<deferred>
## Deferred Ideas

None — infrastructure phase.

</deferred>
