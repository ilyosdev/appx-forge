/**
 * Phase 30 chaos scenario 004 — network partition.
 *
 * Failure: drop control ↔ agent traffic for N seconds, then restore.
 *
 * Invariant under test: heartbeats fail during the partition (control
 * plane's view of the node goes stale), but on heal the agent's next
 * heartbeat re-syncs state without forcing a re-registration. last_seen
 * advances past the partition window. A regression here would mean a
 * brief network blip — common at ISP / Tailscale layer — leaves a
 * permanently-orphaned node row.
 *
 * The harness's inject.sh `partition` action prefers iptables inside
 * the control-plane container (NET_ADMIN) and falls back to docker
 * network disconnect/reconnect when iptables isn't available. Either
 * path produces the same observable outcome from the test.
 *
 *     RUN_CHAOS=true npx jest chaos-network-partition.spec.ts --runInBand
 */
import { startStack, stopStack, inject, waitForCondition } from './harness';

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

describeChaos('chaos: network partition recovery', () => {
  beforeAll(startStack, 10 * 60_000);
  afterAll(stopStack, 5 * 60_000);

  it('node re-syncs after 15s control↔agent partition heals', async () => {
    await waitForCondition(
      async () => (await listNodes()).length >= 1,
      30_000,
      'agent registered before chaos',
    );

    const before = await listNodes();
    const target = before[0];
    const beforeLastSeen = new Date(target.last_seen_at!).getTime();

    // partition blocks for 15s (synchronous — script sleeps and heals)
    inject('partition', '15');

    // After heal, last_seen must advance past the partition end
    await waitForCondition(
      async () => {
        const rows = await listNodes();
        const row = rows.find((n) => n.id === target.id);
        if (!row?.last_seen_at) return false;
        const now = new Date(row.last_seen_at).getTime();
        return now > beforeLastSeen + 14_000;
      },
      45_000,
      `node ${target.id} last_seen advanced past partition window`,
    );
  }, 180_000);
});
