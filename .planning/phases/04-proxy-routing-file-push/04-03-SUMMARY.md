---
phase: 04-proxy-routing-file-push
plan: 03
subsystem: infra
tags: [caddy, reverse-proxy, tls, docker-compose, dns, cloudflare]

# Dependency graph
requires:
  - phase: 04-02
    provides: RouteManager wired into lifecycle, CaddyAdminURL config field
provides:
  - Production Caddy JSON config with TLS via Cloudflare Origin CA cert
  - Dev Caddy JSON config (HTTP only, port 8443) for local docker-compose testing
  - Caddy service in docker-compose.dev.yml with Admin API exposed
  - DNS wildcard verification script for *.myappx.live
affects: [phase-05-drift-detector, production-deployment]

# Tech tracking
tech-stack:
  added: [caddy:2.11-alpine]
  patterns: [Caddy Admin API on 0.0.0.0 for Docker port mapping, Cloudflare Origin CA for TLS]

key-files:
  created:
    - proxy/caddy.json
    - proxy/caddy-dev.json
    - proxy/README.md
    - scripts/verify-dns.sh
  modified:
    - docker-compose.dev.yml

key-decisions:
  - "caddy-dev.json admin listens on 0.0.0.0:2019 (not localhost) so Docker port mapping works"
  - "Dev config uses HTTP only on port 8443 -- no TLS certs needed locally"
  - "forge-control FORGE_CADDY_ADMIN_URL uses Docker service name http://caddy:2019"

patterns-established:
  - "Caddy base config: empty routes array populated via Admin API at runtime"
  - "Production TLS: Cloudflare Origin CA cert at /etc/caddy/certs/ with @id-tagged routes"

requirements-completed: [PRXY-01, PRXY-05]

# Metrics
duration: 4min
completed: 2026-04-15
---

# Phase 04 Plan 03: Caddy Base Config & Docker Compose Summary

**Production Caddy JSON config with Origin CA TLS, dev compose service with Admin API, and DNS wildcard verification script**

## Performance

- **Duration:** 4 min (257s)
- **Started:** 2026-04-15T23:36:58Z
- **Completed:** 2026-04-15T23:41:15Z
- **Tasks:** 2
- **Files modified:** 5

## Accomplishments
- Production caddy.json with TLS via Cloudflare Origin CA cert, admin API on localhost:2019, empty routes for dynamic population
- Dev caddy-dev.json with HTTP-only config on port 8443 for local testing without certificates
- Caddy service added to docker-compose.dev.yml with volume-mounted config and exposed ports
- DNS verification script checks wildcard resolution and prints Cloudflare settings checklist
- Verified Caddy Admin API route CRUD: add route (200), list routes (array with route), remove route (200), verify empty

## Task Commits

Each task was committed atomically:

1. **Task 1: Caddy base config and docker-compose Caddy service** - `9642112` (feat)
2. **Task 2: Fix admin bind + verify Admin API** - `7362286` (fix)

## Files Created/Modified
- `proxy/caddy.json` - Production Caddy config with TLS, admin API, empty routes
- `proxy/caddy-dev.json` - Dev Caddy config (HTTP only, 0.0.0.0:2019 admin, port 8443)
- `proxy/README.md` - Production cert placement and local dev usage docs
- `scripts/verify-dns.sh` - Wildcard DNS resolution checker for *.myappx.live
- `docker-compose.dev.yml` - Added caddy service, updated FORGE_CADDY_ADMIN_URL to http://caddy:2019

## Decisions Made
- **Admin API bind address**: caddy-dev.json uses `0.0.0.0:2019` instead of `localhost:2019` because Docker port mapping requires the process to listen on all interfaces inside the container. Production caddy.json keeps `localhost:2019` since Caddy runs on the host (no port mapping needed).
- **Dev HTTP-only**: caddy-dev.json omits TLS config entirely since we have no Origin CA certs locally. Port 8443 chosen to avoid conflict with standard ports.
- **Docker service name**: FORGE_CADDY_ADMIN_URL changed from `http://localhost:2019` to `http://caddy:2019` for container-to-container networking via Docker DNS.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Fixed Caddy admin API bind address for Docker**
- **Found during:** Task 2 (checkpoint verification)
- **Issue:** caddy-dev.json had `"listen": "localhost:2019"` which binds only to 127.0.0.1 inside the container, making the Admin API unreachable via Docker port mapping (connection reset on curl)
- **Fix:** Changed to `"listen": "0.0.0.0:2019"` in caddy-dev.json only (production keeps localhost for security)
- **Files modified:** proxy/caddy-dev.json
- **Verification:** `curl http://localhost:2019/config/` returns full config JSON; route add/remove both return 200
- **Committed in:** 7362286

---

**Total deviations:** 1 auto-fixed (1 bug fix)
**Impact on plan:** Essential fix -- without it the dev Caddy Admin API is unreachable from outside the container. No scope creep.

## Issues Encountered
- Port 8443 occupied by Docker Desktop's Kubernetes service on the dev machine. Verification was done by running Caddy with only port 2019 mapped (skipping 8443). This is a local environment issue, not a config problem -- on production/CI the port will be available.

## User Setup Required
None - no external service configuration required. Production cert placement is documented in proxy/README.md.

## Next Phase Readiness
- Caddy base config ready for production deployment with Origin CA certs
- Dev Caddy available in docker-compose for local integration testing
- Admin API verified working with route CRUD -- ready for drift detector (Phase 05)
- DNS verification script ready for pre-deployment checks

## Self-Check: PASSED

- All 6 files exist on disk
- Both commits (9642112, 7362286) found in git log
- caddy.json and caddy-dev.json are valid JSON
- verify-dns.sh is executable

---
*Phase: 04-proxy-routing-file-push*
*Completed: 2026-04-15*
