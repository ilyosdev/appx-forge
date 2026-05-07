---
phase: 01-infrastructure-contracts
plan: 05
subsystem: infra
tags: [docker, expo, metro, react-native, sandbox, dockerfile]

# Dependency graph
requires: []
provides:
  - Sandbox Docker image definition (Dockerfile + app template)
  - Pre-installed Expo SDK 54 dependency manifest
  - Smoke test script for cold start verification
  - Sandbox image documentation (env vars, ports, bind mounts)
affects: [agent, control, sdk-ts, deploy]

# Tech tracking
tech-stack:
  added: [node:20-slim, expo-sdk-54, react-native-0.81.5, metro]
  patterns: [shared-deps-layer, bind-mount-code-dir, non-root-container-user]

key-files:
  created:
    - sandbox-image/Dockerfile
    - sandbox-image/app/package.json
    - sandbox-image/app/entry.js
    - sandbox-image/app/App.tsx
    - sandbox-image/app/tsconfig.json
    - sandbox-image/app/babel.config.js
    - sandbox-image/app/metro.config.js
    - sandbox-image/smoke-test.sh
    - sandbox-image/README.md
  modified: []

key-decisions:
  - "npm install without --production flag -- babel-preset-expo and typescript needed at runtime for Metro bundling"
  - "Shared deps at /opt/expo-shared-deps with symlink into app -- separates heavy layer from app template for Docker cache efficiency"
  - "UID 1000 appuser -- agent must chown bind-mount dirs to match"

patterns-established:
  - "Shared deps layer: heavy node_modules at /opt/expo-shared-deps, symlinked into app directory"
  - "Bind-mount pattern: /app/code is the user code directory, watched by Metro via inotify"
  - "Non-root execution: container runs as appuser (UID 1000), not root"

requirements-completed: [IMG-01, IMG-02, IMG-03, IMG-04, IMG-05]

# Metrics
duration: 3min
completed: 2026-04-15
---

# Phase 01 Plan 05: Sandbox Image Summary

**Sandbox Docker image with Expo SDK 54, Metro on port 8081, 30+ pre-installed RN packages, bind-mount code dir, and cold start smoke test**

## Performance

- **Duration:** 3 min
- **Started:** 2026-04-15T19:38:39Z
- **Completed:** 2026-04-15T19:41:29Z
- **Tasks:** 2
- **Files modified:** 9

## Accomplishments
- Dockerfile based on node:20-slim with Expo SDK 54 and ~30 pre-installed RN packages at /opt/expo-shared-deps
- Container runs as appuser (UID 1000), exposes port 8081, accepts code via bind-mount at /app/code
- Smoke test script verifies Metro responds within timeout, reports cold start vs 10s target
- README documents all env vars (APP_NAME, PORT, NODE_ENV), ports, bind mounts, pre-installed deps, and <500MB image size target

## Task Commits

Each task was committed atomically:

1. **Task 1: Create sandbox Dockerfile and app template** - `9d1ad0e` (feat)
2. **Task 2: Create smoke test script and README with env var documentation** - `a775943` (docs)

## Files Created/Modified
- `sandbox-image/Dockerfile` - Sandbox container image definition (node:20-slim, Expo SDK 54, shared deps layer)
- `sandbox-image/app/package.json` - Expo dependency manifest (~30 packages for AI-generated RN apps)
- `sandbox-image/app/entry.js` - Entry point registering App component via expo
- `sandbox-image/app/App.tsx` - Default "Forge Sandbox Ready" placeholder screen
- `sandbox-image/app/tsconfig.json` - TypeScript config extending expo base
- `sandbox-image/app/babel.config.js` - Babel config with expo preset
- `sandbox-image/app/metro.config.js` - Metro config watching /app/code, resolving shared deps
- `sandbox-image/smoke-test.sh` - Build + run + verify Metro responds within 30s timeout
- `sandbox-image/README.md` - Full documentation of env vars, ports, bind mounts, deps, usage

## Decisions Made
- npm install without --production flag: babel-preset-expo and typescript are needed at runtime for Metro bundling
- Shared deps at /opt/expo-shared-deps with symlink into /app/app/node_modules: separates heavy layer from app template for Docker cache efficiency
- UID 1000 appuser: standard non-root user, agent must chown bind-mount dirs to match

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered
None

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- Sandbox image definition complete, ready for agent to build and run containers
- Image can be built and tested locally with `cd sandbox-image && ./smoke-test.sh`
- Agent (Phase 2) will use this Dockerfile to build the image and create containers with the documented bind-mount and port patterns

## Self-Check: PASSED

All 9 created files verified on disk. Both commit hashes (9d1ad0e, a775943) verified in git log.

---
*Phase: 01-infrastructure-contracts*
*Completed: 2026-04-15*
