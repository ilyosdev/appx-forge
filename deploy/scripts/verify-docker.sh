#!/usr/bin/env bash
# Verify Docker Engine 27.x is installed (NOT 28.x or 29.x).
# Docker SDK v28+ has Go import path breakage.
# Docker v29 has Swarm 60s restart bug.
# Exit 0 = Docker 27.x confirmed
# Exit 1 = Wrong version or not installed

set -euo pipefail

echo "=== Forge Infrastructure: Docker Engine ==="

# Check Docker is installed
if ! command -v docker &>/dev/null; then
    echo "FAIL: docker CLI not found."
    echo "      Install Docker Engine 27.x: https://docs.docker.com/engine/install/"
    exit 1
fi

# Get Docker version
DOCKER_VERSION=$(docker version --format '{{.Server.Version}}' 2>/dev/null || echo "")

if [ -z "$DOCKER_VERSION" ]; then
    echo "FAIL: Cannot get Docker server version. Is Docker daemon running?"
    echo "      Try: sudo systemctl start docker"
    exit 1
fi

echo "Docker Engine version: $DOCKER_VERSION"

# Extract major version
MAJOR=$(echo "$DOCKER_VERSION" | cut -d. -f1)

if [ "$MAJOR" = "27" ]; then
    echo "OK: Docker Engine 27.x confirmed"
elif [ "$MAJOR" = "28" ]; then
    echo "FAIL: Docker 28.x detected. Go SDK import paths are broken in v28+."
    echo "      Downgrade to Docker Engine 27.5.x."
    echo "      See: https://github.com/moby/moby/issues/49712"
    exit 1
elif [ "$MAJOR" = "29" ]; then
    echo "FAIL: Docker 29.x detected. Known Swarm 60s restart bug in v29."
    echo "      Downgrade to Docker Engine 27.5.x."
    exit 1
else
    echo "WARN: Docker $MAJOR.x detected. Forge is tested with 27.x only."
    echo "      Recommended: Docker Engine 27.5.x"
fi

# Check Docker info for resource limits
echo ""
echo "--- Docker System Info ---"
echo "Storage Driver: $(docker info --format '{{.Driver}}' 2>/dev/null)"
echo "Cgroup Driver:  $(docker info --format '{{.CgroupDriver}}' 2>/dev/null)"
echo "OS:             $(docker info --format '{{.OperatingSystem}}' 2>/dev/null)"
echo "Kernel:         $(docker info --format '{{.KernelVersion}}' 2>/dev/null)"
echo "CPUs:           $(docker info --format '{{.NCPU}}' 2>/dev/null)"
echo "Memory:         $(docker info --format '{{.MemTotal}}' 2>/dev/null | numfmt --to=iec 2>/dev/null || docker info --format '{{.MemTotal}}' 2>/dev/null)"

# Verify Docker is NOT in Swarm mode
SWARM_STATUS=$(docker info --format '{{.Swarm.LocalNodeState}}' 2>/dev/null || echo "")
if [ "$SWARM_STATUS" = "active" ]; then
    echo ""
    echo "WARN: Docker Swarm is ACTIVE. Forge does not use Swarm."
    echo "      Consider: docker swarm leave --force"
fi

echo ""
echo "=== Docker verification complete ==="
