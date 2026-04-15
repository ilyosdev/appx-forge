---
phase: 1
slug: infrastructure-contracts
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-04-15
---

# Phase 1 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | Go stdlib `testing` + `testcontainers-go` v0.42.0 |
| **Config file** | None needed (Go convention: `_test.go` next to source) |
| **Quick run command** | `go test ./... -short` |
| **Full suite command** | `go test ./... -v -count=1` |
| **Estimated runtime** | ~30 seconds (testcontainers-go Postgres startup dominates) |

---

## Sampling Rate

- **After every task commit:** Run `go test ./... -short`
- **After every plan wave:** Run `go test ./... -v -count=1`
- **Before `/gsd-verify-work`:** Full suite must be green
- **Max feedback latency:** 30 seconds

---

## Per-Task Verification Map

| Task ID | Plan | Wave | Requirement | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|-----------|-------------------|-------------|--------|
| 1-01-01 | 01 | 1 | CNTR-06 | unit | `go test ./shared-go/models/ -v` | ❌ W0 | ⬜ pending |
| 1-02-01 | 02 | 2 | CNTR-05 | integration | `go test ./control/tests/ -run TestMigration -v` | ❌ W0 | ⬜ pending |
| 1-02-02 | 02 | 2 | CNTR-06 | integration | `go test ./control/tests/ -run TestCAS -v` | ❌ W0 | ⬜ pending |
| 1-04-01 | 04 | 1 | INFRA-01..04 | manual-only | `bash deploy/scripts/verify-tailscale.sh` | ❌ W0 | ⬜ pending |
| 1-05-01 | 05 | 1 | IMG-01..05 | smoke | `bash sandbox-image/smoke-test.sh` | ❌ W0 | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [ ] `shared-go/models/sandbox_test.go` — state machine transition tests (property-based)
- [ ] `control/tests/testhelpers/postgres.go` — testcontainers-go setup with snapshot/restore
- [ ] `control/tests/migration_test.go` — migration up/down/up cycle tests
- [ ] `control/tests/store_test.go` — CAS integration tests with real Postgres
- [ ] `sandbox-image/smoke-test.sh` — image build + Metro cold start timing

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Tailscale direct peering | INFRA-01 | Requires 2 Contabo VDS nodes | Run `tailscale netcheck` on both nodes, verify DirectConnection=true |
| Docker Engine 27.x | INFRA-02 | Requires access to production nodes | SSH to node, run `docker version --format '{{.Server.Version}}'` |
| inotify watch limit | INFRA-03 | Requires kernel parameter on node | `cat /proc/sys/fs/inotify/max_user_watches` >= 524288 |
| Cloudflare wildcard DNS | INFRA-04 | Requires Cloudflare config | `dig +short '*.myappx.live'` returns proxy node IP |
| OpenAPI spec completeness | CNTR-01 | Review against REQUIREMENTS.md | Diff spec endpoints against requirement list |
| Agent protocol docs | CNTR-02 | Review against STARTER_PLAN | Verify all command types documented |
| File push protocol docs | CNTR-03 | Review against STARTER_PLAN | Verify signed URL flow documented |
| Proxy routing docs | CNTR-04 | Review against STARTER_PLAN | Verify Caddy Admin API shapes documented |
| Env vars documented | IMG-04 | Review Dockerfile | Verify APP_NAME, PORT ENV directives present |

---

*Validation strategy created: 2026-04-15*
