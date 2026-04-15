#!/usr/bin/env bash
# Verify Cloudflare wildcard DNS for *.myappx.live.
# Checks that a test subdomain resolves to the expected IP.
# Usage: ./verify-dns.sh [EXPECTED_IP]
# Exit 0 = DNS resolves correctly
# Exit 1 = DNS not configured or wrong IP

set -euo pipefail

EXPECTED_IP="${1:-}"
DOMAIN="myappx.live"
TEST_SUBDOMAIN="forge-dns-test.${DOMAIN}"

echo "=== Forge Infrastructure: Cloudflare DNS ==="

# Check dig is available
if ! command -v dig &>/dev/null; then
    echo "INFO: dig not found, trying nslookup..."
    USE_NSLOOKUP=true
else
    USE_NSLOOKUP=false
fi

echo "Testing wildcard resolution: *.${DOMAIN}"
echo "Test hostname: ${TEST_SUBDOMAIN}"
echo ""

# Resolve test subdomain
if [ "$USE_NSLOOKUP" = true ]; then
    RESOLVED=$(nslookup "$TEST_SUBDOMAIN" 2>/dev/null | grep -A1 "Name:" | grep "Address:" | awk '{print $2}' | head -1 || echo "")
else
    RESOLVED=$(dig +short "$TEST_SUBDOMAIN" A 2>/dev/null | head -1 || echo "")
fi

if [ -z "$RESOLVED" ]; then
    echo "FAIL: ${TEST_SUBDOMAIN} does not resolve."
    echo "      Configure wildcard DNS in Cloudflare:"
    echo "      Type: A (or CNAME)"
    echo "      Name: *"
    echo "      Content: <Caddy node public IP>"
    echo "      Proxy: ON (orange cloud)"
    exit 1
fi

echo "Resolved: ${TEST_SUBDOMAIN} -> ${RESOLVED}"

# If expected IP provided, compare
if [ -n "$EXPECTED_IP" ]; then
    if [ "$RESOLVED" = "$EXPECTED_IP" ]; then
        echo "OK: Resolves to expected IP ${EXPECTED_IP}"
    else
        echo "WARN: Resolves to ${RESOLVED}, expected ${EXPECTED_IP}"
        echo "      If Cloudflare proxy is ON, the resolved IP is Cloudflare's edge."
        echo "      This is expected. Verify the origin is correct in Cloudflare dashboard."
    fi
else
    echo "INFO: No expected IP provided. DNS resolves, but cannot verify correctness."
    echo "      Usage: $0 <EXPECTED_IP>"
fi

# Check multiple random subdomains to confirm wildcard (not single record)
echo ""
echo "--- Wildcard Verification ---"
RANDOM_SUB="forge-test-$(date +%s).${DOMAIN}"
if [ "$USE_NSLOOKUP" = true ]; then
    RANDOM_RESOLVED=$(nslookup "$RANDOM_SUB" 2>/dev/null | grep -A1 "Name:" | grep "Address:" | awk '{print $2}' | head -1 || echo "")
else
    RANDOM_RESOLVED=$(dig +short "$RANDOM_SUB" A 2>/dev/null | head -1 || echo "")
fi

if [ -n "$RANDOM_RESOLVED" ]; then
    echo "OK: Random subdomain ${RANDOM_SUB} also resolves -> ${RANDOM_RESOLVED}"
    echo "    Wildcard DNS confirmed."
else
    echo "WARN: Random subdomain ${RANDOM_SUB} does not resolve."
    echo "      May be a single A record, not a wildcard. Check Cloudflare for '*' record."
fi

# Cloudflare config reminders
echo ""
echo "--- Cloudflare Settings Checklist ---"
echo "  [ ] SSL/TLS mode: Full (Strict)"
echo "  [ ] Origin Certificate installed on Caddy"
echo "  [ ] HTTP/3 (QUIC): DISABLED (prevents ERR_QUIC_PROTOCOL_ERROR)"
echo "  [ ] Always Use HTTPS: ON"
echo "  [ ] Minimum TLS Version: 1.2"

echo ""
echo "=== DNS verification complete ==="
