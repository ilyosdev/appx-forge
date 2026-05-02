/**
 * Phase 30 chaos scenario 005 — orphan flood.
 *
 * SKIPPED in current harness — see explanation below. Spec committed
 * so the test plan stays complete; promote to active once the harness
 * can warm a real container pool.
 *
 * Why skipped: this scenario `docker kill`s 20 `pool-*` sandbox
 * containers and validates the reconciler detects 20 drifts in the
 * next heartbeat and the pool re-warms. The harness has no backend
 * service in the loop, so nothing creates pool-* containers in the
 * first place. inject.sh's `kill-pool` correctly no-ops with a
 * "no pool-* containers to kill" message.
 *
 * To make this scenario real, two paths:
 *   a) Add a minimal NestJS backend to the harness compose (real
 *      pool warming, costs ~500MB image footprint)
 *   b) Have the test itself POST 20 sandboxes via control-plane API
 *      with `pool-NNN` names, wait for them to reach RUNNING, then
 *      kill 20 of them.
 *
 * Invariant we WANT to test: reconciler detects 20 drifts in next
 * heartbeat; pool re-warms; alert metric `forge.drift.detected_total`
 * exceeds 10/min during recovery; system returns to steady state.
 */
describe.skip('chaos: orphan flood (deferred — needs pool warming in harness)', () => {
  it('reconciler detects 20 drifts and pool re-warms', () => {
    expect(true).toBe(true);
  });
});
