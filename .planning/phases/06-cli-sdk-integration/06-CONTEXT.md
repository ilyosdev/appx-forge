# Phase 6: CLI, SDK & appx-api Integration - Context

**Gathered:** 2026-04-16
**Status:** Ready for planning
**Mode:** Auto-generated (code phase — discuss skipped)

<domain>
## Phase Boundary

Ops can manage the fleet via CLI, appx-api creates sandboxes via the TypeScript SDK, and Railover is fully replaced. Covers: forge-cli with cobra (13 commands), forge-sdk-ts TypeScript SDK (9 methods), appx-api migration from Railover to ForgeClient, Ansible playbook for node bootstrapping, and runbooks.

</domain>

<decisions>
## Implementation Decisions

### Claude's Discretion
All choices at Claude's discretion. Use docs/contracts/control-api.openapi.yaml as source of truth for CLI and SDK.

**TDD mandate:** Write tests FIRST for Go CLI and TypeScript SDK.

### From Prior Phases
- Control plane API is complete and tested (Phase 3)
- All endpoints match control-api.openapi.yaml
- Bearer token auth required on all endpoints except /healthz and /metrics
- Agent protocol, file push protocol, proxy routing all documented

</decisions>

<code_context>
## Existing Code Insights

### Reusable Assets
- docs/contracts/control-api.openapi.yaml — Full API spec for CLI and SDK generation
- control/internal/api/ — All HTTP handlers (the server CLI and SDK talk to)
- shared-go/models/ — Go types that CLI can import directly
- deploy/ — Existing scripts (verify-tailscale, verify-docker, setup-inotify, forge-agent.service)

### Integration Points
- CLI → control plane HTTP API (same as agent, but with admin bearer token)
- SDK → control plane HTTP API (TypeScript client for appx-api NestJS backend)
- appx-api → ForgeClient replaces RailoverService calls
- Ansible → installs Docker, Tailscale, agent binary, systemd service on nodes

</code_context>

<specifics>
## Specific Ideas

- cli/cmd/forge/main.go — cobra root command
- cli/cmd/forge/node.go — node list/add/drain/remove
- cli/cmd/forge/sandbox.go — sandbox list/inspect/logs/restart/destroy
- cli/cmd/forge/routes.go — routes list/verify
- cli/cmd/forge/events.go — events command
- sdk-ts/src/client.ts — ForgeClient class
- sdk-ts/src/types.ts — Generated from OpenAPI
- deploy/ansible/ — Node bootstrap playbook
- docs/runbooks/ — add-node, recover-sandbox, debug-container

</specifics>

<deferred>
## Deferred Ideas

- appx-api integration (INT-01..04) touches the text2design repo — defer to manual integration after Forge is deployed. Plan should generate the SDK and document the migration steps, but NOT modify the appx-api codebase directly (different repo).

</deferred>
