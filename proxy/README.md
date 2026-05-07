# Forge Proxy (Caddy)

## Production Setup
1. Place Cloudflare Origin CA cert at /etc/caddy/certs/origin.pem
2. Place Origin CA key at /etc/caddy/certs/origin-key.pem
3. Run: caddy run --config /etc/caddy/caddy.json

## Local Development
Uses caddy-dev.json (HTTP only, port 8443, no TLS).
Started automatically by docker-compose.dev.yml.
