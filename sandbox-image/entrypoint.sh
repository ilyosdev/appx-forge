#!/bin/bash
set -e

# Seed template files into bind-mounted /app/code (preserves agent-written files).
# -n: never overwrite existing files -- agent-written code takes precedence.
cp -rn /opt/template/. /app/code/ 2>/dev/null || true

# Symlink shared node_modules once (idempotent).
if [ ! -e /app/code/node_modules ]; then
  ln -sf /opt/expo-shared-deps/node_modules /app/code/node_modules
fi

# Enforce our tuned Metro config (three-layer defense vs. user/AI overrides).
# Layer 1: remove any user-pushed metro.config.* at every container start.
rm -f /app/code/metro.config.js \
      /app/code/metro.config.ts \
      /app/code/metro.config.cjs \
      /app/code/metro.config.mjs

# Layer 2: copy baked config, mark read-only so appuser cannot modify at runtime.
cp /opt/metro-base-config.js /app/code/metro.config.js
chmod 444 /app/code/metro.config.js

# Layer 3 lives in forge-agent: http/writer.go rejects file-push writes to
# metro.config.*. See appx-forge/agent/internal/http/writer.go.

exec "$@"
