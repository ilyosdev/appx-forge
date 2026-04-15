#!/usr/bin/env bash
# Configure inotify watch limits for 80+ containers with Metro file watchers.
# Metro uses inotify to watch for file changes (HMR). Each container needs
# ~5000-6000 watches. 80 containers * 6000 = 480,000 minimum.
# We set 524288 (512K) for headroom.
# Must be run as root.
# Exit 0 = limits configured
# Exit 1 = not root or failed

set -euo pipefail

echo "=== Forge Infrastructure: inotify Watch Limits ==="

# Must be root
if [ "$(id -u)" -ne 0 ]; then
    echo "FAIL: Must be run as root. Use: sudo $0"
    exit 1
fi

# Target values
TARGET_WATCHES=524288
TARGET_INSTANCES=8192

# Current values
CURRENT_WATCHES=$(cat /proc/sys/fs/inotify/max_user_watches 2>/dev/null || echo "0")
CURRENT_INSTANCES=$(cat /proc/sys/fs/inotify/max_user_instances 2>/dev/null || echo "0")

echo "Current:  max_user_watches=$CURRENT_WATCHES  max_user_instances=$CURRENT_INSTANCES"
echo "Target:   max_user_watches=$TARGET_WATCHES  max_user_instances=$TARGET_INSTANCES"

# Apply immediately
sysctl -w fs.inotify.max_user_watches=$TARGET_WATCHES
sysctl -w fs.inotify.max_user_instances=$TARGET_INSTANCES

# Persist across reboots
SYSCTL_CONF="/etc/sysctl.d/99-forge-inotify.conf"
cat > "$SYSCTL_CONF" << EOF
# Forge: inotify limits for 80+ Metro containers
# Each Metro instance uses ~5000-6000 watches for HMR
# 80 containers * 6000 = 480,000 minimum, set 524,288 for headroom
fs.inotify.max_user_watches = $TARGET_WATCHES
fs.inotify.max_user_instances = $TARGET_INSTANCES
EOF

echo ""
echo "OK: Limits applied and persisted to $SYSCTL_CONF"

# Verify
VERIFY_WATCHES=$(cat /proc/sys/fs/inotify/max_user_watches)
VERIFY_INSTANCES=$(cat /proc/sys/fs/inotify/max_user_instances)
echo "Verified: max_user_watches=$VERIFY_WATCHES  max_user_instances=$VERIFY_INSTANCES"

if [ "$VERIFY_WATCHES" -eq "$TARGET_WATCHES" ] && [ "$VERIFY_INSTANCES" -eq "$TARGET_INSTANCES" ]; then
    echo ""
    echo "=== inotify setup complete ==="
else
    echo "FAIL: Values did not apply correctly"
    exit 1
fi
