#!/usr/bin/env bash
# Verify Tailscale direct peering between two nodes.
# Usage: ./verify-tailscale.sh [PEER_TAILSCALE_IP]
# Exit 0 = direct peering confirmed
# Exit 1 = DERP relay detected or Tailscale not running

set -euo pipefail

PEER_IP="${1:-}"

echo "=== Forge Infrastructure: Tailscale Connectivity ==="

# Check Tailscale is installed and running
if ! command -v tailscale &>/dev/null; then
    echo "FAIL: tailscale CLI not found. Install Tailscale first."
    exit 1
fi

if ! tailscale status &>/dev/null; then
    echo "FAIL: Tailscale is not running. Start with: sudo tailscale up"
    exit 1
fi

echo "OK: Tailscale is running"

# Show current node info
echo ""
echo "--- Local Node ---"
tailscale status | head -3

# Run netcheck to verify UDP connectivity
echo ""
echo "--- Network Check ---"
NETCHECK=$(tailscale netcheck 2>&1)
echo "$NETCHECK"

# Check for UDP connectivity
if echo "$NETCHECK" | grep -q "UDP: true"; then
    echo ""
    echo "OK: UDP connectivity confirmed"
else
    echo ""
    echo "WARN: UDP connectivity not confirmed. May use DERP relay (adds 30-80ms latency)."
    echo "      To fix: allow inbound UDP port 41641 in Contabo firewall."
fi

# If peer IP provided, check direct connectivity
if [ -n "$PEER_IP" ]; then
    echo ""
    echo "--- Peer Connectivity: $PEER_IP ---"
    if tailscale ping --c=3 "$PEER_IP" 2>&1 | grep -q "pong from"; then
        PING_OUTPUT=$(tailscale ping --c=1 "$PEER_IP" 2>&1)
        if echo "$PING_OUTPUT" | grep -q "via DERP"; then
            echo "WARN: Connected to $PEER_IP via DERP relay (not direct)."
            echo "      Latency will be 30-80ms higher than direct."
            echo "      Check firewall rules on both nodes."
            exit 1
        else
            echo "OK: Direct peering with $PEER_IP confirmed"
            echo "$PING_OUTPUT"
        fi
    else
        echo "FAIL: Cannot reach $PEER_IP via Tailscale."
        echo "      Is the peer node online and connected to the same tailnet?"
        exit 1
    fi
else
    echo ""
    echo "INFO: No peer IP provided. Skipping peer connectivity check."
    echo "      Usage: $0 <PEER_TAILSCALE_IP>"
fi

echo ""
echo "=== Tailscale verification complete ==="
