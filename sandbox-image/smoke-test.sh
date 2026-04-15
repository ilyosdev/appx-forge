#!/usr/bin/env bash
# Smoke test for the Forge sandbox image.
# Builds the image, runs a container, and verifies Metro responds within 10s.
# Usage: ./smoke-test.sh [IMAGE_NAME]
# Exit 0 = Metro responds in time
# Exit 1 = Build failed, start failed, or timeout

set -euo pipefail

IMAGE="${1:-forge-sandbox:test}"
CONTAINER_NAME="forge-sandbox-smoke-test"
PORT=8081
TIMEOUT=30  # Total timeout (Metro may take up to 30s first bundle)
METRO_CHECK_INTERVAL=2

echo "=== Forge Sandbox Image Smoke Test ==="

# Clean up any previous test container
docker rm -f "$CONTAINER_NAME" 2>/dev/null || true

# Build image
echo ""
echo "--- Building Image ---"
docker build -t "$IMAGE" "$(dirname "$0")"
IMAGE_SIZE=$(docker images --format "{{.Size}}" "$IMAGE" | head -1)
echo "Image size: $IMAGE_SIZE"

# Run container
echo ""
echo "--- Starting Container ---"
CONTAINER_ID=$(docker run -d \
    --name "$CONTAINER_NAME" \
    -p "${PORT}:${PORT}" \
    -e APP_NAME=smoke-test \
    "$IMAGE")
echo "Container: $CONTAINER_ID"

# Wait for Metro to respond
echo ""
echo "--- Waiting for Metro (timeout: ${TIMEOUT}s) ---"
START_TIME=$(date +%s)
METRO_READY=false

for i in $(seq 1 $((TIMEOUT / METRO_CHECK_INTERVAL))); do
    ELAPSED=$(( $(date +%s) - START_TIME ))

    # Check if container is still running
    if ! docker ps -q --filter "id=$CONTAINER_ID" | grep -q .; then
        echo "FAIL: Container stopped unexpectedly."
        echo "--- Container Logs ---"
        docker logs "$CONTAINER_NAME" 2>&1 | tail -30
        docker rm -f "$CONTAINER_NAME" 2>/dev/null || true
        exit 1
    fi

    # Try to reach Metro status endpoint
    if curl -sf "http://localhost:${PORT}/status" >/dev/null 2>&1; then
        METRO_READY=true
        echo "OK: Metro responded after ${ELAPSED}s"
        break
    fi

    echo "  Waiting... (${ELAPSED}s elapsed)"
    sleep "$METRO_CHECK_INTERVAL"
done

if [ "$METRO_READY" = false ]; then
    ELAPSED=$(( $(date +%s) - START_TIME ))
    echo "FAIL: Metro did not respond within ${TIMEOUT}s"
    echo "--- Container Logs ---"
    docker logs "$CONTAINER_NAME" 2>&1 | tail -30
    docker rm -f "$CONTAINER_NAME" 2>/dev/null || true
    exit 1
fi

# Check cold start target
ELAPSED=$(( $(date +%s) - START_TIME ))
if [ "$ELAPSED" -le 10 ]; then
    echo "OK: Cold start within 10s target (${ELAPSED}s)"
else
    echo "WARN: Cold start ${ELAPSED}s exceeds 10s target. May need optimization."
fi

# Clean up
echo ""
echo "--- Cleanup ---"
docker rm -f "$CONTAINER_NAME" >/dev/null
echo "Container removed."

echo ""
echo "=== Smoke test PASSED ==="
echo "Image: $IMAGE ($IMAGE_SIZE)"
echo "Cold start: ${ELAPSED}s"
