/**
 * Phase 30 chaos scenario 003 — docker daemon restart.
 *
 * SKIPPED in current harness — see explanation below. Spec is committed
 * so the test plan stays complete, but it self-skips at collection
 * time. Promote to active when the harness can run a self-contained
 * dockerd inside the agent container.
 *
 * Why skipped: the harness's agent talks to the HOST docker daemon
 * (via /var/run/docker.sock bind-mount). inject.sh's `restart-docker`
 * action is correspondingly a documented no-op (see inject.sh
 * `restart_docker_in_agent`). Restarting the host daemon would kill
 * the developer's other containers, including the harness itself.
 *
 * To make this scenario real:
 *   - Switch the agent container to docker-in-docker (privileged,
 *     own dockerd) instead of bind-mounting the host socket
 *   - Then `inject('restart-docker')` can actually `pkill dockerd`
 *     inside the agent and validate cache rebuild
 *
 * Invariant we WANT to test: after dockerd restart, agent rebuilds
 * its container cache from `docker ps`, reconciler propagates the
 * new state to the control plane, user-visible operations recover
 * within 90s.
 */
describe.skip('chaos: docker daemon restart (deferred — needs DinD agent)', () => {
  it('agent rebuilds container cache and reconciler propagates state', () => {
    expect(true).toBe(true);
  });
});
