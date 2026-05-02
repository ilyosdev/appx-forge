/**
 * Phase 30 chaos scenario 002 — control plane restart.
 *
 * Failure: kill the control-plane process mid-flight.
 *
 * Invariant under test: when control plane comes back up, it accepts
 * heartbeats from already-registered agents without forcing them to
 * re-register, and /v1/healthz starts answering 200 again. A regression
 * here would mean a routine control-plane deploy or crash silently
 * pauses the entire fleet's freshness loop.
 *
 *     RUN_CHAOS=true npx jest chaos-control-plane-restart.spec.ts --runInBand
 */
import { startStack, stopStack, inject, waitForCondition, ok } from './harness';

const RUN = process.env.RUN_CHAOS === 'true';
const describeChaos = RUN ? describe : describe.skip;

const CONTROL = 'http://localhost:8090';
const TOKEN = 'harness-token';

interface NodeRow {
  id: string;
  last_seen_at?: string;
}

async function listNodes(): Promise<NodeRow[]> {
  const res = await fetch(`${CONTROL}/v1/nodes`, {
    headers: { Authorization: `Bearer ${TOKEN}` },
    signal: AbortSignal.timeout(2000),
  });
  if (!res.ok) throw new Error(`list nodes: ${res.status}`);
  const body = (await res.json()) as { nodes: NodeRow[] };
  return body.nodes ?? [];
}

describeChaos('chaos: control plane restart recovery', () => {
  beforeAll(startStack, 10 * 60_000);
  afterAll(stopStack, 5 * 60_000);

  it('control plane recovers within 30s and heartbeats resume', async () => {
    await waitForCondition(
      async () => (await listNodes()).length >= 1,
      30_000,
      'agent registered before chaos',
    );

    const before = await listNodes();
    const target = before[0];
    const baselineLastSeen = new Date(target.last_seen_at!).getTime();

    inject('restart-control');

    await waitForCondition(
      () => ok(`${CONTROL}/v1/healthz`),
      60_000,
      'control plane healthz back online',
    );

    await waitForCondition(
      async () => {
        const rows = await listNodes();
        const row = rows.find((n) => n.id === target.id);
        if (!row?.last_seen_at) return false;
        return new Date(row.last_seen_at).getTime() > baselineLastSeen;
      },
      30_000,
      `node ${target.id} resumed heartbeats post-restart`,
    );
  }, 180_000);
});
