# forge-test-harness

Chaos test harness for the Forge stack (Phase 30, T26+).

## What this is

A docker-compose stack that brings up the Forge control plane + agent +
their dependencies on a developer machine, plus shell + TS helpers for
injecting failures and asserting recovery.

T26 (this directory) is the scaffolding. T27-T32 (next) add 6 chaos
scenarios on top of it.

## Bring it up

First-time builds the Go images from `../control/` and `../agent/` (a few
minutes); subsequent runs reuse cached layers (~10s).

```sh
cd appx-forge/forge-test-harness
npm install                            # one-time, for jest/ts-jest deps
npm run stack:up                       # docker compose up -d --build --wait
npm run harness                        # RUN_HARNESS=true jest --runInBand
npm run stack:down                     # docker compose down -v
```

## Ports

`+1` offsets from `../docker-compose.dev.yml` so a developer can run both:

| Service       | Harness host port | Dev host port |
|---------------|-------------------|---------------|
| Postgres      | 5433              | 5433 (same — only one will run at a time) |
| Control plane | 8090              | 8080          |
| Agent         | 8091              | n/a (commented out in dev) |
| MySQL         | 3308              | n/a           |
| Redis         | 6380              | n/a           |

## Failure injection

`inject.sh` is the API. Verbs:

| Action | What |
|---|---|
| `restart-agent` / `restart-control` / `restart-postgres` / `restart-mysql` | Process restart (volumes preserved) |
| `partition <seconds>` | iptables-drop control → agent for N seconds |
| `kill-pool <count>` | `docker kill` N random `pool-*` sandbox containers |
| `restart-docker` | (no-op in host-docker mode) |

`assert.sh` exposes `wait_for_condition` for ad-hoc CLI repro;
`harness.ts`'s `waitForCondition()` is the same idea for jest specs.

## Why a separate test runner from the control-plane Go tests

Control-plane unit + integration tests (under `../control/tests/`) cover
single-process behavior end-to-end. The chaos harness covers multi-process
behavior the unit suite can't see — agent restarts, network partitions,
docker daemon kills. They share build artifacts but not test runners.
