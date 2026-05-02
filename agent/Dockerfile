FROM golang:1.25-bookworm AS builder
WORKDIR /app
COPY go.work go.work.sum ./
COPY shared-go/ shared-go/
COPY agent/ agent/
RUN cd agent && go build -o /usr/local/bin/forge-agent ./cmd/forge-agent/

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
WORKDIR /app
EXPOSE 8090
CMD ["forge-agent"]
