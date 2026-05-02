/**
 * Phase 30 chaos test harness — orchestration utilities.
 *
 * Each chaos-*.spec.ts in this directory describes a single failure
 * injection scenario:
 *   1. Spin up docker-compose.test.yml stack
 *   2. Wait for health (all services responding)
 *   3. Run scenario setup (provision projects, seed pool, etc.)
 *   4. Inject a failure via inject.sh
 *   5. Wait for the recovery condition the scenario asserts
 *   6. Tear down stack
 *
 * The functions here are deliberately small and synchronous-feeling
 * (with awaits) — chaos specs are easier to reason about as a linear
 * narrative than as event-driven code.
 */
import { execSync } from 'node:child_process';
import { resolve } from 'node:path';

const HARNESS_DIR = resolve(__dirname);

/** Bring the stack up. Builds images on first run, reuses cache otherwise. */
export function startStack(): void {
  execSync(
    'docker compose -f docker-compose.test.yml up -d --build --wait',
    { cwd: HARNESS_DIR, stdio: 'inherit' },
  );
}

/** Tear the stack down + drop volumes (no state survives). */
export function stopStack(): void {
  execSync('docker compose -f docker-compose.test.yml down -v', {
    cwd: HARNESS_DIR,
    stdio: 'inherit',
  });
}

/**
 * Trigger a failure injection. Args are passed through to inject.sh
 * verbatim — see that script's help text for the action vocabulary.
 *
 * Throws on non-zero exit, so a scenario that injects an unknown
 * action fails loudly instead of silently passing.
 */
export function inject(action: string, ...args: string[]): void {
  const argv = [action, ...args].map((a) => `'${a.replace(/'/g, `'\\''`)}'`).join(' ');
  execSync(`./inject.sh ${argv}`, { cwd: HARNESS_DIR, stdio: 'inherit' });
}

/**
 * Poll `check` every 500ms until it resolves true, or fail after
 * `timeoutMs`. Logs OK / FAIL with elapsed time so the scenario log
 * makes the recovery latency obvious without a separate assertion.
 */
export async function waitForCondition(
  check: () => Promise<boolean>,
  timeoutMs: number,
  description: string,
): Promise<void> {
  const start = Date.now();
  while (Date.now() - start < timeoutMs) {
    if (await check()) {
      // eslint-disable-next-line no-console
      console.log(`OK: ${description} in ${Date.now() - start}ms`);
      return;
    }
    await new Promise((r) => setTimeout(r, 500));
  }
  throw new Error(`FAIL: ${description} after ${timeoutMs}ms`);
}

/** Convenience — fetch + 2xx check, suitable for healthz polls. */
export async function ok(url: string): Promise<boolean> {
  try {
    const res = await fetch(url, { signal: AbortSignal.timeout(2000) });
    return res.ok;
  } catch {
    return false;
  }
}

/**
 * Count pool-* sandbox containers running on the host's Docker daemon.
 * Used by chaos scenarios to assert pool replenishment after a
 * kill-pool injection.
 */
export function countPoolContainers(): number {
  const out = execSync(
    `docker ps --filter "name=pool-" --filter "status=running" -q`,
    { encoding: 'utf-8' },
  );
  return out.split('\n').filter(Boolean).length;
}
