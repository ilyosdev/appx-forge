FROM golang:1.26-bookworm AS builder
WORKDIR /app
COPY go.work go.work.sum ./
COPY shared-go/ shared-go/
COPY control/ control/
RUN go work edit -dropuse=./agent -dropuse=./cli && \
    cd control && go build -o /usr/local/bin/forge-control ./cmd/forge-control/

FROM debian:bookworm-slim
RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates wget && \
    rm -rf /var/lib/apt/lists/*
COPY --from=builder /usr/local/bin/forge-control /usr/local/bin/forge-control
COPY control/migrations/ /app/migrations/
WORKDIR /app
CMD ["forge-control"]
