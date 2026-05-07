#!/usr/bin/env bash
# Verify Cloudflare wildcard DNS for *.myappx.live
set -euo pipefail

TEST_HOST="test-dns-check.myappx.live"
echo "Checking DNS resolution for $TEST_HOST..."

# Try dig first, fall back to nslookup
if command -v dig &>/dev/null; then
    RESULT=$(dig +short "$TEST_HOST" 2>/dev/null || true)
elif command -v nslookup &>/dev/null; then
    RESULT=$(nslookup "$TEST_HOST" 2>/dev/null | grep -A1 "Name:" | grep "Address:" | awk '{print $2}' || true)
else
    echo "ERROR: Neither dig nor nslookup available"
    exit 1
fi

if [ -z "$RESULT" ]; then
    echo "FAIL: $TEST_HOST does not resolve"
    echo "Action: Configure *.myappx.live wildcard DNS in Cloudflare"
    exit 1
fi

echo "OK: $TEST_HOST resolves to $RESULT"
echo ""
echo "Verifying WebSocket support via Cloudflare settings..."
echo "Manual check required:"
echo "  1. Cloudflare Dashboard -> myappx.live -> SSL/TLS -> Full (Strict)"
echo "  2. Cloudflare Dashboard -> Network -> WebSockets: ON"
echo "  3. Cloudflare Dashboard -> Network -> HTTP/3 (QUIC): OFF"
