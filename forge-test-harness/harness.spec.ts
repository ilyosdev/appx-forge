/**
 * Phase 30 chaos harness — smoke test for the harness itself.
 *
 * This is NOT a chaos scenario; it just verifies that the docker-compose
 * stack comes up, the control plane reports healthy, and the agent
 * registers itself. T27-T32 add the actual scenarios on top of this
 * foundation.
 *
 * Gated on RUN_HARNESS=true — running unconditionally would mean every
 * `jest` invocation in CI tries to docker compose up, which is too
 * expensive for a normal unit run. Bring it up when explicitly running
 * the chaos suite.
 *
 *     RUN_HARNESS=true npx jest --runInBand
 */
import { startStack, stopStack, ok, waitForCondition } from './harness';

const RUN = process.env.RUN_HARNESS === 'true';
const describeHarness = RUN ? describe : describe.skip;

describeHarness('Phase 30 chaos harness — smoke', () => {
  beforeAll(() => {
    startStack();
  }, 10 * 60_000); // up to 10min for first-time image build

  afterAll(() => {
    stopStack();
  }, 5 * 60_000);

  it('control plane responds to /v1/healthz within 2 minutes of stack up', async () => {
    await waitForCondition(
      () => ok('http://localhost:8090/v1/healthz'),
      120_000,
      'control plane healthz',
    );
  }, 130_000);

  it('agent registers itself with control plane within 30s', async () => {
    // After registration, the control plane lists at least one node.
    // We don't auth this endpoint here — just confirming the agent
    // got far enough to insert a row.
    await waitForCondition(
      async () => {
        try {
          const res = await fetch('http://localhost:8090/v1/nodes', {
            headers: { Authorization: 'Bearer harness-token' },
            signal: AbortSignal.timeout(2000),
          });
          if (!res.ok) return false;
          const body = (await res.json()) as { nodes: unknown[] };
          return Array.isArray(body.nodes) && body.nodes.length >= 1;
        } catch {
          return false;
        }
      },
      30_000,
      'agent registered (>=1 node visible)',
    );
  }, 40_000);
});
