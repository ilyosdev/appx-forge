---
phase: 01-infrastructure-contracts
plan: 03
subsystem: contracts
tags: [openapi, protocol, caddy, hmac, agent, proxy, documentation]

# Dependency graph
requires:
  - phase: none
    provides: standalone contract documents
provides:
  - OpenAPI 3.1 spec with 16 operationIds at docs/contracts/control-api.openapi.yaml
  - Agent protocol document covering registration, heartbeat, long-poll, commands, events
  - File push protocol document with HMAC-SHA256 signed URL redirect flow
  - Proxy routing protocol document with Caddy Admin API patterns and drift detection
affects: [agent, control-plane, proxy, sdk, cli]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "RFC 7807 application/problem+json error responses in OpenAPI spec"
    - "HMAC-SHA256 signed URL with 60s expiry for file push redirect"
    - "Caddy @id-based route management for stable path access"
    - "500ms debounce batching to prevent WebSocket drops on route churn"
    - "60s drift detection cycle comparing Caddy state to Postgres"

key-files:
  created:
    - docs/contracts/control-api.openapi.yaml
    - docs/contracts/agent-protocol.md
    - docs/contracts/filepush-protocol.md
    - docs/contracts/proxy-routing.md
  modified: []

key-decisions:
  - "Error responses use RFC 7807 application/problem+json with type/title/status/detail/instance fields"
  - "Agent registration endpoint is unauthenticated (security: []) since it provides the token"
  - "File push uses 60s HMAC-SHA256 signed URLs with full path+query canonical message"

patterns-established:
  - "Contract documents versioned at 0.1.0, live in docs/contracts/"
  - "OpenAPI spec is the single source of truth for HTTP API; changes require ADR"
  - "Protocol docs use consistent structure: Overview, Flow, Format, Error Handling, Security"

requirements-completed: [CNTR-01, CNTR-02, CNTR-03, CNTR-04]

# Metrics
duration: 6min
completed: 2026-04-15
---

# Phase 01 Plan 03: Contract Documents Summary

**OpenAPI 3.1 spec with 16 endpoints and 3 protocol documents (agent, file-push, proxy routing) defining all inter-service communication contracts**

## Performance

- **Duration:** 6 min
- **Started:** 2026-04-15T19:24:36Z
- **Completed:** 2026-04-15T19:31:07Z
- **Tasks:** 2
- **Files modified:** 4

## Accomplishments
- Moved OpenAPI spec to canonical location (docs/contracts/) and finalized with /metrics endpoint, RFC 7807 error schema, and 16 operationIds
- Created agent protocol document covering registration, heartbeat, long-poll commands (5 types), command ack, event reporting, and reconnection
- Created file push protocol document with 307 redirect flow, HMAC-SHA256 signed URLs, JSON/tar formats, and security considerations
- Created proxy routing protocol document with Caddy Admin API patterns, @id route management, 500ms debounce batching, drift detection, and Cloudflare DNS config

## Task Commits

Each task was committed atomically:

1. **Task 1: Move and finalize the OpenAPI 3.1 spec** - `4812c97` (docs)
2. **Task 2: Create agent, file-push, and proxy routing protocol documents** - `0676e60` (docs)

## Files Created/Modified
- `docs/contracts/control-api.openapi.yaml` - Full OpenAPI 3.1 spec with 16 operationIds, RFC 7807 errors, /metrics endpoint
- `docs/contracts/agent-protocol.md` - Agent communication protocol: registration, heartbeat, long-poll, 5 command types, event reporting
- `docs/contracts/filepush-protocol.md` - File push with 307 redirect, HMAC-SHA256 signed URLs, JSON/tar formats
- `docs/contracts/proxy-routing.md` - Caddy Admin API route management, batch updates, drift detection, Cloudflare DNS

## Decisions Made
- Error responses standardized on RFC 7807 `application/problem+json` with `type`, `title`, `status`, `detail`, `instance` fields (replaces the original `error`/`code`/`details` schema)
- Agent registration endpoint (`/v1/nodes/register`) marked with `security: []` since it is the mechanism by which agents obtain their token
- File push HMAC signature uses full URL path + query canonical message (`/v1/sandboxes/{id}/files?sandbox_id={id}&expires={ts}`) rather than just sandbox ID for stronger security

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 2 - Missing Critical] Added RFC 7807 error schema to additional endpoints**
- **Found during:** Task 1 (OpenAPI spec review)
- **Issue:** Several endpoints (restart, files, logs, heartbeat) returned 404 without a response body schema
- **Fix:** Added `application/problem+json` error responses with `$ref: Error` to all 4xx/5xx responses
- **Files modified:** docs/contracts/control-api.openapi.yaml
- **Verification:** All error responses now reference the Error schema
- **Committed in:** 4812c97 (Task 1 commit)

---

**Total deviations:** 1 auto-fixed (1 missing critical)
**Impact on plan:** Ensures consistent error handling across all API endpoints. No scope creep.

## Issues Encountered
None

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- All 4 contract documents are in place for downstream phases to build against
- Agent (Phase 2) can implement against agent-protocol.md and the OpenAPI spec
- Control plane (Phase 3) can implement the OpenAPI endpoints and file push redirect
- Proxy integration (Phase 4) can implement Caddy routing per proxy-routing.md
- SDK (Phase 6) can generate TypeScript types from the OpenAPI spec

## Self-Check: PASSED

- All 4 contract files exist in docs/contracts/
- Both task commits verified (4812c97, 0676e60)
- SUMMARY.md exists

---
*Phase: 01-infrastructure-contracts*
*Completed: 2026-04-15*
