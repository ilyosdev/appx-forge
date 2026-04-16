---
phase: 06-cli-sdk-integration
plan: 02
subsystem: sdk
tags: [typescript, sdk, fetch, nock, jest, openapi, rfc7807]

requires:
  - phase: 01-contracts-bootstrap
    provides: "OpenAPI spec defining all control plane endpoints"
provides:
  - "@appx/forge-sdk npm package with ForgeClient class"
  - "Typed SDK for all sandbox/node/route operations"
  - "Error hierarchy mapping HTTP status to typed exceptions"
affects: [06-cli-sdk-integration, appx-api-integration]

tech-stack:
  added: [typescript, jest, ts-jest, nock]
  patterns: ["native fetch (Node 18+) for HTTP", "RFC 7807 error mapping", "307 redirect following for file push"]

key-files:
  created:
    - sdk-ts/package.json
    - sdk-ts/tsconfig.json
    - sdk-ts/jest.config.ts
    - sdk-ts/src/types.ts
    - sdk-ts/src/errors.ts
    - sdk-ts/src/client.ts
    - sdk-ts/src/index.ts
    - sdk-ts/tests/client.test.ts
    - sdk-ts/.gitignore
  modified: []

key-decisions:
  - "Native fetch over axios -- zero HTTP dependencies, Node 18+ built-in"
  - "Hand-written types over openapi-typescript-codegen -- 6 types don't justify a codegen dependency"
  - "buildQuery uses as unknown cast for interface-to-index-signature -- standard TS pattern"
  - "isolatedModules: true in tsconfig for ts-jest NodeNext compatibility"

patterns-established:
  - "SDK method pattern: namespace object (sandboxes/nodes/routes) with async arrow functions"
  - "Error hierarchy: ForgeError base -> ForgeNotFoundError/ForgeConflictError/ForgeServiceError"
  - "request<T>/requestRaw pattern: JSON body vs void-returning endpoints"
  - "307 redirect: manual redirect mode + re-send body to Location header"

requirements-completed: [SDK-01, SDK-02, SDK-03, SDK-04, SDK-05, SDK-06, SDK-07, SDK-08, SDK-09, INT-01, INT-02, INT-03, INT-04]

duration: 6min
completed: 2026-04-16
---

# Phase 06 Plan 02: ForgeClient TypeScript SDK Summary

**Native-fetch TypeScript SDK with 21 nock-tested methods covering all sandbox/node/route operations, typed RFC 7807 errors, and 307 redirect following for file push**

## Performance

- **Duration:** 6 min
- **Started:** 2026-04-16T00:57:36Z
- **Completed:** 2026-04-16T01:04:27Z
- **Tasks:** 2 (RED + GREEN TDD phases)
- **Files created:** 10

## Accomplishments
- ForgeClient with 7 sandbox methods (create, get, list, destroy, restart, pushFiles, logs), nodes.list, routes.list, healthcheck
- Full test suite with 21 tests using nock HTTP mocking -- all pass
- RFC 7807 error parsing with typed error hierarchy (404/409/503 mapped to specific classes)
- 307 redirect following for pushFiles (control plane -> agent endpoint with signed URL)
- Zero external HTTP dependencies -- uses Node 18+ native fetch
- TypeScript strict mode compiles with zero errors

## TDD Gate Compliance

| Gate | Commit | Status |
|------|--------|--------|
| RED | `b6dd1fd` | Pass -- 21 tests fail against stub implementation |
| GREEN | `1af940e` | Pass -- all 21 tests pass after implementation |
| REFACTOR | -- | Skipped -- code clean, no behavior-preserving improvements needed |

## Task Commits

Each task was committed atomically:

1. **RED: Failing tests for ForgeClient** - `b6dd1fd` (test)
2. **GREEN: Implement ForgeClient SDK** - `1af940e` (feat)

## Files Created/Modified
- `sdk-ts/package.json` - @appx/forge-sdk package definition
- `sdk-ts/tsconfig.json` - ES2022/NodeNext strict TypeScript config
- `sdk-ts/jest.config.ts` - ts-jest preset for tests
- `sdk-ts/.gitignore` - node_modules, dist, *.tgz
- `sdk-ts/src/types.ts` - All OpenAPI schema types (Sandbox, Node, Route, etc.)
- `sdk-ts/src/errors.ts` - ForgeError hierarchy for RFC 7807 responses
- `sdk-ts/src/client.ts` - ForgeClient class with all API methods
- `sdk-ts/src/index.ts` - Barrel export of client, types, and errors
- `sdk-ts/tests/client.test.ts` - 21 tests with nock HTTP mocking

## Decisions Made
- Used native fetch over axios -- zero HTTP dependencies, Node 18+ built-in
- Hand-wrote types instead of codegen -- 6 types don't justify openapi-typescript-codegen
- Added isolatedModules: true for ts-jest compatibility with NodeNext module resolution
- pushFiles uses redirect: 'manual' then re-sends body to Location -- per filepush-protocol.md

## Deviations from Plan

None -- plan executed exactly as written.

## Issues Encountered
- ts-jest warns about hybrid module kind without isolatedModules -- fixed by adding isolatedModules: true to tsconfig
- SandboxListFilters interface not assignable to index signature -- fixed with standard `as unknown` cast pattern

## Threat Mitigations Applied

| Threat | Mitigation | Status |
|--------|-----------|--------|
| T-06-05 (apiKey disclosure) | apiKey stored in private field, never logged or included in error messages | Applied |
| T-06-06 (pushFiles redirect tampering) | SDK follows redirect as-is; URL is HMAC-signed by control plane | Accepted |
| T-06-07 (credential spoofing) | Always Authorization: Bearer header, never URL query params | Applied |

## Next Phase Readiness
- SDK ready for appx-api integration (plan 06-03)
- All types exported for consumer use
- Package publishable via npm (build script configured)

## Self-Check: PASSED

- All 9 created files exist on disk
- RED commit b6dd1fd verified in git log
- GREEN commit 1af940e verified in git log
- 21/21 tests pass, TypeScript strict mode zero errors

---
*Phase: 06-cli-sdk-integration*
*Completed: 2026-04-16*
