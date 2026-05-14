# ADR-0001: Sandbox Exec Primitive

- Status: Accepted
- Date: 2026-05-14
- Deciders: solo dev (pre-launch prod)
- Tags: forge, exec, security, contracts

## Context

The V2 Edit Engine of the parent project (AppX / text2design) needs an LLM-bound `run_command` tool so its architect/editor can install dependencies (`npm install foo`), run tests (`jest`, `tsc --noEmit`), and inspect runtime state inside the user app sandbox. Today no forge layer exposes shell exec — agents handle 5 command verbs (start/stop/restart/get_logs/prune); control has no /exec endpoint; SDK has no exec method.

Opencode, aider, bloom, and most production coding agents have this tool. AppX is the outlier.

## Decision

Add an exec primitive across all 4 forge layers (shared types + agent + control + SDK) with HMAC-signed exec_completed webhook back to the consumer. Layered staged deploy avoids orphan endpoints at any step.

## Design

### Wire-level contract (OpenAPI)

- `POST /v1/sandboxes/{id}/exec` — body `{ command, cwd?, env?, timeout_seconds? }` → 202 `{ command_id, status: "queued" }`. Security: Bearer (existing) + optional execJwt (X-Exec-Token).
- `GET /v1/sandboxes/{id}/exec/{cmd_id}` — 200 `{ status, exit_code, stdout, stderr, stdout_truncated, stderr_truncated, duration_ms }`. Same security.
- `exec_completed` webhook payload — `{ type: "exec_completed", sandbox_id, command_id, exit_code, stdout, stderr, *_truncated, duration_ms, ts }`. Same URL as state-changed webhook, type-discriminated.

### Layer responsibilities

- **shared-go** — extends `CommandType` enum with `CmdExec`. DB migration 00007 alters `commands.command_type` CHECK constraint to permit "exec".
- **forge-agent** — `executeExec` handler in command-loop; uses moby SDK `ExecCreate` + `ExecAttach` + `ExecInspect`; `stdcopy.StdCopy` demultiplexes stdout/stderr; `limitedBuffer` truncates at 30KB head per stream; `context.WithTimeout(timeout_seconds)` kills runaway exec. POSTs result back to control via existing `/agents/{id}/commands/{cmd_id}/ack`.
- **forge-control** — `POST /exec` validates input, resolves sandbox state (must be running), dispatches via lifecycle.DispatchExec → store.CreateCommand. `GET /exec/{cmd_id}` reads commands.result JSONB. On agent ack, HandleAck fires `OnExecCompleted` notifier (HMAC-signed POST to webhook URL). New `ExecJWT` middleware validates X-Exec-Token alongside Bearer for /exec routes.
- **backend SDK** (text2design/backend/src/lib/forge-sdk/) — `ForgeClient.sandboxes.exec/getExecResult/waitForExec` methods. New `signExecJwt` helper for X-Exec-Token. Reuses existing Bearer auth + adds optional scoped-JWT layer.

## Decisions (D1-D8 from the C0 design spec, all "Accept")

- **D1 Egress** — Scoped iptables at Server 2: npm registry + expo + bun registries only; everything else blocked.
- **D2 Allow-list enforcement** — Defense-in-depth. SDK does pre-check (saves round-trips on obviously-bad input); forge-agent does final check before docker exec.
- **D3 Auth scope** — Per-call scoped JWT with `sandbox_id` claim, alongside existing global Bearer. Backend signs HS256 JWT with shared FORGE_EXEC_JWT_SECRET; control middleware validates claim matches path parameter. Falls back to Bearer-only if secret unset (graceful).
- **D4 Audit log** — Admin-only via existing `commands.result` JSONB column. No per-user visibility v1.
- **D5 Output delivery** — Poll MVP. Backend SDK's waitForExec polls every 500ms until terminal. WebSocket streaming deferred to a future C0.5.
- **D6 Allow-list** — 19 binaries: npm/yarn/pnpm/bun/node + ls/cat/head/tail/grep/find/tree/pwd/which/echo/printf/file/wc + jest/vitest/tsc. Banned: rm/mv/mkdir/touch/curl/wget/sudo/su/chmod/chown/bash/sh/eval/dd/kill plus regex guards (no $(), backticks, <(...), >(...), pipe-to-shell, chained rm, path traversal).
- **D7 Timeouts** — 120s default · 300s max. SDK clamps; agent enforces via context.WithTimeout.
- **D8 Truncation** — 30KB head per stream at v1. 10KB tail deferred (limitedBuffer simplification).

## Threat model & security review outcomes (C5)

Run by cavecrew-reviewer. Findings + closures:

- CRIT-1 process substitution bypass (`cat <(echo evil)`) — closed by extending COMMAND_SUBSTITUTION regex.
- CRIT-2 exec_completed webhook silently dropped at backend — closed by ForgeWebhookPayload union + payload.type dispatch in forge-events.controller.ts.
- MED-3 parser is defense-in-depth not full sandbox — documented in run-command-parser.ts top-of-file.
- MED-4 JWT TTL invariant — MIN_TTL=400s + MAX_TTL=3600s asserted in signExecJwt.
- MED-5 JWT claim presence — explicit exists + type + nonempty triple guard in exec_jwt.go.
- MED-6 undefined default + production warn — forge.config.ts + forge.service.ts.

Residual risks (documented, accepted):

- Scripted payloads inside allowed binaries (`npm run X` where script body is evil; `node -e 'evil()'`) — by-design shell-language inherent. Forge microVM isolation is the LAST line of defense.
- npm registry exfiltration via published evil package — accepted by D1 scoped egress (npm is on the allow-list).
- DNS tunnels via allowed node runtime — accepted by D1; mitigation deferred to network policy.

## Alternatives considered

1. **No exec primitive, redirect to git+push flow** — rejected. Doesn't unblock `npm install` use case.
2. **Pure WebSocket streaming for output** — rejected for v1. Adds complexity for tail-output cases that polling handles fine at 500ms; deferred to C0.5.
3. **Per-call API key rotation instead of JWT** — rejected. JWT with sandbox_id claim binds the capability to the resource cleanly; per-call rotation is more state to manage.
4. **No JWT, just Bearer everywhere** — rejected per D3. Global Bearer leak exposes ALL forge endpoints; scoped JWT contains blast radius to one sandbox.

## Migration / rollback

- Migration 00007 is additive (widens CHECK constraint). Roll back by running its Down step which restores the 5-type constraint. Existing rows with `command_type='exec'` would block rollback; in practice no production rows yet.
- forge-agent + forge-control binaries deploy via systemd restart on Servers 1+2 (10-30s downtime). Rollback = previous binary + restart.
- Backend SDK additions are pure-additive; rollback = revert backend commits `7af1b91` + `962054a` + `6aca20d`.

## Consequences

### Positive

- LLM can now install deps + run tests + inspect runtime state inside sandboxes.
- Defense-in-depth: SDK parser + agent allow-list + microVM substrate isolation + scoped JWT.
- Audit trail via commands.result for every exec, persisted indefinitely.
- Webhook notifier completes the loop for downstream consumers (chat events, audit dashboards).

### Negative

- New attack surface — model can now run shell. Mitigations above.
- Pre-existing sandbox image binary set unverified — D6 allow-list assumes `npm/node/jest/tsc/vitest/...` present in PATH. If missing, exec returns "command not found" gracefully.
- Output truncation at 30KB head loses tail of stderr from runaway processes. Acceptable for v1; tail capture deferred.

### Neutral

- Tool count at editor binding goes 5 → 6.
- DB schema change requires production migration before binary deploy.
- New env var FORGE_EXEC_JWT_SECRET must be shared between backend + appx-forge.

## References

- Design spec — `text2design/docs/superpowers/specs/2026-05-14-c0-forge-exec.md`
- Phase tracker — `text2design/docs/superpowers/plans/2026-05-14-phase-tracker.html`
- OpenAPI changes — `appx-forge/docs/contracts/control-api.openapi.yaml` lines 255-345 + 530-534
- Agent protocol — `appx-forge/docs/contracts/agent-protocol.md` § exec
- Commits: appx-forge `2fc86b1` `beb2c69` `36520ce` `8c6e668` `0c78bff` ; backend `7af1b91` `962054a` `6aca20d`
- C5 security review captured in `text2design/docs/superpowers/plans/2026-05-14-phase-tracker.html` update log
