#!/usr/bin/env bash
# Phase 30 — failure injection helpers for the chaos harness.
#
# Each action triggers a specific class of failure the production stack
# is expected to recover from. Scenarios in chaos-*.spec.ts files call
# these via the TS harness wrapper.
#
# All actions assume `docker-compose.test.yml` is running. They no-op
# (return non-zero) if it isn't — fail loudly rather than silently
# pretending to inject a failure.

set -e
HARNESS_DIR="$(cd "$(dirname "$0")" && pwd)"
COMPOSE="docker compose -f $HARNESS_DIR/docker-compose.test.yml"

# --- Service restarts -------------------------------------------------

restart_agent() {
  echo "[inject] restart agent"
  $COMPOSE restart agent
}

restart_control_plane() {
  echo "[inject] restart control plane"
  $COMPOSE restart control-plane
}

restart_postgres() {
  echo "[inject] restart postgres (control plane DB)"
  $COMPOSE restart postgres
}

restart_mysql() {
  echo "[inject] restart mysql (backend DB)"
  $COMPOSE restart mysql
}

# --- Network partitions -----------------------------------------------

# Block control → agent traffic for $1 seconds, then restore.
# Requires NET_ADMIN on the control-plane container (set in compose).
# Uses iptables OUTPUT chain so it doesn't impact incoming heartbeats
# from other agents — only the egress to *this* agent.
partition_agent_from_control() {
  local seconds="${1:-30}"
  echo "[inject] partition control → agent for ${seconds}s"
  $COMPOSE exec -T control-plane sh -c "iptables -A OUTPUT -d agent -j DROP" || {
    echo "[inject] WARN: iptables not available in control-plane container; falling back to docker network disconnect"
    docker network disconnect "$(docker inspect -f '{{range $k,$v := .NetworkSettings.Networks}}{{$k}}{{end}}' "$(${COMPOSE} ps -q agent)" | head -1)" "$(${COMPOSE} ps -q agent)"
    sleep "$seconds"
    docker network connect "$(docker inspect -f '{{range $k,$v := .NetworkSettings.Networks}}{{$k}}{{end}}' "$(${COMPOSE} ps -q agent)" | head -1)" "$(${COMPOSE} ps -q agent)"
    return 0
  }
  sleep "$seconds"
  $COMPOSE exec -T control-plane sh -c "iptables -D OUTPUT -d agent -j DROP" || true
}

# --- Sandbox-level chaos ----------------------------------------------

# Kill $1 random pool-* sandbox containers from the host's Docker daemon.
# Pool containers are identified by the `pool-` name prefix the agent
# stamps on every spawned sandbox.
kill_random_pool_containers() {
  local count="${1:-5}"
  echo "[inject] kill $count random pool-* sandboxes"
  local victims
  victims=$(docker ps --filter "name=pool-" --format '{{.ID}}' | shuf | head -n "$count" || true)
  if [ -z "$victims" ]; then
    echo "[inject] no pool-* containers to kill"
    return 0
  fi
  echo "$victims" | xargs -r docker kill
}

# Kill the docker daemon inside the agent's container (if it has its
# own dockerd). For our compose this is a no-op since the agent uses
# the host's daemon — kept here so chaos scripts have a stable name
# regardless of how the agent's docker access is provisioned.
restart_docker_in_agent() {
  echo "[inject] (no-op for host-docker mode) restart-docker"
  return 0
}

# --- Dispatch ---------------------------------------------------------

case "$1" in
  restart-agent)            restart_agent ;;
  restart-control)          restart_control_plane ;;
  restart-postgres)         restart_postgres ;;
  restart-mysql)            restart_mysql ;;
  restart-docker)           restart_docker_in_agent ;;
  partition)                partition_agent_from_control "$2" ;;
  kill-pool)                kill_random_pool_containers "$2" ;;
  *)
    cat <<EOF >&2
Usage: $0 <action> [args]

Service restarts (state survives, only process restarts):
  restart-agent
  restart-control
  restart-postgres
  restart-mysql

Network partitions:
  partition <seconds>     Block control → agent for N seconds, then heal.

Sandbox chaos:
  kill-pool <count>       docker kill N random pool-* sandbox containers.
  restart-docker          (no-op in host-docker mode)
EOF
    exit 1
    ;;
esac
