/**
 * Phase 30 chaos scenario 001 — agent restart.
 *
 * Failure: kill the agent process and let docker-compose bring it back.
 *
 * Invariant under test (W2 + W3): after the agent re-registers and resumes
 * heartbeats, the control plane's view of the node refreshes within the
 * heartbeat grace window. A regression here would mean an agent flap
 * (kernel update, oom-kill, manual restart) leaves a node permanently
 * marked stale even though it's healthy again.
 *
 * The PLAN template assumed a backend service in the harness so the
 * scenario could push files mid-restart and observe verified_at on a
 * sandbox row. The harness deliberately doesn't run the backend, so we
 * exercise the same recovery path one layer down — node-level
 * `last_seen_at` instead of sandbox-level `verified_at`. Same code path
 * (HeartbeatReconciler), same outcome under failure.
 *
 *     RUN_CHAOS=true npx jest chaos-agent-restart.spec.ts --runInBand
 */
import { startStack, stopStack, inject, waitForCondition } from './harness';

const RUN = process.env.RUN_CHAOS === 'true';
const describeChaos = RUN ? describe : describe.skip;

const CONTROL = 'http://localhost:8090';
const TOKEN = 'harness-token';

interface NodeRow {
  id: string;
  hostname: string;
  status: string;
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

describeChaos('chaos: agent restart recovery', () => {
  beforeAll(startStack, 10 * 60_000);
  afterAll(stopStack, 5 * 60_000);

  it('node re-heartbeats within 30s of agent restart', async () => {
    await waitForCondition(
      async () => (await listNodes()).length >= 1,
      30_000,
      'agent registered before chaos',
    );

    const before = await listNodes();
    expect(before.length).toBeGreaterThan(0);
    const target = before[0];
    const baselineLastSeen = target.last_seen_at
      ? new Date(target.last_seen_at).getTime()
      : 0;
    expect(baselineLastSeen).toBeGreaterThan(0);

    inject('restart-agent');

    await waitForCondition(
      async () => {
        const rows = await listNodes();
        const row = rows.find((n) => n.id === target.id);
        if (!row?.last_seen_at) return false;
        return new Date(row.last_seen_at).getTime() > baselineLastSeen;
      },
      30_000,
      `node ${target.id} last_seen_at advanced past baseline`,
    );

    const after = (await listNodes()).find((n) => n.id === target.id);
    expect(after?.status).not.toBe('lost');
  }, 90_000);
});
