FROM golang:1.25-bookworm AS builder
WORKDIR /app
COPY go.work go.work.sum ./
COPY shared-go/ shared-go/
COPY control/ control/
RUN cd control && go build -o /usr/local/bin/forge-control ./cmd/forge-control/

FROM debian:bookworm-slim
COPY --from=builder /usr/local/bin/forge-control /usr/local/bin/forge-control
COPY control/migrations/ /app/migrations/
WORKDIR /app
CMD ["forge-control"]
