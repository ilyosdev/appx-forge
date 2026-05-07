/**
 * Phase 30 chaos scenario 006 — mass provision.
 *
 * SKIPPED in current harness — see explanation below. Spec committed
 * so the test plan stays complete; promote to active once the harness
 * can provision real sandboxes end-to-end.
 *
 * Why skipped: this scenario fires 50 simultaneous claim requests for
 * new projects and validates that all complete or fail loud, the pool
 * fills, and metrics stay sane. Provisioning a real sandbox requires
 *   1) a workload-running container image (production: bundle-server
 *      built from template/, ~300MB; not present in the harness)
 *   2) the agent's start_sandbox flow, which builds a Docker spec from
 *      the API request's `image` field. A generic image like
 *      `alpine:latest` would create a container that exits immediately,
 *      because the lifecycle expects a long-running HTTP server on
 *      port 3000.
 *
 * The chaos invariant being tested is at the orchestration layer
 * (claim concurrency, capacity accounting, no silent corruption) —
 * it is not actually about what runs inside the sandbox. So a
 * one-line workaround is: ship a tiny `forge-test-server:slim` image
 * that just `nc -l -p 3000` forever and use it as the harness's
 * default sandbox image. That's a follow-up phase.
 *
 * Invariant we WANT to test: 50 simultaneous sandbox creates resolve
 * (success or 503 on capacity exhaustion — never silent corruption);
 * pool capacity accounting stays consistent; control-plane queue
 * depth metric stays bounded; no orphans left in the agent's docker
 * cache after teardown.
 */
describe.skip('chaos: mass provision (deferred — needs harness sandbox image)', () => {
  it('50 concurrent claims complete or fail loud, pool stays consistent', () => {
    expect(true).toBe(true);
  });
});
