FROM golang:1.26-bookworm AS builder
WORKDIR /app
COPY go.work go.work.sum ./
COPY shared-go/ shared-go/
COPY agent/ agent/
RUN go work edit -dropuse=./cli -dropuse=./control && \
    cd agent && go build -o /usr/local/bin/forge-agent ./cmd/forge-agent/

FROM debian:bookworm-slim
# Phase 30 chaos harness needs Docker CLI inside the agent container so
# inject.sh's `kill-pool` style operations can run from within the agent's
# process namespace if a future scenario needs it. iproute2 is for the
# partition/iptables injection. Slim install — no docker-engine, just the
# CLI; it talks to the host's /var/run/docker.sock mounted at runtime.
RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates curl iproute2 iptables && \
    install -m 0755 -d /etc/apt/keyrings && \
    curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc && \
    chmod a+r /etc/apt/keyrings/docker.asc && \
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian bookworm stable" > /etc/apt/sources.list.d/docker.list && \
    apt-get update && \
    apt-get install -y --no-install-recommends docker-ce-cli && \
    rm -rf /var/lib/apt/lists/*

COPY --from=builder /usr/local/bin/forge-agent /usr/local/bin/forge-agent

# Phase 30 chaos harness — there is no Tailscale interface inside the
# compose network, but control-plane registration requires a routable IP.
# Resolve the container's own IPv4 at start time and export it as
# FORGE_TAILSCALE_IP unless the operator already set one. Production
# deployments that DO have Tailscale just pass FORGE_TAILSCALE_IP
# explicitly and this lookup is a no-op.
RUN printf '#!/bin/sh\nset -e\nif [ -z "${FORGE_TAILSCALE_IP}" ]; then\n  FORGE_TAILSCALE_IP=$(hostname -I | awk "{print \\$1}")\n  export FORGE_TAILSCALE_IP\nfi\nexec forge-agent "$@"\n' > /usr/local/bin/forge-agent-entrypoint && \
    chmod +x /usr/local/bin/forge-agent-entrypoint
WORKDIR /app
EXPOSE 8090
CMD ["forge-agent-entrypoint"]
